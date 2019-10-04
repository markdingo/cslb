package cslb

/*
The srv structs cache SRV RRs. They are structured to make life easy for bestTarget() as that is
presumed to be the most heavily used function in this package. The relationship between structs is:
ceSRV->cePriority->ceTarget where cePriority contains all matching priorities and ceTarget contains
all targets with that priority.
*/

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	smallChanceMultiplier = 1000 // Fraction of weight given to zero weighted targets
)

type srvCache struct {
	sync.RWMutex                   // Protects everything within this struct
	done         chan bool         // Shuts down the cache cleaner
	cache        map[string]*ceSRV // The cache key is ToLower(qName).
}

type ceSRV struct {
	expires           time.Time     // When this entry expire out of the cache
	lookups           int           // Includes initial lookup that creates the cache entry
	priorities        []*cePriority // Slice of targets with equal priority
	uniqueTargetCount int           // Count of all unique targets (host:port)
}

// String return a printable string of the cached Entry SRV
func (t *ceSRV) String() (s string) {
	s = fmt.Sprintf("%s (%d):", t.expires, len(t.priorities))
	for _, cep := range t.priorities {
		s += fmt.Sprintf("\n\tp=%d totw=%d (%d):", cep.priority, cep.totalWeight, len(cep.targets))
		for _, cet := range cep.targets {
			s += fmt.Sprintf("\n\t\ttarw=%d %s:%d", cet.weight, cet.target, cet.port)
		}
	}
	return
}

type cePriority struct {
	priority    int
	totalWeight int         // Sum of all weights - used as an upper limit for the PRNG
	targets     []*ceTarget // Slice of all targets within priority
}

type ceTarget struct {
	weight int
	port   int
	target string
}

// healthStoreKey generates the lookup key for the healthStore. It's of the form host:port
func (t *ceTarget) healthStoreKey() string {
	return makeHealthStoreKey(t.target, t.port)
}

func newSrvCache() *srvCache {
	return &srvCache{cache: make(map[string]*ceSRV), done: make(chan bool)}
}

func (t *srvCache) start(cacheInterval time.Duration) {
	go t.cleaner(cacheInterval)
}

func (t *srvCache) stop() {
	close(t.done)
}

// lookupSRV looks up the SRV RR for the domain. First it tries looking in the cache and if not
// there, the DNS is consulted. The qName is of the form ToLower(_http._tcp.$domain) where "http" is
// the service and "tcp" is the proto. The cache is updated with the results of the DNS lookup.
//
// lookupSRV returns a *ceSRV with an array of (possibly zero) net.SRV RRs even with an NXDomain
// response (which comes back as an error). An empty list means the DNS lookup failed; normally this
// means that the domain should not be considered to be under cslb control.
//
// We construct the full key/qName ourselves rather than rely on LookupSRV as we need the formed
// qName for the cache lookup. I suppose we could have a different cache key and let LookupSRV do
// it's thing but that would be a little confusing. Besides, the SRV name construction is well-known
// and simple.
func (t *cslb) lookupSRV(ctx context.Context, now time.Time, service, proto, domain string) *ceSRV {
	key := strings.ToLower("_" + service + "._" + proto + "." + domain) // rfc2782 format
	t.srvStore.RLock()
	cesrv := t.srvStore.cache[key]
	if cesrv != nil { // If cache entry exists we're done
		cesrv.lookups++
		t.srvStore.RUnlock()
		return cesrv
	}
	t.srvStore.RUnlock() // Don't hold mutex across a possible DNS lookup

	cesrv = &ceSRV{expires: now.Add(t.NotFoundSRVTTL), lookups: 1} // Assume NXDomain
	_, srvList, _ := t.netResolver.LookupSRV(ctx, "", "", key)
	if len(srvList) > 0 { // Found something so transfer to the new ceSRV
		cesrv.expires = now.Add(t.FoundSRVTTL)
		cesrv.populate(srvList)
	}

	// Insert/over-write the cache entry. It's possible another go-routine snuck in while we
	// were off in DNS-land and created an entry with the same key. Oh well. The slowest
	// go-routine wins. We don't bother queuing all callers for the same cache key behind each
	// other and let one do the resolution. I suppose we could at some point in the future by
	// creating a sync.Cond and the resolving go-routine could Broadcast when done. Go makes
	// this quite easy but that's bound to be a premature optimization as a single program is
	// unlikely to be banging away at a single domain at the same instant in time without an
	// entry existing in our cache.

	targetKeys := cesrv.uniqueTargetKeys()
	cesrv.uniqueTargetCount = len(targetKeys)
	t.srvStore.Lock()
	t.srvStore.cache[key] = cesrv // cesrv is now read-only for the rest of its life
	t.srvStore.Unlock()
	t.populateHealthStore(now, targetKeys)

	return cesrv
}

