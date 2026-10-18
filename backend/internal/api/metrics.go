package api

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metrics, in Prometheus text format, without a dependency.
//
// # Why hand-rolled
//
// The client library is not large, but it is a dependency on the request path
// of a system that holds identified farmers' data, and what it buys here is
// histograms and a registry. Four counters and one latency histogram is the
// whole requirement, and the exposition format is a documented, stable text
// format that fits in this file.
//
// # What is deliberately not measured
//
// Nothing per-farmer, per-report or per-village. A metrics endpoint is
// usually the least-guarded surface in a deployment -- scraped by an agent,
// often exposed without authentication -- and a counter labelled by village
// would leak which villages are filing claims to anyone who can reach it.
// Labels here are the request's method, a *templated* route, and its status.
//
// The route template matters: labelling by raw path would create one time
// series per report id, which is both a memory leak in the scraper and the
// same disclosure by another name.

const maxTrackedRoutes = 200

// latencyBuckets are in seconds, chosen around what this API actually does.
// The long tail is real: an assessment waits on Earth Engine and the panel,
// and 30s is a normal answer rather than an outlier.
var latencyBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120,
}

type routeKey struct {
	method string
	route  string
	status int
}

// Metrics collects counters for one process.
type Metrics struct {
	mu sync.Mutex

	requests map[routeKey]uint64
	// Latency is kept per method+route, not per status: a histogram per
	// status code multiplies the series count for no question anyone asks.
	buckets map[routeKey][]uint64
	sums    map[routeKey]float64
	counts  map[routeKey]uint64

	// truncated counts requests dropped from the route map once it is full,
	// so a flood of unmatched paths shows up as a number rather than as
	// unbounded memory.
	truncated uint64

	started time.Time

	// window is the last five minutes, one slot per second, for the platform
	// console's live load view. The counters above are cumulative since start
	// and answer "how many ever"; this answers "how busy is it right now"
	// without a Prometheus in front of the console.
	window [loadWindowSeconds]loadSlot
}

const loadWindowSeconds = 300

type loadSlot struct {
	second   int64 // unix second this slot holds; stale slots are ignored
	requests uint64
	errors   uint64 // 5xx
	sumMs    float64
	maxMs    float64
}

func NewMetrics() *Metrics {
	return &Metrics{
		requests: map[routeKey]uint64{},
		buckets:  map[routeKey][]uint64{},
		sums:     map[routeKey]float64{},
		counts:   map[routeKey]uint64{},
		started:  time.Now(),
	}
}

// observe records one finished request.
func (m *Metrics) observe(method, route string, status int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	k := routeKey{method: method, route: route, status: status}
	if _, known := m.requests[k]; !known && len(m.requests) >= maxTrackedRoutes {
		m.truncated++
		return
	}
	m.requests[k]++

	lk := routeKey{method: method, route: route}
	b, ok := m.buckets[lk]
	if !ok {
		b = make([]uint64, len(latencyBuckets))
		m.buckets[lk] = b
	}
	secs := d.Seconds()
	for i, edge := range latencyBuckets {
		if secs <= edge {
			b[i]++
		}
	}
	m.sums[lk] += secs
	m.counts[lk]++

	now := time.Now().Unix()
	slot := &m.window[now%loadWindowSeconds]
	if slot.second != now {
		*slot = loadSlot{second: now}
	}
	slot.requests++
	if status >= 500 {
		slot.errors++
	}
	ms := secs * 1000
	slot.sumMs += ms
	if ms > slot.maxMs {
		slot.maxMs = ms
	}
}

// LoadPoint is ten seconds of traffic.
type LoadPoint struct {
	At       time.Time `json:"at"`
	Requests uint64    `json:"requests"`
	Errors   uint64    `json:"errors"`
	AvgMs    float64   `json:"avg_ms"`
	MaxMs    float64   `json:"max_ms"`
}

// Load summarises the last five minutes of requests.
type Load struct {
	WindowSeconds int         `json:"window_seconds"`
	RPS1m         float64     `json:"rps_1m"`
	RPS5m         float64     `json:"rps_5m"`
	ErrorRate5m   float64     `json:"error_rate_5m"`
	AvgMs1m       float64     `json:"avg_ms_1m"`
	MaxMs5m       float64     `json:"max_ms_5m"`
	Requests5m    uint64      `json:"requests_5m"`
	Series        []LoadPoint `json:"series"`
	UptimeSeconds float64     `json:"uptime_seconds"`
}

// Load reads the rolling window. Oldest point first, ten-second points.
func (m *Metrics) Load() Load {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().Unix()
	out := Load{
		WindowSeconds: loadWindowSeconds,
		UptimeSeconds: time.Since(m.started).Seconds(),
	}
	var req1m, req5m, err5m uint64
	var sum1m float64
	points := make([]LoadPoint, loadWindowSeconds/10)
	for i := range points {
		points[i].At = time.Unix(now-int64(loadWindowSeconds)+int64(i*10)+1, 0).UTC()
	}
	for _, sl := range m.window {
		age := now - sl.second
		if sl.second == 0 || age < 0 || age >= loadWindowSeconds {
			continue
		}
		req5m += sl.requests
		err5m += sl.errors
		if sl.maxMs > out.MaxMs5m {
			out.MaxMs5m = sl.maxMs
		}
		if age < 60 {
			req1m += sl.requests
			sum1m += sl.sumMs
		}
		idx := int((int64(loadWindowSeconds) - 1 - age) / 10)
		p := &points[idx]
		p.AvgMs = (p.AvgMs*float64(p.Requests) + sl.sumMs) / float64(p.Requests+sl.requests)
		p.Requests += sl.requests
		p.Errors += sl.errors
		if sl.maxMs > p.MaxMs {
			p.MaxMs = sl.maxMs
		}
	}
	out.Requests5m = req5m
	out.RPS1m = float64(req1m) / 60
	out.RPS5m = float64(req5m) / loadWindowSeconds
	if req5m > 0 {
		out.ErrorRate5m = float64(err5m) / float64(req5m)
	}
	if req1m > 0 {
		out.AvgMs1m = sum1m / float64(req1m)
	}
	out.Series = points
	return out
}

