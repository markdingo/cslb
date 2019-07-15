package cslb

import (
	"fmt"
	"testing"
	"time"
)

// Much of health is checked indirectly via srv_test so this test module only tests those things
// that are missed.

// Test the health check go-routine
func TestHealthFetchAndRun(t *testing.T) {
	cslb := realInit()
	mr := newMockResolver()
	mr.appendTXT("_80"+cslb.HealthCheckTXTPrefix+"s1.example.net", []string{"http://google.com"})
	mr.appendTXT("_80"+cslb.HealthCheckTXTPrefix+"s2.example.net", []string{"http:\ngoogle.badurl.com"})
	cslb.netResolver = mr

	cslb.HealthTTL = time.Second * 5
	cslb.HealthCheckFrequency = time.Second * 5
	cslb.HealthCheckContentOk = "No Way This is in Google" // Make it highly unlikely content

	cslb.start() // Safe to start now that all configs have been set
	defer cslb.stop()

	ceh := &ceHealth{expires: time.Now().Add(time.Second * 3)}

	cslb.fetchAndRunHealthCheck(makeHealthStoreKey("s2.example.net", 80), ceh)
	if ceh.unHealthy {
		t.Error("Fetch should have failed as URL in TXT is bogus", ceh.url)
	}
	ceh = &ceHealth{expires: time.Now().Add(time.Second * 3)}
	cslb.fetchAndRunHealthCheck(makeHealthStoreKey("s1.example.net", 80), ceh)
	if !ceh.unHealthy {
		t.Error("Expected unhealthy to be set as Content should't match")
	}
	cslb.HealthCheckContentOk = "html" // Make it something google is bound to return
	ceh = &ceHealth{expires: time.Now().Add(time.Second * 3)}
	cslb.fetchAndRunHealthCheck(makeHealthStoreKey("s1.example.net", 80), ceh)
	if ceh.unHealthy {
		t.Error("Expected healthy to be set as Content should match")
	}
}

// Test that the cache cleaner works
func TestHealthCleaner(t *testing.T) {
	cslb := realInit() // Do not start cleaners automatically
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1) // Yesterday

	cslb.setDialResult(now, "residual.example.net", 443, fmt.Errorf(""))
	for ix := 0; ix < 99; ix++ {
		cslb.setDialResult(yesterday, fmt.Sprintf("%d.example.net", ix), 80, nil)
	}
	cslb.healthStore.start(time.Second / 2)
	defer cslb.healthStore.stop()

	time.Sleep(time.Second) // Give it a chance to do its job
	cslb.healthStore.RLock()
	origLen := len(cslb.healthStore.cache)
	cslb.healthStore.RUnlock()
	if origLen != 1 {
		t.Error("Expected one entry, not", origLen)
	}
}

func TestTrimTo(t *testing.T) {
	s1 := "Not truncated at all"
	s := trimTo(s1, 100)
	if s1 != s {
		t.Error("trimTo truncated when not expected to", s1, s)
	}

	s2 := "Is truncated somewhat"
	s = trimTo(s2, 10)
	if s == s2 {
		t.Error("trimTo did not truncated when expected to", s2)
	}
	if len(s) > 10 {
		t.Error("trimTo did not trim enough. Wanted 10, got", len(s), s)
	}

	s = trimTo("xxxxx", 2)
	if s != "..." {
		t.Error("Extremely short trimTo not converted to ...", s)
	}
}
