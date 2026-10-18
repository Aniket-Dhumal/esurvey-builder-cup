package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// A grant says one account answered for one farmer. Everything below is about
// the ways it could say more than that.

func grantKeyring(t *testing.T) *Keyring {
	t.Helper()
	seed, err := GenerateSeed()
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKeyring("esurvey", "esurvey-api", seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAGrantNamesTheFarmerItWasIssuedFor(t *testing.T) {
	k := grantKeyring(t)
	token, exp, err := k.IssueFarmerGrant("acct-1", "56567763672", GrantTTL)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.After(time.Now()) {
		t.Error("a grant was issued already expired")
	}

	got, err := k.FarmerGranted(token, "acct-1")
	if err != nil {
		t.Fatalf("a grant did not verify for the account it was issued to: %v", err)
	}
	if got != "56567763672" {
		t.Errorf("grant names %q, want the farmer it was issued for", got)
	}
}

func TestAGrantDoesNotWorkForAnotherAccount(t *testing.T) {
	// The failure that matters most on a shared handset. A grant copied out
	// of one account's storage and presented by another must prove nothing --
	// otherwise the knowledge check is answered once and the answer travels.
	k := grantKeyring(t)
	token, _, err := k.IssueFarmerGrant("acct-1", "56567763672", GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := k.FarmerGranted(token, "acct-2"); !errors.Is(err, ErrGrantClaims) {
		t.Errorf("error = %v; a grant must be bound to the account that "+
			"answered for it", err)
	}
}

func TestAnExpiredGrantAsksForTheAnswerAgain(t *testing.T) {
	k := grantKeyring(t)
	token, _, err := k.IssueFarmerGrant("acct-1", "56567763672", -time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	_, err = k.FarmerGranted(token, "acct-1")
	if !errors.Is(err, ErrGrantExpired) {
		t.Errorf("error = %v, want ErrGrantExpired -- an expired grant is "+
			"answered by confirming the farmer again, not by signing in again", err)
	}
}

func TestATamperedGrantIsRefused(t *testing.T) {
	// The payload carries the farmer id. Editing it to name a different
	// farmer is the obvious attack and the signature is what stops it.
	k := grantKeyring(t)
	token, _, err := k.IssueFarmerGrant("acct-1", "56567763672", GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(token, ".")
	other, _, err := k.IssueFarmerGrant("acct-1", "99999999999", GrantTTL)
	if err != nil {
		t.Fatal(err)
	}
	swapped := parts[0] + "." + strings.Split(other, ".")[1] + "." + parts[2]

	if _, err := k.FarmerGranted(swapped, "acct-1"); !errors.Is(err, ErrGrantSignature) {
		t.Errorf("error = %v; a payload swapped under a good signature must "+
			"not verify", err)
	}
}

func TestAnAccessTokenIsNotAGrant(t *testing.T) {
	// Both are signed by the same keyring, so the only thing keeping them
	// apart is the type claim. An access token accepted as a grant would let
	// any signed-in farmer name any farmer id.
	k := grantKeyring(t)
	access, _, err := k.Issue(&rbac.Principal{
		UserID: "acct-1", Kind: rbac.KindFarmer, FarmerID: "56567763672",
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := k.FarmerGranted(access, "acct-1"); !errors.Is(err, ErrGrantClaims) {
		t.Errorf("error = %v; an access token must never pass as a grant", err)
	}
}

func TestAGrantIsNotAnAccessToken(t *testing.T) {
	// And the other way round: a grant presented as a bearer token must not
	// authenticate anybody.
	k := grantKeyring(t)
	token, _, err := k.IssueFarmerGrant("acct-1", "56567763672", GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := k.Parse(token); err == nil {
		t.Error("a farmer grant authenticated as an access token")
	}
}

func TestAGrantFromAnotherKeyringIsRefused(t *testing.T) {
	// Someone who mints their own Ed25519 key can produce a well-formed
	// grant. The key id must not be enough to make it trusted.
	k := grantKeyring(t)
	stranger := grantKeyring(t)

	token, _, err := stranger.IssueFarmerGrant("acct-1", "56567763672", GrantTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.FarmerGranted(token, "acct-1"); err == nil {
		t.Error("a grant signed by an unknown key verified")
	}
}

func TestAGrantNeedsBothAnAccountAndAFarmer(t *testing.T) {
	k := grantKeyring(t)
	for _, c := range []struct{ user, farmer string }{
		{"", "56567763672"},
		{"acct-1", ""},
		{"", ""},
	} {
		if _, _, err := k.IssueFarmerGrant(c.user, c.farmer, GrantTTL); err == nil {
			t.Errorf("IssueFarmerGrant(%q, %q) succeeded; a grant with a hole "+
				"in it is a grant for anyone", c.user, c.farmer)
		}
	}
}
