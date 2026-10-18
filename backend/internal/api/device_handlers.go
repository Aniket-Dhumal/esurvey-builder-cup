package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// How each handset's queue is doing, and which app builds may still write.
//
// # Why the server needs to hear from phones that have nothing to send
//
// A queue that drains is invisible, which is right. A queue that does not drain
// is also invisible, which is the problem: the farmer sees a card saying their
// report is waiting, assumes it will go, and nobody at the office knows the
// claim exists. Across lakhs of handsets that is not a support ticket, it is a
// statistic nobody can see until an appeal window closes.
//
// So after each sync pass the app reports its counts -- no content, no
// photographs, no positions -- and an operator can ask which phones have had
// something waiting for more than a day.

// DeviceStaleAfter is how long an item may wait before its phone counts as
// stuck. A day: a farmer out of signal overnight is ordinary, and a claim still
// on the phone the next evening is not.
const DeviceStaleAfter = 24 * time.Hour

var validDeviceID = regexp.MustCompile(`^[A-Za-z0-9_\-:.]{8,128}$`)

type syncStatusRequest struct {
	DeviceID   string `json:"device_id"`
	AppVersion string `json:"app_version"`
	Platform   string `json:"platform"`

	PendingReports    int   `json:"pending_reports"`
	PendingCaptures   int   `json:"pending_captures"`
	PendingBoundaries int   `json:"pending_boundaries"`
	PendingAppeals    int   `json:"pending_appeals"`
	FailedItems       int   `json:"failed_items"`
	PendingBytes      int64 `json:"pending_bytes"`

	OldestPendingAt *time.Time `json:"oldest_pending_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastErrorCode   string     `json:"last_error_code,omitempty"`
}

// reportSyncStatus records one handset's queue.
//
// Accepted from any signed-in account, farmer or officer, because both carry
// queues. Idempotent by construction -- it is an upsert of the latest state --
// and cheap: one row per device, written at most once a minute however often
// the app calls.
func (s *Server) reportSyncStatus(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())

	var req syncStatusRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if !validDeviceID.MatchString(req.DeviceID) {
		writeError(w, http.StatusBadRequest, "INVALID_DEVICE",
			"device_id must be 8 to 128 letters, digits, hyphens or underscores")
		return
	}

	clamp := func(n int) int { return max(0, min(n, 1_000_000)) }
	now := time.Now()
	// A handset clock can be anywhere. A date in the future is not a queue
	// that has waited negative time; it is a wrong clock, and it is dropped
	// rather than shown as the freshest queue in the state.
	sane := func(t *time.Time) *time.Time {
		if t == nil || t.After(now.Add(time.Hour)) || t.Before(now.AddDate(-1, 0, 0)) {
			return nil
		}
		return t
	}

	// At most once a minute per device. A misbehaving build that reported on
	// every request would otherwise turn this table into the busiest write in
	// the database during exactly the event it exists to watch.
	_, err := s.DB.Exec(r.Context(), `
		INSERT INTO device_sync_status (
			device_id, principal_id, app_version, platform,
			pending_reports, pending_captures, pending_boundaries, pending_appeals,
			failed_items, pending_bytes, oldest_pending_at, last_success_at,
			last_error_code, reported_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''), now())
		ON CONFLICT (device_id) DO UPDATE SET
			principal_id       = EXCLUDED.principal_id,
			app_version        = EXCLUDED.app_version,
			platform           = EXCLUDED.platform,
			pending_reports    = EXCLUDED.pending_reports,
			pending_captures   = EXCLUDED.pending_captures,
			pending_boundaries = EXCLUDED.pending_boundaries,
			pending_appeals    = EXCLUDED.pending_appeals,
			failed_items       = EXCLUDED.failed_items,
			pending_bytes      = EXCLUDED.pending_bytes,
			oldest_pending_at  = EXCLUDED.oldest_pending_at,
			last_success_at    = COALESCE(EXCLUDED.last_success_at, device_sync_status.last_success_at),
			last_error_code    = EXCLUDED.last_error_code,
			reported_at        = now()
		-- Throttled, except when something changed. A report saying the
		-- queue just emptied must land even if the last one was seconds ago,
		-- or the operator goes on seeing a stuck phone that is not.
		WHERE device_sync_status.reported_at < now() - interval '60 seconds'
		   OR device_sync_status.principal_id <> EXCLUDED.principal_id
		   OR (device_sync_status.pending_reports, device_sync_status.pending_captures,
		       device_sync_status.pending_boundaries, device_sync_status.pending_appeals,
		       device_sync_status.failed_items)
		      IS DISTINCT FROM
		      (EXCLUDED.pending_reports, EXCLUDED.pending_captures,
		       EXCLUDED.pending_boundaries, EXCLUDED.pending_appeals,
		       EXCLUDED.failed_items)`,
		req.DeviceID, p.UserID, trim(req.AppVersion, 32), trim(req.Platform, 16),
		clamp(req.PendingReports), clamp(req.PendingCaptures),
		clamp(req.PendingBoundaries), clamp(req.PendingAppeals),
		clamp(req.FailedItems), max(0, req.PendingBytes),
		sane(req.OldestPendingAt), sane(req.LastSuccessAt),
		trim(req.LastErrorCode, 64))
	if err != nil {
		s.Log.Error("recording device sync status", "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// --- the operator's view -----------------------------------------------------

type deviceSummary struct {
	Devices           int    `json:"devices"`
	ReportedToday     int    `json:"reported_last_24h"`
	WithPending       int    `json:"with_pending"`
	Stuck             int    `json:"stuck"`
	WithFailed        int    `json:"with_failed"`
	PendingReports    int    `json:"pending_reports"`
	PendingCaptures   int    `json:"pending_captures"`
	PendingBytes      int64  `json:"pending_bytes"`
	StaleAfterHours   int    `json:"stuck_after_hours"`
	MinimumAppVersion string `json:"minimum_app_version,omitempty"`
}

type versionCount struct {
	AppVersion string `json:"app_version"`
	Devices    int    `json:"devices"`
	BelowMin   bool   `json:"below_minimum"`
}

type stuckDevice struct {
	DeviceID        string     `json:"device_id"`
	Account         string     `json:"account"`
	AppVersion      string     `json:"app_version"`
	PendingReports  int        `json:"pending_reports"`
	PendingCaptures int        `json:"pending_captures"`
	FailedItems     int        `json:"failed_items"`
	OldestPendingAt *time.Time `json:"oldest_pending_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastErrorCode   string     `json:"last_error_code,omitempty"`
	ReportedAt      time.Time  `json:"reported_at"`
}

