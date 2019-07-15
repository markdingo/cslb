package cslb

/*

globals and the manipulation of the current cslb predominantly exist for tests. If we didn't have
tests, globals would solely consist of an init() function something like:

func init() {
        currentCSLB = newCslb()
	Enable(http.DefaultTransport.(*http.Transport))
}

because in a normal application only one cslb is created and it lives for the life of the program.

*/

import (
	"net/http"
	"sync"
)

var (
	cslbMu      sync.RWMutex // For go test -race as tests creates new cslbs so they can
	currentCSLB *cslb        // be sure they are working with a known initial state.
)

// init enables the http DefaultTransport for CSLB processing. Perhaps we should have an env
// variable to control this or possibly not even enable it by default?
func init() {
	realInit().start()
}

// realInit is separated out from init() so tests can call it multiple times without knowing the
// innards of what is needed to reset the globals to their initial state.
func realInit() *cslb {
	cslb := setCSLB(newCslb())
	Enable(http.DefaultTransport.(*http.Transport))

	return cslb
}

func setCSLB(c *cslb) *cslb {
	cslbMu.Lock()
	defer cslbMu.Unlock()

	currentCSLB = c

	return c
}

func getCSLB() *cslb {
	cslbMu.RLock()
	defer cslbMu.RUnlock()

	return currentCSLB
}
