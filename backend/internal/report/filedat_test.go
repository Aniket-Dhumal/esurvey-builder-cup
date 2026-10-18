package report

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/event"
)

// When a claim counts as filed.
//
// The 48-hour intimation window is about when the farmer acted, and the farmer
// acts in a field with no signal. Measuring it on arrival recorded a farmer who
// filed on the first morning and found a network on the third day as having
// filed late -- and filed_late is the one flag an officer reads when weighing
// whether to believe a claim.
//
// So the handset says when the form was completed, the server keeps both
// instants, and the window is measured on the farmer's. The handset's clock is
// recorded rather than trusted: see the impossibility tests below.

func TestTheWindowIsMeasuredOnWhenTheFarmerFiled(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000001")
	// Hail on the ninth day before now; the form filled the same afternoon.
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	filed := in.OccurredAt.Add(4 * time.Hour)
	in.FiledAt = &filed

	// It reaches the office a week later, because that is when the village
	// had a signal.
	r, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("filing: %v", err)
	}

	if r.FiledLate {
		t.Error("a claim filed four hours after the hail is recorded as late")
	}
	if r.FiledAt == nil {
		t.Fatal("the filing instant was not kept")
	}
	if !r.FiledAt.Equal(filed.UTC().Truncate(time.Microsecond)) &&
		r.FiledAt.Sub(filed).Abs() > time.Second {
		t.Errorf("filed_at = %v, want the handset's %v", r.FiledAt, filed)
	}
	// Both instants, so the delay the queue introduced is visible rather
	// than inferred.
	if !r.ReportedAt.After(*r.FiledAt) {
		t.Errorf("reported_at %v is not after filed_at %v -- the gap is the "+
			"queue doing its job and should be on the record",
			r.ReportedAt, *r.FiledAt)
	}
}

func TestAClaimActuallyFiledLateIsStillLate(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000002")
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	// The farmer got to the form five days after the loss. The window is the
	// farmer's to meet, and this one did not.
	filed := in.OccurredAt.Add(5 * 24 * time.Hour)
	in.FiledAt = &filed

	r, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("filing: %v", err)
	}
	if !r.FiledLate {
		t.Error("a form filled five days after the loss is not flagged late")
	}
}

func TestAHandsetClockAheadOfTheServerIsNotBelieved(t *testing.T) {
	// A clock is something the holder of the phone can set. Believing it
	// outright would let a claim filed a month late present itself as timely.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000003")
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	ahead := time.Now().Add(72 * time.Hour)
	in.FiledAt = &ahead

	r, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("filing: %v", err)
	}
	// The claim is still filed -- a wrong clock is not a verdict on a farmer
	// -- but on the arrival instant, and nothing is written to filed_at.
	if r.FiledAt != nil {
		t.Errorf("filed_at = %v; an instant this server has not reached is "+
			"not a record of anything", r.FiledAt)
	}
	if !r.FiledLate {
		t.Error("with the handset's answer discarded, nine days is late")
	}
}

func TestAFilingInstantBeforeTheLossIsNotBelieved(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000004")
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	before := in.OccurredAt.Add(-2 * time.Hour)
	in.FiledAt = &before

	r, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("filing: %v", err)
	}
	if r.FiledAt != nil {
		t.Errorf("filed_at = %v; a claim cannot be filed before the loss it "+
			"reports", r.FiledAt)
	}
}

func TestACounterFilingHasNoFilingInstantOfItsOwn(t *testing.T) {
	// Nothing queues behind a counter, so arrival is the filing and there is
	// no second instant to record. The behaviour before this change, kept.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000005")
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	in.FiledAt = nil

	r, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("filing: %v", err)
	}
	if r.FiledAt != nil {
		t.Errorf("filed_at = %v for a filing that reported none", r.FiledAt)
	}
	// And the window falls back to arrival, exactly as before this change:
	// nine days after the loss, at a counter, is late.
	if !r.FiledLate {
		t.Error("with no filing instant reported, the window is measured on " +
			"arrival and nine days is late")
	}
}

