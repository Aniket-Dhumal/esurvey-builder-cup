package auth

import (
	"context"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// A handset signing in for the first time.
//
// The account is not the authority: it holds a number and nothing else, and
// every record it can open is opened by proving a Farmer ID and that farmer's
// date of birth. These tests hold both halves of that -- that a stranger can
// get in, and that getting in reaches nothing.

func TestAHandsetNobodyEnrolledCanSignIn(t *testing.T) {
	svc, sender, _ := otpService(t)
	const mobile = "+919000000001"
	ctx := context.Background()

	if err := svc.RequestFarmerOTP(ctx, mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	if sender.code == "" {
		t.Fatal("no code was sent to a handset with no account")
	}

	tokens, user, err := svc.VerifyFarmerOTP(ctx, mobile, sender.code, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("a new handset could not sign in: %v", err)
	}
	if tokens.AccessToken == "" {
		t.Fatal("no access token")
	}
	if user.Kind != rbac.KindFarmer {
		t.Fatalf("the account was created as %q", user.Kind)
	}
	if user.AgriStackFarmerID != "" {
		t.Fatalf("a self-enrolled handset was given a farmer id: %q",
			user.AgriStackFarmerID)
	}
}

func TestSigningInAloneReachesNothing(t *testing.T) {
	svc, sender, _ := otpService(t)
	const mobile = "+919000000001"
	ctx := context.Background()

	if err := svc.RequestFarmerOTP(ctx, mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	_, user, err := svc.VerifyFarmerOTP(ctx, mobile, sender.code, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}

	// Every store call refuses an empty scope outright, so this is the line
	// between "signed in" and "can read somebody's claim".
	if got := user.Principal("session-1").Scope(); !got.Empty() {
		t.Fatalf("a handset with no grant has reach: %+v", got)
	}
}

func TestAskingForACodeCreatesNoAccount(t *testing.T) {
	svc, _, pool := otpService(t)
	const mobile = "+919000000002"

	// Otherwise anybody could fill the user table by naming numbers.
	if err := svc.RequestFarmerOTP(context.Background(), mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE mobile_e164 = $1`, mobile).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("asking for a code created %d account(s)", n)
	}
}

func TestEnrolmentNamesTheHandsetThatAlreadySignedIn(t *testing.T) {
	svc, sender, _ := otpService(t)
	const mobile = "+919000000001"
	ctx := context.Background()

	// The farmer signs in before anybody enrols them.
	if err := svc.RequestFarmerOTP(ctx, mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	_, user, err := svc.VerifyFarmerOTP(ctx, mobile, sender.code, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}

	// The officer enrols them afterwards. A second account is impossible --
	// the handset column is unique -- so the first is named.
	named, err := svc.Users.NameFarmer(ctx, user.ID, identity.NewFarmer{
		FullName:   "Harichndra Yedaba Dhumal",
		MobileE164: mobile,
		// An id of this test's own. A real one would collide with whatever
		// this database already holds, and the farmer id column is unique.
		AgriStackFarmerID: "90000000001",
		AadhaarLast4:      "1234",
	})
	if err != nil {
		t.Fatalf("naming the handset: %v", err)
	}
	if named.ID != user.ID {
		t.Fatal("a second account was made for one handset")
	}
	if named.AgriStackFarmerID != "90000000001" || named.FullName == "" {
		t.Fatalf("the registry identity did not land: %+v", named)
	}

	// And the claims they filed before being enrolled are still theirs.
	if named.Kind != rbac.KindFarmer {
		t.Fatalf("kind changed to %q", named.Kind)
	}
}

func TestOneHandsetIsNotTwoFarmers(t *testing.T) {
	svc, sender, _ := otpService(t)
	const mobile = "+919000000001"
	ctx := context.Background()

	if err := svc.RequestFarmerOTP(ctx, mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	_, user, err := svc.VerifyFarmerOTP(ctx, mobile, sender.code, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Users.NameFarmer(ctx, user.ID, identity.NewFarmer{
		FullName: "First Farmer", MobileE164: mobile, AgriStackFarmerID: "11111111111",
	}); err != nil {
		t.Fatal(err)
	}

	// A second registry identity on the same account would quietly move one
	// farmer's claims under another's name.
	_, err = svc.Users.NameFarmer(ctx, user.ID, identity.NewFarmer{
		FullName: "Second Farmer", MobileE164: mobile, AgriStackFarmerID: "22222222222",
	})
	if err == nil {
		t.Fatal("one handset was named as two different farmers")
	}
}
