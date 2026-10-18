package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

func testKeyring(t *testing.T) *Keyring {
	t.Helper()
	seed, err := GenerateSeed()
	if err != nil {
		t.Fatalf("generating seed: %v", err)
	}
	kr, err := NewKeyring("esurvey", "esurvey-api", seed, nil)
	if err != nil {
		t.Fatalf("building keyring: %v", err)
	}
	return kr
}

func testPrincipal() *rbac.Principal {
	return &rbac.Principal{
		UserID:    "0d1a1e9e-0000-4000-8000-000000000001",
		Kind:      rbac.KindOfficer,
		Name:      "R. Deshmukh",
		SessionID: "sess-1",
		Grants: []rbac.Grant{{
			Role:              rbac.RoleTalathi,
			JurisdictionID:    "11111111-0000-4000-8000-000000562111",
			JurisdictionCode:  "562111",
			JurisdictionLevel: rbac.LevelVillage,
			JurisdictionName:  "Malegaon",
		}},
	}
}

func TestRoundTripCarriesGrants(t *testing.T) {
	kr := testKeyring(t)

	token, exp, err := kr.Issue(testPrincipal(), 15*time.Minute)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	if time.Until(exp) > 15*time.Minute+time.Second {
		t.Errorf("expiry %v is beyond the requested lifetime", exp)
	}

	got, err := kr.Parse(token)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(got.Grants) != 1 || got.Grants[0].Role != rbac.RoleTalathi {
		t.Fatalf("grants did not survive the round trip: %+v", got.Grants)
	}
	if got.Grants[0].JurisdictionCode != "562111" {
		t.Errorf("jurisdiction = %q, want 562111", got.Grants[0].JurisdictionCode)
	}
	if !got.Can(rbac.PermAssessmentCreate) {
		t.Error("a Talathi should be able to create an assessment")
	}
}

// The classic JWT attack: rewrite the header to an algorithm the verifier
// treats as trivially satisfiable. This implementation knows one algorithm
// and must refuse anything else before it looks at a signature.
func TestAlgorithmConfusionIsRefused(t *testing.T) {
	kr := testKeyring(t)
	token, _, _ := kr.Issue(testPrincipal(), time.Minute)
	parts := strings.Split(token, ".")

	for _, alg := range []string{"none", "None", "HS256", "RS256", ""} {
		forged := b64([]byte(`{"alg":"` + alg + `","typ":"JWT","kid":"` +
			kr.SigningKeyID() + `"}`))
		if _, err := kr.Parse(forged + "." + parts[1] + "." + parts[2]); err == nil {
			t.Errorf("alg=%q was accepted", alg)
		}
	}

	// And with an empty signature, which is what alg=none would carry.
	forged := b64([]byte(`{"alg":"none","typ":"JWT","kid":"` + kr.SigningKeyID() + `"}`))
	if _, err := kr.Parse(forged + "." + parts[1] + "."); err == nil {
		t.Error("an unsigned token was accepted")
	}
}

// Escalating a role by editing the payload must invalidate the signature.
func TestTamperedClaimsAreRefused(t *testing.T) {
	kr := testKeyring(t)
	token, _, _ := kr.Issue(testPrincipal(), time.Minute)
	parts := strings.Split(token, ".")

	raw, err := unb64(parts[1])
	if err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("decoding claims: %v", err)
	}
	c.Grt[0].Role = string(rbac.RoleDistrictMagistrate)

	edited, _ := json.Marshal(c)
	if _, err := kr.Parse(parts[0] + "." + b64(edited) + "." + parts[2]); err == nil {
		t.Fatal("a token with an edited role was accepted")
	}
}

func TestExpiredTokenIsDistinguishable(t *testing.T) {
	kr := testKeyring(t)

	// Negative lifetime: already expired when issued.
	token, _, err := kr.Issue(testPrincipal(), -2*time.Minute)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	if _, err := kr.Parse(token); err == nil {
		t.Fatal("an expired token was accepted")
	} else if !isExpired(err) {
		t.Errorf("error = %v, want ErrTokenExpired so the client knows to refresh", err)
	}
}

