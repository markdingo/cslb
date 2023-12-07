/*
Package cslb provides transparent HTTP/HTTPS Client Side Load Balancing for Go programs.

Cslb intercepts "net/http" Dial Requests and re-directs them to a preferred set of target hosts
based on the load balancing configuration expressed in DNS SRV and TXT Resource Records (RRs).

Only one trivial change is required to client applications to benefit from cslb which is to import
this package and (if needed) enabling it for non-default http.Transport instances. Cslb processing
is triggered by the presence of SRV RRs. If no SRVs exist cslb is benign which means you can deploy
your application with cslb and independently activate and deactivate cslb processing for each
service at any time.

No server-side changes are required at all - apart for possibly dispensing with your server-side
load-balancers!

# DEFAULT USAGE

Importing cslb automatically enables interception for http.DefaultTransport. In this program
snippet:

	import (
	        "net/http"
	        _ "github.com/markdingo/cslb"
	)

	func main() {
	       resp, err := http.Get("http://example.net/resource")

the Dial Request made by http.Get is intercepted and processed by cslb.

# NON DEFAULT USAGE

If the application uses its own http.Transport then cslb processing needs to be activated by calling
the cslb.Enable() function, i.e.:

	import (
	        "net/http"
	        "github.com/markdingo/cslb"
	)

	func main() {
	        myTransport := http.Transport{...}
	        cslb.Enable(myTransport)
	        client := &http.Client{Transport: myTransport}
	        resp, err := client.Get("http://mydomain/resource")
	        ...

The cslb.Enable() function replaces http.Transport.DialContext with its own intercept function.

# WHEN TO USE CSLB

Server-side load-balancers are no panacea. They add deployment and diagnostic complexity, cost,
throughput constraints and become an additional point of possible failure.

Cslb can help you achieve good load-balancing and fail-over behaviour without the need for *any*
server-side load-balancers. This is particularly useful in enterprise and micro-service deployments
as well as smaller application deployments where configuring and managing load-balancers is a
significant resource drain.

Cslb can be used to load-balance across geographically dispersed targets or where "hot stand-by"
systems are purposely deployed on diverse infrastructure.

# DNS ACTIVATION

When cslb intercepts a http.Transport Dial Request to port 80 or port 443 it looks up SRV RRs as
prescribed by RFC2782. That is, _http._tcp.$domain and _https._tcp.$domain respectively. Cslb
directs the Dial Request to the highest preference target based on the SRV algorithm. If that Dial
Request fails, it tries the next lower preference target until a successful connection is returned
or all unique targets fail or it runs out of time.

Cslb caches the SRV RRs (or their non-existence) as well as the result of Dial Requests to the SRV
targets to optimize subequent intercepted calls and the selection of preferred targets. If no SRV
RRs exist, cslb passes the Dial Request on to net.DialContext.

# RULES OF INTERCEPTION

Cslb has specific rules about when interception occurs. It normally only considers intercepting port
80 and port 443 however if the "cslb_allports" environment variable is set, cslb intercepts
non-standard HTTP ports and maps them to numeric service names. For example http://example.net:8080
gets mapped to _8080._tcp.example.net as the SRV name to resolve.

# ACTIVE HEALTH CHECKS

While cslb runs passively by caching the results of previous Dial Requests, it can also run actively
by periodically performing health checks on targets. This is useful as an administrator can control
health check behaviour to move a target "in and out of rotation" without changing DNS entries and
waiting for TTLs to age out. Health checks are also likely to make the application a little more
responsive as they are less likely to make a dial attempt to a target that is not working.

Active health checking is enabled by the presence of a TXT RR in the sub-domain "_$port._cslb" of
the target. E.g. if the SRV target is "s1.example.net:80" then cslb looks for the TXT RR at
"_80._cslb.s1.example.net". If that TXT RR contains a URL then it becomes the health check URL. If
no TXT RR exists or the contents do not form a valid URL then no active health check is performed
for that target.

The health check URL does not have to be related to the target in any particular way. It could be a
URL to a central monitoring system which performs complicated application level tests and
performance monitoring. Or it could be a URL on the target system itself.

A health check is considered successful when a GET of the URL returns a 200 status and the content
contains the uppercase text "OK" somewhere in the body (See the "cslb_hc_ok" environment variable
for how this can be modified). Unless both those conditions are met the target is considered
unavailable.

Active health checks cease once a target becomes idle for too long and health check Dial Requests
are *not* get intercepted by cslb.

# CONVERTING A SITE TO CSLB

If your current service exists on a single server called "s1.example.net" and you want to spread the
load across additional servers "s2.example.net" and "s3.example.net" and assuming you've added the
"cslb" package to your application then the following DNS changes active cslb processing:

Current DNS

	s1.example.net.             IN A    172.16.254.1
	                            IN AAAA 2001:db8::1

	s2.example.net.             IN A    172.16.254.2
	                            IN AAAA 2001:db8::2

	s3.example.net.             IN A    172.16.254.3
	                            IN AAAA 2001:db8::3

Additional DNS

	_http._tcp.s1.example.net.  IN SRV  1 70 80 s1.example.net.
	                            IN SRV  1 30 80 s2.example.net.
	                            IN SRV  2 0 8080 s3.example.net.

	_80._cslb.s1.example.net.   IN TXT "http://healthchecker.example.com/s1"
	_80._cslb.s2.example.net.   IN TXT "http://healthchecker.example.com/s2"
	_8080._cslb.s3.example.net. IN TXT "http://s3.example.net/ok"

A number of observations about this DNS setup:

  - "s1" and "s2" are the highest priority
  - "s3" is only ever considered if both "s1" and "s2" are not responding
  - On average 70 out of 100 requests will be directed to "s1"
  - Connections to "s3" are made on port 8080
  - The health check for "s3" is on the same system as the service
  - The heallth checks for "s1" and "s2" are on a centralized system

# CACHE AGEING

Cslb maintains a cache of SRV lookups and the health status of targets. Cache entries automatically
age out as a form of garbage collection. Removed cache entries stop any associated active health
checks. Unfortunately the cache ageing does not have access to the DNS TTLs associated with the SRV
RRs so it makes a best-guess at reasonable time-to-live values.

The important point to note is that *all* values get periodically refreshed from the DNS. Nothing
persists internally forever regardless of the level of activity. This means you can be sure that any
changes to your DNS will be noticed by cslb in due course.

# STATUS WEB PAGE

Cslb optional runs a web server which presents internal statistics on its performance and
activity. This web service has *no* access controls so it's best to only run it on a loopback
address. Setting the environment variable "cslb_listen" to a listen address activates the status
server. E.g.:

	$ cslb_listen=127.0.0.1:8081 ./myProgram

# RUN TIME CONTROLS

On initialization the cslb package examines the "cslb_options" environment variable for single
letter options which have the following meaning:

	'd' - Debug print dialContext calls
	'h' - Debug print Health Check results
	'i' - Debug print intercepted Dial Requests
	'r' - Debug print system Dial Context results
	's' - Debug print SRV Lookups

	'C' - Disable all Dial Request interception
	'H' - Disable all health checks
	'N' - Allow numeric service lookups for non-HTTP(S) ports

An example of how this might by used from a shell:

	$ cslb_options=dh ./yourProgram -options ...

Many internal configuration values can be over-ridden with environment variables as shown in this
table:

	+----------------+----------------------------------------+---------+---------------+
	| Variable Name  | Description                            | Default | Format        |
	+----------------+----------------------------------------+---------+---------------+
	| cslb_dial_veto | Target veto period after dial fails    | 1m      | time.Duration |
	| cslb_hc_freq   | Frequency of health checks per target  | 50s     | time.Duration |
	| cslb_hc_ok     | strings.Contains in health check body  | "OK"    | String        |
	| cslb_listen    | Listen address for status server       |         | address:port  |
	| cslb_nxd_ttl   | Cache lifetime for NXDOMAIN SRVs       | 20m     | time.Duration |
	| cslb_srv_ttl   | Cache lifetime for found SRVs          | 5m      | time.Duration |
	| cslb_tar_ttl   | Cache lifetime for dial Targets        | 5m      | time.Duration |
	| cslb_templates | Alternate status server html/templates |         | filepath.Glob |
	| cslb_timeout   | Default intercept Dial duration        | 1m      | time.Duration |
	+----------------+----------------------------------------+---------+---------------+

Any values which are invalid or fall outside a reasonable range are ignored.

# DETECTING A GOOD SERVICE

Cslb only knows about the results of network connection attempts made by DialContext and the results
of any configured health checks. If a service is accepting network connections but not responding to
HTTP requests - or responding negatively - the client experiences failures but cslb will be unaware
of these failures. The result is that cslb will continue to direct future Dial Requests to that
faulty service in accordance with the SRV priorities. If your service is vulnerable to this
scenario, active health checks are recommended. This could be something ss simple as an on-service
health check which responds based on recent "200 OK" responses in the service log file.
Alternatively an on-service monitor which closes the listen socket will also work.

In general, defining a failing service is a complicated matter that only the application truly
understands. For this reason health checks are used as an intermediary which does understand
application level failures and converts them to simple language which cslb groks.

# RECOMMENDED SETUP

While every service is different there are a few general guidelines which apply to most services
when using cslb. First of all, run simple health checks if you can and configure them for use by
cslb. Second, have each target configured with both ipv4 and ipv6 addresses. This affords two
potentially independent network paths to the targets. Furthermore, net.Dialer attempts both ipv4 and
ipv6 connections simultaneously which maximizes responsiveness for the client.

Third, consider a "canary" target as a low preference (highest numeric value SRV priority)
target. If this "canary" target is accessed by cslb clients it tells you they are having trouble
reaching their "real" targets. Being able to run a "canary" service is one of the side-benefits of
cslb and SRVs.

# CAVEATS

Whan analyzing the Status Web Page or watching the Run Time Control output, observers need to be
aware of caching by the http (and possibly other) packages. For example not every call to http.Get()
results in a Dial Request as httpClient tries to re-use connections.

In a similar vein if you change a DNS entry and don't believe cslb has noticed this change within an
appropriate TTL amount of time, be aware that on some platforms the intervening recursive resolvers
adjust TTLs as they see fit. For example some home-gamer routers are known to increase short TTLs to
values they believe to be a more "appropriate" in an attempt to reduce their cache churn.

Perhaps the biggest caveat of all is that cslb relies on being enabled for all http.Transports in
use by your application. If you are importing a package (either directly or indirectly) which
constructs its own http.Transports then you'll need to modify that package to call cslb.Enable()
otherwise those http requests will not be intercepted. Of course if the package is making requests
incidental to the core functionality of your application then maybe it doesn't matter and you can
leave them be. Something to be aware of.

-----
*/
package cslb
