package cslb

/*
The status server presents a read-only web page of insights into cslb. This includes the contents of
the SRV and health caches as well as the results of any health checks and connection attempts.

There are default html templates uses to render the pages but most of these can be over-ridden with
user-supplied templates defined with the "cslb_templates" environment variable.
*/

import (
	"context"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	header = `<html>
<head><title>CSLB - Client Side Load Balancing - Status</title></head>
<body>
`
	configStr = `{{define "config"}}
<h3>CSLB Global State</h3>
<table border=1>
<tr><th align=left>Start Time</th><td align=right>{{.StartTime.Format "2006-01-02T15:04:05Z07:00"}}</td></tr>
<tr><th align=left>Up time</th><td align=right>{{.Uptime}}</td></tr>
<tr><th align=left>DialContext Intercepts</th><td align=right>{{.DialContext}}</td></tr>
<tr><th align=left>Time In Intercept</th><td align=right>{{.Duration}}</td></tr>
<tr><th align=left>Status Server Address</th><td>http://{{.StatusServerAddress}}</td></tr>
<tr><th align=left>Executable</th><td>{{.Executable}}</td></tr>
</table>

<h3>CSLB Config</h3>
<table border=1>
<tr><th align=left>PrintDialContext</th><td>Print entry into cslb.DialContext</td><td align=center>{{.PrintDialContext}}</td></tr>
<tr><th align=left>PrintHCResults</th><td>Print results of Health Check</td><td align=center>{{.PrintHCResults}}</td></tr>
<tr><th align=left>PrintIntercepts</th><td>Print each domain to Target intercept</td><td align=center>{{.PrintIntercepts}}</td></tr>
<tr><th align=left>PrintSRVLookup</th><td>Print results of SRV Lookups</td><td align=center>{{.PrintSRVLookup}}</td></tr>
<tr><th align=left>DisableInterception</th><td>Turn off Interception</td><td align=center>{{.DisableInterception}}</td></tr>
<tr><th align=left>DisableHealthChecks</th><td>Turn off Health Checks</td><td align=center>{{.DisableHealthChecks}}</td></tr>
<tr><th align=left>AllowNumericServices</th><td>Allow Numeric Service SRV lookups</td><td align=center>{{.AllowNumericServices}}</td></tr>
<tr><th align=left>HealthCheckTXTPrefix</th><td>Forms part of TXT qName</td><td>{{.HealthCheckTXTPrefix}}</td></tr>
<tr><th align=left>HealthCheckContentOk</th><td>strings.Contains in health check body</td><td align=center>"{{.HealthCheckContentOk}}"</td></tr>
<tr><th align=left>HealthCheckFrequency</th><td>Time between health checks</td><td align=right>{{.HealthCheckFrequency}}</td></tr>
<tr><th align=left>InterceptTimeout</th><td>Maximum time to try targets</td><td align=right>{{.InterceptTimeout}}</td></tr>
<tr><th align=left>DialVetoDuration</th><td>Ignore downed targets for this duration</td><td align=right>{{.DialVetoDuration}}</td></tr>
<tr><th align=left>NotFoundSRVTTL</th><td>Cache lifetime for SRV NXDomain</td><td align=right>{{.NotFoundSRVTTL}}</td></tr>
<tr><th align=left>FoundSRVTTL</th><td>Cache lifetime for SRV found</td><td align=right>{{.FoundSRVTTL}}</td></tr>
<tr><th align=left>HealthTTL</th><td>Cache lifetime for SRV Target</td><td align=right>{{.HealthTTL}}</td></tr>
</table>
{{end}}
`

	cslbStr = `{{define "cslb"}}
<h3>CSLB Global Statistics</h3>
<table border=1>
<tr><th align=left>Intercepted calls to DialContext</th><td align=right>{{.DialContext}}</td></tr>
<tr><th align=left>Host or service don't match or interception disabled</th><td align=right>{{.MissHostService}}</td></tr>
<tr><th align=left>Times PTR lookup returned zero targets</th><td align=right>{{.NoPTR}}</td></tr>
<tr><th align=left>Calls to bestTarget()</th><td align=right>{{.BestTarget}}</td></tr>
<tr><th align=left>Times when all targets failed</th><td align=right>{{.DupesStopped}}</td></tr>
<tr><th align=left>system DialContext returned a good connection</th><td align=right>{{.GoodDials}}</td></tr>
<tr><th align=left>system DialContext returned an error</th><td align=right>{{.FailedDials}}</td></tr>
<tr><th align=left>Times intercept deadline expired</th><td align=right>{{.Deadline}}</td></tr>
</table>
{{end}}
`

	srvStr = `{{define "srv"}}
<h3>SRV DNS Cache</h3>
<table border=1>
<tr><th>CName</th><th align=right>Expires</th><th align=right>Lookups</th>
<th>Priority</th><th>Weight</th><th>Port</th><th>Target</th>
<th>GoodDials</th><th>FailedDials</th><th align=center>IsGood</th></tr>
{{range .Srvs}}
<tr>
<td>{{.CName}}</td><td align=right>{{.Expires}}</td></td><td align=right>{{.Lookups}}</td>
<td align=right>{{.Priority}}</td><td align=right>{{.Weight}}</td>
<td align=right>{{.Port}}</td><td>{{.Target}}</td><td align=right>{{.GoodDials}}</td>
<td align=right>{{.FailedDials}}</td><td align=center>{{.IsGood}}</td>
</tr>
{{end}}
</table>
{{end}}
`

	healthStr = `{{define "health"}}
<h3>Target Health Cache</h3>
<table border=1>
<tr>
<th>Target</th><th align=right>Expires</th><th>Good Dials</th><th>Failed Dials</th><th>Next Dial<br>Attempt</th>
<th>Last Dial<br>Attempt</th><th>isGood</th><th>Last Dial<br>Status</th><th>Last Health<br>Check</th>
<th>Health Check URL</th><th>Last Health<br>Status</th>
<tr>
{{range .Targets}}
<tr>
<td>{{.Key}}</td>
<td align=right>{{.Expires}}</td><td align=right>{{.GoodDials}}</td><td align=right>{{.FailedDials}}</td>
<td align=right>{{.NextDialAttempt}}</td><td align=right>{{.LastDialAttempt}}</td><td align=center>{{.IsGood}}</td>
<td>{{.LastDialStatus}}</td><td align=right>{{.LastHealthCheck}}</td><td>{{.Url}}</td><td>{{.LastHealthCheckStatus}}</td>
</tr>
{{end}}
</table>
{{end}}
`

	trailerStr = `
<div><hr><font size=-1>Client-Side Load Balancing {{.Version}} released on {{.ReleaseDate}}. Brought to you by
<a href=https://github/markdingo/cslb>https://github/markdingo/cslb</a> at {{.RunAt}}</font>
</body></html>
`
)