// populate transfers the SRV RRs into the ceSRV. This means sorting the SRVs by priority order and
// also calculating total weight so we can conveniently apply the SRV selection algorithm.
//
// The Golang resolver has pre-sorted the SRV RRs for us in priority/weight order but we don't rely
// on that as there may be replacement resolvers or mock resolvers involved.
//
// RFC2782 says that "weights of 0 should have a very small chance of being selected" without
// defining "very small". They way we achieve this as well as make our selection algorithm simple is
// to give them collectively an effective weight of 0.1% of the total weights so that all zero
// targets will on average get 1/1000th of traffic. That seems like a "very small chance" to me.
func (t *ceSRV) populate(srvs []*net.SRV) {
	sort.Slice(srvs, func(i, j int) bool { return srvs[i].Priority < srvs[j].Priority })
	var cep *cePriority // Ptr to current priority or nil if none yet
	for _, srv := range srvs {
		if len(srv.Target) == 0 { // RFC2782 says to ignore any zero length targets completely
			continue
		}
		if cep == nil || int(srv.Priority) != cep.priority { // Create a new higher priority?
			cep = &cePriority{priority: int(srv.Priority)}
			t.priorities = append(t.priorities, cep)
		}
		cet := &ceTarget{weight: int(srv.Weight) * smallChanceMultiplier, port: int(srv.Port),
			target: strings.ToLower(srv.Target)}
		cep.targets = append(cep.targets, cet)
		cep.totalWeight += cet.weight
	}

	// Assign a non-zero weight to targets with an SRV weight of zero

	for _, cep := range t.priorities {
		zeroWeightEntryCount := 0
		for _, cet := range cep.targets {
			if cet.weight == 0 {
				zeroWeightEntryCount++
			}
		}

		if zeroWeightEntryCount == 0 {
			continue
		}

		verySmall := cep.totalWeight / smallChanceMultiplier
		verySmall = (verySmall + zeroWeightEntryCount - 1) / zeroWeightEntryCount
		if verySmall == 0 { // This can be true if totalWeight is very small or zero!
			verySmall = 1
		}
		for _, cet := range cep.targets {
			if cet.weight == 0 {
				cet.weight = verySmall
				cep.totalWeight += verySmall
			}
		}
	}
}

// bestTarget selects the "best" target to try and connect to based on the SRV selection algorithm
// and the health of the targets. That is, pick the SRV set with the lowest-numerical priority
// first. If all those targets are unavailable then pick the SRV set with the next lowest-numerical
// priority. Within the selected priority weight is used to distribute load. E.g. a weight list of
// a=1, b=2, c=3 would ideally have 12 requests distributed such that 2 go to a, 4 go to b and 6 go
// to c.
//
// If all targets in all priorities are "bad" due to health checks or connection failures, pick the
// the least-worst target which is the target with a nextDialAttempt closest to now.
//
// A synthesized SRV is always returned if there are any targets in the SRV. In the case of the
// least-worst return, maybe the caller will get lucky and the connection will come good this time?
// Or maybe they won't get lucky but at least they get to see a "connection failed" outcome and can
// report it to something or someone.
//
// To summarize, the returned SRV will be one of the following in the order shown:
//
// - Highest Priority in good health in weight range - first choice within priority
// - First target in highest Priority in good health - second choice within priority
// - Same thing for each Priority down if none of the previous priorities are in good health
// - Target with soonest next connection attempt regardless of priority or weight - least worst
//
// The caller should always check for a nil return, the other values in the returned SRV are mostly
// returned as a convenience to the caller. They should not presume they are the exact same values
// as retrieved from the DNS but they will be comparable.
func (t *cslb) bestTarget(cesrv *ceSRV) (srv *net.SRV) {
	if len(cesrv.priorities) == 0 { // Either an NXDomain or SRV with zero length targets
		return nil
	}

	srv = &net.SRV{} // We will return something!
	now := time.Now()
	t.healthStore.RLock()         // Apply Read lock across whole search rather than a nickle & dime approach
	defer t.healthStore.RUnlock() // whereby we may cycle the lock many times.

	// Search for the in-range weight but also note a target in good health in passing (called
	// our secondChoice) as the preferred weight may be in bad health in which case we'll take
	// any weight in the same priority as our second choice in preference to a lower priority.

	haveSecondChoice := false
	for _, cep := range cesrv.priorities {
		wix := t.randIntn(cep.totalWeight) // Select the weight value using a "cheap" RNG
		lower := 0
		upper := 0
		for _, cet := range cep.targets {
			ceh := t.healthStore.cache[cet.healthStoreKey()]
			upper += cet.weight
			if ceh == nil || ceh.isGood(now) {
				if wix >= lower && wix < upper { // Is this target in the weight range?
					srv.Target = cet.target
					srv.Port = uint16(cet.port)
					srv.Priority = uint16(cep.priority)
					srv.Weight = uint16(cet.weight) / smallChanceMultiplier
					return // This is expected to be the "happy path"
				}
				if !haveSecondChoice { // If we don't have a second choice yet, use this one
					haveSecondChoice = true
					srv.Target = cet.target
					srv.Port = uint16(cet.port)
					srv.Priority = uint16(cep.priority)
					srv.Weight = uint16(cet.weight) / smallChanceMultiplier
				}
			}
			lower = upper // Iterate over targets
		}
		if haveSecondChoice { // Preferred weight range was in bad health but we
			return // found a good health target in the preferred priority
		}
	}

	// Didn't find *any* healthy targets so search over *all* targets for least-worst. We don't
	// expect this to occur very often so the search loop is run a second time rather than add
	// complexity to the relatively simple search loop above. The least-worst target has the
	// soonest nextDialAttempt. Priority and weight are ignored as there is no point in
	// considering a higher priority target which has a nextDialAttempt way off into the future
	// as that means it's *just* failed whereas one that's a millisecond away from now has had
	// the longest time period to "come good".

	var smallestLeastWorst time.Time
	for _, cep := range cesrv.priorities {
		for _, cet := range cep.targets {
			ceh := t.healthStore.cache[cet.healthStoreKey()]
			nextAttempt := now
			if ceh != nil { // This could have changed underneath us, so be defensive
				nextAttempt = ceh.nextDialAttempt
			}

			// smallestLeastWorst starts life as IsZero() and nextAttempt is always
			// greater than zero so the first time thru this test always comes true and
			// sets a least-worst.

			if smallestLeastWorst.IsZero() || nextAttempt.Before(smallestLeastWorst) {
				srv.Target = cet.target
				srv.Port = uint16(cet.port)
				srv.Priority = uint16(cep.priority)
				srv.Weight = uint16(cet.weight) / smallChanceMultiplier
				smallestLeastWorst = nextAttempt
			}
		}
	}

	return
}

