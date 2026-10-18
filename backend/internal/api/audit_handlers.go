package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// listAudit reads the audit trail.
//
// Read-only by construction: the table refuses UPDATE and DELETE at the
// database, and nothing in this service writes to it except the recorder.
// The endpoint exists because an audit log nobody can read is a log nobody
// checks -- an inquiry should not need a database console.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}
	search := ""
	if pg.Query != "" {
		search = page.Like(pg.Query)
	}

	var since any
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_SINCE",
				"since must be an RFC 3339 timestamp")
			return
		}
		since = t
	}

	// The audit table is append-only and the fastest-growing thing here -- one
	// row per request that touches a record -- so the count has to run
	// against the same predicates as the page, and against nothing else.
	// Whose log. The administrator's is the whole platform's. Every other
	// reader sees what happened inside their own area -- entries whose
	// jurisdiction lies at or below their postings -- and their own actions,
	// such as signing in, which carry no place. It used to be the whole log for
	// anyone holding audit:read: a state officer read every state's record,
	// and a Collector every district's.
	p := rbac.FromContext(r.Context())
	var scope any
	self := ""
	// The product administrator reads the platform's own record -- sign-ins
	// and account changes by product-side accounts, retention runs -- and none
	// of the client's: no claim, farmer, parcel, assessment, grievance or event
	// entry, and no sign-in by a farmer or a state's own officers.
	platformOnly := p.HasRole(rbac.RoleSystemAdmin)
	if !platformOnly {
		ids := p.Scope().JurisdictionIDs
		if ids == nil {
			ids = []string{}
		}
		scope = ids
		self = p.UserID
	}

	const where = `($1::text = '' OR a.action = $1)
		  AND ($2::text = '' OR a.outcome = $2)
		  AND ($3::text = '' OR a.actor_user_id::text = $3)
		  AND ($4::text = '' OR a.resource_id = $4)
		  AND ($5::timestamptz IS NULL OR a.at >= $5)
		  AND ($8::text = '' OR a.action ILIKE $8
		                     OR COALESCE(a.actor_label, '') ILIKE $8
		                     OR COALESCE(a.resource_id, '') ILIKE $8)
		  AND ($S::uuid[] IS NULL
		       OR ($U::text <> '' AND a.actor_user_id::text = $U)
		       -- The exact place, for every entry written since 0044.
		       OR (a.jurisdiction_id IS NOT NULL AND EXISTS (
		              SELECT 1 FROM jurisdictions j
		               WHERE j.id = a.jurisdiction_id
		                 AND (j.id = ANY($S::uuid[]) OR j.ancestor_ids && $S::uuid[])))
		       -- An older entry has only a code, which is unique only within a
		       -- level. It is attributed only when the code names exactly one
		       -- jurisdiction; a code shared across levels could be another
		       -- state's village, and is not guessed at.
		       OR (a.jurisdiction_id IS NULL AND a.jurisdiction_code IS NOT NULL
		           AND (SELECT count(*) FROM jurisdictions c
		                 WHERE c.code = a.jurisdiction_code) = 1
		           AND EXISTS (
		              SELECT 1 FROM jurisdictions j
		               WHERE j.code = a.jurisdiction_code
		                 AND (j.id = ANY($S::uuid[]) OR j.ancestor_ids && $S::uuid[]))))
		  AND ($P::bool IS NOT TRUE OR (
		       a.action NOT LIKE ALL (ARRAY['report.%', 'capture.%', 'assessment.%',
		           'farmer.%', 'grievance.%', 'boundary.%', 'measurement.%',
		           'event.%', 'authz.%', 'auth.otp.%', 'user.farmer_enrolled'])
		       AND (a.action LIKE 'retention.%'
		            OR a.action LIKE 'platform.%'
		            OR a.actor_role IN ('system_admin', 'state_officer')
		            -- A failed sign-in has no role yet: it is the platform's
		            -- only if the address tried belongs to a product-side account.
		            OR (a.action LIKE 'auth.login.%' AND EXISTS (
		                SELECT 1 FROM users pu
		                  JOIN user_roles pr ON pr.user_id = pu.id AND pr.revoked_at IS NULL
		                 WHERE lower(pu.username) = lower(a.actor_label)
		                   AND pr.role IN ('system_admin', 'state_officer'))))))`

	// Bounded. audit_log is append-only and grows by one row per request that
	// touches a record, so an exact count here is a sequential scan of the
	// whole table on every page load -- for a figure printed beside a pager.
	var total int
	if err := s.readDB().QueryRow(r.Context(),
		page.CountSQL(`SELECT 1 FROM audit_log a WHERE `+
			strings.NewReplacer("$8", "$6", "$S", "$7", "$U", "$8", "$P", "$9").Replace(where)),
		q.Get("action"), q.Get("outcome"), q.Get("actor_user_id"),
		q.Get("resource_id"), since, search, scope, self, platformOnly).Scan(&total); err != nil {
		s.Log.Error("counting audit log", "error", err)
		writeError(w, http.StatusInternalServerError, "AUDIT_READ_FAILED",
			"the request could not be completed")
		return
	}

	rows, err := s.readDB().Query(r.Context(), `
		SELECT a.id, a.at,
		       COALESCE(a.actor_user_id::text, ''), COALESCE(a.actor_label, ''),
		       COALESCE(a.actor_role, ''), COALESCE(host(a.actor_ip), ''),
		       a.action, COALESCE(a.resource_type, ''), COALESCE(a.resource_id, ''),
		       COALESCE(a.jurisdiction_code, ''), a.outcome, a.detail,
		       COALESCE(a.request_id, '')
		FROM audit_log a
		WHERE `+strings.NewReplacer("$S", "$9", "$U", "$10", "$P", "$11").Replace(where)+`
		ORDER BY a.at DESC, a.id DESC
		LIMIT $6 OFFSET $7`,
		q.Get("action"), q.Get("outcome"), q.Get("actor_user_id"),
		q.Get("resource_id"), since, pg.Limit, pg.Offset, search, scope, self, platformOnly)
	if err != nil {
		s.Log.Error("reading audit log", "error", err)
		writeError(w, http.StatusInternalServerError, "AUDIT_READ_FAILED",
			"the request could not be completed")
		return
	}
	defer rows.Close()

	type entry struct {
		ID               int64          `json:"id"`
		At               time.Time      `json:"at"`
		ActorUserID      string         `json:"actor_user_id,omitempty"`
		ActorLabel       string         `json:"actor_label,omitempty"`
		ActorRole        string         `json:"actor_role,omitempty"`
		ActorIP          string         `json:"actor_ip,omitempty"`
		Action           string         `json:"action"`
		ResourceType     string         `json:"resource_type,omitempty"`
		ResourceID       string         `json:"resource_id,omitempty"`
		JurisdictionCode string         `json:"jurisdiction_code,omitempty"`
		Outcome          string         `json:"outcome"`
		Detail           map[string]any `json:"detail"`
		RequestID        string         `json:"request_id,omitempty"`
	}

	out := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.ID, &e.At, &e.ActorUserID, &e.ActorLabel,
			&e.ActorRole, &e.ActorIP, &e.Action, &e.ResourceType, &e.ResourceID,
			&e.JurisdictionCode, &e.Outcome, &e.Detail, &e.RequestID); err != nil {
			s.Log.Error("scanning audit row", "error", err)
			writeError(w, http.StatusInternalServerError, "AUDIT_READ_FAILED",
				"the request could not be completed")
			return
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		s.Log.Error("reading audit log", "error", err)
		writeError(w, http.StatusInternalServerError, "AUDIT_READ_FAILED",
			"the request could not be completed")
		return
	}

	writeJSON(w, http.StatusOK, page.Wrap(out, total, pg))
}
