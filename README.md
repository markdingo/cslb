## CSLB - A Go Client-Side Load-Balancer for HTTP/HTTPS

Cslb is a client-side load-balancer for Go HTTP/HTTPS applications. Cslb is an alternative to
server-side load-balancers which add deployment and diagnostic complexity, cost, throughput
constraints and which also create an additional point of possible failure. Cslb puts load-balancer
intelligence into your Go clients so you can simplify your deployment and potentially eliminate
server-side load-balancers.

In many cases the only action needed to take advantage of cslb is to import the package and add an
SRV entry to your DNS. At that point, on behalf of your application, cslb automatically deals with
failed servers and spreads load across serving targets according to your load-distribution rules. In
addition, once you have cslb in place you can also run a "canary" alerting service which can
notify you when clients are failing to reach their correct services.

The primary goal of cslb is to make client-side load-balancing a no-brainer for your Go application.

[![Build Status](https://travis-ci.org/markdingo/cslb.svg?branch=master)](https://travis-ci.org/markdingo/cslb)
[![Go Report Card](https://goreportcard.com/badge/github.com/markdingo/cslb)](https://goreportcard.com/report/github.com/markdingo/cslb)
[![codecov](https://codecov.io/gh/markdingo/cslb/branch/master/graph/badge.svg)](https://codecov.io/gh/markdingo/cslb)
[![](https://godoc.org/github.com/markdingo/cslb?status.svg)](https://godoc.org/github.com/markdingo/cslb)


### Installation

Cslb is a standard Go package thus if your program is go-module aware (which is to say
you've run "go mod init") then cslb is pulled in automatically the first time you compile
your program. If your program is not module-aware you can get this package the old way
with:

```sh
$ go get -u github.com/markdingo/cslb
```

At this stage cslb has no package dependencies beyond the standard packages shipped with the Go
compiler. Cslb requires Go 1.12.x or greater.

### Application Changes

To take advantage of cslb a program simply imports the package at which point cslb automatically
starts performing client-side load-balancing by over-riding the `DialContext` of the
`http.DefaultTransport`. If the program uses its own `http.Transport` then its `DialContext` needs to
be similarly replaced. Here is the before and after code which shows the application changes needed:

### Before

```go

 import (
         "net/http"
 )

 func main() {
        resp, err := http.Get("http://example.net/resource")
        ...
```

### After

```go

 import (
         "net/http"
         _ "github.com/markdingo/cslb"
 )

 func main() {
        resp, err := http.Get("http://example.net/resource")
        ...
```

and that's it!

One line of import code and no changes to application code fetching HTTP resources. The package
documentation describes what to do if your applications use its own `http.Transport`. Essentially
you have to enable cslb for that Transport.

### SRV Resource Records

Cslb processing is activated by the presence of SRV Resource Records (RRs) matching the requested
hostname using the prescribed [RFC2782](https://tools.ietf.org/rfc/rfc2782.txt) formulation. If no
SRV RR exists, cslb is completely transparent and passive. Cslb caches the presence or otherwise of
SRVs to minimize its impact in a non-SRV environment. This means you can deploy with cslb at any
time and activate the functionality at a later stage.

### Fail-Over and Load-Balancing Mechanism

Cslb intercepts `DialContext` Requests made by `net/http` and makes internal `DialContext` Requests
to the SRV targets. The first successful connection is returned to the calling application. In
effect this provides a fail-over capability.

Cslb achieves load-balancing by implementing the SRV selection algorithm which provides a lot of
flexibility in terms of preferring targets by priority and distributing connections by weight within
priority. The package documentation shows how this works.

### No Server-side changes required

No server-side changes are required to use cslb - apart for possibly dispensing with your
server-side load-balancers! You can even use cslb-enabled applications on third-party services with
appropriate DNS finagling. It's also possible to use cslb in conjunction with an existing
server-side load-balancer deployment by placing the load-balancers targets in an SRV RR.

### Active Health Checks

In addition to the passive collection of fail-over data based on `DialContext` results, cslb has an
optional "active mode" where a per-target health-check URL is periodically polled to determine the
health of a target. If a health-check URL fails, that target is removed from the target candidate
list for a configured time period. The health-check URL is defined by a TXT RR in the DNS. The
package documentation describes the naming convention and syntax.

### Status Web Page

Insights into the behaviour of cslb within your application are available via an optional status
page. When activated via an environment variable the web page provides statistics on intercepted
`DialContext` Requests, information on cached SRV and healthcheck results.

### Community

If you have any problems using cslb or suggestions on how it can do a better job, don't hesitate to
create an [issue](https://github.com/markdingo/cslb/issues) or email the
[authors](https://github.com/markdingo/cslb/blob/master/AUTHORS) directly. This package can only
improve with your feedback.

### Copyright and License

Cslb is Copyright :copyright: 2019,2020 Mark Delany. This software  is licensed under the BSD 2-Clause "Simplified" License.
