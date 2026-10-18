package report

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/assessjob"
	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// A claim moving itself from arriving to sitting with the two officers.
//
// Everything here is about the ways a pipeline stops without saying so. A
// claim that waits for evidence that is never coming, one held because a
// satellite service is down, one that cannot be allocated because nobody is
// posted to the village -- each of those is a farmer's claim going nowhere
// while every screen says it is being processed.

// fakeAssessor stands in for the assessment queue.
type fakeAssessor struct {
	queued   int
	village  string
	jobID    string
	ended    bool
	queueErr error
	endedErr error
}

func (f *fakeAssessor) Queue(
	ctx context.Context, q db.Querier, reportID, farmerID, farmID, villageCode string, at time.Time,
) (string, error) {
	if f.queueErr != nil {
		return "", f.queueErr
	}
	f.village = villageCode
	f.queued++
	if f.jobID == "" {
		f.jobID = "11111111-1111-1111-1111-111111111111"
	}
	return f.jobID, nil
}

func (f *fakeAssessor) Ended(ctx context.Context, jobID string) (bool, error) {
	if f.endedErr != nil {
		return false, f.endedErr
	}
	return f.ended, nil
}

// postAuthorities puts the village's Talathi and Krushi Sahayak back on duty.
//
// seedAdjudicated posts them; this undoes clearAuthorities, so a test can show
// that staffing a village is enough to unstick the claims waiting on it.
func postAuthorities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE user_roles SET revoked_at = NULL
		 WHERE role IN ('talathi', 'krushi_sahayak')`); err != nil {
		t.Fatal(err)
	}
}

// clearAuthorities empties the village: nobody posted to verify anything.
func clearAuthorities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE user_roles SET revoked_at = now()
		 WHERE role IN ('talathi', 'krushi_sahayak')`); err != nil {
		t.Fatal(err)
	}
}

// completeClaim files a claim whose line names a registry parcel.
//
// The parcel matters: an assessment is of a piece of land, so a claim naming
// none is skipped rather than queued. See completeClaimWithoutAParcel.
func completeClaim(t *testing.T, s *Store, pool *pgxpool.Pool, ref string) string {
	t.Helper()
	in := newFiling(t, pool, ref)
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	in.Items[0].FarmID = "FARM-" + ref
	r, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("filing: %v", err)
	}
	return r.ID
}

func completeClaimWithoutAParcel(t *testing.T, s *Store, pool *pgxpool.Pool, ref string) string {
	t.Helper()
	in := newFiling(t, pool, ref)
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	r, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("filing: %v", err)
	}
	return r.ID
}

