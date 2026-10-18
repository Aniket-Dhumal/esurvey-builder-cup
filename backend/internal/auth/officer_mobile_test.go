package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/identity"
)

// An officer putting a handset on their own account.
//
// The number becomes the second factor, so what these hold is that it cannot
// be saved unverified -- a typo would otherwise demand a code from a phone
// nobody has -- and that it cannot be pointed at a number somebody else is
// already signing in with.

func officerAccount(t *testing.T, name, mobile string) (*Service, *capturedSender, string) {
	t.Helper()
	svc, sender, pool := otpService(t)
	makeAccount(t, pool, "officer", name, mobile)
	var id string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE mobile_e164 = $1`, mobile).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return svc, sender, id
}

func TestANumberIsNotSavedUntilItAnswers(t *testing.T) {
	// The account starts with one number and is moved to another.
	svc, sender, id := officerAccount(t, "Test Officer", "+919000000001")
	ctx := context.Background()

	challenge, err := svc.StartMobileChange(ctx, id, "9000000002", Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	if sender.code == "" {
		t.Fatal("no code was sent to the new number")
	}

	// Nothing is saved yet: the officer has typed a number, not proved one.
	before, err := svc.Users.ByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.MobileE164 != "+919000000001" {
		t.Fatalf("the number changed before it was proved: %q", before.MobileE164)
	}

	after, err := svc.ConfirmMobileChange(ctx, id, challenge.Token, sender.code, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	if after.MobileE164 != "+919000000002" {
		t.Fatalf("the proved number was not saved: %q", after.MobileE164)
	}
}

func TestAWrongCodeLeavesTheOldNumber(t *testing.T) {
	svc, sender, id := officerAccount(t, "Test Officer", "+919000000001")
	ctx := context.Background()

	challenge, err := svc.StartMobileChange(ctx, id, "9000000002", Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	wrong := "000000"
	if sender.code == wrong {
		wrong = "111111"
	}
	if _, err := svc.ConfirmMobileChange(ctx, id, challenge.Token, wrong, Caller{IP: "1.2.3.4"}); err == nil {
		t.Fatal("an unproved number was saved")
	}

	user, err := svc.Users.ByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if user.MobileE164 != "+919000000001" {
		t.Fatalf("the account moved to %q on a wrong code", user.MobileE164)
	}
}

func TestANumberAnotherAccountUsesIsRefusedBeforeACodeIsSent(t *testing.T) {
	svc, sender, pool := otpService(t)
	makeAccount(t, pool, "officer", "First Officer", "+919000000001")
	makeAccount(t, pool, "farmer", "A Farmer", "+919000000002")

	var id string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE mobile_e164 = '+919000000001'`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	_, err := svc.StartMobileChange(context.Background(), id, "9000000002",
		Caller{IP: "1.2.3.4"})
	if !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("a number in use elsewhere was accepted: %v", err)
	}
	// And no code went to the other person's handset, which is the point of
	// checking before sending rather than after.
	if sender.code != "" {
		t.Fatal("a code was sent to somebody else's number")
	}
}

func TestSomebodyElsesChallengeCannotMoveMyNumber(t *testing.T) {
	svc, sender, pool := otpService(t)
	makeAccount(t, pool, "officer", "First Officer", "+919000000001")
	makeAccount(t, pool, "officer", "Second Officer", "+919000000002")

	ctx := context.Background()
	var first, second string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM users WHERE mobile_e164 = '+919000000001'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id FROM users WHERE mobile_e164 = '+919000000002'`).Scan(&second); err != nil {
		t.Fatal(err)
	}

	// The first officer verifies a spare handset...
	challenge, err := svc.StartMobileChange(ctx, first, "9000000003", Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	// ...and the second tries to spend that proof on their own account.
	_, err = svc.ConfirmMobileChange(ctx, second, challenge.Token, sender.code,
		Caller{IP: "1.2.3.4"})
	if !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("one officer's proof moved another's number: %v", err)
	}
}

func TestAnOfficerCanTakeTheirHandsetOff(t *testing.T) {
	svc, _, id := officerAccount(t, "Test Officer", "+919000000001")
	ctx := context.Background()

	// A lost phone must not be a locked account.
	user, err := svc.RemoveMobile(ctx, id, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	if user.MobileE164 != "" {
		t.Fatalf("the number is still on the account: %q", user.MobileE164)
	}

	// And the next sign-in asks for the password alone.
	_, _, challenge, err := svc.LoginOfficer(ctx,
		"officer0001@test.invalid", "timing-equalisation-placeholder",
		Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	if challenge != nil {
		t.Fatal("a second factor was demanded of an account with no handset")
	}
}
