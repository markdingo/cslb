package cslb

import (
	"net/http"
)

// Enable activates cslb processing for the http.Transport. The same transport is returned as a
// convenience to the caller so they can make the Enable function part of a wrapper chain, thus:
//
//  client := &http.Client{Transport: cslb.Enable(&http.Transport{})}
//
// The Enable function replaces the http.Transport.DialContent with cslb's dialContext.
func Enable(ht *http.Transport) *http.Transport {
	ht.DialContext = getCSLB().dialContext

	return ht
}