// instrument wraps a handler, timing what it does.
func (s *Server) instrument(next http.Handler) http.Handler {
	if s.Metrics == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		// The pattern the mux matched, not the path that was requested --
		// "/api/v1/reports/{reportID}" rather than one series per report.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		s.Metrics.observe(r.Method, route, rec.status, time.Since(start))
	})
}

// metrics serves the exposition format.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if s.Metrics == nil {
		http.Error(w, "metrics are not enabled", http.StatusNotFound)
		return
	}
	if !s.mayScrape(r) {
		// 404, not 403. A scraper that is configured knows where this is; to
		// anybody else the endpoint may as well not exist.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	s.Metrics.mu.Lock()
	defer s.Metrics.mu.Unlock()
	m := s.Metrics

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format, args...)
	}

	write("# HELP esurvey_uptime_seconds How long this process has been running.\n")
	write("# TYPE esurvey_uptime_seconds gauge\n")
	write("esurvey_uptime_seconds %.0f\n", time.Since(m.started).Seconds())

	write("# HELP esurvey_http_requests_total Requests by method, route and status.\n")
	write("# TYPE esurvey_http_requests_total counter\n")
	keys := make([]routeKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	// Sorted so successive scrapes are diffable by a human reading them.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].status < keys[j].status
	})
	for _, k := range keys {
		write("esurvey_http_requests_total{method=%q,route=%q,status=\"%d\"} %d\n",
			k.method, k.route, k.status, m.requests[k])
	}

	write("# HELP esurvey_http_request_duration_seconds Request latency.\n")
	write("# TYPE esurvey_http_request_duration_seconds histogram\n")
	lkeys := make([]routeKey, 0, len(m.buckets))
	for k := range m.buckets {
		lkeys = append(lkeys, k)
	}
	sort.Slice(lkeys, func(i, j int) bool {
		if lkeys[i].route != lkeys[j].route {
			return lkeys[i].route < lkeys[j].route
		}
		return lkeys[i].method < lkeys[j].method
	})
	for _, k := range lkeys {
		for i, edge := range latencyBuckets {
			write("esurvey_http_request_duration_seconds_bucket"+
				"{method=%q,route=%q,le=\"%g\"} %d\n",
				k.method, k.route, edge, m.buckets[k][i])
		}
		write("esurvey_http_request_duration_seconds_bucket"+
			"{method=%q,route=%q,le=\"+Inf\"} %d\n", k.method, k.route, m.counts[k])
		write("esurvey_http_request_duration_seconds_sum{method=%q,route=%q} %g\n",
			k.method, k.route, m.sums[k])
		write("esurvey_http_request_duration_seconds_count{method=%q,route=%q} %d\n",
			k.method, k.route, m.counts[k])
	}

	if m.truncated > 0 {
		write("# HELP esurvey_metrics_truncated_total Requests not recorded " +
			"because the route cardinality limit was reached.\n")
		write("# TYPE esurvey_metrics_truncated_total counter\n")
		write("esurvey_metrics_truncated_total %d\n", m.truncated)
	}

	// The satellite breaker, because a run of failed assessments has exactly
	// one likely cause and this is it.
	if s.Satellite != nil {
		open, failures := s.Satellite.Health()
		write("# HELP esurvey_satellite_circuit_open Whether the analytics " +
			"engine is currently considered unreachable.\n")
		write("# TYPE esurvey_satellite_circuit_open gauge\n")
		write("esurvey_satellite_circuit_open %d\n", boolToInt(open))
		write("# HELP esurvey_satellite_consecutive_failures Consecutive " +
			"failures against the analytics engine.\n")
		write("# TYPE esurvey_satellite_consecutive_failures gauge\n")
		write("esurvey_satellite_consecutive_failures %d\n", failures)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mayScrape decides who may read the metrics.
//
// With a token configured, whoever presents it. With none, only a caller on
// the loopback interface -- a sidecar, or an operator with a shell on the box.
//
// The address comes from the connection rather than from clientIP, which
// honours X-Forwarded-For. Trusting a forwarded address here would let anyone
// claim to be 127.0.0.1 by setting a header, which is the whole of the
// control. The consequence is deliberate: behind a proxy, loopback never
// matches and a token has to be configured.
func (s *Server) mayScrape(r *http.Request) bool {
	if s.MetricsToken != "" {
		// The scheme is required, not optional. TrimPrefix leaves the value
		// untouched when the prefix is absent, so accepting a bare token was
		// accepting anything that happened to equal the secret in a header
		// this endpoint had not agreed to read.
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			return false
		}
		got := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		// Constant time: the token is a secret, and a comparison that returns
		// early on the first wrong byte is one that can be guessed a byte at
		// a time.
		return subtle.ConstantTimeCompare([]byte(got), []byte(s.MetricsToken)) == 1
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
