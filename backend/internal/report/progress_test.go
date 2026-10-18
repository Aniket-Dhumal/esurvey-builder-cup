package report

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// What a claim's progress says, and what it withholds.
//
// Two things are being guarded here. A farmer looking at this wants to know
// whether anybody is doing anything, so a claim that has stopped must not read
// as one in progress. And the two village officers record blind to each other
// -- if the first officer's remark reached the farmer before the second had
// filed, the farmer would be the channel between them and the blind would be
// gone without anybody deciding to remove it.

func farmerScope(farmerID string) rbac.Scope { return rbac.Scope{FarmerID: farmerID} }

// stage pulls one step out of the answer.
func stage(t *testing.T, p *Progress, key string) Stage {
	t.Helper()
	for _, s := range p.Stages {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no %s stage in %v", key, p.Stages)
	return Stage{}
}

func TestAFreshClaimReadsAsReceivedAndNothingElse(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	postAuthorities(t, pool)

	id := completeClaim(t, s, pool, "prog-0000000001")
	p, err := s.ProgressFor(context.Background(), id, farmerScope("F-RV-1"), true)
	if err != nil {
		t.Fatal(err)
	}

	if p.Reference == "" {
		t.Error("no reference; it is what the farmer tracks the claim by")
	}
	if got := stage(t, p, StageReceived); got.State != StageDone {
		t.Errorf("received = %s, want done", got.State)
	}
	if got := stage(t, p, StageField); got.State != StagePending {
		t.Errorf("field = %s before anything happened", got.State)
	}
	if p.AssessedPercent != nil {
		t.Errorf("a figure of %v was released before either officer recorded",
			*p.AssessedPercent)
	}
	if len(p.Remarks) != 0 {
		t.Error("remarks were released before either officer recorded")
	}
}

func TestEvidenceStillArrivingReadsAsNotYetReceived(t *testing.T) {
	// "Received" means the office has all of it. A claim whose photographs are
	// still uploading has been taken and not yet received, and saying
	// otherwise would have the farmer waiting on a stage that had not started.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)

	id := completeClaim(t, s, pool, "prog-0000000002")
	if _, err := pool.Exec(context.Background(), `
		UPDATE crop_loss_reports SET expected_captures = 8,
		       evidence_complete_at = NULL WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	p, err := s.ProgressFor(context.Background(), id, farmerScope("F-RV-1"), true)
	if err != nil {
		t.Fatal(err)
	}
	got := stage(t, p, StageReceived)
	if got.State != StageCurrent || got.Why != WhyEvidenceArriving {
		t.Errorf("received = %s/%s, want current/%s",
			got.State, got.Why, WhyEvidenceArriving)
	}
}

// aClaimWithTheOfficers files a claim and runs the pipeline until it is
// sitting with the village pair, which is where findings can be recorded.
//
// Drains until *this* claim has arrived rather than for a fixed number of
// steps. AdvanceOne takes whichever claim is due across the whole table, so on
// a database shared with anything else -- another test package, or a live stack
// on a developer's machine -- a fixed budget of six steps can be spent entirely
// on other people's claims. The test then failed on an assertion about its own
// claim having reached the officers, which was true of the code and false of
// the run.
func aClaimWithTheOfficers(t *testing.T, s *Store, pool *pgxpool.Pool, ref string) string {
	t.Helper()
	postAuthorities(t, pool)
	id := completeClaim(t, s, pool, ref)
	// Either state counts as arrived: the claim is with the pair as soon as it
	// is allocated, and one of them opening it does not un-arrive it.
	drainUntilWith(t, s, &fakeAssessor{ended: true}, pool, id,
		string(StateAssigned), string(StateUnderVerification))
	return id
}

// drainUntilWith advances the pipeline until one claim reaches a state.
//
// Bounded by a step count large enough to absorb other claims being worked in
// the same passes, and by AdvanceOne running out of work. Fails with the state
// the claim actually reached, because "want village_pair, got submitted" says
// what went wrong and a bare timeout does not.
func drainUntilWith(
	t *testing.T, s *Store, a Assessor, pool *pgxpool.Pool,
	id string, want ...string,
) {
	t.Helper()
	arrived := func() (string, bool) {
		state, _ := stateOf(t, pool, id)
		for _, w := range want {
			if state == w {
				return state, true
			}
		}
		return state, false
	}

	const maxSteps = 200
	for i := 0; i < maxSteps; i++ {
		if _, ok := arrived(); ok {
			return
		}
		_, worked, err := s.AdvanceOne(context.Background(), a)
		if err != nil {
			t.Fatalf("advancing: %v", err)
		}
		if _, err := pool.Exec(context.Background(),
			`UPDATE crop_loss_reports SET pipeline_next_at = now()
			  WHERE id = $1 AND pipeline_error IS NULL`, id); err != nil {
			t.Fatal(err)
		}
		if !worked {
			// Nothing anywhere is due. One more read, so a claim finished by
			// the final step is not reported as stalled.
			state, ok := arrived()
			if ok {
				return
			}
			t.Fatalf("the pipeline ran out of work with the claim at %q, "+
				"want one of %v", state, want)
		}
	}
	state, _ := arrived()
	t.Fatalf("the claim reached %q in %d steps, want one of %v",
		state, maxSteps, want)
}

func TestOneOfficerOfTwoIsNotEnoughToReleaseAnything(t *testing.T) {
	// The blind. The Talathi has recorded and the Krushi Sahayak has not; a
	// farmer told the first figure could carry it to the second, who is
	// supposed to be reaching their own.
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()
	id := aClaimWithTheOfficers(t, s, pool, "prog-0000000005")

	if _, err := s.RecordFinding(ctx, NewFinding{
		ReportID: id, OfficerID: f.talathi, Role: rbac.RoleTalathi,
		Tier: TierVillagePair, Outcome: OutcomeConfirmed,
		DamagePercent: ptr(60.0),
		Findings:      "Standing water across the eastern half.",
	}); err != nil {
		t.Fatalf("recording the first finding: %v", err)
	}

	p, err := s.ProgressFor(ctx, id, farmerScope("F-RV-1"), true)
	if err != nil {
		t.Fatal(err)
	}

	if p.AssessedPercent != nil {
		t.Errorf("released %v with one officer of two recorded",
			*p.AssessedPercent)
	}
	if len(p.Remarks) != 0 {
		t.Errorf("released %d remarks with one officer of two recorded; the "+
			"farmer would be the channel between officers who record blind",
			len(p.Remarks))
	}
	if got := stage(t, p, StageField); got.Why != WhyAwaitingOne {
		t.Errorf("field.why = %s, want %s -- a farmer can see that one of the "+
			"two has been", got.Why, WhyAwaitingOne)
	}
}

func TestBothOfficersReleaseTheFigureAndTheirRemarks(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()
	id := aClaimWithTheOfficers(t, s, pool, "prog-0000000006")

	for _, rec := range []struct {
		officer string
		role    rbac.Role
		percent float64
		text    string
	}{
		{f.talathi, rbac.RoleTalathi, 60, "Standing water across the eastern half."},
		{f.sahayak, rbac.RoleKrushiSahayak, 62, "Soybean at pod fill, largely flattened."},
	} {
		if _, err := s.RecordFinding(ctx, NewFinding{
			ReportID: id, OfficerID: rec.officer, Role: rec.role,
			Tier: TierVillagePair, Outcome: OutcomeConfirmed,
			DamagePercent: ptr(rec.percent), Findings: rec.text,
		}); err != nil {
			t.Fatalf("recording %s: %v", rec.role, err)
		}
	}

	p, err := s.ProgressFor(ctx, id, farmerScope("F-RV-1"), true)
	if err != nil {
		t.Fatal(err)
	}

	if p.AssessedPercent == nil {
		t.Fatal("no figure once both officers had recorded")
	}
	if *p.AssessedPercent != 61 {
		t.Errorf("figure = %v, want the pair's mean of 61", *p.AssessedPercent)
	}
	if len(p.Remarks) != 2 {
		t.Fatalf("%d remarks, want both officers'", len(p.Remarks))
	}
	if got := stage(t, p, StageField); got.State != StageDone {
		t.Errorf("field = %s once both had recorded", got.State)
	}
}

func TestAFarmerIsNotToldTheOfficersNamesOrWhereItIsStuck(t *testing.T) {
	// The remark is what a farmer needs to understand the decision. The name
	// mostly tells them who to go and find, and "this village has no Talathi"
	// is the District's problem in a sentence a farmer can do nothing with.
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()
	id := aClaimWithTheOfficers(t, s, pool, "prog-0000000007")

	for _, rec := range []struct {
		officer string
		role    rbac.Role
	}{{f.talathi, rbac.RoleTalathi}, {f.sahayak, rbac.RoleKrushiSahayak}} {
		if _, err := s.RecordFinding(ctx, NewFinding{
			ReportID: id, OfficerID: rec.officer, Role: rec.role,
			Tier: TierVillagePair, Outcome: OutcomeConfirmed,
			DamagePercent: ptr(60.0), Findings: "Flattened.",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx,
		`UPDATE crop_loss_reports SET pipeline_error = 'village RV-V1 has no Talathi'
		 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	forFarmer, err := s.ProgressFor(ctx, id, farmerScope("F-RV-1"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range forFarmer.Remarks {
		if r.By != "" {
			t.Errorf("a farmer was given the officer's name (%q)", r.By)
		}
		if r.Text == "" {
			t.Error("a remark reached the farmer with no text; the text is " +
				"the whole point of showing it")
		}
	}
	if forFarmer.Stuck != "" {
		t.Errorf("a farmer was told %q, which they can do nothing about",
			forFarmer.Stuck)
	}

	// The same claim, read by an officer.
	forOfficer, err := s.ProgressFor(ctx, id, scopeOf(t, pool, "RV-D1"), false)
	if err != nil {
		t.Fatal(err)
	}
	if forOfficer.Stuck == "" {
		t.Error("an officer was not told what is stopping the claim")
	}
	named := false
	for _, r := range forOfficer.Remarks {
		if r.By != "" {
			named = true
		}
	}
	if !named {
		t.Error("no officer name in the officers' own view; a finding without " +
			"a name behind it is not a record")
	}
}

func TestAWithdrawnClaimDoesNotPretendToBeInProgress(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)

	id := completeClaim(t, s, pool, "prog-0000000003")
	if _, err := pool.Exec(context.Background(),
		`UPDATE crop_loss_reports SET state = 'withdrawn' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	p, err := s.ProgressFor(context.Background(), id, farmerScope("F-RV-1"), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := stage(t, p, StageReceived); got.Why != WhyWithdrawn {
		t.Errorf("received.why = %s, want %s", got.Why, WhyWithdrawn)
	}
	for _, key := range []string{StageChecked, StageField, StageDecision} {
		if got := stage(t, p, key); got.State != StagePending {
			t.Errorf("%s = %s on a withdrawn claim", key, got.State)
		}
	}
}

func TestAClaimThatWentOnWithoutAllItsEvidenceSaysSo(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)

	id := completeClaim(t, s, pool, "prog-0000000004")
	if _, err := pool.Exec(context.Background(), `
		UPDATE crop_loss_reports SET evidence_incomplete = true WHERE id = $1`,
		id); err != nil {
		t.Fatal(err)
	}

	p, err := s.ProgressFor(context.Background(), id, farmerScope("F-RV-1"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !p.EvidenceShort {
		t.Error("the claim went on short of evidence and nothing said so")
	}
	if got := stage(t, p, StageReceived); got.Why != WhyEvidenceShort {
		t.Errorf("received.why = %s, want %s", got.Why, WhyEvidenceShort)
	}
}

func ptr[T any](v T) *T { return &v }