type statusServer struct {
	cslb        *cslb
	httpServer  *http.Server
	allTmpl     *template.Template
	trailerTmpl *template.Template
}

// newStatusServer creates the base status server ready for starting
func newStatusServer(cslb *cslb) *statusServer {
	t := &statusServer{cslb: cslb}
	err := t.loadTemplates()
	if err != nil {
		log.Fatal(err)
	}
	t.httpServer = &http.Server{Addr: cslb.StatusServerAddress}
	mux := http.NewServeMux()
	mux.HandleFunc("/", t.generateStatus)
	t.httpServer.Handler = mux

	return t
}

// start is normally called as a separate go-routine since it calls the http listener which blocks.
func (t *statusServer) start() {
	err := t.httpServer.ListenAndServe()
	if !strings.Contains(err.Error(), "http: Server closed") { // Good return?
		log.Fatal(err)
	}
}

// stop shuts down the http listener
func (t *statusServer) stop(ctx context.Context) {
	t.httpServer.Shutdown(ctx)
}

// loadTemplates performs a one-time parse of all the internal templates needed for the status
// page. It also attempts to "glob" load any template files found in the directory identified by the
// "cslb_templates" environment variable. If the glob load fails it only causes a warning as the
// default templates will still function..
func (t *statusServer) loadTemplates() error {
	t.allTmpl = template.New("")
	_, err := t.allTmpl.Parse(configStr)
	if err != nil {
		return err
	}
	_, err = t.allTmpl.Parse(cslbStr)
	if err != nil {
		return err
	}
	_, err = t.allTmpl.Parse(srvStr)
	if err != nil {
		return err
	}
	_, err = t.allTmpl.Parse(healthStr)
	if err != nil {
		return err
	}
	t.trailerTmpl, err = template.New("trailer").Parse(trailerStr)
	if err != nil {
		return err
	}

	if len(t.cslb.StatusServerTemplates) > 0 { // If an alternate template glob has been configured
		_, err = t.allTmpl.ParseGlob(t.cslb.StatusServerTemplates)
		if err != nil {
			log.Print("cslb Warning:", err) // Not fatal if replacement templates fail to load
		}
	}

	return nil
}

