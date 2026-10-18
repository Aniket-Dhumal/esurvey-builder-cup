package report

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// One loss, one claim.
//
// The office refused a duplicate only when the same handset re-sent the same
// draft, matched on its client reference. A farmer who walked the field twice
// -- or who was shown the parcel again because the app had forgotten the
// first claim -- filed a second claim for the same damage: two assessments,
// two trips for two officers, and two figures for one field.

// onParcel is newFiling with the parcel named, and no client reference, so
// each call is a genuinely separate filing rather than a retry.
func onParcel(t *testing.T, pool *pgxpool.Pool, survey, farmID string) New {
	t.Helper()
	return onCrop(t, pool, survey, farmID, "Soybean")
}

// onCrop is onParcel with the crop named too, because the crop is the other
// half of what makes a claim distinct: one farm carries several of them, each
// sown on its own part of the ground and each losing its own harvest.
func onCrop(t *testing.T, pool *pgxpool.Pool, survey, farmID, crop string) New {
	t.Helper()
	in := newFiling(t, pool, "")
	in.Items = []Item{{
		SurveyNumber: survey, FarmID: farmID, CropName: crop,
		AreaSownHa: 1.2, AreaAffectedHa: 1.0, DamagePercent: 60,
	}}
	return in
}

func TestASecondClaimForTheSameParcelIsRefused(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	first, err := s.Create(ctx, onParcel(t, pool, "453", "FARM-A"))
	if err != nil {
		t.Fatalf("the first claim: %v", err)
	}

	_, err = s.Create(ctx, onParcel(t, pool, "453", "FARM-A"))
	if !errors.Is(err, ErrAlreadyFiled) {
		t.Fatalf("a second claim for the same parcel was accepted: %v", err)
	}
	// The refusal names the claim that already exists, so the farmer is sent
	// to it rather than left thinking their walk vanished.
	if !strings.Contains(err.Error(), first.Reference) {
		t.Fatalf("the refusal does not name the earlier claim: %v", err)
	}
}

func TestAnotherParcelIsNotADuplicate(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	if _, err := s.Create(ctx, onParcel(t, pool, "453", "FARM-A")); err != nil {
		t.Fatal(err)
	}
	// One hailstorm across two of a farmer's fields is two claims.
	if _, err := s.Create(ctx, onParcel(t, pool, "454", "FARM-B")); err != nil {
		t.Fatalf("a claim for a different parcel was refused: %v", err)
	}
}

