package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/gate"
)

// The product administrator's console: how the platform is being used, and
// whether it is well -- in numbers, never in client records.
//
// # What it deliberately does not show
//
// No claim, farmer, parcel, officer name or village. Every figure here is a
// count, grouped no finer than a district. The operator of the product needs
// to know that Solapur has 40 claims waiting on officers and that the database
// pool is saturated; it does not need to know whose claims they are, and a
// console that could show it would be one more place that data leaks from.

// ClaimStages counts claims by where they are, in the stages the farmer and
// the officers already see.
type ClaimStages struct {
	Received     int `json:"received"`
	Checking     int `json:"checking"`
	WithOfficers int `json:"with_officers"`
	Decided      int `json:"decided"`
	Closed       int `json:"closed"`
	Withdrawn    int `json:"withdrawn"`
	Total        int `json:"total"`
}

func (c *ClaimStages) add(state string, n int) {
	switch state {
	case "draft", "submitted":
		c.Received += n
	case "acknowledged":
		c.Checking += n
	case "assigned", "under_verification", "verified":
		c.WithOfficers += n
	case "adjudicated":
		c.Decided += n
	case "closed":
		c.Closed += n
	case "withdrawn":
		c.Withdrawn += n
	}
	c.Total += n
}

type stateStages struct {
	StateCode string `json:"state_code"`
	StateName string `json:"state_name"`
	ClaimStages
}

type districtStages struct {
	StateCode    string `json:"state_code"`
	DistrictCode string `json:"district_code"`
	DistrictName string `json:"district_name"`
	ClaimStages
}

type stateAccounts struct {
	StateCode      string         `json:"state_code"`
	StateName      string         `json:"state_name"`
	ByLevel        map[string]int `json:"by_level"`
	Active         int            `json:"active"`
	PendingInvites int            `json:"pending_invites"`
}

type trendPoint struct {
	Day       string `json:"day"`
	StateCode string `json:"state_code"`
	Filed     int    `json:"filed"`
}