// stateOf reads where a claim has got to.
func stateOf(t *testing.T, pool *pgxpool.Pool, id string) (state, assessment string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT state, assessment_state FROM crop_loss_reports WHERE id = $1`,
		id).Scan(&state, &assessment); err != nil {
		t.Fatal(err)
	}
	return state, assessment
}

// drain advances until nothing is due, or the step budget runs out.
func drain(t *testing.T, s *Store, a Assessor, steps int) []PipelineStep {
	t.Helper()
	var took []PipelineStep
	for i := 0; i < steps; i++ {
		step, worked, err := s.AdvanceOne(context.Background(), a)
		if err != nil {
			t.Fatalf("advancing: %v", err)
		}
		if !worked {
			break
		}
		took = append(took, step)
		// The pipeline puts a claim a little into the future after each step,
		// so a test that wants the next one has to be there already.
		//
		// A claim that failed keeps its backoff. Resetting that too would let
		// one stuck claim win every pass on the ordering and starve every
		// other claim in the test -- which is exactly what the real backoff
		// exists to prevent.
		if _, err := s.pool.Exec(context.Background(),
			`UPDATE crop_loss_reports SET pipeline_next_at = now()
			  WHERE state IN ('submitted', 'acknowledged')
			    AND pipeline_error IS NULL`); err != nil {
			t.Fatal(err)
		}
	}
	return took
}

func TestACompleteClaimAcknowledgesItselfAndReachesTheOfficers(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	id := completeClaim(t, s, pool, "pipe-0000000001")
	assessor := &fakeAssessor{ended: true}

	steps := drain(t, s, assessor, 6)

	state, assessment := stateOf(t, pool, id)
	if state != "assigned" {
		t.Errorf("state = %s after %v; a complete claim should reach the two "+
			"officers without anybody pressing anything", state, steps)
	}
	if assessment != "done" {
		t.Errorf("assessment_state = %s, want done", assessment)
	}
	if assessor.queued != 1 {
		t.Errorf("queued %d assessments; exactly one, asked for by nobody",
			assessor.queued)
	}
}

func TestAClaimWhoseEvidenceIsStillArrivingWaits(t *testing.T) {
	// The failure this guards: assessing half a walk. The claim lands first
	// and its photographs follow, so a pipeline that starts on arrival
	// produces an assessment of whatever happened to have uploaded.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	id := completeClaim(t, s, pool, "pipe-0000000002")
	if _, err := pool.Exec(context.Background(), `
		UPDATE crop_loss_reports
		   SET expected_captures = 8, evidence_complete_at = NULL
		 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	assessor := &fakeAssessor{ended: true}
	if steps := drain(t, s, assessor, 4); len(steps) != 0 {
		t.Errorf("advanced %v with evidence still arriving", steps)
	}
	if state, _ := stateOf(t, pool, id); state != "submitted" {
		t.Errorf("state = %s; the claim should still be waiting", state)
	}
}

