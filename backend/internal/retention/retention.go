// Package retention removes what this platform is no longer allowed to keep.
//
// # Why anything here runs at all
//
// The platform holds a named person, their Aadhaar reference, their land,
// photographs of that land, and a GPS fix accurate to a few metres of where
// they stood. Under the DPDP Act personal data may be kept only as long as
// the purpose it was collected for requires, and "we keep everything" is not
// a policy but the absence of one.
//
// # Why it anonymises rather than deletes
//
// A crop loss assessment is a public record of public money. Deleting the
// report would erase the fact that a claim was made and decided, which is
// what an audit needs years later. So the record survives and the person is
// removed from it: the area and the figure stay; the name, the mobile, the
// Aadhaar hash and the photographs go.
//
// # Why it will not run without being asked twice
//
// This is the only irreversible operation in the system. Every policy is
// disabled in the migration that creates it, a dry run is the default, and
// the caller has to pass Confirm to make it write. None of that is
// belt-and-braces: an accidental purge cannot be undone from a backup taken
// after it.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/blob"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Policy is one class of data and what happens to it.
type Policy struct {
	Subject   string        `json:"subject"`
	RetainFor time.Duration `json:"retain_for_seconds"`
	Action    string        `json:"action"`
	Enabled   bool          `json:"enabled"`
	Basis     string        `json:"basis"`
}

// Result is what a run did, or would have done.
type Result struct {
	Subject  string    `json:"subject"`
	Action   string    `json:"action"`
	Cutoff   time.Time `json:"cutoff"`
	Affected int       `json:"affected"`
	DryRun   bool      `json:"dry_run"`
	Skipped  string    `json:"skipped,omitempty"`
}

// Sweeper applies the policies.
type Sweeper struct {
	pool     *db.Pool
	evidence blob.Store
	log      *slog.Logger
}

func New(pool *db.Pool, evidence blob.Store, log *slog.Logger) *Sweeper {
	return &Sweeper{pool: pool, evidence: evidence, log: log}
}

// ErrNotEnabled is returned when a policy exists but has not been turned on.
var ErrNotEnabled = errors.New("this retention policy is not enabled")

