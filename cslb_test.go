package cslb

import (
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func unsetAll() {
	os.Unsetenv(cslbEnvPrefix + "options")
	os.Unsetenv(cslbEnvPrefix + "hc_ok")

	os.Unsetenv(cslbEnvPrefix + "dial_veto")
	os.Unsetenv(cslbEnvPrefix + "hc_freq")
	os.Unsetenv(cslbEnvPrefix + "nxd_ttl")
	os.Unsetenv(cslbEnvPrefix + "srv_ttl")
	os.Unsetenv(cslbEnvPrefix + "tar_ttl")
	os.Unsetenv(cslbEnvPrefix + "timeout")
}

// Test that newCslb notices good env variables. This blows away any env variables that might have
// been inherited by the test executable.
func TestCSLBGoodOptions(t *testing.T) {
	os.Setenv(cslbEnvPrefix+"options", "dhisHCN")
	os.Setenv(cslbEnvPrefix+"hc_ok", "BIG OK")

	os.Setenv(cslbEnvPrefix+"dial_veto", "5m")
	os.Setenv(cslbEnvPrefix+"hc_freq", "10m")
	os.Setenv(cslbEnvPrefix+"nxd_ttl", "15m")
	os.Setenv(cslbEnvPrefix+"srv_ttl", "20m")
	os.Setenv(cslbEnvPrefix+"tar_ttl", "25m")
	os.Setenv(cslbEnvPrefix+"timeout", "30m")

	cslb := newCslb()
	if !cslb.PrintHCResults || !cslb.PrintIntercepts || !cslb.PrintSRVLookup || !cslb.PrintDialContext ||
		!cslb.DisableHealthChecks || !cslb.DisableInterception || !cslb.AllowNumericServices {
		t.Error("At least one option not set", cslb.config)
	}

	if cslb.HealthCheckContentOk != "BIG OK" {
		t.Error("HealthCheckContentOk not set")
	}
	if cslb.DialVetoDuration != time.Minute*5 {
		t.Error("DialVetoDuration not set")
	}
	if cslb.HealthCheckFrequency != time.Minute*10 {
		t.Error("HealthCheckFrequency not set")
	}
	if cslb.NotFoundSRVTTL != time.Minute*15 {
		t.Error("NotFoundSRVTTL not set")
	}
	if cslb.FoundSRVTTL != time.Minute*20 {
		t.Error("FoundSRVTTL not set")
	}
	if cslb.HealthTTL != time.Minute*25 {
		t.Error("HealthTTL not set")
	}
	if cslb.InterceptTimeout != time.Minute*30 {
		t.Error("InterceptTimeout not set")
	}

	unsetAll()
}

func TestCSLBBadOptions(t *testing.T) {
	os.Setenv(cslbEnvPrefix+"options", "xxXX")

	os.Setenv(cslbEnvPrefix+"dial_veto", "0s") // Cover
	os.Setenv(cslbEnvPrefix+"hc_freq", "2h")     // all
	os.Setenv(cslbEnvPrefix+"nxd_ttl", "junk") // error paths
	os.Setenv(cslbEnvPrefix+"srv_ttl", "junk")
	os.Setenv(cslbEnvPrefix+"tar_ttl", "junk")
	os.Setenv(cslbEnvPrefix+"timeout", "junk")

	cslb := newCslb()
	if cslb.PrintHCResults || cslb.PrintIntercepts || cslb.PrintSRVLookup || cslb.PrintDialContext ||
		cslb.DisableHealthChecks || cslb.DisableInterception {
		t.Error("At least one option unexpectedly set", cslb.config)
	}

	if cslb.DialVetoDuration != defaultDialVetoDuration {
		t.Error("DialVetoDuration was set")
	}
	if cslb.HealthCheckFrequency != defaultHealthCheckFrequency {
		t.Error("HealthCheckFrequency was set")
	}
	if cslb.NotFoundSRVTTL != defaultNotFoundSRVTTL {
		t.Error("NotFoundSRVTTL was set")
	}
	if cslb.FoundSRVTTL != defaultFoundSRVTTL {
		t.Error("FoundSRVTTL was set")
	}
	if cslb.HealthTTL != defaultHealthTTL {
		t.Error("HealthTTL was set")
	}
	if cslb.InterceptTimeout != defaultInterceptTimeout {
		t.Error("InterceptTimeout was set")
	}

	unsetAll()
}

func TestCloneStats(t *testing.T) {
	cslb := newCslb()

	var ls cslbStats
	ls.FailedDials = 23
	ls.Deadline = 12
	ls.DialContext = 101

	cslb.addStats(&ls)

	s := cslb.cloneStats()

	if s.FailedDials != ls.FailedDials || s.Deadline != ls.Deadline || s.DialContext != ls.DialContext {
		t.Error("cloneStats does not agree with added stats", ls, s)
	}
}

func TestCslbStartStop(t *testing.T) {
	cslb := newCslb()
	cslb.StatusServerAddress = sssListen
	cslb.start()
	time.Sleep(time.Second) // Give server a chance to start

	resp, err := http.Get("http://" + sssListen + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	str := string(body)
	if !strings.Contains(str, "Client Side Load Balancing") {
		t.Error("GET of status page did not return title 'Client Side Load Balancing'", trimTo(str, 200))
	}

	cslb.stop()
	time.Sleep(time.Second)

	resp, err = http.Get("http://" + sssListen + "/")
	if err == nil {
		t.Error("Expected connection refused after cslb.stop()")
	}
}