func TestEvidenceThatStopsArrivingDoesNotStrandTheClaim(t *testing.T) {
	// A phone lost on the way home. The photograph is not coming, and a claim
	// held for ever waiting for it is worse than one assessed on what arrived
	// -- so long as the officers are told it is short.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	id := completeClaim(t, s, pool, "pipe-0000000003")
	if _, err := pool.Exec(context.Background(), `
		UPDATE crop_loss_reports
		   SET expected_captures = 8, evidence_complete_at = NULL,
		       reported_at = now() - interval '3 days'
		 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	n, err := s.ReleaseOnDeadline(context.Background(), EvidenceDeadline)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("released %d claims; the deadline had passed", n)
	}

	var incomplete bool
	if err := pool.QueryRow(context.Background(),
		`SELECT evidence_incomplete FROM crop_loss_reports WHERE id = $1`,
		id).Scan(&incomplete); err != nil {
		t.Fatal(err)
	}
	if !incomplete {
		t.Error("released without being marked short; an officer would have " +
			"to count the photographs to find out")
	}

	drain(t, s, &fakeAssessor{ended: true}, 6)
	if state, _ := stateOf(t, pool, id); state != "assigned" {
		t.Errorf("state = %s; a released claim carries on to the officers", state)
	}
}

func TestAFailingAssessmentDoesNotHoldTheClaim(t *testing.T) {
	// Earth Engine has a bad week. The two officers are the ones who decide
	// the claim; holding it until a satellite service recovers is this
	// system's problem being charged to a farmer.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	id := completeClaim(t, s, pool, "pipe-0000000004")

	// Ended, however it ended: assessjob.Ended answers true for done and for
	// failed alike.
	drain(t, s, &fakeAssessor{ended: true}, 6)

	if state, _ := stateOf(t, pool, id); state != "assigned" {
		t.Errorf("state = %s; a claim whose assessment failed still goes to "+
			"the officers", state)
	}
}

func TestAClaimWithNoAssessmentQueueStillReachesTheOfficers(t *testing.T) {
	// A deployment with no assessment pipeline at all. The verification
	// workflow predates the machine assessment and stands without it.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	id := completeClaim(t, s, pool, "pipe-0000000005")

	// The pipeline records the refusal and retries rather than losing it.
	for i := 0; i < 3; i++ {
		if _, _, err := s.AdvanceOne(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(),
			`UPDATE crop_loss_reports SET pipeline_next_at = now() WHERE id = $1`,
			id); err != nil {
			t.Fatal(err)
		}
	}

	var msg *string
	if err := pool.QueryRow(context.Background(),
		`SELECT pipeline_error FROM crop_loss_reports WHERE id = $1`, id).
		Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if state, _ := stateOf(t, pool, id); state == "submitted" {
		t.Error("the claim was not even acknowledged without an assessor; " +
			"the receipt does not depend on one")
	}
	if msg == nil {
		t.Error("nothing was recorded about why the claim stopped; a stuck " +
			"claim with no reason is one nobody can unstick")
	}
}

func TestAVillageWithNobodyPostedSaysSoAndKeepsTrying(t *testing.T) {
	// The staffing gap. Allocation cannot invent a Talathi, and a claim that
	// silently stops here is the failure this pipeline exists to prevent --
	// so the reason is recorded on the claim where the District can read it,
	// and staffing the village is enough to unstick it.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	clearAuthorities(t, pool)

	id := completeClaim(t, s, pool, "pipe-0000000006")
	drain(t, s, &fakeAssessor{ended: true}, 8)

	state, _ := stateOf(t, pool, id)
	if state != "acknowledged" {
		t.Errorf("state = %s; it should have got as far as acknowledged and "+
			"stopped at allocation", state)
	}

	var msg *string
	var attempts int
	if err := pool.QueryRow(context.Background(),
		`SELECT pipeline_error, pipeline_attempts FROM crop_loss_reports WHERE id = $1`,
		id).Scan(&msg, &attempts); err != nil {
		t.Fatal(err)
	}
	if msg == nil || attempts == 0 {
		t.Fatal("a claim stuck with nobody to verify it recorded nothing")
	}
	if !strings.Contains(*msg, "RV-V1") {
		t.Errorf("the recorded reason is %q; it has to name the village that "+
			"needs staffing, or nobody knows where to post anybody", *msg)
	}

	// Staff the village, and the same claim goes through.
	postAuthorities(t, pool)
	if _, err := pool.Exec(context.Background(),
		`UPDATE crop_loss_reports SET pipeline_next_at = now() WHERE id = $1`,
		id); err != nil {
		t.Fatal(err)
	}
	drain(t, s, &fakeAssessor{ended: true}, 4)
	if state, _ := stateOf(t, pool, id); state != "assigned" {
		t.Errorf("state = %s after the village was staffed; posting an "+
			"officer has to be enough to unstick it", state)
	}
}

func TestTheAssessmentIsQueuedOnceHoweverOftenThePipelineRuns(t *testing.T) {
	// The pipeline will be interrupted between queueing an assessment and
	// recording that it did. Asking twice must not produce two.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	completeClaim(t, s, pool, "pipe-0000000007")
	assessor := &fakeAssessor{ended: false}

	drain(t, s, assessor, 10)
	if assessor.queued != 1 {
		t.Errorf("queued %d assessments for one claim", assessor.queued)
	}
}

func TestTheQueuedAssessmentSaysWhereTheParcelIs(t *testing.T) {
	// The village is what decides who may read the job. Queued without it,
	// every pipeline job was unplaceable -- and the read check skipped the
	// scope test entirely for a job it could not place, so anybody holding
	// assessment:read could read it. Farmers hold assessment:read.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	completeClaim(t, s, pool, "pipe-0000000009")
	assessor := &fakeAssessor{ended: true}
	drain(t, s, assessor, 6)

	if assessor.village == "" {
		t.Error("the assessment was queued with nowhere to place it, so " +
			"nothing downstream can decide who may read it")
	}
}

func TestAClaimNamingNoParcelIsNotHeldForAnAssessment(t *testing.T) {
	// An assessment is of a piece of land. A claim whose line names no
	// registry parcel has nothing to assess, and waiting for one would hold it
	// for ever -- so it goes straight to the officers, who would have decided
	// it on the ground either way.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	id := completeClaimWithoutAParcel(t, s, pool, "pipe-0000000008")
	assessor := &fakeAssessor{ended: true}
	drain(t, s, assessor, 6)

	state, assessment := stateOf(t, pool, id)
	if assessment != "skipped" {
		t.Errorf("assessment_state = %s, want skipped", assessment)
	}
	if assessor.queued != 0 {
		t.Errorf("queued %d assessments for a claim with no parcel", assessor.queued)
	}
	if state != "assigned" {
		t.Errorf("state = %s; it still reaches the officers", state)
	}
}

func TestBackoffGrowsAndIsNotAllTheSameInstant(t *testing.T) {
	// Ten thousand claims from one district event retry together unless the
	// wait is jittered, and a queue that spikes in lockstep is one that keeps
	// failing for the same reason.
	first := PipelineBackoff(0)
	later := PipelineBackoff(5)
	if later <= first {
		t.Errorf("backoff did not grow: %v then %v", first, later)
	}

	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		seen[PipelineBackoff(3)] = true
	}
	if len(seen) < 2 {
		t.Error("every wait was identical; a district's claims would return " +
			"in lockstep")
	}

	if PipelineBackoff(30) > time.Hour {
		t.Error("the wait grew without bound; a claim would stop being retried")
	}
}

func TestStuckClaimsAreGroupedByTheVillageThatNeedsStaffing(t *testing.T) {
	// The answer to every row here is the same single action -- post an
	// officer to that village -- so a District reading four hundred
	// individually identical rows will not take it.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	clearAuthorities(t, pool)

	for _, ref := range []string{"stuck-000000001", "stuck-000000002"} {
		completeClaim(t, s, pool, ref)
	}
	// A claim whose allocation fails is picked again on the next pass, so
	// two of them need more turns than four steps each.
	drain(t, s, &fakeAssessor{ended: true}, 40)

	rows, err := s.StuckClaims(context.Background(), scopeOf(t, pool, "RV-D1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows; two claims in one village is one village to staff", len(rows))
	}
	if rows[0].Claims != 2 {
		t.Errorf("claims = %d, want both", rows[0].Claims)
	}
	if rows[0].VillageCode != "RV-V1" {
		t.Errorf("village = %s, want the one with nobody posted", rows[0].VillageCode)
	}
	if !strings.Contains(rows[0].Reason, "Talathi") {
		t.Errorf("reason = %q; it has to say what is missing", rows[0].Reason)
	}
}

func TestStuckClaimsStopAtTheEdgeOfAnOfficersOwnArea(t *testing.T) {
	// The same rule as every other listing. A screen that shows a District
	// which villages need staffing must not show them another District's.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	clearAuthorities(t, pool)

	completeClaim(t, s, pool, "stuck-000000003")
	drain(t, s, &fakeAssessor{ended: true}, 8)

	// Another district entirely, which this officer is not posted to.
	// Another district entirely, under the same state: a district with no
	// parent is refused, and rightly.
	var elsewhere string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO jurisdictions (level, code, name, parent_id)
		SELECT 'district', 'ZZ-D9-' || substr(md5(random()::text), 1, 6),
		       'Elsewhere', j.id
		  FROM jurisdictions j WHERE j.level = 'state' LIMIT 1
		RETURNING id`).Scan(&elsewhere); err != nil {
		t.Fatal(err)
	}
	// Removed before the fixture's own cleanup runs (cleanups run last-first).
	// Left behind, it holds a reference to the fixture's state, the fixture
	// cannot delete that state, and every later test that seeds it fails on a
	// duplicate key.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM jurisdictions WHERE id = $1`, elsewhere)
	})

	rows, err := s.StuckClaims(context.Background(),
		rbac.Scope{JurisdictionIDs: []string{elsewhere}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("%d villages from another district's claims", len(rows))
	}
}

