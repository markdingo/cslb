package cslb

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type extractTestCase struct {
	address    string
	host, port string // Expected values
}

var extractTestCases = []extractTestCase{
	{"example.net:80", "example.net", "80"},
	{"www.example.net:443", "www.example.net", "443"},
	{":www.example.net", "", ""},
	{"www.example.net:", "", ""},
	{"127.0.0.1", "", ""},
	{"127.0.0.1:80", "", ""},
	{"[::1]", "", ""},
	{"[::1]:80", "", ""},
	{"[fe80::3c:740d:aca7:dea0]:443", "", ""},
}

func TestExtractHostPort(t *testing.T) {
	for _, tc := range extractTestCases {
		t.Run(tc.address, func(t *testing.T) {
			h, p := extractHostPort(tc.address)
			if h != tc.host || p != tc.port {
				t.Error("Expected", tc.host, tc.port, "got", h, p)
			}
		})
	}
}

type mockDialer struct {
	mu                         sync.RWMutex // Ensure go test -race doesn't barf
	delay                      time.Duration
	conn                       net.Conn
	err                        error
	networkS, addressS         string
	networkSList, addressSList []string
}

func (t *mockDialer) network() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.networkS
}

func (t *mockDialer) networkList() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.networkSList[:]
}

func (t *mockDialer) address() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.addressS
}

func (t *mockDialer) addressList() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.addressSList[:]
}

func newMockDialer() *mockDialer {
	t := &mockDialer{}
	t.reset() // Init all lists

	return t
}

func (t *mockDialer) reset() {
	t.conn = nil
	t.err = nil
	t.networkS = ""
	t.addressS = ""
	t.networkSList = []string{}
	t.addressSList = []string{}
}

func (t *mockDialer) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	t.mu.Lock()

	t.networkS = network
	t.addressS = address
	t.networkSList = append(t.networkSList, network)
	t.addressSList = append(t.addressSList, address)
	conn := t.conn
	err := t.err
	delay := t.delay

	t.mu.Unlock() // Don't lock across sleep

	if delay > 0 {
		time.Sleep(delay)
	}

	return conn, err
}

func TestDialContext(t *testing.T) {
	cslb := realInit()
	mr := newMockResolver() // Empty DNS
	cslb.netResolver = mr
	dialer := newMockDialer()
	cslb.systemDialContext = dialer.dialContext

	cslb.start()
	defer cslb.stop()

	_, err := cslb.dialContext(context.Background(), "tcp", "localhost:81") // Not port 80 or 443
	if dialer.network() != "tcp" || dialer.address() != "localhost:81" {
		t.Error("Intercept did not call global cslb.dialContext with port 81", dialer.address(), err)
	}
	if len(mr.lastSRV) > 0 {
		t.Error("Non-standard port should not have attempted an SRV lookup", mr.lastSRV)
	}

	cslb.AllowNumericServices = true                              // Test that non-standard port is now ok
	cslb.dialContext(context.Background(), "tcp", "localhost:81") // Not port 80 or 443
	if len(mr.lastSRV) == 0 {
		t.Error("Non-standard port should have attempted an SRV lookup with AllowNumericServices set")
	}

	cslb.AllowNumericServices = false // Back to normal for rest of test

	dialer.reset()
	_, err = cslb.dialContext(context.Background(), "tcp", "localhost:80") // Should do an SRV Lookup and fail
	if dialer.network() != "tcp" || dialer.address() != "localhost:80" {
		t.Error("Intercept did not call global cslb.dialContext with port 80", dialer.address(), err)
	}
	if mr.lastSRV != "_http._tcp.localhost" {
		t.Error("Intercept did not attempt srv lookup", mr.lastSRV)
	}

	mr.appendSRV("http", "tcp", "localhost", "", 1, 1, 1)
	dialer.reset()
	_, err = cslb.dialContext(context.Background(), "tcp", "localhost:80") // Should do an SRV Lookup and fail on bestTarget()
	if dialer.network() != "tcp" || dialer.address() != "localhost:80" {
		t.Error("Intercept did not call system dialContext with port 80 after empty SRV", dialer.address(), err)
	}
	if mr.lastSRV != "_http._tcp.localhost" {
		t.Error("dial did not attempt srv lookup", mr.lastSRV)
	}

	mr.appendSRV("http", "tcp", "example.net", "realtarget", 8080, 1, 1)
	dialer.reset()
	_, err = cslb.dialContext(context.Background(), "tcp", "example.net:80") // Go all the way thru
	if dialer.network() != "tcp" || dialer.address() != "realtarget:8080" {
		t.Error("dial did not call system dialContext with realtarget:8080, rather",
			dialer.network(), dialer.address(), err)
	}
}

