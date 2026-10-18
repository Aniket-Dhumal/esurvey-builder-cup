package survey

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/blob"
)

// The two refusals.
//
// Every other gate records what it found and leaves the judgement to an
// officer. These two stop the capture, so they are the ones worth proving
// actually fire -- a control that is only believed to be there is not one.

// aCapture is a submission that would otherwise be accepted, so that each test
// below changes exactly one thing and the refusal cannot be coming from
// somewhere else.
func aCapture(reportID, farmID string) New {
	return New{
		ReportID:    reportID,
		FarmID:      farmID,
		Kind:        "damage_closeup",
		ContentType: "image/jpeg",
		Media:       strings.NewReader("not really a jpeg, but bytes"),
		CapturedAt:  time.Now().Add(-1 * time.Hour),
		// Inside the reference parcel.
		Lat: 18.1700, Lon: 75.8131,
	}
}

func TestACaptureFromOutsideTheParcelIsRefused(t *testing.T) {
	// The farmer photographs the right kind of damage in the wrong field.
	// Blocking rather than flagging is a decision taken deliberately: a loss
	// may only be evidenced from the land being claimed for.
	store, pool := testStations(t)
	placeBoundary(t, pool, "ST-9")
	reportID := makeReport(t, pool)

	in := aCapture(reportID, "ST-9")
	// Roughly a kilometre north: comfortably past the 25 m tolerance, and
	// unambiguously somebody else's survey number.
	in.Lat = 18.1800

	_, err := store.Submit(context.Background(), in)
	if !errors.Is(err, ErrOutsideParcel) {
		t.Fatalf("got %v, want ErrOutsideParcel", err)
	}
	// The distance belongs in the message: "your photograph was taken 900 m
	// outside the plot" is something a farmer can act on, and "refused" is
	// not.
	if !strings.Contains(err.Error(), "boundary") {
		t.Errorf("the refusal does not say why: %q", err)
	}
}

func TestARefusedCaptureNeverReachesTheEvidenceStore(t *testing.T) {
	// Proven by construction rather than by inspection: this store has no
	// blob backend at all, so anything that tried to write media would panic
	// rather than return. Reaching a clean refusal is the assertion.
	//
	// It matters because the alternative leaves bytes in the evidence store
	// with no row pointing at them -- unreachable, undeletable, and growing
	// with every refusal.
	store, pool := testStations(t)
	placeBoundary(t, pool, "ST-10")
	reportID := makeReport(t, pool)

	if store.Evidence() != nil {
		t.Fatal("this test is only meaningful with no blob store attached")
	}

	in := aCapture(reportID, "ST-10")
	in.Lat = 18.1800

	if _, err := store.Submit(context.Background(), in); !errors.Is(err, ErrOutsideParcel) {
		t.Fatalf("got %v, want ErrOutsideParcel", err)
	}
}

func TestAMockedPositionIsRefused(t *testing.T) {
	// A geofence that stopped honest farmers and waved through anyone who
	// installed a location-spoofing app would be worse than no geofence,
	// because it would look like a control while being none. The coordinates
	// here are perfectly inside the parcel; only the provider is a lie.
	store, pool := testStations(t)
	placeBoundary(t, pool, "ST-11")
	reportID := makeReport(t, pool)

	mocked := true
	in := aCapture(reportID, "ST-11")
	in.IsMocked = &mocked

	if _, err := store.Submit(context.Background(), in); !errors.Is(err, ErrMockedPosition) {
		t.Fatalf("got %v, want ErrMockedPosition", err)
	}
}

func TestAHandsetThatSaysTheFixIsGenuineIsNotRefused(t *testing.T) {
	// The other side of the same check, so the tests above are not passing
	// merely because everything is refused. A genuine fix inside the parcel
	// must go all the way through to a stored capture.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-12")
	reportID := makeReport(t, pool)

	dir, err := blob.NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("evidence store: %v", err)
	}
	store := NewStore(pool, dir)

	genuine := false
	in := aCapture(reportID, "ST-12")
	in.IsMocked = &genuine

	got, err := store.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("a genuine fix inside the parcel was refused: %v", err)
	}
	if got.ID == "" {
		t.Fatal("the capture was not recorded")
	}

	// And the gate is recorded as passed, not merely absent -- an officer
	// reading the capture should see that the provider was checked.
	var found bool
	for _, g := range got.GatesPassed {
		if g == GateGenuineFix {
			found = true
		}
	}
	if !found {
		t.Errorf("the genuine-position gate is not recorded as passed: %v",
			got.GatesPassed)
	}
}

func TestAParcelWithNoBoundaryIsNotFenced(t *testing.T) {
	// AgriStack returns no geometry for any farmer we can reach, so an absent
	// boundary is the ordinary case and not the farmer's fault. Refusing here
	// would mean nobody could file until somebody else's data improved.
	_, pool := testStations(t)
	reportID := makeReport(t, pool)

	dir, err := blob.NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("evidence store: %v", err)
	}
	store := NewStore(pool, dir)

	got, err := store.Submit(context.Background(), aCapture(reportID, "ST-no-such-parcel"))
	if err != nil {
		t.Fatalf("a parcel with no boundary was fenced against; the farmer "+
			"cannot file and has no way to fix it: %v", err)
	}

	// The gate is skipped, not passed. "There was nothing to check against"
	// and "it was checked and was fine" are different things to put in front
	// of an officer, and only one of them is true here.
	for _, g := range append(got.GatesPassed, got.GatesFailed...) {
		if g == GateInsideParcel {
			t.Errorf("the geofence claims a verdict with no boundary to "+
				"check against: passed=%v failed=%v",
				got.GatesPassed, got.GatesFailed)
		}
	}
}