// Policies lists what is on record.
func (s *Sweeper) Policies(ctx context.Context) ([]Policy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT subject,
		       COALESCE(EXTRACT(EPOCH FROM retain_for), 0)::bigint,
		       action, enabled, basis
		FROM retention_policy ORDER BY subject`)
	if err != nil {
		return nil, fmt.Errorf("reading retention policies: %w", err)
	}
	defer rows.Close()

	out := []Policy{}
	for rows.Next() {
		var p Policy
		var seconds int64
		if err := rows.Scan(&p.Subject, &seconds, &p.Action, &p.Enabled, &p.Basis); err != nil {
			return nil, err
		}
		p.RetainFor = time.Duration(seconds) * time.Second
		out = append(out, p)
	}
	return out, rows.Err()
}

// Sweep applies one policy.
//
// confirm is the second ask. Without it the run counts what it would touch
// and writes nothing, which is what any first invocation should do — the
// number it reports is the thing worth looking at before anyone makes it
// real.
func (s *Sweeper) Sweep(ctx context.Context, subject string, confirm bool) (*Result, error) {
	var (
		seconds int64
		action  string
		enabled bool
	)
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(EXTRACT(EPOCH FROM retain_for), 0)::bigint, action, enabled
		FROM retention_policy WHERE subject = $1`, subject).
		Scan(&seconds, &action, &enabled)
	if err != nil {
		return nil, fmt.Errorf("no retention policy for %q: %w", subject, err)
	}

	if action == "keep" || seconds == 0 {
		return &Result{
			Subject: subject, Action: action, DryRun: true,
			Skipped: "this subject is kept indefinitely by policy",
		}, nil
	}
	if !enabled && confirm {
		// A disabled policy refuses to write even when confirmed. Turning it
		// on is a separate, recorded decision.
		return nil, ErrNotEnabled
	}

	cutoff := time.Now().Add(-time.Duration(seconds) * time.Second)
	res := &Result{
		Subject: subject, Action: action, Cutoff: cutoff,
		DryRun: !confirm || !enabled,
	}

	switch subject {
	case "evidence_media":
		err = s.sweepEvidence(ctx, res)
	case "farmer_contact":
		err = s.sweepFarmerContact(ctx, res)
	case "capture_location":
		err = s.sweepCaptureLocation(ctx, res)
	case "assessment_imagery":
		err = s.sweepAssessmentImagery(ctx, res)
	default:
		return nil, fmt.Errorf("no sweeper implemented for %q", subject)
	}
	if err != nil {
		return nil, err
	}

	// Recorded whether or not it wrote. A dry run that is not recorded is a
	// number somebody quoted from memory.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO retention_runs (subject, affected, dry_run, cutoff)
		VALUES ($1, $2, $3, $4)`,
		subject, res.Affected, res.DryRun, cutoff); err != nil {
		s.log.Warn("recording retention run", "subject", subject, "error", err)
	}

	s.log.Info("retention sweep",
		"subject", subject, "action", action, "cutoff", cutoff,
		"affected", res.Affected, "dry_run", res.DryRun)
	return res, nil
}

// sweepEvidence removes photographs and video past their retention.
//
// The blob is deleted first and the row is cleared second. The reverse would
// leave a row pointing at nothing, which an officer meets as a broken page;
// this way leaves an object with no row, which is space to reclaim.
func (s *Sweeper) sweepEvidence(ctx context.Context, res *Result) error {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.media_path
		FROM survey_captures c
		JOIN crop_loss_reports r ON r.id = c.report_id
		WHERE r.reported_at < $1 AND c.media_path <> ''`, res.Cutoff)
	if err != nil {
		return fmt.Errorf("finding expired evidence: %w", err)
	}
	defer rows.Close()

	type target struct{ id, path string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.path); err != nil {
			return err
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	res.Affected = len(targets)
	if res.DryRun {
		return nil
	}

	for _, t := range targets {
		if err := s.evidence.Delete(ctx, t.path); err != nil {
			// One unreadable object must not stop the sweep. It is logged and
			// the row is left alone, so the next run tries it again.
			s.log.Warn("deleting expired evidence", "capture", t.id, "error", err)
			continue
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE survey_captures SET media_path = '' WHERE id = $1`, t.id); err != nil {
			return fmt.Errorf("clearing capture %s: %w", t.id, err)
		}
	}
	return nil
}

// MatchFarmerContact selects the reports whose claimant may be removed.
//
// Exported, and a constant rather than an inline string, because these
// conditions are the difference between forgetting a settled claim and
// destroying a live one — and they are the only part of an irreversible
// operation that can be checked without a database. The tests read this.
//
// Three conditions, each load-bearing:
//
//   - anonymised_at IS NULL, so a second sweep does not reprocess rows and a
//     reader can tell a withheld name from a missing one.
//   - a settled state, because removing the farmer from a report still being
//     verified leaves officers assessing a claim with no claimant.
//   - no unanswered appeal, because a grievance is about this person's claim
//     and answering it needs to know whose it was.
const MatchFarmerContact = `
		FROM crop_loss_reports r
		WHERE r.reported_at < $1
		  AND r.anonymised_at IS NULL
		  AND r.state IN ('closed', 'withdrawn', 'adjudicated')
		  AND NOT EXISTS (
		      SELECT 1 FROM grievances g
		      WHERE g.report_id = r.id AND g.state IN ('open', 'under_review')
		  )`

// sweepFarmerContact removes the person from a settled report.
func (s *Sweeper) sweepFarmerContact(ctx context.Context, res *Result) error {
	const match = MatchFarmerContact

	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+match, res.Cutoff).
		Scan(&res.Affected); err != nil {
		return fmt.Errorf("counting reports to anonymise: %w", err)
	}
	if res.DryRun || res.Affected == 0 {
		return nil
	}

	// The figures stay. Only the person goes.
	_, err := s.pool.Exec(ctx, `
		UPDATE crop_loss_reports r SET
			farmer_name    = 'शेतकरी / withheld',
			farmer_address = NULL,
			farmer_mobile  = NULL,
			farmer_email   = NULL,
			anonymised_at  = now()
		WHERE r.id IN (SELECT r.id`+match+`)`, res.Cutoff)
	return err
}

// sweepCaptureLocation drops the GPS fix, keeping what it established.
//
// The gate results stay: "this photograph was taken inside the parcel" is the
// finding, and it survives without the coordinates that produced it.
func (s *Sweeper) sweepCaptureLocation(ctx context.Context, res *Result) error {
	const match = `
		FROM survey_captures c
		JOIN crop_loss_reports r ON r.id = c.report_id
		WHERE r.reported_at < $1 AND c.anonymised_at IS NULL
		  AND c.lat IS NOT NULL`

	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+match, res.Cutoff).
		Scan(&res.Affected); err != nil {
		return fmt.Errorf("counting captures to anonymise: %w", err)
	}
	if res.DryRun || res.Affected == 0 {
		return nil
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE survey_captures SET
			lat = NULL, lon = NULL, accuracy_m = NULL,
			heading_deg = NULL, device_label = NULL,
			anonymised_at = now()
		WHERE id IN (SELECT c.id`+match+`)`, res.Cutoff)
	return err
}

// sweepAssessmentImagery drops derived overlays, which can be recomputed.
func (s *Sweeper) sweepAssessmentImagery(ctx context.Context, res *Result) error {
	// Keyed by assessment_id; there is no separate id column.
	const match = `FROM assessment_images i
		JOIN assessments a ON a.id = i.assessment_id
		WHERE a.created_at < $1`

	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+match, res.Cutoff).
		Scan(&res.Affected); err != nil {
		return fmt.Errorf("counting imagery to remove: %w", err)
	}
	if res.DryRun || res.Affected == 0 {
		return nil
	}

	_, err := s.pool.Exec(ctx,
		`DELETE FROM assessment_images
		 WHERE assessment_id IN (SELECT i.assessment_id `+match+`)`,
		res.Cutoff)
	return err
}
