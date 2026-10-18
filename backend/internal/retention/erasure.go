package retention

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Erasing one person, on request.
//
// # Why this is separate from the sweep
//
// Everything else in this package is time-based: data reaches an age and the
// policy applies. That covers the obligation to not keep things forever. It
// does not cover the other one -- a named person asking to be erased now,
// under the DPDP Act, years before any policy would have reached them.
//
// Before this existed there was no answer to that request. The sweep could not
// be aimed at a person, and deleting the account was impossible: the audit log
// references it, and the reference is ON DELETE SET NULL, which is an UPDATE,
// which the append-only trigger refuses. So a request would have been met by
// somebody editing rows by hand -- the worst possible way to carry out a
// data-protection duty, since it leaves no record that it was done properly.
//
// # What erasure means here, and what it cannot mean
//
// The person is removed from the records. The records are not removed.
//
// A crop loss assessment is a decision about public money, and the fact that a
// claim was made, assessed at a figure, and paid or refused has to survive for
// audit. So the area, the figures and the decisions remain; the name, the
// mobile, the Aadhaar reference, the photographs and the GPS fixes go.
//
// The audit log is untouched and cannot be otherwise. It is append-only and
// hash-chained precisely so that nobody -- including someone acting on a
// legitimate erasure request, and including someone claiming to be -- can
// rewrite the record of who did what. What it retains about an erased person
// is an opaque uuid and, on authentication events, a mobile number masked to
// its last two digits. That is a deliberate and defensible limit, and it is
// the reason actor labels are masked at the point they are written rather than
// at the point somebody asks.

// ErrOpenGrievance refuses erasure while an objection is being decided.
//
// Erasing somebody mid-appeal destroys the appellant's own case: the
// photographs an officer is weighing, the figures under challenge, the record
// of what was claimed. A request made during an appeal is far more likely to
// be somebody else acting in that person's name than the person choosing to
// abandon their own claim, and the cost of being wrong is one-directional.
var ErrOpenGrievance = errors.New(
	"this person has an objection still being decided; erasing now would " +
		"destroy the evidence their own appeal rests on")

// withheld is what a redacted name reads as.
//
// Bilingual and identical everywhere, so a reader who meets it on a report and
// again on an objection recognises the same thing rather than wondering
// whether two different processes ran.
const withheld = "शेतकरी / withheld"

// ErrNoSuchSubject is returned when the farmer id matches nothing.
var ErrNoSuchSubject = errors.New("no records for that farmer id")

// Erasure is what a request did, or would have done.
type Erasure struct {
	FarmerID string `json:"farmer_id"`

	// DryRun is the default. The first call always counts and writes nothing:
	// this is the one operation here that cannot be undone from a backup taken
	// afterwards, so the number it reports is the thing to look at before
	// anybody makes it real.
	DryRun bool `json:"dry_run"`

	Reports    int64 `json:"reports"`
	Captures   int64 `json:"captures"`
	Accounts   int64 `json:"accounts"`
	Grievances int64 `json:"grievances"`

	At time.Time `json:"at"`
}

// ErasePerson removes a named person from the records that name them.
//
// confirm is the second ask, matching Sweep: without it nothing is written.
func (s *Sweeper) ErasePerson(
	ctx context.Context, farmerID string, confirm bool,
) (*Erasure, error) {
	if farmerID == "" {
		return nil, ErrNoSuchSubject
	}

	res := &Erasure{FarmerID: farmerID, DryRun: !confirm, At: time.Now().UTC()}

	// Refused before anything is counted, so a caller cannot learn what a
	// person's records look like by asking to erase them mid-appeal.
	var open int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM grievances g
		JOIN crop_loss_reports r ON r.id = g.report_id
		WHERE r.farmer_id = $1 AND g.state IN ('open', 'under_review')`,
		farmerID).Scan(&open); err != nil {
		return nil, fmt.Errorf("checking for open objections: %w", err)
	}
	if open > 0 {
		return nil, ErrOpenGrievance
	}

	if err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM crop_loss_reports
			 WHERE farmer_id = $1 AND anonymised_at IS NULL),
			(SELECT count(*) FROM survey_captures c
			 JOIN crop_loss_reports r ON r.id = c.report_id
			 WHERE r.farmer_id = $1 AND c.media_path <> ''),
			(SELECT count(*) FROM users
			 WHERE agristack_farmer_id = $1 AND aadhaar_hash IS NOT NULL),
			(SELECT count(*) FROM grievances
			 WHERE farmer_id = $1 AND farmer_name <> $2)`,
		farmerID, withheld).Scan(&res.Reports, &res.Captures, &res.Accounts,
		&res.Grievances); err != nil {
		return nil, fmt.Errorf("counting what would be erased: %w", err)
	}

	if res.Reports == 0 && res.Captures == 0 && res.Accounts == 0 &&
		res.Grievances == 0 {
		// Either the id is unknown or this person has already been erased.
		// The two are answered identically: distinguishing them would make
		// this an oracle for which farmer ids exist.
		return res, nil
	}
	if res.DryRun {
		return res, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("erasing: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The media first. An object with no row pointing at it is unreachable
	// and unaccountable; a row pointing at an object that is gone is merely
	// a missing photograph, which is what erasure means anyway.
	if _, err := tx.Exec(ctx, `
		UPDATE survey_captures c SET media_path = '', lat = NULL, lon = NULL
		FROM crop_loss_reports r
		WHERE r.id = c.report_id AND r.farmer_id = $1`, farmerID); err != nil {
		return nil, fmt.Errorf("erasing captures: %w", err)
	}

	// The reports keep their figures and lose their person.
	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports SET
			farmer_name    = $2,
			farmer_address = NULL,
			farmer_mobile  = NULL,
			farmer_email   = NULL,
			anonymised_at  = now()
		WHERE farmer_id = $1`, farmerID, withheld); err != nil {
		return nil, fmt.Errorf("erasing reports: %w", err)
	}

	// Objections carry the person twice: their name, and a statement they
	// wrote themselves.
	//
	// The statement goes with the name. Free text a farmer wrote about their
	// own field is some of the most re-identifying data here -- "the plot
	// below Ramrao's well" names a person without naming them -- and no
	// redaction of a name reaches it. What survives is the ground, which is a
	// coded value, and the decision: an auditor can still see that an
	// objection on these grounds was made and how it was decided, which is
	// what the record of a decision about public money has to show.
	if _, err := tx.Exec(ctx, `
		UPDATE grievances SET
			farmer_name = $2,
			statement   = 'withheld at the claimant''s request'
		WHERE farmer_id = $1`, farmerID, withheld); err != nil {
		return nil, fmt.Errorf("erasing objections: %w", err)
	}

	// The account. Not deleted -- it cannot be, and should not be: the audit
	// log's references to it are how "who did this" stays answerable. Emptied
	// of everything that identifies a person, and deactivated so the shell
	// cannot be signed into.
	//
	// agristack_farmer_id is kept. It is the registry's own key, it is what
	// makes this erasure repeatable and verifiable, and without it a second
	// request for the same person could not be matched to anything.
	if _, err := tx.Exec(ctx, `
		UPDATE users SET
			full_name     = 'withheld',
			email         = NULL,
			mobile_e164   = NULL,
			aadhaar_hash  = NULL,
			aadhaar_last4 = NULL,
			status        = 'deactivated'
		WHERE agristack_farmer_id = $1`, farmerID); err != nil {
		return nil, fmt.Errorf("erasing the account: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("erasing: %w", err)
	}
	return res, nil
}