func TestForeignKeyIsRefused(t *testing.T) {
	mine := testKeyring(t)
	theirs := testKeyring(t)

	token, _, _ := theirs.Issue(testPrincipal(), time.Minute)
	if _, err := mine.Parse(token); err == nil {
		t.Fatal("a token signed by another deployment was accepted")
	}
}

// Rotation must not sign everyone out: a token minted under the old key stays
// verifiable while that key is still in the verify set.
func TestPreviousKeyStillVerifies(t *testing.T) {
	oldSeed, _ := GenerateSeed()
	newSeed, _ := GenerateSeed()

	before, err := NewKeyring("esurvey", "esurvey-api", oldSeed, nil)
	if err != nil {
		t.Fatalf("building keyring: %v", err)
	}
	token, _, _ := before.Issue(testPrincipal(), time.Minute)

	after, err := NewKeyring("esurvey", "esurvey-api", newSeed, []string{oldSeed})
	if err != nil {
		t.Fatalf("building rotated keyring: %v", err)
	}
	if _, err := after.Parse(token); err != nil {
		t.Errorf("token from the previous key was rejected: %v", err)
	}
	if after.SigningKeyID() == before.SigningKeyID() {
		t.Error("rotation did not change the signing key id")
	}

	// Once the old key is dropped, its tokens stop working.
	dropped, _ := NewKeyring("esurvey", "esurvey-api", newSeed, nil)
	if _, err := dropped.Parse(token); err == nil {
		t.Error("a token from a retired key was still accepted")
	}
}

func TestAudienceAndIssuerAreChecked(t *testing.T) {
	seed, _ := GenerateSeed()
	signer, _ := NewKeyring("esurvey", "esurvey-api", seed, nil)
	token, _, _ := signer.Issue(testPrincipal(), time.Minute)

	otherAudience, _ := NewKeyring("esurvey", "some-other-service", seed, nil)
	if _, err := otherAudience.Parse(token); err == nil {
		t.Error("a token minted for another audience was accepted")
	}

	otherIssuer, _ := NewKeyring("elsewhere", "esurvey-api", seed, nil)
	if _, err := otherIssuer.Parse(token); err == nil {
		t.Error("a token from another issuer was accepted")
	}
}

// A role the build does not recognise must not survive as an unchecked one.
func TestUnknownRoleIsDropped(t *testing.T) {
	kr := testKeyring(t)

	p := testPrincipal()
	p.Grants = append(p.Grants, rbac.Grant{Role: rbac.Role("commissioner")})
	token, _, err := kr.Issue(p, time.Minute)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}

	got, err := kr.Parse(token)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	for _, g := range got.Grants {
		if g.Role == rbac.Role("commissioner") {
			t.Fatal("an unrecognised role survived parsing")
		}
	}
}

func TestMalformedTokensAreRefused(t *testing.T) {
	kr := testKeyring(t)
	valid, _, _ := kr.Issue(testPrincipal(), time.Minute)

	cases := map[string]string{
		"empty":           "",
		"two segments":    "a.b",
		"four segments":   valid + ".extra",
		"not base64":      "!!!.???.###",
		"empty signature": strings.Join(strings.Split(valid, ".")[:2], ".") + ".",
		"signature swap":  valid[:len(valid)-4] + "AAAA",
	}
	for name, token := range cases {
		if _, err := kr.Parse(token); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A seed of the wrong size must fail at startup rather than produce a key
// that silently signs nothing useful.
func TestBadSeedsAreRejected(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	long := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize+1))

	for name, seed := range map[string]string{
		"empty":      "",
		"not base64": "not-base-64-!!",
		"too short":  short,
		"too long":   long,
	} {
		if _, err := NewKeyring("esurvey", "esurvey-api", seed, nil); err == nil {
			t.Errorf("%s seed was accepted", name)
		}
	}
}

func isExpired(err error) bool {
	return err != nil && strings.Contains(err.Error(), "expired")
}