func TestWithdrawingFreesTheParcel(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	first, err := s.Create(ctx, onParcel(t, pool, "453", "FARM-A"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE crop_loss_reports SET state = 'withdrawn' WHERE id = $1`,
		first.ID); err != nil {
		t.Fatal(err)
	}

	// Withdrawing is how a farmer takes back a claim they should not have
	// made; it has to leave them able to file the right one.
	if _, err := s.Create(ctx, onParcel(t, pool, "453", "FARM-A")); err != nil {
		t.Fatalf("a parcel stayed blocked after its claim was withdrawn: %v", err)
	}
}

// A farmer holds one registered farm on a survey number and grows more than
// one thing on it. Each crop is its own claim, at its own sown extent, and the
// parcel rule used to refuse the second of them -- so a farmer with a hectare
// of soybean and a hectare of udad could file for one hectare of a two-hectare
// loss and was told the land was already claimed.
func TestSoybeanAndUdadOnOneFarmAreTwoClaims(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	soybean, err := s.Create(ctx, onCrop(t, pool, "453", "FARM-A", "Soybean"))
	if err != nil {
		t.Fatalf("the soybean claim: %v", err)
	}
	udad, err := s.Create(ctx, onCrop(t, pool, "453", "FARM-A", "Udad"))
	if err != nil {
		t.Fatalf("the udad claim on the same farm was refused: %v", err)
	}
	if soybean.ID == udad.ID {
		t.Fatal("two crops on one farm were merged into one claim")
	}
}

func TestTheSameCropTwiceOnOneFarmIsStillRefused(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	first, err := s.Create(ctx, onCrop(t, pool, "453", "FARM-A", "Udad"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(ctx, onCrop(t, pool, "453", "FARM-A", "Udad"))
	if !errors.Is(err, ErrAlreadyFiled) {
		t.Fatalf("one crop was claimed twice on one farm: %v", err)
	}
	// The farmer is sent to the claim they already made, and told which of
	// their crops it was about -- on a farm carrying two, the survey number
	// alone names the wrong thing.
	if !strings.Contains(err.Error(), first.Reference) {
		t.Fatalf("the refusal does not name the earlier claim: %v", err)
	}
	if !strings.Contains(err.Error(), "Udad") {
		t.Fatalf("the refusal does not say which crop is already claimed: %v", err)
	}
}

// Withdrawing is how a farmer takes back a claim they should not have made,
// and it has to leave them able to file the right one -- for that crop, while
// their other crops on the same farm stay claimed.
func TestAWithdrawnClaimDoesNotBlockRefilingThatCrop(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	soybean, err := s.Create(ctx, onCrop(t, pool, "453", "FARM-A", "Soybean"))
	if err != nil {
		t.Fatal(err)
	}
	udad, err := s.Create(ctx, onCrop(t, pool, "453", "FARM-A", "Udad"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE crop_loss_reports SET state = 'withdrawn' WHERE id = $1`,
		udad.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Create(ctx, onCrop(t, pool, "453", "FARM-A", "Udad")); err != nil {
		t.Fatalf("a crop stayed blocked after its claim was withdrawn: %v", err)
	}
	// The soybean claim was not withdrawn and still holds its own ground.
	_, err = s.Create(ctx, onCrop(t, pool, "453", "FARM-A", "Soybean"))
	if !errors.Is(err, ErrAlreadyFiled) {
		t.Fatalf("withdrawing the udad claim freed the soybean one: %v", err)
	}
	if !strings.Contains(err.Error(), soybean.Reference) {
		t.Fatalf("the refusal does not name the standing claim: %v", err)
	}
}

// Two copies of one claim sent at the same moment both pass the lookup at the
// top of Create and only one wins the insert; the loser reaches the duplicate
// check and must be told it is the same claim, not refused.
//
// create is called rather than Create because Create answers a retry from the
// client reference before the transaction opens, which is the branch this test
// is not about.
func TestARetryWithTheSameClientRefStillReturnsErrRetry(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	in := onCrop(t, pool, "453", "FARM-A", "Udad")
	in.ClientRef = "draft-0000000101"
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}

	again := onCrop(t, pool, "453", "FARM-A", "Udad")
	again.ClientRef = "draft-0000000101"
	if _, err := s.create(ctx, again); !errors.Is(err, ErrRetry) {
		t.Fatalf("a handset's own claim arriving twice was refused rather than retried: %v", err)
	}
}

// A farmer may report loss on land the registry has not linked, and then the
// survey number is all there is to match on. The crop still divides it: an
// unlinked parcel has more than one thing growing on it too.
func TestAParcelWithNoFarmIDStillFallsBackToItsSurveyNumber(t *testing.T) {
	s, pool := testStore(t)
	seedAdjudicated(t, s, pool)
	ctx := context.Background()

	if _, err := s.Create(ctx, onCrop(t, pool, "701", "", "Soybean")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, onCrop(t, pool, "701", "", "Soybean")); !errors.Is(err, ErrAlreadyFiled) {
		t.Fatalf("an unlinked survey number was claimed twice for one crop: %v", err)
	}
	if _, err := s.Create(ctx, onCrop(t, pool, "701", "", "Udad")); err != nil {
		t.Fatalf("a second crop on an unlinked survey number was refused: %v", err)
	}
	if _, err := s.Create(ctx, onCrop(t, pool, "702", "", "Soybean")); err != nil {
		t.Fatalf("a different unlinked survey number was refused: %v", err)
	}
}