func TestAnEmptyScopeSeesNoStuckClaims(t *testing.T) {
	// Empty means "posted nowhere". Answering with everything would be the
	// worst possible direction for this particular failure to go.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	clearAuthorities(t, pool)
	completeClaim(t, s, pool, "stuck-000000004")
	drain(t, s, &fakeAssessor{ended: true}, 8)

	rows, err := s.StuckClaims(context.Background(), rbac.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("an empty scope was shown %d villages", len(rows))
	}
}

func TestReleasingAVillageClearsTheBackoffAndNothingElse(t *testing.T) {
	// A District who has just posted the officer those claims were waiting for
	// should not then watch nothing happen for half an hour.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	clearAuthorities(t, pool)

	id := completeClaim(t, s, pool, "retry-000000001")
	drain(t, s, &fakeAssessor{ended: true}, 8)

	var before time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT pipeline_next_at FROM crop_loss_reports WHERE id = $1`, id).
		Scan(&before); err != nil {
		t.Fatal(err)
	}
	if !before.After(time.Now()) {
		t.Fatal("the stuck claim was not in backoff to begin with")
	}

	postAuthorities(t, pool)
	n, err := s.RetryVillage(context.Background(), "RV-V1", scopeOf(t, pool, "RV-D1"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("released %d claims, want the one that had stopped", n)
	}

	var after time.Time
	var attempts int
	if err := pool.QueryRow(context.Background(),
		`SELECT pipeline_next_at, pipeline_attempts FROM crop_loss_reports WHERE id = $1`,
		id).Scan(&after, &attempts); err != nil {
		t.Fatal(err)
	}
	if after.After(time.Now().Add(time.Second)) {
		t.Errorf("still waiting until %v", after)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d; the count should start again", attempts)
	}

	// And it goes through.
	drain(t, s, &fakeAssessor{ended: true}, 4)
	if state, _ := stateOf(t, pool, id); state != "assigned" {
		t.Errorf("state = %s after releasing a staffed village", state)
	}
}

func TestReleasingStopsAtTheEdgeOfAnOfficersOwnArea(t *testing.T) {
	// Same rule as every write in this system. An officer must not be able to
	// reach into another district's queue, even to help.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	clearAuthorities(t, pool)
	completeClaim(t, s, pool, "retry-000000002")
	drain(t, s, &fakeAssessor{ended: true}, 8)

	n, err := s.RetryVillage(context.Background(), "RV-V1", rbac.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("an empty scope released %d claims", n)
	}
}

func TestAutomaticAcknowledgementStillSendsTheReceipt(t *testing.T) {
	// The पोहोच पावती: the one message a farmer is owed by the act of filing,
	// and the only proof they have that the report reached anyone. The manual
	// acknowledgement always sent it. Automating the acknowledgement without
	// it would have taken that message away in the very change meant to make
	// the receipt arrive sooner -- and taken it away silently.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	// A store with somewhere to put the message. The queue only has to record
	// it here: sending is the outbox worker's job and needs no sender for the
	// message to be owed and written down.
	s = s.WithNotifier(notify.NewQueue(pool, nil, slog.New(slog.DiscardHandler)))

	id := completeClaim(t, s, pool, "ack-00000000001")
	// A farmer with a number on record. Without one the message is correctly
	// not queued -- plenty of farmers have none -- and this test would pass
	// for the wrong reason whatever the pipeline did.
	if _, err := pool.Exec(context.Background(),
		`UPDATE crop_loss_reports SET farmer_mobile = '9527464552' WHERE id = $1`,
		id); err != nil {
		t.Fatal(err)
	}
	drain(t, s, &fakeAssessor{ended: true}, 6)

	var queued int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM notifications
		 WHERE resource_id = $1 AND event_code = 'report.acknowledged'`, id).
		Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued == 0 {
		t.Error("the claim was acknowledged and the farmer was told nothing")
	}
}

