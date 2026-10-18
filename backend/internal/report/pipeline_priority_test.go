package report

import (
	"context"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/assessjob"
)

// The pipeline at volume: new claims are not starved by waiting ones, a
// finished assessment moves its claim without being polled for, the job and
// the claim commit together, and the evidence deadline releases a calamity's
// worth of claims without one enormous update.

func TestANewClaimIsAcknowledgedBeforeWaitingClaimsAreLookedAt(t *testing.T) {
	// The waiting claim has been due for an hour; the new one only now. By
	// time alone the waiting one goes first, and with twenty thousand of them
	// the new claim's receipt never came.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)
	ctx := context.Background()

	waiting := completeClaim(t, s, pool, "prio-0000000001")
	drain(t, s, &fakeAssessor{ended: false}, 3)
	if _, a := stateOf(t, pool, waiting); a != "queued" {
		t.Fatalf("assessment_state = %s; the setup wants a claim waiting on one", a)
	}
	if _, err := pool.Exec(ctx, `UPDATE crop_loss_reports
		SET pipeline_next_at = now() - interval '1 hour' WHERE id = $1`, waiting); err != nil {
		t.Fatal(err)
	}

	fresh := completeClaim(t, s, pool, "prio-0000000002")
	step, worked, err := s.AdvanceOne(ctx, &fakeAssessor{ended: false})
	if err != nil || !worked {
		t.Fatalf("advancing: worked=%v err=%v", worked, err)
	}
	if step != StepAcknowledged {
		t.Fatalf("took %s; the new claim's receipt comes before a look at a waiting one", step)
	}
	if state, _ := stateOf(t, pool, fresh); state != "acknowledged" {
		t.Errorf("new claim is %s, want acknowledged", state)
	}

	// With no work left, the waiting claim is looked at.
	step, _, err = s.AdvanceOne(ctx, &fakeAssessor{ended: false})
	if err != nil {
		t.Fatal(err)
	}
	if step != StepAssessQueued && step != StepAssessWait {
		t.Errorf("took %s next", step)
	}
}

func TestAWaitingClaimIsLookedAtRarelyAndWokenWhenItsAssessmentEnds(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	id := completeClaim(t, s, pool, "wake-0000000001")
	queue := assessjob.New(pool)

	// Acknowledge, queue, look once.
	var took []PipelineStep
	for range 3 {
		step, _, err := s.AdvanceOne(ctx, queue)
		if err != nil {
			t.Fatal(err)
		}
		took = append(took, step)
		if step != StepAssessWait {
			if _, err := pool.Exec(ctx, `UPDATE crop_loss_reports
				SET pipeline_next_at = now() WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if took[2] != StepAssessWait {
		t.Fatalf("steps %v; want a look at the queued assessment third", took)
	}

	var wait time.Duration
	var jobID string
	if err := pool.QueryRow(ctx, `
		SELECT extract(epoch FROM pipeline_next_at - now())::float8 * 1e9,
		       assessment_job_id::text
		  FROM crop_loss_reports WHERE id = $1`, id).
		Scan((*float64Duration)(&wait), &jobID); err != nil {
		t.Fatal(err)
	}
	if wait < AssessmentLookEvery-time.Minute {
		t.Errorf("next look in %s; a waiting claim is looked at every %s or so, "+
			"not every minute", wait.Round(time.Second), AssessmentLookEvery)
	}

	// The assessment gives up. The job wakes the claim; nobody polls.
	if err := queue.Fail(ctx, jobID, "no clear observation"); err != nil {
		t.Fatal(err)
	}
	var due bool
	if err := pool.QueryRow(ctx, `SELECT pipeline_next_at <= now()
		FROM crop_loss_reports WHERE id = $1`, id).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("the assessment ended and the claim is still asleep for fifteen minutes")
	}

	step, _, err := s.AdvanceOne(ctx, queue)
	if err != nil || step != StepAssessEnded {
		t.Fatalf("took %s (%v), want %s", step, err, StepAssessEnded)
	}
	step, _, err = s.AdvanceOne(ctx, queue)
	if err != nil || step != StepAllocated {
		t.Fatalf("took %s (%v); an ended assessment goes straight to the officers", step, err)
	}
}

// float64Duration scans a float number of nanoseconds into a Duration.
type float64Duration time.Duration

func (d *float64Duration) Scan(src any) error {
	*d = float64Duration(time.Duration(src.(float64)))
	return nil
}

func TestAnAssessmentQueuedInARolledBackStepLeavesNoJob(t *testing.T) {
	// The job commits with the claim or not at all. On a second connection
	// it survived the step that asked for it failing.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)
	ctx := context.Background()

	id := completeClaim(t, s, pool, "txjob-000000001")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assessjob.New(pool).Queue(ctx, tx, id, "F-1", "FARM-txjob-000000001",
		"123456", time.Now()); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("queueing: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM assessment_jobs
		WHERE crop_loss_report_id = $1`, id).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Errorf("%d jobs left behind by a step that rolled back", jobs)
	}
}

func TestTheEvidenceDeadlineReleasesAHailstormInBatches(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)
	ctx := context.Background()

	template := completeClaim(t, s, pool, "hail-template01")
	const claims = 2*releaseBatch + 37
	if _, err := pool.Exec(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id, farmer_id, farmer_name,
			village_lgd_code, village_name, sub_district_lgd_code,
			sub_district_name, district_lgd_code, district_name,
			season, crop_year, loss_category, calamity_type, occurred_at,
			reported_channel, declaration_accepted, documents_promised,
			farmer_signature_kind, farmer_signed_at, state, client_ref,
			expected_captures, evidence_complete_at, reported_at)
		SELECT 'HAIL-' || g, r.survey_event_id, r.farmer_id, r.farmer_name,
		       r.village_lgd_code, r.village_name, r.sub_district_lgd_code,
		       r.sub_district_name, r.district_lgd_code, r.district_name,
		       r.season, r.crop_year, r.loss_category, r.calamity_type,
		       r.occurred_at, r.reported_channel, r.declaration_accepted,
		       r.documents_promised, r.farmer_signature_kind,
		       r.farmer_signed_at, 'submitted', 'hail-claim-' || g,
		       8, NULL, now() - interval '3 days'
		  FROM generate_series(1, $1) g,
		       (SELECT * FROM crop_loss_reports WHERE id = $2) r`,
		claims, template); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM crop_loss_reports WHERE reference LIKE 'HAIL-%'`)
	})

	n, err := s.ReleaseOnDeadline(ctx, EvidenceDeadline)
	if err != nil {
		t.Fatal(err)
	}
	if n != claims {
		t.Fatalf("released %d of %d; every claim past the deadline goes, "+
			"however many batches it takes", n, claims)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM crop_loss_reports
		WHERE reference LIKE 'HAIL-%' AND evidence_complete_at IS NULL`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d claims still held", left)
	}
}
