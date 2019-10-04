package cslb

import (
	"context"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// limitedResolver is a subset of net.Resolver that is used by cslb. The idea is to create a smaller
// interface that's easier to mock.
type limitedResolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (cname string, addrs []*net.SRV, err error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

const (
	cslbEnvPrefix               = "cslb_"   // All cslb environment variables are prefixed with this
	defaultHealthCheckTXTPrefix = "._cslb." // Prepended to target name to form a TXT qName containing URL

	defaultHealthCheckContentOk = "OK"             // Must be in the body of a good health check response
	defaultHealthCheckFrequency = time.Second * 50 // How often to run the health check query
	defaultInterceptTimeout     = time.Minute      // Default context duration for dialContextIntercept
	defaultDialVetoDuration     = time.Minute      // Ignore targets for this duration after dial fails

	// We need to configure our own TTLs because the go DNS APIs don't return TTLs. Most DNS
	// libraries don't, but they all should as it is vital data for long-running programs that
	// mistakenly hold onto a DNS results for the lifetime of the program istead of the lifetime
	// of the DNS response.

	defaultNotFoundSRVTTL = time.Minute * 20 // How long a NXDomain SRV is retained in the cache
	defaultFoundSRVTTL    = time.Minute * 5  // How long a found SRV is retained in the cache
	defaultHealthTTL      = time.Minute * 5  // How long a target stays in the cache
)

// Config parameters manipulated by tests or possibly external options
type config struct {
	Version string

	PrintDialContext bool // "d" - diagnostics settings are lowercase
	PrintHCResults   bool // "h"
	PrintIntercepts  bool // "i"
	PrintDialResults bool // "r"
	PrintSRVLookup   bool // "s"

	DisableInterception  bool // "C" - behaviour settings are uppercase
	DisableHealthChecks  bool // "H"
	AllowNumericServices bool // "N"

	StatusServerAddress   string // Listen address of status server
	StatusServerTemplates string // filepath.Glob of replacement templates for status server

	HealthCheckTXTPrefix string // Prepended to target name to form a TXT URL
	HealthCheckContentOk string // Must be in the body of the health check response
	HealthCheckFrequency time.Duration
	InterceptTimeout     time.Duration // Maximum time to run connect attempts with an intercept call
	DialVetoDuration     time.Duration // Ignore targets for this duration after dial fails

	NotFoundSRVTTL time.Duration // How long a not-found SRV is retained in the cache
	FoundSRVTTL    time.Duration // How long a found SRV is retained in the cache
	HealthTTL      time.Duration // How long a target stays in the cache
}

// cslbStats holds all the state for the cslb package. See addStats() for typical usage.
type cslbStats struct {
	StartTime       time.Time
	Duration        time.Duration // Total elapse time in DialContext
	DialContext     int           // intercepted calls to DialContext
	MissHostService int           // Host or service don't match or interception disabled
	NoSRV           int           // Times SRV lookup returned zero targets
	BestTarget      int           // Calls to bestTarget()
	DupesStopped    int           // Times that a dupe target stopped the bestTarget() iteration (all failed)
	GoodDials       int           // system DialContext returned a good connection
	FailedDials     int           // system DialContext returned an error
	Deadline        int           // Times intercept deadline expired
}

// cloneStats creates a safe copy of the stats - primarily for the status server
func (t *cslb) cloneStats() cslbStats {
	t.statsMu.RLock()
	clone := t.cslbStats
	t.statsMu.RUnlock()

	return clone
}

// addStats safely transfers a local copy of the cslbStats to the cslb's version. Rather than
// updating a cslb's stats directly, callers tend to update a local version of cslbStats then
// transfer it via addStats() to minimize locking calls (or more likely minimizing the risk of
// forgetting a locking call).
func (t *cslb) addStats(ls *cslbStats) {
	t.statsMu.Lock()
	defer t.statsMu.Unlock()

	if !ls.StartTime.IsZero() { // Nested local cslbStats must not set StartTime else we'll double count
		t.Duration += time.Now().Sub(ls.StartTime)
	}

	t.DialContext += ls.DialContext
	t.MissHostService += ls.MissHostService
	t.NoSRV += ls.NoSRV
	t.BestTarget += ls.BestTarget
	t.DupesStopped += ls.DupesStopped
	t.GoodDials += ls.GoodDials
	t.FailedDials += ls.FailedDials
	t.Deadline += ls.Deadline
}

// cslb is the main structure which holds all the state for the life of the application. The main
// reason it's a struct rather than a big lump of globals is to make it easy to test.
type cslb struct {
	config

	netResolver       limitedResolver // Replaceable functions for test mocks
	netDialer         *net.Dialer     // Not used - only here in case we later decide to modify Dialer values
	systemDialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	randIntn          func(int) int // Sufficient rand function used to select weight by bestTarget()

	srvStore    *srvCache
	healthStore *healthCache

	statusServer *statusServer // Optional status web server
	hcClient     *http.Client  // Shared Health Check Client - it purposely avoids a cslb-intercepted transport

	statsMu sync.RWMutex // Protects everything below here
	cslbStats
}

// newCslb is the cslb constructor. It must be used in preference to a raw cslb{} approach as there
// are numerous variables which must be set for any cslb methods to work.
func newCslb() *cslb {
	t := &cslb{}
	t.netResolver = net.DefaultResolver
	t.netDialer = &net.Dialer{ // Set up a net.Dialer identical to the
		Timeout:   30 * time.Second, // way that net.http does.
		KeepAlive: 30 * time.Second,
		DualStack: true,
		Resolver:  net.DefaultResolver,
	}
	t.systemDialContext = t.netDialer.DialContext
	t.randIntn = rand.Intn

	t.srvStore = newSrvCache()
	t.healthStore = newHealthCache()
	t.hcClient = &http.Client{Transport: &http.Transport{}} // Use a non-cslb http.Transport

	// Transfer in all the default config values and then over-ride them

	t.Version = Version

	t.HealthCheckTXTPrefix = defaultHealthCheckTXTPrefix
	t.HealthCheckContentOk = defaultHealthCheckContentOk
	t.HealthCheckFrequency = defaultHealthCheckFrequency
	t.InterceptTimeout = defaultInterceptTimeout
	t.DialVetoDuration = defaultDialVetoDuration

	t.NotFoundSRVTTL = defaultNotFoundSRVTTL
	t.FoundSRVTTL = defaultFoundSRVTTL
	t.HealthTTL = defaultHealthTTL

	// Check for environment variable over-rides

	flags := os.Getenv(cslbEnvPrefix + "options")
	for _, opt := range []byte(flags) {
		switch opt {
		case 'd':
			t.PrintDialContext = true
		case 'h':
			t.PrintHCResults = true
		case 'i':
			t.PrintIntercepts = true
		case 'r':
			t.PrintDialResults = true
		case 's':
			t.PrintSRVLookup = true

		case 'C':
			t.DisableInterception = true
		case 'H':
			t.DisableHealthChecks = true
		case 'N':
			t.AllowNumericServices = true
		default:
		}
	}

	e := os.Getenv(cslbEnvPrefix + "hc_ok")
	if len(e) > 0 {
		t.HealthCheckContentOk = e
	}

	t.StatusServerAddress = os.Getenv(cslbEnvPrefix + "listen")
	t.StatusServerTemplates = os.Getenv(cslbEnvPrefix + "templates")

	t.HealthCheckFrequency = getAndParseDuration(cslbEnvPrefix+"hc_freq", t.HealthCheckFrequency)
	t.InterceptTimeout = getAndParseDuration(cslbEnvPrefix+"timeout", t.InterceptTimeout)
	t.DialVetoDuration = getAndParseDuration(cslbEnvPrefix+"dial_veto", t.DialVetoDuration)

	t.NotFoundSRVTTL = getAndParseDuration(cslbEnvPrefix+"nxd_ttl", t.NotFoundSRVTTL)
	t.FoundSRVTTL = getAndParseDuration(cslbEnvPrefix+"srv_ttl", t.FoundSRVTTL)
	t.HealthTTL = getAndParseDuration(cslbEnvPrefix+"tar_ttl", t.HealthTTL)

	t.StartTime = time.Now()

	return t
}

// start starts up the cache cleaners and optionally the status web server. It is called *after* all
// config settings have been over-ridden so as to avoid any race conditions - particularly with
// tests.
func (t *cslb) start() *cslb {
	t.srvStore.start((t.FoundSRVTTL / 5) + time.Second)
	t.healthStore.start((t.HealthTTL / 5) + time.Second)

	if len(t.StatusServerAddress) > 0 {
		t.statusServer = newStatusServer(t)
		go t.statusServer.start()
	}

	return t
}

// stop stops what start started. Go figure.
func (t *cslb) stop() {
	t.srvStore.stop()
	t.healthStore.stop()

	if t.statusServer != nil {
		t.statusServer.stop(context.Background())
		t.statusServer = nil
	}
}

const (
	lowerDurationLimit = time.Second // Arbitrary limits to avoid
	upperDurationLimit = time.Hour   // absurd values being used
)

// getAndParseDuration is a helper to get the env variable and convert it to a reasonable
// duration. Returns the current value if the proposed value is outside reasonable limits.
func getAndParseDuration(name string, currValue time.Duration) time.Duration {
	e := os.Getenv(name)
	if len(e) == 0 {
		return currValue
	}
	d, err := time.ParseDuration(e)
	if err != nil {
		return currValue
	}
	if d < lowerDurationLimit || d > upperDurationLimit {
		return currValue
	}

	return d
}
