package cslb

/*
The health structs track the health of the target systems in terms of whether we have been able to
establish successful connections to them or not and what the results of the health check is - if one
is running.

The health check URL is used as by a background check to pre-determine the state of the target
rather than waiting for a failed connection. Defining a health check URL is recommended as a
successful connect() does not necessarily imply a successful service - it merely implies a
successful TCP setup. Furthermore a health check URL can be used to administratively turn a target
on and off. Or take it "out of rotation" in devop parlance.

Most of these functions are actually cslb functions rather than healthCache functions because they
need access to cslb variables such as config and resolver. This could be restructured to bring all
those values within a healthStore, but there's not a lot of value in that apart from slightly better
encapsulation.
*/

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type healthCache struct {
	sync.RWMutex                      // Protects everything within this struct
	done         chan bool            // Shuts down the cache cleaner
	cache        map[string]*ceHealth // The key is ToLower(target:port) - use makeHealthStoreKey()
}

type ceHealth struct {
	expires               time.Time // When this entry expire out of the cache
	goodDials             int
	failedDials           int
	nextDialAttempt       time.Time // When we can next consider this target - IsZero() means now
	lastDialAttempt       time.Time
	lastDialStatus        string
	lastHealthCheck       time.Time
	lastHealthCheckStatus string // From http.Get()
	url                   string // URL to probe to confirm target is healthy
	unHealthy             bool   // True if last health check failed
}

// isGood returns whether a target can be used. Caller must have locked beforehand.
func (t *ceHealth) isGood(now time.Time) bool {
	return !t.unHealthy && !t.nextDialAttempt.After(now) // Don't use Before as it might be right now!
}

// makeHealthStoreKey generates the lookup key for the healthStore. It's of the form host:port
func makeHealthStoreKey(host string, port int) string {
	return host + ":" + strconv.FormatUint(uint64(port), 10)
}

func unpackHealthStoreKey(targetKey string) (host, port string) {
	colon := strings.IndexByte(targetKey, ':')
	if colon > 0 {
		host = targetKey[:colon]
		port = targetKey[colon+1:]
	}

	return
}

func newHealthCache() *healthCache {
	return &healthCache{cache: make(map[string]*ceHealth), done: make(chan bool)}
}

func (t *healthCache) start(cacheInterval time.Duration) {
	go t.cleaner(cacheInterval)
}

func (t *healthCache) stop() {
	close(t.done)
}

// populateHealthStore adds a list of targets to the healthStore. Supplied keys are fully formed
// cache keys, that is, target:port. It also starts off the health check for each new target if HC
// is enabled.
func (t *cslb) populateHealthStore(now time.Time, healthStoreKeys []string) {
	t.healthStore.Lock()
	defer t.healthStore.Unlock()

	for _, healthStoreKey := range healthStoreKeys {
		ceh := t.healthStore.cache[healthStoreKey]
		if ceh == nil {
			ceh = &ceHealth{expires: now.Add(t.HealthTTL)}
			t.healthStore.cache[healthStoreKey] = ceh
			if !t.DisableHealthChecks {
				go t.fetchAndRunHealthCheck(healthStoreKey, ceh)
			}
		}
	}
}

var zeroTime time.Time

// setDialResult records the results of the last dial attempt. If this is a previously unknown
// target then a health check is start for the target, if HC is enabled. This should rarely be the
// case but it can happen if the HealthTTL is shorter than the SRV TTL or if a connection runs
// across a target expiration.
func (t *cslb) setDialResult(now time.Time, host string, port int, err error) {
	t.healthStore.Lock()
	defer t.healthStore.Unlock()

	healthStoreKey := makeHealthStoreKey(host, port)
	ceh := t.healthStore.cache[healthStoreKey]
	if ceh == nil { // I would expect an entry to be here
		ceh = &ceHealth{expires: now.Add(t.HealthTTL)}
		t.healthStore.cache[healthStoreKey] = ceh
		if !t.DisableHealthChecks {
			go t.fetchAndRunHealthCheck(healthStoreKey, ceh)
		}
	}
	ceh.lastDialAttempt = now
	if err == nil {
		ceh.goodDials++
		ceh.nextDialAttempt = zeroTime
		ceh.lastDialStatus = ""
	} else {
		ceh.failedDials++
		ceh.nextDialAttempt = now.Add(t.DialVetoDuration)
		ceh.lastDialStatus = err.Error()
	}
}