func TestAClaimCompletedBeforeTheCloseIsTakenAfterIt(t *testing.T) {
	// The deadline is the farmer's to meet, and they meet it by filling in
	// the form -- not by finding a network. Refusing on arrival meant this
	// system's own delay destroyed a claim made in time, after the app had
	// told the farmer it was safe and would send itself. That delay is longer
	// by design now that a claim waits for its evidence.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000006")
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)

	// The event stopped taking claims yesterday.
	closed := time.Now().Add(-24 * time.Hour)
	if _, err := pool.Exec(ctx,
		`UPDATE survey_events SET survey_closes_at = $1 WHERE id = $2`,
		closed, in.SurveyEventID); err != nil {
		t.Fatalf("closing the fixture event: %v", err)
	}

	// The form was filled two days before that, and spent the time since in
	// a phone's queue.
	filed := closed.Add(-48 * time.Hour)
	in.FiledAt = &filed

	r, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("a claim completed before the close was refused: %v", err)
	}
	if !r.ArrivedAfterClose {
		t.Error("the claim was taken but not recorded as arriving after the " +
			"close -- the district signed off a count that did not include it")
	}
}

func TestAClaimCompletedAfterTheCloseIsStillRefused(t *testing.T) {
	// The close still means something. What moved is which instant it is
	// measured against, not whether it applies.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000007")
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)

	closed := time.Now().Add(-48 * time.Hour)
	if _, err := pool.Exec(ctx,
		`UPDATE survey_events SET survey_closes_at = $1 WHERE id = $2`,
		closed, in.SurveyEventID); err != nil {
		t.Fatalf("closing the fixture event: %v", err)
	}

	// Filled in a day after the close.
	filed := closed.Add(24 * time.Hour)
	in.FiledAt = &filed

	if _, err := s.Create(ctx, in); err == nil {
		t.Fatal("a form filled after the close was accepted")
	} else if !strings.Contains(err.Error(), "closed on") {
		t.Errorf("refusal = %q, want the closing date", err)
	}
}

func TestAClaimWithNoFilingInstantIsStillRefusedAfterTheClose(t *testing.T) {
	// Nothing to go on but arrival, which is after the close. A claim filed
	// at a counter today against an event that closed last week is exactly
	// what the deadline is for.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000008")
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	in.FiledAt = nil

	if _, err := pool.Exec(ctx,
		`UPDATE survey_events SET survey_closes_at = $1 WHERE id = $2`,
		time.Now().Add(-24*time.Hour), in.SurveyEventID); err != nil {
		t.Fatalf("closing the fixture event: %v", err)
	}

	if _, err := s.Create(ctx, in); err == nil {
		t.Fatal("a claim with no filing instant was taken after the close")
	}
}

func TestAnEventClosedEarlyIsMeasuredOnWhenItActuallyStopped(t *testing.T) {
	// Two deadlines: the one the farmer was shown, and the moment the
	// district actually stopped. A district that closes early has stopped
	// early, and a form filled after that was not filled in time -- even
	// though the date printed on the farmer's copy had not yet passed.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	// Both filings are built before the event is closed: a closed event
	// refuses changes to its own area coverage, which the fixture sets up.
	in := newFiling(t, pool, "filed-0000000009")
	in.OccurredAt = time.Now().Add(-9 * 24 * time.Hour)
	in2 := newFiling(t, pool, "filed-0000000010")
	in2.OccurredAt = in.OccurredAt

	// Advertised as closing tomorrow, actually stopped three days ago.
	stoppedAt := time.Now().Add(-72 * time.Hour)
	if _, err := pool.Exec(ctx, `
		UPDATE survey_events
		SET survey_closes_at = $1, closed_at = $2, state = 'closed'
		WHERE id = $3`,
		time.Now().Add(24*time.Hour), stoppedAt, in.SurveyEventID); err != nil {
		t.Fatalf("closing the fixture event early: %v", err)
	}

	// Filled in yesterday: inside the advertised window, after the district
	// stopped.
	filed := time.Now().Add(-24 * time.Hour)
	in.FiledAt = &filed

	if _, err := s.Create(ctx, in); err == nil {
		t.Fatal("a form filled after the district stopped was accepted")
	}

	// And one filled before it stopped is taken.
	earlier := stoppedAt.Add(-2 * time.Hour)
	in2.FiledAt = &earlier

	r, err := s.Create(ctx, in2)
	if err != nil {
		t.Fatalf("a claim completed before the district stopped was refused: %v", err)
	}
	if !r.ArrivedAfterClose {
		t.Error("taken after the close but not recorded as such")
	}
}