// platformOverview is GET /api/v1/platform/overview.
func (s *Server) platformOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := struct {
		GeneratedAt time.Time        `json:"generated_at"`
		States      []stateStages    `json:"states"`
		Districts   []districtStages `json:"districts"`
		Accounts    []stateAccounts  `json:"accounts"`
		Trend       []trendPoint     `json:"trend"`
	}{GeneratedAt: time.Now().UTC()}

	fail := func(what string, err error) {
		s.Log.Error("platform overview: "+what, "error", err)
		writeError(w, http.StatusInternalServerError, "OVERVIEW_FAILED", "")
	}

	// Every loaded state appears, claims or not: a state onboarded last week
	// with nothing filed is exactly what the operator is looking for.
	states := map[string]*stateStages{}
	accounts := map[string]*stateAccounts{}
	rows, err := s.readDB().Query(ctx,
		`SELECT code, name FROM jurisdictions WHERE level = 'state' ORDER BY name`)
	if err != nil {
		fail("states", err)
		return
	}
	var order []string
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			rows.Close()
			fail("states", err)
			return
		}
		states[code] = &stateStages{StateCode: code, StateName: name}
		accounts[code] = &stateAccounts{StateCode: code, StateName: name, ByLevel: map[string]int{}}
		order = append(order, code)
	}
	rows.Close()

	// Claims, by district and state, by stage.
	rows, err = s.readDB().Query(ctx, `
		SELECT st.code, d.code, d.name, r.state::text, count(*)
		  FROM crop_loss_reports r
		  JOIN jurisdictions d ON d.level = 'district' AND d.code = r.district_lgd_code
		  JOIN jurisdictions st ON st.level = 'state' AND st.id = ANY(d.ancestor_ids)
		 GROUP BY st.code, d.code, d.name, r.state`)
	if err != nil {
		fail("claims", err)
		return
	}
	districts := map[string]*districtStages{}
	for rows.Next() {
		var stCode, dCode, dName, state string
		var n int
		if err := rows.Scan(&stCode, &dCode, &dName, &state, &n); err != nil {
			rows.Close()
			fail("claims", err)
			return
		}
		if st := states[stCode]; st != nil {
			st.add(state, n)
		}
		d := districts[dCode]
		if d == nil {
			d = &districtStages{StateCode: stCode, DistrictCode: dCode, DistrictName: dName}
			districts[dCode] = d
		}
		d.add(state, n)
	}
	rows.Close()

	// Officer accounts, by the level they are posted at.
	rows, err = s.readDB().Query(ctx, `
		SELECT st.code, j.level::text, count(DISTINCT ur.user_id)
		  FROM user_roles ur
		  JOIN users u ON u.id = ur.user_id AND u.kind = 'officer' AND u.status = 'active'
		  JOIN jurisdictions j ON j.id = ur.jurisdiction_id
		  JOIN jurisdictions st ON st.level = 'state'
		       AND (st.id = j.id OR st.id = ANY(j.ancestor_ids))
		 WHERE ur.revoked_at IS NULL
		 GROUP BY st.code, j.level`)
	if err != nil {
		fail("accounts", err)
		return
	}
	for rows.Next() {
		var stCode, level string
		var n int
		if err := rows.Scan(&stCode, &level, &n); err != nil {
			rows.Close()
			fail("accounts", err)
			return
		}
		if a := accounts[stCode]; a != nil {
			a.ByLevel[level] = n
		}
	}
	rows.Close()

	// Active accounts and invitations not yet accepted, per state.
	rows, err = s.readDB().Query(ctx, `
		SELECT st.code,
		       count(DISTINCT u.id) FILTER (WHERE u.password_changed_at IS NOT NULL),
		       count(DISTINCT u.id) FILTER (WHERE u.password_changed_at IS NULL)
		  FROM users u
		  JOIN user_roles ur ON ur.user_id = u.id AND ur.revoked_at IS NULL
		  JOIN jurisdictions j ON j.id = ur.jurisdiction_id
		  JOIN jurisdictions st ON st.level = 'state'
		       AND (st.id = j.id OR st.id = ANY(j.ancestor_ids))
		 WHERE u.kind = 'officer' AND u.status = 'active'
		 GROUP BY st.code`)
	if err != nil {
		fail("invitations", err)
		return
	}
	for rows.Next() {
		var stCode string
		var active, pending int
		if err := rows.Scan(&stCode, &active, &pending); err != nil {
			rows.Close()
			fail("invitations", err)
			return
		}
		if a := accounts[stCode]; a != nil {
			a.Active, a.PendingInvites = active, pending
		}
	}
	rows.Close()

	// Claims filed per day, per state, over thirty days, in India's day.
	rows, err = s.readDB().Query(ctx, `
		SELECT to_char((COALESCE(r.filed_at, r.created_at) AT TIME ZONE 'Asia/Kolkata')::date, 'YYYY-MM-DD'),
		       st.code, count(*)
		  FROM crop_loss_reports r
		  JOIN jurisdictions d ON d.level = 'district' AND d.code = r.district_lgd_code
		  JOIN jurisdictions st ON st.level = 'state' AND st.id = ANY(d.ancestor_ids)
		 WHERE COALESCE(r.filed_at, r.created_at) >= now() - interval '30 days'
		 GROUP BY 1, 2
		 ORDER BY 1, 2`)
	if err != nil {
		fail("trend", err)
		return
	}
	out.Trend = []trendPoint{}
	for rows.Next() {
		var p trendPoint
		if err := rows.Scan(&p.Day, &p.StateCode, &p.Filed); err != nil {
			rows.Close()
			fail("trend", err)
			return
		}
		out.Trend = append(out.Trend, p)
	}
	rows.Close()

	for _, code := range order {
		out.States = append(out.States, *states[code])
		out.Accounts = append(out.Accounts, *accounts[code])
	}
	out.Districts = []districtStages{}
	for _, d := range districts {
		out.Districts = append(out.Districts, *d)
	}
	sort.Slice(out.Districts, func(i, j int) bool {
		if out.Districts[i].StateCode != out.Districts[j].StateCode {
			return out.Districts[i].StateCode < out.Districts[j].StateCode
		}
		return out.Districts[i].DistrictName < out.Districts[j].DistrictName
	})
	if out.States == nil {
		out.States = []stateStages{}
		out.Accounts = []stateAccounts{}
	}

	writeJSON(w, http.StatusOK, out)
}