// fetchAndRunHealthCheck is normally started as a separate go-routine when a target is added to the
// healthStore. It fetches the health check URL and if present runs a periodic GET check until the
// ceHealth entry expires. The health check URL is stored in a TXT RR. It could be a TypeURI RR
// (RFC7553) I suppose, but who supports/uses those? The qName for the TXT RR is of the form
// _$port._cslb.$target, thus something like _80._cslb.example.net where port is from the SRV RR.
func (t *cslb) fetchAndRunHealthCheck(healthStoreKey string, ceh *ceHealth) {
	host, port := unpackHealthStoreKey(healthStoreKey)
	qName := "_" + port + t.HealthCheckTXTPrefix + host
	txts, err := t.netResolver.LookupTXT(context.Background(), qName)
	if err != nil {
		return // No TXT
	}
	hcURL := strings.Join(txts, "") // TXT is a slice of sub-strings so bang them all together
	if len(hcURL) == 0 {
		return // Empty string can't be fetched!
	}
	t.healthStore.Lock()
	ceh.url = hcURL        // For reporting purposes only
	expires := ceh.expires // Extract under protection of the lock
	t.healthStore.Unlock()

	_, err = url.Parse(hcURL) // Check that the URL is in fact a URL
	if err != nil {
		return // Doesn't look like it!
	}

	// Run the health check until the ceh expires.

	sleepFor := time.Second // Only wait a short time for the first health check
	for {
		time.Sleep(sleepFor)
		sleepFor = t.HealthCheckFrequency // Second and subsequents wait a normal amount of time
		now := time.Now()
		if expires.Before(now) {
			return
		}

		resp, err := t.hcClient.Get(hcURL)
		if err != nil {
			if t.PrintHCResults {
				fmt.Println("Health Check:", healthStoreKey, err)
			}
			t.healthStore.Lock()
			ceh.unHealthy = true
			ceh.lastHealthCheck = now
			ceh.lastHealthCheckStatus = err.Error()
			t.healthStore.Unlock()
			return // Fatal error - leave the ceh to its own devices
		}
		body, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if t.PrintHCResults {
				fmt.Println("Health Check:", healthStoreKey, err)
			}
			continue
		}

		ok := resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte(t.HealthCheckContentOk))
		if t.PrintHCResults {
			fmt.Println("Health Check Set:", healthStoreKey, ok)
		}
		t.healthStore.Lock()
		ceh.unHealthy = !ok
		ceh.lastHealthCheck = now
		ceh.lastHealthCheckStatus = resp.Status
		t.healthStore.Unlock()
	}
}

// cleaner periodically scans the cache to delete expired entries. Normally run as a go-routine.
func (t *healthCache) cleaner(cleanInterval time.Duration) {
	ticker := time.NewTicker(cleanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.done:
			return
		case now := <-ticker.C:
			t.clean(now)
		}
	}
}

func (t *healthCache) clean(now time.Time) {
	t.Lock()
	defer t.Unlock()

	for key, ceh := range t.cache {
		if ceh.expires.Before(now) {
			delete(t.cache, key)
		}
	}
}

// ceHealthAsStats is a clone of ceHealth with exported variables for html.Template
type ceHealthAsStats struct {
	Key                   string
	GoodDials             int
	FailedDials           int
	Expires               time.Duration // In the future
	NextDialAttempt       time.Duration // In the future
	LastDialAttempt       time.Duration // In the past
	LastDialStatus        string
	LastHealthCheck       time.Duration // In the past
	LastHealthCheckStatus string
	Url                   string
	IsGood                bool
}

type healthStats struct {
	Targets []ceHealthAsStats
}

// getStats clones all the ceHealth entries into a struct suitable for the status service. This
// shouldn't be too expensive as we don't expect a huge number of targets, but who knows?
func (t *healthCache) getStats() *healthStats {
	now := time.Now()
	s := &healthStats{}
	t.RLock()
	defer t.RUnlock()

	s.Targets = make([]ceHealthAsStats, 0, len(t.cache))
	for k, v := range t.cache {
		entry := ceHealthAsStats{
			Key:            k,
			GoodDials:      v.goodDials,
			FailedDials:    v.failedDials,
			LastDialStatus: trimTo(v.lastDialStatus, 60),
			Url:            v.url,
			IsGood:         v.isGood(now),
		}
		if !v.expires.IsZero() {
			entry.Expires = v.expires.Sub(now).Truncate(time.Second)
		}
		if !v.nextDialAttempt.IsZero() {
			entry.NextDialAttempt = v.nextDialAttempt.Sub(now).Truncate(time.Second)
		}
		if !v.lastDialAttempt.IsZero() {
			entry.LastDialAttempt = now.Sub(v.lastDialAttempt).Truncate(time.Second)
		}
		if !v.lastHealthCheck.IsZero() {
			entry.LastHealthCheck = now.Sub(v.lastHealthCheck).Truncate(time.Second)
			entry.LastHealthCheckStatus = trimTo(v.lastHealthCheckStatus, 90)
		}
		s.Targets = append(s.Targets, entry)
	}

	return s
}

func trimTo(s string, max int) string {
	if len(s) > max {
		if max <= 3 {
			return "..."
		}
		s = s[:max-3] + "..."
	}

	return s
}
