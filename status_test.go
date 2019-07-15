package cslb

import (
	"context"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStatusTemplates(t *testing.T) {
	ss := newStatusServer(newCslb())
	for _, tn := range []string{"config", "cslb", "srv", "health"} { // Check that all templates have parsed ok
		tmpl := ss.allTmpl.Lookup(tn)
		if tmpl == nil {
			t.Error("Template", tn, "missing from parsed template allTmpl")
		}
	}

	if ss.trailerTmpl.Lookup("trailer") == nil {
		t.Error("Template 'trailer' missing from parsed template trailerTmpl")
	}
}

const (
	sssListen = "127.0.0.1:55080"
)

// Test that the web server starts and ostensibly serves the intended web page
func TestStatusStartStop(t *testing.T) {
	cslb := newCslb()
	cslb.StatusServerAddress = sssListen
	ss := newStatusServer(cslb)
	go ss.start()
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

	if !strings.Contains(str, "brought to you by") {
		t.Error("GET of status page did not return trailer 'brought to you by'", trimTo(str, 200))
	}
	ss.stop(context.Background())
	time.Sleep(time.Second)
	resp, err = http.Get("http://" + sssListen + "/") // Should get a connection failed if stop() worked
	if err == nil {
		t.Error("Expected 'Connection refused' since server is meant to have stopped")
	}
}

// Check that the SRV/HC caches are represented in the status server output
func TestStatusCacheEntries(t *testing.T) {
	cslb := newCslb()
	cslb.InterceptTimeout = time.Second
	cslb.StatusServerAddress = sssListen
	mr := newMockResolver()
	mr.appendSRV("http", "tcp", "localhost", "localhost", 50088, 1, 1)
	mr.appendSRV("https", "tcp", "notfound.example.net", "", 0, 0, 0)
	mr.appendTXT("_50088"+cslb.HealthCheckTXTPrefix+"localhost", []string{"http://google.com"})
	cslb.netResolver = mr
	cslb.start()
	defer cslb.stop()

	cslb.dialContext(context.Background(), "tcp", "localhost:80")
	cslb.dialContext(context.Background(), "tcp", "notfound.example.net:443")

	time.Sleep(2 * time.Second) // Give both status server and HC a chance to get started

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
	for _, expect := range []string{"_http._tcp.localhost", "http://google.com", "**NXDomain**"} {
		if !strings.Contains(str, expect) {
			t.Error("Status page", sssListen, "does not have", expect, trimTo(str, 300))
		}
	}
}