func TestThePipelineQueuesARealAssessmentWithoutWaitingOnItself(t *testing.T) {
	// The failure the fake assessor could not show. Queueing an assessment
	// inserts into assessment_jobs on another connection, and that row's
	// foreign key to the claim needs a lock the pipeline's own FOR UPDATE
	// blocked. On the first real run every acknowledged claim stopped there
	// with "canceling statement due to statement timeout" -- while every test
	// passed, because the fake never touched the database.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	id := completeClaim(t, s, pool, "real-0000000001")
	queue := assessjob.New(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		if _, _, err := s.AdvanceOne(ctx, queue); err != nil {
			t.Fatalf("advancing with the real queue: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE crop_loss_reports SET pipeline_next_at = now() WHERE id = $1`,
			id); err != nil {
			t.Fatal(err)
		}
	}

	var state, failure string
	if err := pool.QueryRow(ctx, `
		SELECT assessment_state, COALESCE(pipeline_error, '')
		  FROM crop_loss_reports WHERE id = $1`, id).
		Scan(&state, &failure); err != nil {
		t.Fatal(err)
	}
	if failure != "" {
		t.Fatalf("the pipeline stopped: %s", failure)
	}
	if state != "queued" {
		t.Errorf("assessment_state = %s, want queued", state)
	}

	var jobs int
	var village string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(village_lgd_code) FROM assessment_jobs
		 WHERE crop_loss_report_id = $1`, id).Scan(&jobs, &village); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Errorf("%d jobs queued for one claim", jobs)
	}
	if village == "" {
		t.Error("the job was queued with no village, so nobody's scope can place it")
	}
}

func TestTheAutomaticAssessmentsScopeReachesItsOwnClaimAndNoOther(t *testing.T) {
	// The principal an automatic assessment runs under: no user, no role, and
	// exactly one village. It has to be enough to read the farmer's own
	// claim -- that is the whole job -- and it must reach nothing beyond it.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	id := completeClaim(t, s, pool, "scope-000000001")
	ctx := context.Background()

	var village string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM jurisdictions WHERE level = 'village' AND code = 'RV-V1'`).
		Scan(&village); err != nil {
		t.Fatal(err)
	}
	automatic := &rbac.Principal{
		Kind: rbac.KindOfficer, Name: "eSurvey (automatic)",
		Grants: []rbac.Grant{{JurisdictionID: village, JurisdictionLevel: rbac.LevelVillage}},
	}

	if _, err := s.Get(ctx, id, automatic.Scope()); err != nil {
		t.Errorf("the automatic assessment could not read its own claim: %v", err)
	}
	for _, perm := range []rbac.Permission{
		rbac.PermReportAdjudicate, rbac.PermUserCreate, rbac.PermRoleGrant,
		rbac.PermAssessmentApprove,
	} {
		if automatic.Can(perm) {
			t.Errorf("the automatic principal carries %s; it should carry nothing", perm)
		}
	}

	var elsewhere string
	if err := pool.QueryRow(ctx, `
		INSERT INTO jurisdictions (level, code, name, parent_id)
		SELECT 'village', 'ZZ-V9-' || substr(md5(random()::text), 1, 6), 'Elsewhere', j.id
		  FROM jurisdictions j WHERE j.code = 'RV-T1'
		RETURNING id`).Scan(&elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM jurisdictions WHERE id = $1`, elsewhere)
	})
	stranger := &rbac.Principal{
		Kind:   rbac.KindOfficer,
		Grants: []rbac.Grant{{JurisdictionID: elsewhere, JurisdictionLevel: rbac.LevelVillage}},
	}
	if _, err := s.Get(ctx, id, stranger.Scope()); err == nil {
		t.Error("an automatic assessment scoped to another village read this claim")
	}
}