var errUnreachable = errors.New("not reachable")

func errUnhealthy(status int) error {
	return fmt.Errorf("answered %d", status)
}

// ServiceProbe is an internal service the health view checks by URL.
type ServiceProbe struct {
	Key  string
	Name string
	URL  string // polled with GET; any 2xx is up
}

type componentHealth struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Status    string  `json:"status"` // up, degraded, down, unconfigured
	LatencyMs float64 `json:"latency_ms"`
	Detail    string  `json:"detail,omitempty"`
}

// platformHealth is GET /api/v1/platform/health: live, polled by the console.
func (s *Server) platformHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	var (
		mu         sync.Mutex
		components []componentHealth
		wg         sync.WaitGroup
	)
	add := func(c componentHealth) {
		mu.Lock()
		components = append(components, c)
		mu.Unlock()
	}
	timed := func(key, name string, check func() (string, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			detail, err := check()
			c := componentHealth{Key: key, Name: name,
				LatencyMs: float64(time.Since(start).Microseconds()) / 1000}
			if err != nil {
				c.Status, c.Detail = "down", err.Error()
			} else {
				c.Status, c.Detail = "up", detail
				if c.LatencyMs > 1000 {
					c.Status = "degraded"
				}
			}
			add(c)
		}()
	}

	timed("postgres", "Database", func() (string, error) {
		return "", s.DB.Ping(ctx)
	})
	if s.Redis != nil {
		timed("redis", "Cache and sessions", func() (string, error) {
			return "", s.Redis.Ping(ctx).Err()
		})
	}
	if s.AI != nil {
		timed("ai", "Assessment agent", func() (string, error) {
			_, err := s.AI.Health(ctx)
			return "", err
		})
	}
	for _, p := range s.Probes {
		if p.URL == "" {
			add(componentHealth{Key: p.Key, Name: p.Name, Status: "unconfigured"})
			continue
		}
		timed(p.Key, p.Name, func() (string, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
			if err != nil {
				return "", err
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return "", errUnreachable
			}
			res.Body.Close()
			if res.StatusCode >= 300 {
				return "", errUnhealthy(res.StatusCode)
			}
			return "", nil
		})
	}
	// The remote satellite engine is not polled from here -- it is metered
	// and far away -- but the client's circuit breaker already knows whether
	// recent calls to it have been failing.
	if s.Satellite != nil {
		open, failures := s.Satellite.Health()
		c := componentHealth{Key: "satellite", Name: "Satellite engine", Status: "up"}
		if open {
			c.Status = "down"
			c.Detail = fmt.Sprintf("circuit open after %d consecutive failures", failures)
		} else if failures > 0 {
			c.Status = "degraded"
			c.Detail = fmt.Sprintf("%d recent failures", failures)
		}
		add(c)
	}

	wg.Wait()
	sort.Slice(components, func(i, j int) bool { return components[i].Key < components[j].Key })

	out := map[string]any{
		"checked_at": time.Now().UTC(),
		"env":        s.Env,
		"components": components,
	}

	// The database's own load, beyond whether it answers.
	stat := s.DB.Stat()
	dbInfo := map[string]any{
		"pool_max":     stat.MaxConns(),
		"pool_total":   stat.TotalConns(),
		"pool_in_use":  stat.AcquiredConns(),
		"pool_idle":    stat.IdleConns(),
		"pool_waits":   stat.EmptyAcquireCount(),
		"pool_wait_ms": stat.AcquireDuration().Milliseconds(),
	}
	var active, total int
	var size int64
	if err := s.DB.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state = 'active'), count(*),
		       pg_database_size(current_database())
		  FROM pg_stat_activity WHERE datname = current_database()`).
		Scan(&active, &total, &size); err == nil {
		dbInfo["server_active"] = active
		dbInfo["server_connections"] = total
		dbInfo["size_bytes"] = size
	}
	// Every pool, so a starved one is visible by name: the workers' pool
	// waiting is a backlog; the requests' pool waiting is farmers waiting.
	pools := make([]map[string]any, 0, len(s.Pools))
	for _, np := range s.Pools {
		if np.Pool == nil {
			pools = append(pools, map[string]any{"name": np.Name, "configured": false})
			continue
		}
		st := np.Pool.Stat()
		pools = append(pools, map[string]any{
			"name": np.Name, "configured": true,
			"max": st.MaxConns(), "in_use": st.AcquiredConns(),
			"idle": st.IdleConns(), "waits": st.EmptyAcquireCount(),
			"wait_ms": st.AcquireDuration().Milliseconds(),
		})
	}
	dbInfo["pools"] = pools
	dbInfo["replica"] = s.Reads.Status()
	out["database"] = dbInfo

	// What is waiting.
	queues := map[string]any{}
	if s.Jobs != nil {
		if b, err := s.Jobs.Waiting(ctx); err == nil {
			queues["assessments"] = b
		}
	}
	if s.Notify != nil {
		if b, err := s.Notify.Backlog(ctx); err == nil {
			queues["sms"] = b
		}
	}
	if s.Mail != nil {
		if n, err := s.Mail.Pending(ctx); err == nil {
			queues["email_pending"] = n
		}
	}
	var stuck int
	if err := s.DB.QueryRow(ctx, `
		SELECT count(*) FROM crop_loss_reports
		 WHERE pipeline_error IS NOT NULL`).Scan(&stuck); err == nil {
		queues["claims_stuck_in_pipeline"] = stuck
	}
	// The pipeline's two questions, as the health view should see them: how
	// many claims are waiting on an assessment, and how long the oldest claim
	// owed a receipt has been owed it. The second is the one that went wrong
	// at volume (migration 0047); it is minutes at worst when healthy.
	var waitingOnAssessment int
	var receiptOwedSec *float64
	if err := s.DB.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM crop_loss_reports
		         WHERE state IN ('submitted', 'acknowledged')
		           AND assessment_state = 'queued'),
		       (SELECT extract(epoch FROM now() - min(evidence_complete_at))
		          FROM crop_loss_reports
		         -- The first two conditions are the work index's own
		         -- predicate (migration 0047), so this reads the claims in
		         -- flight and not the table: polled every five seconds.
		         WHERE state IN ('submitted', 'acknowledged')
		           AND assessment_state <> 'queued'
		           AND state = 'submitted' AND evidence_complete_at IS NOT NULL)`).
		Scan(&waitingOnAssessment, &receiptOwedSec); err == nil {
		queues["claims_waiting_on_assessment"] = waitingOnAssessment
		if receiptOwedSec != nil {
			queues["oldest_receipt_owed_seconds"] = *receiptOwedSec
		}
	}
	if s.Jobs != nil {
		if t, err := s.Jobs.RecentThroughput(ctx); err == nil {
			queues["assessment_throughput"] = t
		}
	}
	out["queues"] = queues

	// How full each upstream is, across every instance.
	gates := make([]gate.Stats, 0, len(s.Gates))
	for _, g := range s.Gates {
		gates = append(gates, g.Stats(ctx))
	}
	out["gates"] = gates

	// This process.
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	out["runtime"] = map[string]any{
		"goroutines":     runtime.NumGoroutine(),
		"cpus":           runtime.NumCPU(),
		"heap_in_use":    mem.HeapInuse,
		"memory_from_os": mem.Sys,
		"gc_cycles":      mem.NumGC,
		"go_version":     runtime.Version(),
	}

	if s.Metrics != nil {
		out["load"] = s.Metrics.Load()
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
