package cslb

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// dialResult is passed thru a channel back to the interceptor
type dialResult struct {
	conn net.Conn
	err  error
}

// dialContext replaces the DialContext function of net.Dialer used in http.Transport. It looks up
// the deduced SRV in the DNS and if present, runs the load-balancing logic against the returned
// targets before calling net.DialContext. Multiple connection attempts to different targets are
// tried in an effort to select a functioning target.
//
// dialContext effectively implements what http clients should have implemented years ago but the
// http crowd seem very reluctant to add latency to each web request by precending it with an
// additional DNS lookup so it hasn't happened thus far. Maybe the proposed HTTPSSVC, or whatever
// it ends up being, will solve that problem? We'll see.
//
// If the supplied context contains a deadline dialContext honors that deadline, otherwise it
// creates a "WithTimeout" context using the configure deadline. Unlike net.Dialer.DialContext the
// deadline is not amortized across all targets. In part because we want to prefer the earlier
// targets because that's how we've been instructed via the DNS; in part because we don't really
// know how many address records there are across the different targets and finally in part because
// a large number of targets implies an absurdly small amortised deadline per target - particularly
// as net.Dialer.DialContext is doing its own amortization per target of our amortization. All of
// which can be coded around to arrive at a workable compromise, but it's unclear the additional
// complexity buys us very much and determining the benefit is tough.
func (t *cslb) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var ls cslbStats      // Accumulate stats locally then
	defer t.addStats(&ls) // transfer to cslb at the end

	ls.DialContext++
	host, port := extractHostPort(strings.ToLower(address)) // Slough off trailing :port
	if t.PrintDialContext {
		fmt.Println("cslb.dialContext:intercept", network, address, "gives", host, "and", port)
	}

	// Convert port back to a service. This is error prone as there is not necessarily any
	// correlation between the two. E.g. with http.Get("https://example.net:80/resource") the
	// conversion results in a look up _http._tcp.example.net which is unlikely to be what the
	// caller wanted, but what can you do? The problem is that the scheme on the original URL is
	// not visible to us in any way. Hardly surprising since net.DialContent is a generalized
	// service. The only real solution is if the net/http package were to introduce it's own
	// dialer interface which includes scheme and port.

	service := ""
	switch port { // Map services that we can enable (which is only net/http for now)
	case "80":
		service = "http"
	case "443":
		service = "https"
	default:
		if t.AllowNumericServices { // Are we allowed to try _1443._tcp.$domain ?
			service = port
		}
	}

	// Everything has to be "just right" before we run the intercept logic. If not, pass thru to
	// the system dialContext and fuggedaboutit!
	if len(host) == 0 || len(service) == 0 || t.DisableInterception {
		ls.MissHostService++
		return t.systemDialContext(ctx, network, address)
	}

	now := time.Now()
	ls.StartTime = now

	// If the supplied context does not have a deadline, derive a timeout context and set it
	// with our default deadline.

	if deadline, ok := ctx.Deadline(); !ok || deadline.IsZero() {
		subCtx, cancel := context.WithTimeout(ctx, t.InterceptTimeout)
		defer cancel()
		ctx = subCtx // The timeout context becomes our default context
	}

	cesrv := t.lookupSRV(ctx, now, service, network, host)
	if t.PrintSRVLookup {
		fmt.Println("cslb.dialContext:lookupSRV", service, network, host, cesrv.uniqueTargets(), cesrv)
	}
	if cesrv.uniqueTargets() == 0 { // Empty or non-existent SRV means use system
		ls.NoPTR++
		return t.systemDialContext(ctx, network, address)
	}

	// Because we need to select on the cancel channel, run the iteration in a separate
	// go-routine and have it return the results via a channel that we can also select on. The
	// dialIterate function is responsible for closing the channel to ensure we don't leak.

	returned := make(chan dialResult)
	go t.dialIterate(ctx, cesrv, network, address, returned)
	select {
	case result := <-returned: // Some sort of response from dialIterate
		return result.conn, result.err

	case <-ctx.Done(): // Cancel or deadline exceeded
		return nil, ctx.Err()
	}

	// NOT REACHED
}

// dialIterate iterates over bestTargets until we get a good connection, run out of time or run out
// of unique targets. Because a failed target is put at the bottom of the pile in terms of isGood()
// and nextDialAttempt it should only recur if bestTarget() has cycled thru *all* possible good
// targets and all targets with a closer nextDialAttempt.
//
// Results are returned via the result channel as we're started as a separate go-routine.
func (t *cslb) dialIterate(ctx context.Context, cesrv *ceSRV, network, address string, result chan dialResult) {
	var ls cslbStats      // We do not set StartTime for nested stats
	defer t.addStats(&ls) // Transfer counters back to the parent when we're done
	defer close(result)   // We're responsible for closing the dialResult channel

	dupes := make(map[string]bool) // Track targets to detect bestTarget() cycling
	for {
		ls.BestTarget++
		srv := t.bestTarget(cesrv) // Returns a single synthesized *net.SRV with target
		newAddress := fmt.Sprintf("%s:%d", srv.Target, int(srv.Port))
		if dupes[newAddress] { // If we've iterated over all targets, stop
			ls.DupesStopped++
			result <- dialResult{nil,
				fmt.Errorf("cslb: All unique targets failed for %s. Tried: %d", address, len(dupes))}
			return
		}
		dupes[newAddress] = true
		if t.PrintIntercepts {
			fmt.Println("cslb.dialContext:SRV", address, "to target", newAddress)
		}
		nc, err := t.systemDialContext(ctx, network, newAddress)
		now := time.Now()
		t.setDialResult(now, srv.Target, int(srv.Port), err)
		if err == nil { // Success!
			ls.GoodDials++
			result <- dialResult{nc, nil}
			return
		}
		ls.FailedDials++
	}

	// NOT REACHED
}

// extractHostPort extracts the hostname from the address, if there is one.  Possible inputs are:
// example.com:80, 127.0.0.1:80 and [::1]:443 only the first of which returns a non-zero host of
// "example.com".
func extractHostPort(address string) (host, port string) {
	if address[0] == '[' { // Does it look like a wrapped ipv6 address?
		return
	}
	lastColon := strings.LastIndex(address, ":")
	if lastColon < 1 { // Unrecognized format
		return
	}
	if lastColon+1 == len(address) { // Unrecognized format - trailing colon with no port
		return
	}

	host = address[:lastColon]
	port = address[lastColon+1:]
	ip := net.ParseIP(host) // Easiest way to determine whether hostname or IP address
	if ip != nil {
		host = ""
		port = ""
	}
	return
}
