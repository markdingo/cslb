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

type mockResolver struct {
	mu      sync.Mutex // So go test -race doesn't complain
	srvs    map[string][]*net.SRV
	txts    map[string][]string
	lastSRV string
	lastTXT string
}

func newMockResolver() *mockResolver {
	return &mockResolver{srvs: make(map[string][]*net.SRV), txts: make(map[string][]string)}
}

// append the target to the srv. Last append is always at the end to tests can rely on position.
func (t *mockResolver) appendSRV(service, proto, name string, target string, port, priority, weight int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cname := ""
	if len(service) != 0 || len(proto) != 0 {
		cname = "_" + service + "._" + proto + "."
	}
	cname += name
	ar, ok := t.srvs[cname]
	if !ok {
		ar = make([]*net.SRV, 0)
	}
	if len(target) > 0 {
		ar = append(ar, &net.SRV{Target: target, Port: uint16(port),
			Priority: uint16(priority), Weight: uint16(weight)})
		t.srvs[cname] = ar
	}
}

func (t *mockResolver) LookupSRV(ctx context.Context, service, proto, name string) (cname string, srvs []*net.SRV, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	qName := ""
	if len(service) != 0 || len(proto) != 0 {
		qName = "_" + service + "._" + proto + "."
	}
	qName += name
	t.lastSRV = qName
	srvs, ok := t.srvs[qName]
	if !ok {
		err = fmt.Errorf("mock LookupSRV not found for %s", qName)
		return
	}
	cname = qName

	return
}

func (t *mockResolver) appendTXT(target string, txts []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.txts[target] = txts
}

func (t *mockResolver) LookupTXT(ctx context.Context, qName string) (txts []string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	txts, ok := t.txts[qName]
	t.lastTXT = qName
	if !ok {
		err = fmt.Errorf("mock LookupTXT not found for %s", qName)
	}

	return
}

func makeMockResolver() *mockResolver {
	mr := newMockResolver()
	mr.appendSRV("http", "tcp", "example.net", "t1.example.net", 1, 10, 20) // port used to codify bestTarget() order
	mr.appendSRV("http", "tcp", "example.net", "t2.example.net", 1, 10, 20)
	mr.appendSRV("http", "tcp", "example.net", "t3.example.net", 1, 10, 30)
	mr.appendSRV("http", "tcp", "example.net", "t4.example.net", 1, 10, 40)
	mr.appendSRV("http", "tcp", "example.net", "t5.example.net", 2, 11, 1)
	mr.appendSRV("http", "tcp", "example.net", "t6.example.net", 2, 11, 1)
	mr.appendSRV("http", "tcp", "example.net", "t7.example.net", 2, 11, 2)
	mr.appendSRV("http", "tcp", "example.net", "t8.example.net", 2, 11, 10)
	mr.appendSRV("http", "tcp", "example.net", "t9.example.net", 3, 12, 20)
	mr.appendSRV("http", "tcp", "example.net", "t10.example.net", 3, 12, 30)
	mr.appendSRV("http", "tcp", "example.net", "t11.example.net", 3, 12, 40)
	mr.appendSRV("http", "tcp", "example.net", "t12.example.net", 3, 12, 50)
	mr.appendSRV("http", "tcp", "example.net", "t13.example.net", 3, 12, 60)
	mr.appendSRV("http", "tcp", "example.net", "t14.example.net", 3, 12, 70)
	mr.appendSRV("http", "tcp", "example.net", "t15.example.net", 3, 12, 80)
	mr.appendSRV("http", "tcp", "example.net", "t16.example.net", 4, 13, 90)
	mr.appendSRV("http", "tcp", "example.net", "t17.example.net", 4, 13, 91)
	mr.appendSRV("http", "tcp", "example.net", "t18.example.net", 4, 13, 92)
	mr.appendSRV("http", "tcp", "example.net", "t19.example.net", 4, 13, 93)
	mr.appendSRV("http", "tcp", "example.net", "t20.example.net", 4, 13, 94)

	mr.appendSRV("https", "udp", "example.com", "u1.example.com", 1443, 13, 10)
	mr.appendSRV("https", "udp", "example.com", "u2.example.com", 1444, 13, 20)
	mr.appendSRV("https", "udp", "example.com", "u3.example.com", 1444, 13, 30)
	mr.appendSRV("https", "udp", "example.com", "u4.example.com", 1444, 13, 0)
	mr.appendSRV("https", "udp", "example.com", "u5.example.com", 1444, 13, 0)
	mr.appendSRV("https", "udp", "example.com", "u6.example.com", 1444, 13, 0)
	mr.appendSRV("https", "udp", "example.com", "", 1444, 13, 100) // Should disappear completely
	mr.appendSRV("https", "udp", "example.com", "u7.example.com", 1444, 14, 0)

	mr.appendSRV("http", "tcp", "empty.example.org", "", 0, 0, 0)

	return mr
}

