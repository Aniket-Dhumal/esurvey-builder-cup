package report

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// A claim advancing itself, from arriving to sitting with the two officers who
// will verify it.
//
// # What this replaces
//
// Three manual steps that nobody performed. A claim landed in `submitted` and
// waited for an officer to acknowledge it, for somebody to ask for an
// assessment, and for an allocation the console could not even request. On a
// quiet week that is a delay; on the evening a district takes ten thousand
// claims it is the whole system not working.
//
// # The shape
//
// The same one this platform uses for everything that must not be lost: claim
// a row with FOR UPDATE SKIP LOCKED, do one step, record where it got to, and
// come back. Every step is idempotent, because the process will die between
// two of them eventually and the claim has to be safe to pick up again.
//
// The claim's own `state` drives it rather than a second status column. Two
// columns that must agree about the same thing eventually disagree, and the
// one an officer reads would be the wrong one.
//
// # The steps
//
//	submitted    + evidence complete -> acknowledged, with the receipt sent
//	acknowledged + no assessment yet -> the assessment is queued
//	acknowledged + assessment ended  -> allocated to the village pair, assigned
//
// From `assigned` the existing workflow takes over: the pair record blind,
// Reconcile compares them, and the ladder runs from the Circle upwards.

// PipelineBackoff is how long a step waits before being tried again.
//
// Short, and jittered. The failures here are a village with no Talathi posted
// and an assessment queue that has not finished -- neither is resolved by
// hammering, and both are resolved within minutes to hours. The jitter is what
// stops ten thousand claims from one district event retrying in lockstep.
func PipelineBackoff(attempt int) time.Duration {
	base := time.Duration(1<<min(attempt, 6)) * 30 * time.Second
	if base > 30*time.Minute {
		base = 30 * time.Minute
	}
	return base + jitter(base/2)
}

// jitter is a random wait in [0, max), from crypto/rand.
//
// A backoff delay is not a secret, but this codebase refuses math/rand
// outright (see auth's imports test) rather than judging each use: the one
// place it is harmless is the place somebody copies it from for a token.
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		// No randomness to be had. A retry without jitter still retries.
		return 0
	}
	return time.Duration(n.Int64())
}

// EvidenceDeadline is how long a claim waits for evidence that is still
// arriving before it is sent on without it.
//
// Two days. A farmer whose phone has no signal for a day is ordinary and must
// not have their claim treated as short; a photograph that has not arrived in
// two days is one that is not coming, and the claim is worth more assessed on
// what did arrive than held for ever.
const EvidenceDeadline = 48 * time.Hour

// AssessmentLookEvery is how often a claim waiting on its assessment is looked
// at, with up to AssessmentLookJitter more.
//
// A safety net, not the signal. The assessment queue wakes its claim when a
// job ends (assessjob.wakeClaim); this covers a process that died between the
// two writes. It was one minute, and at twenty thousand waiting claims that
// was 333 looks a second -- more than a pipeline loop does -- so new claims
// never got their receipt. At fifteen minutes a lakh of waiting claims costs
// about a hundred looks a second, taken only when there is no real work (see
// claimNext).
const (
	AssessmentLookEvery  = 15 * time.Minute
	AssessmentLookJitter = 5 * time.Minute
)

// releaseBatch is how many claims one deadline update releases.
//
// A calamity puts tens of thousands of claims on the same deadline two days
// later. One UPDATE over all of them holds every one of those rows locked for
// as long as it runs, and a farmer's late photograph arriving meanwhile waits
// on it; a thousand at a time keeps each lock short.
const releaseBatch = 1000

// ErrNoAssessor means the pipeline has nowhere to send an assessment request.
var ErrNoAssessor = errors.New("no assessment queue is configured")

// Assessor queues an assessment for a claim. Implemented by assessjob.Store,
// taken as an interface so this package does not depend on it -- the pipeline
// is about the claim's progress, not about how an assessment is produced.
type Assessor interface {
	// Queue records a request and returns its id. Must be idempotent for a
	// report that already has one.
	//
	// q is the pipeline's own transaction. The job commits with the claim's
	// assessment_state or not at all: queued on a second connection, a step
	// that failed after the insert left a job no claim pointed at, and a
	// process that died before its commit left a claim marked 'none' with a
	// job already running for it.
	Queue(ctx context.Context, q db.Querier, reportID, farmerID, farmID, villageCode string, incidentAt time.Time) (string, error)
	// Ended reports whether a queued assessment has finished, either way. An
	// assessment that failed is still an answer: the claim goes to the
	// officers without one rather than waiting for a service to recover.
	Ended(ctx context.Context, jobID string) (bool, error)
}