// Aggregate structs are conveniences so we can render derived values in a single template.

type cslbAggConfig struct {
	StartTime   time.Time
	Uptime      time.Duration
	Duration    time.Duration
	DialContext int
	Executable  string
	config
}

type cslbAggTrailer struct {
	Version     string
	ReleaseDate string
	RunAt       string
}

// generateStatus writes the status page out. It's quite extensive because everything is on one page.
func (t *statusServer) generateStatus(w http.ResponseWriter, req *http.Request) {
	var err error
	io.WriteString(w, header)

	var cac cslbAggConfig
	cac.config = t.cslb.config
	cas := t.cslb.cloneStats() // Take a copy to avoid holding a long mutex

	cac.StartTime = cas.StartTime
	cac.Uptime = time.Now().Sub(cas.StartTime).Truncate(time.Second)
	cac.Duration = cas.Duration.Truncate(time.Second) // Total time in intercepts
	cac.DialContext = cas.DialContext
	cac.Executable, _ = os.Executable()

	err = t.allTmpl.ExecuteTemplate(w, "config", &cac)
	if err != nil {
		log.Fatal(err)
	}
	err = t.allTmpl.ExecuteTemplate(w, "cslb", &cas)
	if err != nil {
		log.Fatal(err)
	}

	srvStats := t.cslb.srvStore.getStats(t.cslb.healthStore) // Clone all ceSRVs and ancillary data
	sort.Slice(srvStats.Srvs, func(i, j int) bool {          // Sort for a low-flicker re-render
		return srvStats.Srvs[i].CName < srvStats.Srvs[j].CName
	})
	sort.Slice(srvStats.nxDomains, func(i, j int) bool { // Sort for a low-flicker re-render
		return srvStats.nxDomains[i].CName < srvStats.nxDomains[j].CName
	})

	// Place NXDomains at the end to reduce visual clutter. To further reduce clutter, remove
	// duplicate data which comes from the SRV cache.
	srvStats.Srvs = append(srvStats.Srvs, srvStats.nxDomains...)
	prevCName := ""
	for ix := 0; ix < len(srvStats.Srvs); ix++ {
		if prevCName == srvStats.Srvs[ix].CName {
			srvStats.Srvs[ix].CName = ""
			srvStats.Srvs[ix].Expires = ""
			srvStats.Srvs[ix].Lookups = ""
		} else {
			prevCName = srvStats.Srvs[ix].CName
		}
	}

	err = t.allTmpl.ExecuteTemplate(w, "srv", srvStats)
	if err != nil {
		log.Fatal(err)
	}

	healthStats := t.cslb.healthStore.getStats()          // Clone all ceHealth entries
	sort.Slice(healthStats.Targets, func(i, j int) bool { // Sort for a low-flicker re-render
		return healthStats.Targets[i].Key < healthStats.Targets[j].Key
	})
	err = t.allTmpl.ExecuteTemplate(w, "health", healthStats)
	if err != nil {
		log.Fatal(err)
	}

	tv := cslbAggTrailer{Version: Version, ReleaseDate: ReleaseDate,
		RunAt: time.Now().Format("2006-01-02T15:04:05Z07:00")}
	err = t.trailerTmpl.Execute(w, tv)
	if err != nil {
		log.Fatal(err)
	}
}