type srvTestCase struct {
	service, proto, domain string
	srvCount               int
	target                 string
	bestCount              int    // How many calls to bestTarget before target expected to show up
	never                  string // target which should never be returned
}

var srvTestCases = []srvTestCase{
	{"http", "tcp", "example.net", 20, "t1.example.net", 100, "t20.example.net"},
	{"https", "udp", "example.com", 7, "u4.example.com", 6500, "u7.example.com"}, // 0.1% / 3 is the weight
}

func TestSRVPopulate(t *testing.T) {
	cslb := realInit()
	cslb.netResolver = makeMockResolver()
	cslb.start()

	for _, tc := range srvTestCases {
		t.Run(tc.service+"_"+tc.proto+"_"+tc.domain, func(t *testing.T) {
			cesrv := cslb.lookupSRV(context.Background(), time.Now(), tc.service, tc.proto, tc.domain)
			if cesrv.uniqueTargets() != tc.srvCount {
				t.Error("SRV Count mismatch. Expected", tc.srvCount, "got", cesrv.uniqueTargets())
			}
			distrib := make(map[string]int)
			for ix := 0; ix < tc.bestCount; ix++ {
				srv := cslb.bestTarget(cesrv)
				if srv == nil {
					t.Fatal("bestTarget() returned nil for", tc.domain)
				}
				distrib[srv.Target]++
			}
			if distrib[tc.target] == 0 {
				t.Error("Expected", tc.target, "to have been bestTarget() at least once")
				t.Log(cesrv)
				for k, v := range distrib {
					t.Log(k, v)
				}
			}
			if distrib[tc.never] > 0 {
				t.Error("Never expected", tc.never, "to be returned as best, but", distrib[tc.never])
				t.Log(cesrv)
				for k, v := range distrib {
					t.Log(k, v)
				}
			}
		})
	}
}

// Test that bestTarget() distributes targets according to their weights. At least roughly
// proportionally within the limits of the PRNG. example.com SRV has u1=10, u2=20, u3=30 and u4-u7=0
// thus u3 > u2 > u1 > (u4-u7).
func TestSRVWeightDistribution(t *testing.T) {
	cslb := realInit()
	cslb.netResolver = makeMockResolver()
	cslb.start()

	cesrv := cslb.lookupSRV(context.Background(), time.Now(), "https", "udp", "example.com")
	distrib := make(map[string]int)
	for ix := 0; ix < 1000; ix++ {
		srv := cslb.bestTarget(cesrv)
		distrib[srv.Target]++
	}
	u1 := distrib["u1.example.com"]
	u2 := distrib["u2.example.com"]
	u3 := distrib["u3.example.com"]
	u4 := distrib["u4.example.com"]
	u5 := distrib["u5.example.com"]
	u6 := distrib["u6.example.com"]
	u7 := distrib["u7.example.com"]
	if !(u3 > u2) {
		t.Error("Expected u3 GT u2", u3, u2)
	}
	if !(u2 > u1) {
		t.Error("Expected u2 GT u1", u2, u1)
	}
	if u4 > u1 || u5 > u1 || u6 > u1 || u7 > u1 {
		t.Error("Expected u1 GT U4-u7", u1, u4, u5, u6, u7)
	}
}