// PipelineStep is what the worker did, for logging and for tests.
type PipelineStep string

const (
	StepNone         PipelineStep = "none"
	StepAcknowledged PipelineStep = "acknowledged"
	StepAssessQueued PipelineStep = "assessment_queued"
	StepAssessWait   PipelineStep = "assessment_waiting"
	StepAssessEnded  PipelineStep = "assessment_ended"
	StepAllocated    PipelineStep = "allocated"
)

// AdvanceOne moves the next claim that is due one step.
//
// Reports which step was taken, and false when there was nothing to do. One
// claim at a time: a worker holding a batch leaves the rest of it unworked if
// the process dies, and the steps here are short enough that batching buys
// nothing.
func (s *Store) AdvanceOne(ctx context.Context, assessor Assessor) (PipelineStep, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return StepNone, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var (
		id, state, villageCode, farmerID string
		farmID                           *string
		occurredAt                       time.Time
		assessmentState                  string
		assessmentJobID                  *string
		attempts                         int
	)
	// Work first: a claim with a step to take. Only when there is none, a
	// claim waiting on its assessment. One queue ordered by time put every new
	// claim's receipt behind the waiting ones -- see migration 0047.
	claim := func(waiting bool) error {
		return tx.QueryRow(ctx, claimNext(waiting)).
			Scan(&id, &state, &villageCode, &farmerID, &farmID, &occurredAt,
				&assessmentState, &assessmentJobID, &attempts)
	}
	err = claim(false)
	if errors.Is(err, pgx.ErrNoRows) {
		err = claim(true)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return StepNone, false, nil
	}
	if err != nil {
		return StepNone, false, fmt.Errorf("claiming a report to advance: %w", err)
	}

	// The step runs inside a savepoint, so a statement that fails does not
	// take the transaction with it -- the row stays locked and the failure can
	// be recorded on the very claim that suffered it. Without this, a step
	// that errored left the transaction aborted and the bookkeeping write
	// failed too, so nothing was ever recorded about why a claim stopped.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return StepNone, false, err
	}
	step, stepErr := s.advance(ctx, sp, advancing{
		id: id, state: State(state), villageCode: villageCode,
		farmerID: farmerID, farmID: farmID, occurredAt: occurredAt,
		assessmentState: assessmentState, assessmentJobID: assessmentJobID,
	}, assessor)
	if stepErr != nil {
		_ = sp.Rollback(ctx)
	} else if err := sp.Commit(ctx); err != nil {
		return StepNone, false, err
	}

	if stepErr != nil {
		// Logged as well as recorded. On the first real run every claim was
		// stopped at the same step for the same reason, the reason was on
		// every row, and the service log was clean -- so the one place an
		// operator looks said everything was fine.
		if s.log != nil {
			s.log.Warn("claim did not advance",
				"report", id, "state", state, "attempt", attempts+1,
				"error", stepErr.Error())
		}

		// Not a verdict on the claim: a village with nobody posted to it, or
		// an assessment queue that is not answering. Recorded where somebody
		// can read it, and tried again.
		if _, err := tx.Exec(ctx, `
			UPDATE crop_loss_reports
			   SET pipeline_attempts = pipeline_attempts + 1,
			       pipeline_next_at = now() + $2::interval,
			       pipeline_error = $3,
			       updated_at = now()
			 WHERE id = $1`,
			id, PipelineBackoff(attempts).String(), stepErr.Error()); err != nil {
			return StepNone, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return StepNone, false, err
		}
		// Worked, in the sense that there was something to do. The caller
		// keeps going rather than sleeping on a queue that has entries.
		return StepNone, true, nil
	}

	// A step that did nothing but wait keeps its attempt count: waiting for an
	// assessment is not failing, and counting it would push the backoff out to
	// half an hour on a claim that is progressing perfectly well.
	next := (30 * time.Second).String()
	switch step {
	case StepAssessWait:
		next = (AssessmentLookEvery + jitter(AssessmentLookJitter)).String()
	case StepAssessEnded:
		// Straight on to the officers: the wait is what just ended.
		next = "0s"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET pipeline_attempts = 0,
		       pipeline_next_at = now() + $2::interval,
		       pipeline_error = NULL,
		       updated_at = now()
		 WHERE id = $1`, id, next); err != nil {
		return StepNone, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StepNone, false, err
	}
	return step, true, nil
}

// claimNext is the query that takes the next due claim: one with a step to
// take, or (waiting) one whose assessment is being waited on. Each reads its
// own partial index from migration 0047.
func claimNext(waiting bool) string {
	which := "r.assessment_state <> 'queued'"
	if waiting {
		which = "r.assessment_state = 'queued'"
	}
	return `
		SELECT r.id, r.state, r.village_lgd_code, r.farmer_id,
		       (SELECT i.farm_id FROM crop_loss_report_items i
		         WHERE i.report_id = r.id AND i.farm_id IS NOT NULL
		         ORDER BY i.line_no LIMIT 1),
		       r.occurred_at, r.assessment_state, r.assessment_job_id,
		       r.pipeline_attempts
		FROM crop_loss_reports r
		WHERE r.state IN ('submitted', 'acknowledged')
		  AND ` + which + `
		  AND r.evidence_complete_at IS NOT NULL
		  AND r.pipeline_next_at <= now()
		ORDER BY r.pipeline_next_at, r.reported_at
		-- NO KEY UPDATE, not UPDATE. An assessment job's foreign key to this
		-- report takes a KEY SHARE lock, which FOR UPDATE blocks. The job is
		-- now inserted in this same transaction, but the assessment worker's
		-- wake-up and anything else keying on the report still need that
		-- lock, and this worker never changes a report's key. (It was FOR
		-- UPDATE once, with the insert on a second connection, and every
		-- acknowledged claim waited on itself until the statement timeout.)
		FOR NO KEY UPDATE SKIP LOCKED
		LIMIT 1`
}

type advancing struct {
	id              string
	state           State
	villageCode     string
	farmerID        string
	farmID          *string
	occurredAt      time.Time
	assessmentState string
	assessmentJobID *string
}

func (s *Store) advance(
	ctx context.Context, tx pgx.Tx, a advancing, assessor Assessor,
) (PipelineStep, error) {
	switch {
	// The receipt. Everything the farmer was promised arrived, so the claim is
	// acknowledged by the system rather than by whoever is at a desk: it was
	// decided on 2026-09-16 that complete receipt is the acknowledgement, and
	// the reference the farmer already holds is what they track it by.
	case a.state == StateSubmitted:
		if _, err := tx.Exec(ctx, `
			UPDATE crop_loss_reports
			   SET state = 'acknowledged', acknowledged_at = now(), updated_at = now()
			 WHERE id = $1 AND state = 'submitted'`, a.id); err != nil {
			return StepNone, fmt.Errorf("acknowledging: %w", err)
		}

		// The à¤ªà¥‹à¤¹à¥‹à¤š à¤ªà¤¾à¤µà¤¤à¥€, which the manual acknowledgement has always sent:
		// the one message a farmer is owed by the act of filing, and the only
		// proof they have that the report reached anyone.
		//
		// Automating the acknowledgement without this would have taken that
		// message away in the same change that was meant to make the receipt
		// arrive sooner -- and taken it away silently, which is how it would
		// have stayed gone. Queued in the same transaction as the state, so a
		// process dying between the two cannot leave a farmer acknowledged and
		// untold.
		// target is the helper built for exactly this: it reads inside the
		// caller's transaction and takes no jurisdiction scope, because the
		// notifier acts on behalf of a decision already authorised.
		if rep := s.target(ctx, tx, a.id); rep != nil {
			s.tell(ctx, tx, notify.EventReportAcknowledged, rep, notifyVars(rep))
		}
		return StepAcknowledged, nil

	// The assessment, asked for by nobody.
	case a.assessmentState == "none":
		if assessor == nil {
			return StepNone, ErrNoAssessor
		}
		if a.farmID == nil || *a.farmID == "" {
			// Nothing to assess against: a claim whose line names no registry
			// parcel. The officers verify it on the ground, which is what they
			// would have done with an assessment anyway.
			if _, err := tx.Exec(ctx, `
				UPDATE crop_loss_reports SET assessment_state = 'skipped',
				       updated_at = now() WHERE id = $1`, a.id); err != nil {
				return StepNone, err
			}
			return StepAssessQueued, nil
		}
		jobID, err := assessor.Queue(
			ctx, tx, a.id, a.farmerID, *a.farmID, a.villageCode, a.occurredAt)
		if err != nil {
			return StepNone, fmt.Errorf("queueing the assessment: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE crop_loss_reports
			   SET assessment_state = 'queued', assessment_job_id = $2,
			       updated_at = now()
			 WHERE id = $1`, a.id, jobID); err != nil {
			return StepNone, err
		}
		return StepAssessQueued, nil

	// Waiting on it.
	case a.assessmentState == "queued":
		if assessor == nil {
			return StepNone, ErrNoAssessor
		}
		var jobID string
		if a.assessmentJobID != nil {
			jobID = *a.assessmentJobID
		}
		ended, err := assessor.Ended(ctx, jobID)
		if err != nil {
			return StepNone, fmt.Errorf("reading the assessment: %w", err)
		}
		if !ended {
			return StepAssessWait, nil
		}
		// Done or given up on, the claim moves either way. A satellite service
		// having a bad week is not a reason to hold somebody's claim, and the
		// two officers are the ones who decide it regardless.
		if _, err := tx.Exec(ctx, `
			UPDATE crop_loss_reports SET assessment_state = 'done',
			       updated_at = now() WHERE id = $1`, a.id); err != nil {
			return StepNone, err
		}
		return StepAssessEnded, nil

	// And on to the two officers who will actually decide it.
	default:
		if err := s.allocateTx(ctx, tx, a.id, a.villageCode); err != nil {
			return StepNone, err
		}
		return StepAllocated, nil
	}
}