// Test that an iteration over all targets stops iterating and stops at the appopriate point.
func TestDialExhaustUniqueTargets(t *testing.T) {
	cslb := realInit()
	mr := newMockResolver() // Empty DNS
	cslb.netResolver = mr
	dialer := newMockDialer()
	cslb.systemDialContext = dialer.dialContext // Intercept calls to system dialer

	dialer.err = fmt.Errorf("Dial Exhaustion Mock error")
	mr.appendSRV("https", "tcp", "localhost", "s1.localhost", 4000, 0, 0)
	mr.appendSRV("https", "tcp", "localhost", "s2.localhost", 4001, 0, 0)
	mr.appendSRV("https", "tcp", "localhost", "s3.localhost", 4002, 0, 0)
	now := time.Now()

	cslb.start()
	defer cslb.stop()

	cslb.setDialResult(now.Add(-time.Second*40), "s1.localhost", 4000, dialer.err) // Comes good third
	cslb.setDialResult(now.Add(-time.Second*60), "s2.localhost", 4001, dialer.err) // Comes good first
	cslb.setDialResult(now.Add(-time.Second*50), "s3.localhost", 4002, dialer.err) // Comes good second

	// Order of bestTarget() should be s2, s1 then s3 which should show up in the mock dailer's
	// addressList.

	_, err := cslb.dialContext(context.Background(), "tcp", "localhost:443")
	if err == nil {
		t.Fatal("Expected an error return with Exhausted set")
	}
	if !strings.Contains(err.Error(), "All unique targets failed") {
		t.Error("Expected error to contain 'All unique targets...' but got", err.Error())
	}
	if len(dialer.addressList()) != 3 {
		t.Fatal("Expected three dial attempts by intercept, not", dialer.addressList())
	}
	if dialer.addressList()[0] != "s2.localhost:4001" || dialer.addressList()[1] != "s3.localhost:4002" {
		t.Error("bestTarget() did not present unhealthy in age order", dialer.addressList())
	}
}

// Test that the deadline is honoured. This also exercises the IsZero for the passed in context.
func TestDialDeadline(t *testing.T) {
	cslb := realInit()
	mr := newMockResolver() // Empty DNS
	cslb.netResolver = mr
	dialer := newMockDialer()
	dialer.delay = time.Second * 2
	dialer.err = fmt.Errorf("Dial Deadline error")
	cslb.InterceptTimeout = 5 * time.Second
	cslb.systemDialContext = dialer.dialContext // Mock up calls to the system dialer
	mr.appendSRV("https", "tcp", "localhost", "s1.localhost", 4000, 0, 0)
	mr.appendSRV("https", "tcp", "localhost", "s2.localhost", 4001, 1, 0)
	mr.appendSRV("https", "tcp", "localhost", "s3.localhost", 4002, 2, 0)

	cslb.start()
	defer cslb.stop()

	_, err := cslb.dialContext(context.Background(), "tcp", "localhost:443")
	if err == nil {
		t.Fatal("Expected a timeout error due to deadline exceeded")
	}
	if !strings.Contains(err.Error(), "deadline exceed") {
		t.Error("Expected 'deadline exceed...' error, got", err.Error())
	}

	// Should have tried all three targets

	if len(dialer.addressList()) != 3 {
		t.Error("Expected intercept to try three targets before timing out, not", len(dialer.addressList()))
	}
}

// Test that context cancel is honored.
func TestDialCancel(t *testing.T) {
	cslb := realInit()
	mr := newMockResolver() // Empty DNS
	cslb.netResolver = mr
	dialer := newMockDialer()
	dialer.delay = time.Second * 2 // Each lookup takes 2 seconds
	dialer.err = fmt.Errorf("Dial Deadline error")
	cslb.systemDialContext = dialer.dialContext // Mock up calls to the system dialer
	mr.appendSRV("https", "tcp", "localhost", "s1.localhost", 4000, 0, 0)
	mr.appendSRV("https", "tcp", "localhost", "s2.localhost", 4001, 1, 0)
	mr.appendSRV("https", "tcp", "localhost", "s3.localhost", 4002, 2, 0)

	cancelContext, cancelFunc := context.WithCancel(context.Background())

	go func() { // Trigger a cancel one second from now
		time.Sleep(time.Second)
		cancelFunc()
	}()

	cslb.start()
	defer cslb.stop()

	start := time.Now()
	_, err := cslb.dialContext(cancelContext, "tcp", "localhost:443")
	if err == nil {
		t.Fatal("Expected a timeout error due to deadline exceeded")
	}
	dur := time.Now().Sub(start)
	if dur > (time.Second * 2) { // Allow some wiggle room, but not the six seconds it would take
		t.Error("Cancel did not terminate request within 2 seconds", dur)
	}
}