func TestAPausedEventAsksTheHandsetToComeBack(t *testing.T) {
	// A pause is not a verdict. The handset reads a refusal as final: it
	// marks the claim failed, stops sending it and tells the farmer the
	// office said no -- permanently, over a state the district means to undo
	// that afternoon.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "filed-0000000011")
	if _, err := pool.Exec(ctx,
		`UPDATE survey_events SET state = 'paused' WHERE id = $1`,
		in.SurveyEventID); err != nil {
		t.Fatalf("pausing the fixture event: %v", err)
	}

	_, err := s.Create(ctx, in)
	if err == nil {
		t.Fatal("a paused event took a claim")
	}
	if !errors.Is(err, ErrNotYet) {
		t.Errorf("error = %v; a pause must not reach the handset as a "+
			"refusal, or the claim is discarded over a temporary state", err)
	}
	if errors.Is(err, ErrBadState) {
		t.Error("a pause is still classified as a bad state")
	}
}

func TestAClaimThatPromisesNoDocumentsIsFiled(t *testing.T) {
	// Filed exactly as the farmer's app files: with no documents_promised at
	// all. The column is NOT NULL DEFAULT '{}', but a default only applies to
	// a column left out of the insert, and a nil slice is sent as NULL -- so
	// every claim from a phone was refused "documents_promised is not
	// satisfied", after the farmer had walked and filmed all four points.
	// Every other test in this package sets []string{}, which is why none of
	// them could see it.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)

	in := newFiling(t, pool, "docs-0000000001")
	in.DocumentsPromised = nil

	r, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("a claim promising no documents was refused: %v", err)
	}
	if r.DocumentsPromised == nil || len(r.DocumentsPromised) != 0 {
		t.Errorf("documents_promised = %v, want an empty list", r.DocumentsPromised)
	}
}

func TestTheClaimsSeasonAndCropYearAreTheEvents(t *testing.T) {
	// A draft saved from an event the handset cached before events carried a
	// crop year was sent with none, and refused "crop_year is required" --
	// for a value the event it names already decides.
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := newFiling(t, pool, "year-0000000001")
	var start time.Time
	if err := pool.QueryRow(ctx,
		`SELECT incident_start FROM survey_events WHERE id = $1`, in.SurveyEventID).
		Scan(&start); err != nil {
		t.Fatal(err)
	}
	season, year := event.SeasonAndCropYear(start)

	for name, handset := range map[string]struct {
		season Season
		year   string
	}{
		"none":     {"", ""},
		"disagree": {Summer, "1999-00"},
	} {
		in := newFiling(t, pool, "year-"+name+"-000001")
		in.Season, in.CropYear = handset.season, handset.year
		// A parcel of its own: two claims from one farmer against one event
		// are two claims only if they are about different land.
		in.Items[0].SurveyNumber = "year-" + name

		r, err := s.Create(ctx, in)
		if err != nil {
			t.Fatalf("%s: refused: %v", name, err)
		}
		if string(r.Season) != season || r.CropYear != year {
			t.Errorf("%s: filed as %s %s, want the event's %s %s",
				name, r.Season, r.CropYear, season, year)
		}
	}
}
