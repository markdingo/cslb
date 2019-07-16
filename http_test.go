package cslb

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type server struct {
	sync.Mutex
	name       string
	port       int
	get        string // Returned from a regular GET
	hc         string // Returned on the health check URL
	getHits    int
	hcHits     int
	httpServer *http.Server
}

var (
	printServerSide        bool // Tells servers to print goop
	srv1, srv2, srv3, srv4 *server
)

func trunc(s string) string {
	if len(s) > 100 {
		s = s[0:99] + "..."
	}

	return strings.ReplaceAll(s, "\n", " ") + "\n"
}

func makeHTTPServer(srv *server, startHC bool) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		srv.Lock()
		defer srv.Unlock()
		if printServerSide {
			fmt.Println(srv.name, "hit. Have:", srv.get)
		}
		io.WriteString(w, "Hello from ")
		io.WriteString(w, srv.name)
		io.WriteString(w, " I have: ")
		io.WriteString(w, srv.get)
		io.WriteString(w, "\n")
		srv.getHits++
	})

	if startHC {
		mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) {
			srv.Lock()
			defer srv.Unlock()
			if printServerSide {
				fmt.Println(srv.name, "HC Hit. Have", srv.hc)
			}
			io.WriteString(w, "Hello from ")
			io.WriteString(w, srv.name)
			io.WriteString(w, " I have: ")
			io.WriteString(w, srv.hc)
			io.WriteString(w, "\n")
			srv.hcHits++
		})
	}

	srv.httpServer = &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", srv.port), Handler: mux}
}

// Run four real servers on localhost ports 5001-5004
func startAllServers(startHC bool) {
	srv1 = &server{name: "s1", port: 5001, get: "ONE", hc: "OK"}
	srv2 = &server{name: "s2", port: 5002, get: "TWO", hc: "OK"}
	srv3 = &server{name: "s3", port: 5003, get: "THREE", hc: "OK"}
	srv4 = &server{name: "s4", port: 5004, get: "FOUR", hc: "OK"}
	makeHTTPServer(srv1, startHC)
	makeHTTPServer(srv2, startHC)
	makeHTTPServer(srv3, startHC)
	makeHTTPServer(srv4, startHC)
	go runServer(srv1)
	go runServer(srv2)
	go runServer(srv3)
	go runServer(srv4)
	time.Sleep(time.Second / 2) // Given them a chance to listen
}

func runServer(srv *server) {
	err := srv.httpServer.ListenAndServe()
	if err != nil {
		if !strings.Contains(err.Error(), "Server closed") {
			fmt.Println(err)
		}
	}
}

func stopAllServers() {
	srv1.httpServer.Shutdown(context.Background())
	srv2.httpServer.Shutdown(context.Background())
	srv3.httpServer.Shutdown(context.Background())
	srv4.httpServer.Shutdown(context.Background())
}

// Test cslb with servers shutting down and thus having connections fail. This is a real E2E test.
func TestHTTPServerShutdowns(t *testing.T) {
	startAllServers(false)
	defer stopAllServers()

	mr := newMockResolver() // Construct DNS entries used by intercept

	// Randomized order should not affect results
	mr.appendSRV("http", "tcp", "example.net", "localhost", 5001, 10, 10) // srv1
	mr.appendSRV("http", "tcp", "example.net", "localhost", 5004, 40, 10) // srv4
	mr.appendSRV("http", "tcp", "example.net", "localhost", 5003, 20, 10) // srv3
	mr.appendSRV("http", "tcp", "example.net", "localhost", 5002, 20, 10) // srv2
	url := "http://example.net/"

	cslb := realInit()
	cslb.netResolver = mr
	/*
		cslb.PrintDialContext = true
		cslb.PrintIntercepts = true
		cslb.PrintSRVLookup = true
		cslb.PrintDialResults = true
	*/

	cslb.start()
	defer cslb.stop()

	start := time.Now()
	get(t, url)        // Should latch on to srv1 as that has the lowest priority
	str := get(t, url) // Should still be srv1
	if !strings.Contains(str, srv1.get) {
		t.Error("Expected", srv1.get, "got", trunc(str))
	}
	elapse := time.Now().Sub(start)
	if elapse > time.Second {
		t.Error("Get to all up servers took way too long", elapse)
	}

	srv1.httpServer.Shutdown(context.Background()) // Kill srv1 so cslb is forced to move to srv2/srv3

	start = time.Now()
	str = get(t, url)
	if !strings.Contains(str, "TWO") && !strings.Contains(str, "THREE") {
		t.Error("Expected srv2 or srv3 to respond with srv1 shut down, but got", trunc(str))
	}
	elapse = time.Now().Sub(start)
	if elapse > time.Second {
		t.Error("Get after srv1 downed took way too long", elapse)
	}

	srv2.httpServer.Shutdown(context.Background()) // Kill all servers except srv4
	srv3.httpServer.Shutdown(context.Background())

	start = time.Now()
	str = get(t, url)
	if !strings.Contains(str, "FOUR") {
		t.Error("Expected srv4 to respond, but got", trunc(str))
	}
	elapse = time.Now().Sub(start)
	if elapse > time.Second {
		t.Error("Get after srv1, 2 &3 downed took way too long", elapse)
	}
}

