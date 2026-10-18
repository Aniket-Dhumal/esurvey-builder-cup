// Package audit records what was done, by whom, and whether it was allowed.
//
// This is not the request log. The request log answers "was the service up";
// this answers "who read that farmer's record in March", which is the question
// a grievance or an inquiry actually arrives with. The table it writes to is
// append-only at the database level, because an audit log the application can
// rewrite is not evidence.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Outcomes.
const (
	OutcomeAllow = "allow"
	OutcomeDeny  = "deny"
	OutcomeError = "error"
)

// Actions. Named as resource.verb so that a prefix query answers "everything
// that happened to assessments" without a schema change.
const (
	ActionLoginSuccess    = "auth.login.success"
	ActionLoginFailure    = "auth.login.failure"
	ActionLoginLockout    = "auth.login.lockout"
	ActionOTPRequested    = "auth.otp.requested"
	ActionOTPFailure      = "auth.otp.failure"
	ActionRefresh         = "auth.refresh"
	ActionRefreshReuse    = "auth.refresh.reuse"
	ActionLogout          = "auth.logout"
	ActionPasswordChanged = "auth.password.changed"

	ActionAuthzDenied = "authz.denied"

	ActionUserCreated    = "user.created"
	ActionUserStatusSet  = "user.status_changed"
	ActionRoleGranted    = "user.role_granted"
	ActionRoleRevoked    = "user.role_revoked"
	ActionFarmerEnrolled = "user.farmer_enrolled"

	ActionAssessmentCreated = "assessment.created"
	ActionAssessmentRead    = "assessment.read"
	ActionFarmerRead        = "farmer.read"

	// A quadrat count, recorded in a field. Audited because it is evidence an
	// adjudicator may later prefer one officer's finding over another's for,
	// and evidence with no trail is testimony.
	ActionMeasurementRecorded = "measurement.recorded"

	// A farmer drawing their own plot, and an officer agreeing it is the
	// plot. Audited because every derived figure -- the geofence on their
	// photographs, the satellite baselines, the damage percentage -- rests on
	// this boundary, and a boundary with no trail is a claim.
	ActionBoundaryTraced    = "boundary.traced"
	ActionBoundaryConfirmed = "boundary.confirmed"

	// One rung of the approval ladder. Audited because four officers may each
	// pass a different figure on the same field and the record has to answer
	// which of them set the one a farmer was paid on.
	ActionReportApproved = "report.approved"

	// Sent back a level, with a reason. Audited separately from an approval
	// because a report that has gone up and down three times is a different
	// case from one that took three days, and only the trail shows it.
	ActionReportReturned = "report.returned"
)

// Event is one auditable act.
type Event struct {
	At time.Time

	ActorUserID string
	// ActorLabel identifies the actor when there is no account behind them
	// yet -- the username someone tried to sign in as, for instance, which is
	// precisely the field a failed-sign-in investigation needs.
	ActorLabel string
	ActorRole  string
	ActorIP    string

	Action           string
	ResourceType     string
	ResourceID       string
	JurisdictionCode string

	// JurisdictionID is the exact node, when the caller holds it. Otherwise
	// it is resolved from JurisdictionCode at JurisdictionLevel when the entry
	// is written -- village, unless the caller says otherwise, because nearly
	// every entry is about a claim, a parcel or a farmer, and those are
	// villages. A code is unique only within a level, so a district's entry
	// must name its level or its id; see migration 0044.
	JurisdictionID    string
	JurisdictionLevel string

	Outcome   string
	Detail    map[string]any
	RequestID string
}

// FromContext fills in the actor from the request's principal.
func (e Event) FromContext(ctx context.Context) Event {
	if p := rbac.FromContext(ctx); p != nil {
		e.ActorUserID = p.UserID
		e.ActorRole = string(p.PrimaryRole())
		if e.ActorLabel == "" {
			e.ActorLabel = p.Name
		}
	}
	return e
}