// deviceHealth is the fleet at a glance, for the platform console.
// deviceHealthTTL is how long a computed health page is reused.
//
// The page is a district office's dashboard, refreshed on a timer by whoever
// has it open. Recomputing per viewer is what made it expensive; half a minute
// old is indistinguishable from live for a number that moves when handsets
// report, which is at most once a minute each.
const deviceHealthTTL = 30 * time.Second

const deviceHealthKey = "devices:health"

func (s *Server) deviceHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.Redis != nil {
		if cached, err := s.Redis.Get(ctx, deviceHealthKey).Bytes(); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "hit")
			_, _ = w.Write(cached)
			return
		}
	}

	body, err := s.computeDeviceHealth(ctx)
	if err != nil {
		s.Log.Error("reading device health", "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "")
		return
	}
	if s.Redis != nil {
		if err := s.Redis.Set(ctx, deviceHealthKey, body, deviceHealthTTL).Err(); err != nil {
			s.Log.Warn("caching device health", "error", err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// computeDeviceHealth reads the fleet's state, from indexes rather than from
// every row.
//
// # Why the queries are shaped like this
//
// The summary used to be one query of count(*) FILTER over the whole table,
// which is a sequential scan per page load -- at a lakh of handsets, on the
// pool the write path shares (docs/SYNC_SCALE_REVIEW.md, M7). Every question
// here is about the minority of phones that have something waiting or refused,
// so each is answered from a partial index over that minority (migration
// 0034). The one unavoidable whole-table number is how many devices exist,
// and that is taken from the statistics rather than counted: it is a headline
// figure, not an accounting one.
func (s *Server) computeDeviceHealth(ctx context.Context) ([]byte, error) {
	stale := fmt.Sprintf("%d seconds", int(DeviceStaleAfter.Seconds()))

	sum := deviceSummary{StaleAfterHours: int(DeviceStaleAfter.Hours()),
		MinimumAppVersion: s.MinAppVersion}

	// Phones with work waiting, and what they are carrying: the partial index
	// covers the columns, so this reads only those rows.
	if err := s.readDB().QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE oldest_pending_at < now() - $1::interval),
		       COALESCE(sum(pending_reports), 0),
		       COALESCE(sum(pending_captures), 0),
		       COALESCE(sum(pending_bytes), 0)
		  FROM device_sync_status
		 WHERE pending_total > 0`, stale).Scan(
		&sum.WithPending, &sum.Stuck, &sum.PendingReports,
		&sum.PendingCaptures, &sum.PendingBytes); err != nil {
		return nil, err
	}

	if err := s.readDB().QueryRow(ctx, `
		SELECT count(*) FROM device_sync_status WHERE failed_items > 0`).
		Scan(&sum.WithFailed); err != nil {
		return nil, err
	}

	if err := s.readDB().QueryRow(ctx, `
		SELECT count(*) FROM device_sync_status
		 WHERE reported_at > now() - interval '24 hours'`).
		Scan(&sum.ReportedToday); err != nil {
		return nil, err
	}

	// How many handsets there are at all. From the planner's statistics, kept
	// current by autovacuum; exact below the point where counting is free.
	if err := s.readDB().QueryRow(ctx, `
		SELECT GREATEST(
		    (SELECT reltuples::bigint FROM pg_class WHERE oid = 'device_sync_status'::regclass),
		    0)`).Scan(&sum.Devices); err != nil {
		return nil, err
	}
	if sum.Devices < 50000 {
		if err := s.readDB().QueryRow(ctx,
			`SELECT count(*) FROM device_sync_status`).Scan(&sum.Devices); err != nil {
			return nil, err
		}
	}

	versions := []versionCount{}
	rows, err := s.readDB().Query(ctx, `
		SELECT app_version, count(*) FROM device_sync_status
		 GROUP BY app_version ORDER BY count(*) DESC LIMIT 20`)
	if err == nil {
		for rows.Next() {
			var v versionCount
			if rows.Scan(&v.AppVersion, &v.Devices) == nil {
				v.BelowMin = s.MinAppVersion != "" && versionBelow(v.AppVersion, s.MinAppVersion)
				versions = append(versions, v)
			}
		}
		rows.Close()
	}

	// The phones an operator has to do something about. Two indexed reads
	// rather than one OR, which the planner cannot answer from either index.
	stuck := []stuckDevice{}
	rows, err = s.readDB().Query(ctx, `
		WITH waiting AS (
			SELECT device_id FROM device_sync_status
			 WHERE pending_total > 0 AND oldest_pending_at < now() - $1::interval
			 ORDER BY oldest_pending_at
			 LIMIT 100
		), refused AS (
			SELECT device_id FROM device_sync_status
			 WHERE failed_items > 0
			 LIMIT 100
		)
		SELECT d.device_id, COALESCE(u.username, ''), d.app_version,
		       d.pending_reports, d.pending_captures, d.failed_items,
		       d.oldest_pending_at, d.last_success_at,
		       COALESCE(d.last_error_code, ''), d.reported_at
		  FROM device_sync_status d
		  LEFT JOIN users u ON u.id = d.principal_id
		 WHERE d.device_id IN (SELECT device_id FROM waiting
		                       UNION SELECT device_id FROM refused)
		 ORDER BY d.oldest_pending_at NULLS LAST
		 LIMIT 100`, stale)
	if err == nil {
		for rows.Next() {
			var d stuckDevice
			if rows.Scan(&d.DeviceID, &d.Account, &d.AppVersion, &d.PendingReports,
				&d.PendingCaptures, &d.FailedItems, &d.OldestPendingAt,
				&d.LastSuccessAt, &d.LastErrorCode, &d.ReportedAt) == nil {
				stuck = append(stuck, d)
			}
		}
		rows.Close()
	}

	return json.Marshal(map[string]any{
		"summary":  sum,
		"versions": versions,
		"stuck":    stuck,
	})
}

const headerAppVersion = "X-App-Version"

// requireCurrentApp refuses writes from an app build below the minimum.
//
// # Why this exists
//
// The sync protocol will change again -- it just did, to add idempotency keys.
// A build from before a change keeps running on a farmer's phone for months,
// and the only honest thing to do with one that can no longer be served
// correctly is to say so: a 426 the app shows as "please update", with the
// farmer's queue kept intact on the phone until they do.
//
// # What it does not touch
//
// Reads, sign-in, and the sync-status report -- an old build must still be
// able to sign in, see its claims, and tell the operator it is old. And any
// request without the header, which is every browser and every build from
// before this line existed; blocking those by default would lock out the
// whole installed base on the day it shipped.
func (s *Server) requireCurrentApp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := strings.TrimSpace(r.Header.Get(headerAppVersion))
		// A farmer's app that sends no version is older than the header
		// itself, so older than any minimum worth setting. Letting it through
		// meant the builds already in the field could never be stopped
		// (docs/SYNC_SCALE_REVIEW.md, M4). The console sends no version and is
		// not a farmer, so it is unaffected.
		if v == "" && s.MinAppVersion != "" && mutating(r.Method) {
			if p := rbac.FromContext(r.Context()); p != nil && p.Kind == rbac.KindFarmer {
				v = "0.0.0"
			}
		}
		if s.MinAppVersion == "" || v == "" || !mutating(r.Method) ||
			strings.HasPrefix(r.URL.Path, "/api/v1/auth/") ||
			r.URL.Path == "/api/v1/devices/sync-status" {
			next.ServeHTTP(w, r)
			return
		}
		if versionBelow(v, s.MinAppVersion) {
			writeError(w, http.StatusUpgradeRequired, "UPGRADE_REQUIRED",
				"this version of the app is too old to send claims; update it "+
					"from the same place you installed it. Nothing on the phone "+
					"has been lost.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// versionBelow compares dotted numeric versions, ignoring any "+build" suffix.
//
// A version that cannot be parsed is not below anything. Refusing writes from
// a build because its version string was unexpected would lock a farmer out
// for a formatting slip, which is the wrong way round.
func versionBelow(have, want string) bool {
	h, ok1 := parseVersion(have)
	m, ok2 := parseVersion(want)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if h[i] != m[i] {
			return h[i] < m[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "+-"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// SweepDeviceStatus forgets handsets that have not reported in a season.
//
// A phone reset, sold or lost stops reporting and would otherwise sit in the
// stuck list for ever. Ninety days outlasts any single event.
func (s *Server) SweepDeviceStatus(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `
		DELETE FROM device_sync_status WHERE reported_at < now() - interval '90 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