// Test cslb with real servers and running HC on them to make sure HC turns off "downed" servers
func TestHTTPHealthCheckFailures(t *testing.T) {
	startAllServers(true)
	defer stopAllServers()

	mr := newMockResolver() // Construct DNS entries used by intercept

	// Randomized order should not affect results
	mr.appendSRV("http", "tcp", "example.net", "localhost", 5001, 10, 10) // srv1
	mr.appendSRV("http", "tcp", "example.net", "localhost", 5004, 40, 10) // srv4
	mr.appendSRV("http", "tcp", "example.net", "localhost", 5003, 20, 10) // srv3
	mr.appendSRV("http", "tcp", "example.net", "localhost", 5002, 20, 10) // srv2
	mr.appendTXT("_5001._cslb.localhost", []string{"http", "://127.0.0.1:5001/", "health"})
	mr.appendTXT("_5002._cslb.localhost", []string{"http://127.0", ".0.1:5002/health"})
	mr.appendTXT("_5003._cslb.localhost", []string{"http://127.0.0.1:500", "3/health"})
	mr.appendTXT("_5004._cslb.localhost", []string{"http://127.0.0.1:5004/healt", "h"})
	url := "http://example.net/"

	cslb := realInit()
	cslb.netResolver = mr
	cslb.HealthCheckFrequency = time.Second // HC should hit every second
	cslb.start()
	defer cslb.stop()

	str := get(t, url) // Should be ONE, but we only care that it's something
	if len(str) == 0 {
		t.Error("Did not get a response from any server. They should all be running")
	}

	// The get will have caused a DNS lookup of the SRV which in turn would have populated the
	// healthStore which in turn would have looked up the TXT RRs for the HC URLs and started
	// running the HCs. Since the HCs have "OK" set they should be setting the targets as good -
	// even if we set them down the subsequent HC should over-ride them. After all, a connection
	// is just a TCP 3-way handshake, not a successful HTTP exchange.

	// To test that the HCs are running we should see the hcHits increase from zero

	time.Sleep(time.Second * 4)

	srv1.Lock() // Bah humbug. Keep go test -race quiet. Not really a meanful race, but still...
	if srv1.hcHits < 2 {
		t.Error("srv1 hcHits too small at", srv1.hcHits)
	}
	srv1.Unlock()

	srv2.Lock()
	if srv2.hcHits < 2 {
		t.Error("srv2 hcHits too small at", srv2.hcHits)
	}
	srv2.Unlock()

	srv3.Lock()
	if srv3.hcHits < 2 {
		t.Error("srv3 hcHits too small at", srv3.hcHits)
	}
	srv3.Unlock()

	srv4.Lock()
	if srv4.hcHits < 2 {
		t.Error("srv4 hcHits too small at", srv4.hcHits)
	}
	srv4.Unlock()

	// Shutdown srv1 and rotate out srv2 & 3

	srv1.httpServer.Shutdown(context.Background())
	srv2.Lock()
	srv2.hc = "Bad"
	srv2.Unlock()

	srv3.Lock()
	srv3.hc = "Bad"
	srv3.Unlock()

	time.Sleep(time.Second * 2) // Give health check time to notice
	str = get(t, url)           // get should now hit srv4

	if !strings.Contains(str, "FOUR") {
		t.Error("Expected srv4 to respond with srv2, 3 HC down, but got", trunc(str))
	}
}

func get(t *testing.T, url string) string {
	resp, err := http.Get(url)
	if err != nil {
		t.Log("Error:", err)
		return ""
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	resp.Body.Close()

	return string(body)
}