// ReleaseOnDeadline sends on the claims whose evidence stopped arriving.
//
// A photograph that has not landed in two days is not coming: the handset has
// been lost, wiped, or is somewhere with no signal it will return from. The
// claim is worth more assessed on what did arrive than held for ever, and the
// officers are told the evidence is short rather than left to count.
//
// Returns how many were released.
func (s *Store) ReleaseOnDeadline(ctx context.Context, after time.Duration) (int, error) {
	total := 0
	for {
		tag, err := s.pool.Exec(ctx, `
			UPDATE crop_loss_reports
			   SET evidence_complete_at = now(),
			       evidence_incomplete = true,
			       pipeline_next_at = now(),
			       updated_at = now()
			 WHERE id IN (
			       SELECT id FROM crop_loss_reports
			        WHERE evidence_complete_at IS NULL
			          AND reported_at < now() - $1::interval
			        ORDER BY reported_at
			        LIMIT $2
			        -- A claim whose photograph is landing right now is
			        -- skipped, not waited on; the next sweep has it if it
			        -- is still short.
			        FOR UPDATE SKIP LOCKED)`, after.String(), releaseBatch)
		if err != nil {
			return total, fmt.Errorf("releasing claims on the evidence deadline: %w", err)
		}
		n := int(tag.RowsAffected())
		total += n
		if n < releaseBatch || ctx.Err() != nil {
			return total, nil
		}
	}
}

