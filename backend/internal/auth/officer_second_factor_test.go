package auth

import (
	"context"
	"errors"
	"testing"
)

// An officer's password is half a sign-in.
//
// The other half is a code to the handset on their account. These tests hold
// what that is worth: the password alone issues nothing, a wrong code issues
// nothing, a spent challenge cannot be spent twice, and an officer with no
// number on file still gets in -- because five of the eight accounts on this
// deployment have no number and locking them out to enforce a factor nobody
// gave them helps no one.

func officerWithMobile(t *testing.T) (*Service, *capturedSender, string, string) {
	t.Helper()
	svc, sender, pool := otpService(t)
	const mobile = "+919000000001"
	makeAccount(t, pool, "officer", "Test Officer", mobile)

	// makeAccount stores the placeholder hash, which is a real argon2 hash of
	// a known string -- so it is also the password.
	const password = "timing-equalisation-placeholder"
	username := "officer" + mobile[len(mobile)-4:] + "@test.invalid"
	return svc, sender, username, password
}

func TestAPasswordAloneIsNotASignIn(t *testing.T) {
	svc, sender, username, password := officerWithMobile(t)

	tokens, user, challenge, err := svc.LoginOfficer(
		context.Background(), username, password, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	if tokens != nil {
		t.Fatal("a token was issued on the password alone")
	}
	if challenge == nil || challenge.Token == "" {
		t.Fatal("no second factor was asked for")
	}
	if user == nil {
		t.Fatal("the caller is not told whose sign-in this is")
	}
	if sender.code == "" {
		t.Fatal("no code was sent to the officer's handset")
	}
	// The hint is for picking up the right handset, not for reading the
	// number off the screen.
	if challenge.MobileHint == "+919000000001" {
		t.Fatalf("the whole number is in the hint: %q", challenge.MobileHint)
	}
}

func TestTheCodeFinishesTheSignIn(t *testing.T) {
	svc, sender, username, password := officerWithMobile(t)
	ctx := context.Background()

	_, _, challenge, err := svc.LoginOfficer(ctx, username, password, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}

	tokens, user, err := svc.VerifyOfficerCode(
		ctx, challenge.Token, sender.code, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("a correct code did not finish the sign-in: %v", err)
	}
	if tokens.AccessToken == "" {
		t.Fatal("no access token")
	}
	if user.FullName != "Test Officer" {
		t.Fatalf("signed in as %q", user.FullName)
	}
}

func TestAWrongCodeIssuesNothing(t *testing.T) {
	svc, sender, username, password := officerWithMobile(t)
	ctx := context.Background()

	_, _, challenge, err := svc.LoginOfficer(ctx, username, password, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}

	wrong := "000000"
	if sender.code == wrong {
		wrong = "111111"
	}
	if _, _, err := svc.VerifyOfficerCode(ctx, challenge.Token, wrong, Caller{IP: "1.2.3.4"}); err == nil {
		t.Fatal("a wrong code signed an officer in")
	}
}

func TestAChallengeIsSpentOnce(t *testing.T) {
	svc, sender, username, password := officerWithMobile(t)
	ctx := context.Background()

	_, _, challenge, err := svc.LoginOfficer(ctx, username, password, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	code := sender.code

	if _, _, err := svc.VerifyOfficerCode(ctx, challenge.Token, code, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	// Replaying the pair -- from a log, a proxy, a screenshot -- must not
	// produce a second session.
	_, _, err = svc.VerifyOfficerCode(ctx, challenge.Token, code, Caller{IP: "9.9.9.9"})
	if !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("a spent challenge was accepted again: %v", err)
	}
}

func TestSomebodyElsesChallengeIsNotASignIn(t *testing.T) {
	svc, sender, username, password := officerWithMobile(t)
	ctx := context.Background()

	if _, _, _, err := svc.LoginOfficer(ctx, username, password, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	// A code in hand and a made-up challenge: the code is only worth a
	// session alongside the token that says whose password was accepted.
	_, _, err := svc.VerifyOfficerCode(ctx, "not-a-real-challenge", sender.code,
		Caller{IP: "1.2.3.4"})
	if !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("an invented challenge was accepted: %v", err)
	}
}

func TestAnOfficerWithNoHandsetStillSignsIn(t *testing.T) {
	svc, sender, pool := otpService(t)
	// No mobile: the column is nullable and five of the eight real accounts
	// are like this.
	_, err := pool.Exec(context.Background(), `
		INSERT INTO users (kind, status, full_name, username, email, password_hash)
		VALUES ('officer', 'active', 'No Handset', $1, $1, $2)`,
		"nohandset@test.invalid", dummyHash)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id IN
			(SELECT id FROM users WHERE username = 'nohandset@test.invalid')`)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE username = 'nohandset@test.invalid'`)
	})

	tokens, _, challenge, err := svc.LoginOfficer(context.Background(),
		"nohandset@test.invalid", "timing-equalisation-placeholder",
		Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("an officer with no handset could not sign in: %v", err)
	}
	if challenge != nil {
		t.Fatal("a second factor was demanded of an account that has none")
	}
	if tokens == nil || tokens.AccessToken == "" {
		t.Fatal("no session for an officer with no handset")
	}
	if sender.code != "" {
		t.Fatal("a code was sent although no number is on file")
	}
}

func TestOfficerCodesDoNotSpendAFarmersDailyCap(t *testing.T) {
	svc, _, pool := otpService(t)
	ctx := context.Background()
	const mobile = "+919000000002"
	makeAccount(t, pool, "farmer", "Shared Handset", mobile)

	// One number, two purposes. Sharing Redis keys would mean an officer
	// signing in all morning could exhaust the farmer's ten codes a day.
	for range 3 {
		if _, err := svc.OfficerOTP().Request(ctx, mobile); err != nil {
			t.Fatalf("officer code: %v", err)
		}
	}
	if err := svc.RequestFarmerOTP(ctx, mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatalf("the farmer was refused a code: %v", err)
	}
}