// Test that failed targets are not considered by lookupSRV.
func TestSRVHealth(t *testing.T) {
	cslb := realInit()
	cslb.netResolver = makeMockResolver()
	cslb.randIntn = func(int) int { return 0 }
	cslb.start()

	cesrv := cslb.lookupSRV(context.Background(), time.Now(), "https", "udp", "example.com")
	srv := cslb.bestTarget(cesrv) // nextRand is zero so first weight should win
	if srv == nil {
		t.Fatal("Setup error")
	}
	if srv.Target != "u1.example.com" {
		t.Error("bestTarget should be u1, not", srv)
	}
	fakeNow := time.Now().Add(time.Minute) // Put it sufficiently in the future
	nowPlusOne := fakeNow.Add(time.Hour)
	cslb.setDialResult(nowPlusOne, "u1.example.com", int(srv.Port), fmt.Errorf(""))
	srv = cslb.bestTarget(cesrv) // nextRand is zero but first is down
	if srv == nil {
		t.Fatal("Setup error")
	}
	if srv.Target != "u2.example.com" {
		t.Error("bestTarget should now be u2, not", srv)
	}
	cslb.setDialResult(nowPlusOne, "u2.example.com", int(srv.Port), fmt.Errorf(""))
	srv = cslb.bestTarget(cesrv)
	if srv == nil {
		t.Fatal("Setup error")
	}
	if srv.Target != "u3.example.com" {
		t.Error("bestTarget should now be u3, not", srv)
	}

	cslb.setDialResult(nowPlusOne, "u3.example.com", int(srv.Port), fmt.Errorf("")) // Last of the highest priority targets
	srv = cslb.bestTarget(cesrv)
	if srv == nil {
		t.Fatal("Setup error")
	}
	if srv.Target != "u4.example.com" { // Should get next priority down as second choice
		t.Error("bestTarget should now be u4, not", srv)
	}

	cslb.setDialResult(nowPlusOne, "u4.example.com", int(srv.Port), fmt.Errorf(""))
	cslb.setDialResult(fakeNow, "u5.example.com", 1444, fmt.Errorf("")) // Closest to now
	cslb.setDialResult(nowPlusOne, "u6.example.com", 1444, fmt.Errorf(""))

	srv = cslb.bestTarget(cesrv)
	if srv == nil {
		t.Fatal("Setup error")
	}
	if srv.Target != "u7.example.com" { // Last second choice
		t.Error("bestTarget should now be u7, not", srv)
	}
	cslb.setDialResult(nowPlusOne, "u7.example.com", int(srv.Port), fmt.Errorf("")) // Every Target is now in bad health

	srv = cslb.bestTarget(cesrv) // Should now get least-worst
	if srv == nil {
		t.Fatal("Setup error")
	}
	if srv.Target != "u5.example.com" { // Closet to now
		t.Error("bestTarget should now be u5, not", srv)
	}
}

func TestSRVNoFind(t *testing.T) {
	cslb := realInit()
	mr := makeMockResolver()
	cslb.netResolver = mr
	cslb.start()

	cesrv := cslb.lookupSRV(context.Background(), time.Now(), "http", "tcp", "empty.example.org")
	srv := cslb.bestTarget(cesrv) // nextRand is zero so first weight should win
	if srv != nil {
		t.Error("Should have got nil for bestTarget as SRV is empty. Got", srv)
	}

	// Test Cache hit while we're here

	mr.appendSRV("http", "tcp", "empty.example.org", "e1.example.org", 0, 0, 0) // Now in DNS
	cesrv = cslb.lookupSRV(context.Background(), time.Now(), "http", "tcp", "empty.example.org")
	srv = cslb.bestTarget(cesrv) // nextRand is zero so first weight should win
	if srv != nil {
		t.Error("Should have still got nil for bestTarget as SRV is in cache. Got", srv)
	}

}

func TestSRVString(t *testing.T) {
	cslb := realInit()
	cslb.netResolver = makeMockResolver()
	cslb.start()
	cesrv := cslb.lookupSRV(context.Background(), time.Now(), "https", "udp", "example.com")
	s := cesrv.String()
	c := strings.Count(s, "tarw=")
	if c != cesrv.uniqueTargets() {
		t.Error("Expected", cesrv.uniqueTargets(), "tarw= patterns, but got", c)
	}
}

// Test that the cache cleaner is expiring entries
func TestSRVcleaner(t *testing.T) {
	cslb := realInit()
	cslb.netResolver = makeMockResolver()
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1) // Yesterday

	cslb.lookupSRV(context.Background(), now, "http", "tcp", "keep.expire.example.com")
	for ix := 0; ix < 99; ix++ {
		cslb.lookupSRV(context.Background(), yesterday, "http", "tcp",
			fmt.Sprintf("%d.expire.example.com", ix))
	}
	cslb.srvStore.start(time.Second / 2)
	defer cslb.srvStore.stop()

	time.Sleep(time.Second) // Give cleaner time to run
	cslb.srvStore.RLock()
	origLen := len(cslb.srvStore.cache)
	cslb.srvStore.RUnlock()
	if origLen != 1 {
		t.Error("Expected one entry, not", origLen)
	}
}