// allocateTx is Allocate inside a caller's transaction.
//
// The pipeline allocates as part of the same commit that records it did, so a
// process dying between the two cannot leave a claim assigned to officers with
// nothing saying why, or marked allocated with nobody holding it.
func (s *Store) allocateTx(ctx context.Context, tx pgx.Tx, reportID, villageCode string) error {
	rows, err := tx.Query(ctx,
		`SELECT user_id, officer_role FROM village_authorities($1)`, villageCode)
	if err != nil {
		return fmt.Errorf("reading village authorities: %w", err)
	}
	type post struct{ user, role string }
	var posted []post
	for rows.Next() {
		var p post
		if err := rows.Scan(&p.user, &p.role); err != nil {
			rows.Close()
			return err
		}
		posted = append(posted, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// The staffing gap, said plainly. A village with nobody posted to it
	// cannot verify anything, and this is the message that tells the District
	// which village to staff -- it is read back on the claim and listed in the
	// console. Retried, because staffing it is the fix and it may happen an
	// hour from now.
	if len(posted) == 0 {
		return fmt.Errorf("%w: village %s has no Talathi or Krushi Sahayak posted",
			ErrNoAuthority, villageCode)
	}

	seen := map[string]bool{}
	due := time.Now().UTC().Add(VerificationSLA)
	for _, p := range posted {
		if seen[p.role] {
			continue // one per role: the first is the nearest posting
		}
		seen[p.role] = true
		if _, err := tx.Exec(ctx, `
			INSERT INTO report_assignments (report_id, officer_id, role, tier, due_at)
			VALUES ($1, $2, $3::officer_role, 'village_pair', $4)
			ON CONFLICT DO NOTHING`, reportID, p.user, p.role, due); err != nil {
			return fmt.Errorf("assigning %s: %w", p.role, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports SET state = 'assigned', updated_at = now()
		 WHERE id = $1 AND state = 'acknowledged'`, reportID); err != nil {
		return err
	}
	return nil
}

// Stuck is one village's claims that the pipeline cannot advance.
//
// Grouped by village rather than listed per claim, because the answer is
// always the same one action -- post an officer to that village -- and a
// District looking at four hundred individually identical rows will not take
// it.
type Stuck struct {
	VillageCode string    `json:"village_lgd_code"`
	VillageName string    `json:"village_name"`
	Claims      int       `json:"claims"`
	Since       time.Time `json:"since"`
	Reason      string    `json:"reason"`
}

// StuckClaims lists where claims have stopped, worst first.
//
// # Why this has a screen of its own
//
// A claim that cannot be allocated stops silently. The farmer's app says it is
// with the office, the office's queues do not show it because it has not
// reached anybody's queue, and the only record is a sentence on a row nobody
// is looking at. That is the failure the pipeline was built to prevent, and it
// would have reintroduced it in a new place.
//
// Worst first by claim count: a village with four hundred waiting is a
// district-wide event with nobody posted to receive it, and a village with one
// is somebody on leave.
func (s *Store) StuckClaims(ctx context.Context, scope rbac.Scope) ([]Stuck, error) {
	if scope.Empty() {
		return []Stuck{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.village_lgd_code, max(r.village_name), count(*),
		       min(r.reported_at), max(r.pipeline_error)
		  FROM crop_loss_reports r
		 WHERE r.pipeline_error IS NOT NULL
		   AND r.state IN ('submitted', 'acknowledged')
		   AND `+scopePredicate("$1", "$2")+`
		 GROUP BY r.village_lgd_code
		 ORDER BY count(*) DESC, min(r.reported_at)`,
		nullableUUIDs(scope.JurisdictionIDs), nullString(scope.FarmerID))
	if err != nil {
		return nil, fmt.Errorf("reading stuck claims: %w", err)
	}
	defer rows.Close()

	out := []Stuck{}
	for rows.Next() {
		var st Stuck
		if err := rows.Scan(&st.VillageCode, &st.VillageName, &st.Claims,
			&st.Since, &st.Reason); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// RetryVillage makes a village's stopped claims due again, now.
//
// The pipeline retries on its own, with a backoff that reaches half an hour --
// right for a queue riding out a bad afternoon, and wrong for the moment a
// District has just posted the officer those claims were waiting for. Without
// this, staffing a village is followed by up to thirty minutes of nothing
// happening, which reads as the staffing not having worked.
//
// Only claims that actually stopped: this clears the backoff, it does not
// reorder the queue.
func (s *Store) RetryVillage(ctx context.Context, villageCode string, scope rbac.Scope) (int, error) {
	if scope.Empty() {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE crop_loss_reports r
		   SET pipeline_next_at = now(), pipeline_attempts = 0, updated_at = now()
		 WHERE r.village_lgd_code = $3
		   AND r.pipeline_error IS NOT NULL
		   AND r.state IN ('submitted', 'acknowledged')
		   AND `+scopePredicate("$1", "$2"),
		nullableUUIDs(scope.JurisdictionIDs), nullString(scope.FarmerID), villageCode)
	if err != nil {
		return 0, fmt.Errorf("releasing a village's claims: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