// uniqueTargetKeys returns a slice of all unique targets keys in the SRV (a key is host:port). The
// SRV might actually have more targets than this count if some of the targets are identical. This
// shouldn't occur in a single well-constructed SRV arrangement but targets might be shared across
// other SRVs so we've generalized the need to cater for that such that a target is an independent
// beast which just happens to be attached to one or more SRVs.
func (t *ceSRV) uniqueTargetKeys() (tSlice []string) {
	dupes := make(map[string]bool)
	for _, cep := range t.priorities {
		for _, cet := range cep.targets {
			dupes[cet.healthStoreKey()] = true
		}
	}

	for k := range dupes {
		tSlice = append(tSlice, k)
	}

	return
}

// uniqueTargets returns the count of uniqueTargetKeys
func (t *ceSRV) uniqueTargets() (count int) {
	return t.uniqueTargetCount
}

// cleaner periodically scans the srvStore for expired entries and deletes them. Normally run as a
// go-routine.
func (t *srvCache) cleaner(cleanInterval time.Duration) {
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

func (t *srvCache) clean(now time.Time) {
	t.Lock()
	defer t.Unlock()

	for key, cesrv := range t.cache {
		if cesrv.expires.Before(now) {
			delete(t.cache, key)
		}
	}
}

// ceSrvAsStats is a clone of ceSRV (and related material) with exported variable for html.Template
type ceSrvAsStats struct {
	CName       string
	Expires     string
	Lookups     string
	Priority    int
	Weight      int
	Port        int
	Target      string
	GoodDials   int // From healthStore
	FailedDials int
	IsGood      bool
}

type srvStats struct {
	Srvs      []ceSrvAsStats
	nxDomains []ceSrvAsStats
}

// getStats clones all the ceSRV entries into a struct suitable for the status service. This
// shouldn't be too expensive as we don't expect a huge number of SRVs, but who knows?
func (t *srvCache) getStats(hc *healthCache) *srvStats {
	now := time.Now()
	s := &srvStats{}
	t.RLock()
	defer t.RUnlock()

	s.Srvs = make([]ceSrvAsStats, 0, len(t.cache)*4)    // Just guesses, but better than nothing and
	s.nxDomains = make([]ceSrvAsStats, 0, len(t.cache)) // over-sized is probably better than under-sized.
	for cname, cesrv := range t.cache {
		if len(cesrv.priorities) == 0 { // NXDomain?
			s.nxDomains = append(s.nxDomains,
				ceSrvAsStats{CName: cname,
					Expires: cesrv.expires.Sub(now).Truncate(time.Second).String(),
					Lookups: fmt.Sprintf("%d", cesrv.lookups),
					Target:  "**NXDomain**"})
			continue
		}
		for _, cep := range cesrv.priorities {
			for _, cet := range cep.targets {
				entry := ceSrvAsStats{CName: cname,
					Expires:  cesrv.expires.Sub(now).Truncate(time.Second).String(),
					Lookups:  fmt.Sprintf("%d", cesrv.lookups),
					Priority: cep.priority,
					Weight:   cet.weight,
					Port:     cet.port,
					Target:   cet.target,
					IsGood:   true}
				hc.RLock()
				ceh := hc.cache[cet.healthStoreKey()]
				if ceh != nil {
					entry.GoodDials = ceh.goodDials
					entry.FailedDials = ceh.failedDials
					entry.IsGood = ceh.isGood(now)
				}
				hc.RUnlock()

				s.Srvs = append(s.Srvs, entry)
			}
		}
	}

	return s
}