// Recorder writes audit events.
//
// Two paths, because the events differ in what a loss would cost:
//
//   - Record is buffered. Reads are high-volume and the worst case of losing
//     one to a hard kill is a gap in a browsing history.
//   - RecordSync writes before returning. Sign-ins, denials, grants and
//     anything that changes what someone may do go through it, because those
//     are the entries an inquiry is actually looking for and a buffer that
//     dies with the process is not where they belong.
type Recorder struct {
	pool *db.Pool
	log  *slog.Logger

	ch     chan Event
	wg     sync.WaitGroup
	closed chan struct{}
	once   sync.Once
}

const (
	bufferSize    = 512
	flushInterval = 500 * time.Millisecond
	flushBatch    = 64
)

// New starts a recorder. Close it to drain.
func New(pool *db.Pool, log *slog.Logger) *Recorder {
	r := &Recorder{
		pool:   pool,
		log:    log,
		ch:     make(chan Event, bufferSize),
		closed: make(chan struct{}),
	}
	r.wg.Add(1)
	go r.run()
	return r
}

// Record queues an event.
//
// When the buffer is full the event is written inline instead of dropped. The
// resulting backpressure on the request is deliberate: an audit trail with
// holes in it under load is worse than a request that took a few milliseconds
// longer.
func (r *Recorder) Record(ctx context.Context, e Event) {
	e = normalise(e)

	select {
	case <-r.closed:
		return
	default:
	}

	select {
	case r.ch <- e:
	default:
		if err := r.write(ctx, []Event{e}); err != nil {
			r.log.Error("audit write failed", "action", e.Action, "error", err)
		}
	}
}

// RecordSync writes an event before returning.
func (r *Recorder) RecordSync(ctx context.Context, e Event) error {
	return r.write(ctx, []Event{normalise(e)})
}

// Close drains the buffer and stops the writer.
func (r *Recorder) Close(ctx context.Context) error {
	r.once.Do(func() {
		close(r.closed)
		close(r.ch)
	})

	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("audit recorder did not drain before shutdown")
	}
}

func (r *Recorder) run() {
	defer r.wg.Done()

	batch := make([]Event, 0, flushBatch)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Detached from any request context: a flush triggered by shutdown
		// must not be cancelled by the request that happened to fill it.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := r.write(ctx, batch); err != nil {
			r.log.Error("audit batch write failed", "events", len(batch), "error", err)
		}
		cancel()
		batch = batch[:0]
	}

	for {
		select {
		case e, ok := <-r.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= flushBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (r *Recorder) write(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	b := &pgx.Batch{}
	for _, e := range events {
		detail, err := json.Marshal(e.Detail)
		if err != nil {
			detail = []byte(`{}`)
		}
		b.Queue(`
			INSERT INTO audit_log (
				at, actor_user_id, actor_label, actor_role, actor_ip,
				action, resource_type, resource_id, jurisdiction_code,
				outcome, detail, request_id, jurisdiction_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
				COALESCE($13::uuid, (
					SELECT j.id FROM jurisdictions j
					 WHERE $9::text IS NOT NULL AND j.code = $9::text
					   AND j.level::text = COALESCE($14::text, 'village'))))`,
			e.At,
			nullString(e.ActorUserID), nullString(e.ActorLabel),
			nullString(e.ActorRole), nullIP(e.ActorIP),
			e.Action, nullString(e.ResourceType), nullString(e.ResourceID),
			nullString(e.JurisdictionCode),
			e.Outcome, detail, nullString(e.RequestID),
			nullString(e.JurisdictionID), nullString(e.JurisdictionLevel))
	}

	results := r.pool.SendBatch(ctx, b)
	defer results.Close()
	for range events {
		if _, err := results.Exec(); err != nil {
			return err
		}
	}
	return nil
}

func normalise(e Event) Event {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeAllow
	}
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	return e
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullIP drops anything that is not a parseable address rather than failing
// the insert. A malformed forwarded header must not be able to suppress an
// audit entry.
func nullIP(s string) any {
	if s == "" {
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return addr.String()
}
