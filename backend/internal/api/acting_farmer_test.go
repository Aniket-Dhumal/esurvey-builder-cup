package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Which farmer a session is acting for.
//
// One mobile number may now act for several farmers, which makes this the
// question every filing and every listing turns on. Getting it wrong in one
// direction files a claim against the wrong person's land; in the other it
// locks a household out of all but one of its people.

func actingKeyring(t *testing.T) *auth.Keyring {
	t.Helper()
	seed, err := auth.GenerateSeed()
	if err != nil {
		t.Fatal(err)
	}
	k, err := auth.NewKeyring("esurvey", "esurvey-api", seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func farmerServer(t *testing.T) *Server {
	t.Helper()
	return &Server{Auth: &auth.Service{Keys: actingKeyring(t)}}
}

func withGrant(grant string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", nil)
	if grant != "" {
		r.Header.Set("X-Farmer-Grant", grant)
	}
	return r
}

func aFarmer() *rbac.Principal {
	return &rbac.Principal{
		UserID: "acct-1", Kind: rbac.KindFarmer, FarmerID: "F-OWN",
	}
}

func TestAFarmerActsForTheirOwnRecordWithNoGrant(t *testing.T) {
	// The ordinary case, and the one that must not have been made harder:
	// the farmer whose registered number signed in needs no grant at all.
	s := farmerServer(t)

	for _, wanted := range []string{"", "F-OWN"} {
		got, err := s.actingFarmer(withGrant(""), aFarmer(), wanted)
		if err != nil {
			t.Fatalf("actingFarmer(%q) refused the account's own farmer: %v",
				wanted, err)
		}
		if got != "F-OWN" {
			t.Errorf("actingFarmer(%q) = %q, want the account's own", wanted, got)
		}
	}
}

func TestAFarmerActsForAnotherOnlyWithAGrantForThem(t *testing.T) {
	s := farmerServer(t)

	grant, _, err := s.Auth.IssueFarmerGrant("acct-1", "F-OTHER", auth.GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.actingFarmer(withGrant(grant), aFarmer(), "F-OTHER")
	if err != nil {
		t.Fatalf("a confirmed farmer was refused: %v", err)
	}
	if got != "F-OTHER" {
		t.Errorf("acting for %q, want F-OTHER", got)
	}
}

func TestNamingAnotherFarmerWithNoGrantIsRefused(t *testing.T) {
	// Refused, not silently rewritten to the account's own farmer. The old
	// code overwrote it, which was safe when an account meant one farmer and
	// is quietly wrong now: the second person's claim would be filed against
	// the first, with the evidence attached to the wrong land.
	s := farmerServer(t)

	got, err := s.actingFarmer(withGrant(""), aFarmer(), "F-OTHER")
	if !errors.Is(err, errNotMyFarmer) {
		t.Errorf("error = %v, want a refusal", err)
	}
	if got == "F-OWN" {
		t.Error("the named farmer was replaced with the account's own -- a " +
			"claim meant for one person would be filed against another")
	}
}

func TestAGrantForOneFarmerDoesNotCoverAnother(t *testing.T) {
	// Confirming one person must not open the rest. The grant names a farmer
	// and the header is checked against what was asked for.
	s := farmerServer(t)

	grant, _, err := s.Auth.IssueFarmerGrant("acct-1", "F-OTHER", auth.GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.actingFarmer(withGrant(grant), aFarmer(), "F-THIRD"); !errors.Is(err, errNotMyFarmer) {
		t.Errorf("error = %v; a grant for one farmer covered another", err)
	}
}

func TestAnotherAccountsGrantIsRefused(t *testing.T) {
	// The failure that matters on a shared handset: a grant lifted out of one
	// account's storage and presented by another.
	s := farmerServer(t)

	grant, _, err := s.Auth.IssueFarmerGrant("acct-2", "F-OTHER", auth.GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.actingFarmer(withGrant(grant), aFarmer(), "F-OTHER"); !errors.Is(err, errNotMyFarmer) {
		t.Errorf("error = %v; a grant issued to another account was accepted", err)
	}
}

func TestAnOfficerIsNotNarrowedByThis(t *testing.T) {
	// Officers file for farmers in their jurisdiction and are checked by the
	// scope rules, not by a grant. This must pass their id through untouched.
	s := farmerServer(t)
	officer := &rbac.Principal{UserID: "u-1", Kind: rbac.KindOfficer}

	got, err := s.actingFarmer(withGrant(""), officer, "F-ANY")
	if err != nil {
		t.Fatalf("an officer was refused: %v", err)
	}
	if got != "F-ANY" {
		t.Errorf("acting for %q, want the id the officer named", got)
	}
}

func TestTheActingScopeFollowsTheGrant(t *testing.T) {
	// The break this closes. A farmer acting for the second person in a
	// household could file a claim -- createReport takes the grant -- and then
	// every route that resolved the claim by id used the raw scope, which is
	// the account's own farmer and nobody else's. So the claim was filed, the
	// field was walked, and every photograph came back "no such report".
	s := farmerServer(t)
	grant, _, err := s.Auth.IssueFarmerGrant("acct-1", "F-OTHER", auth.GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	r := withGrant(grant)
	r.URL.RawQuery = "farmer_id=F-OTHER"
	scope, err := s.actingScope(r, aFarmer())
	if err != nil {
		t.Fatalf("a confirmed farmer was refused: %v", err)
	}
	if scope.FarmerID != "F-OTHER" {
		t.Errorf("scope names %q, want the farmer being acted for", scope.FarmerID)
	}
}

func TestTheActingScopeIsTheAccountsOwnFarmerByDefault(t *testing.T) {
	s := farmerServer(t)
	scope, err := s.actingScope(withGrant(""), aFarmer())
	if err != nil {
		t.Fatal(err)
	}
	if scope.FarmerID != "F-OWN" {
		t.Errorf("scope names %q, want the account's own", scope.FarmerID)
	}
}

func TestTheActingScopeRefusesAFarmerWithNoGrant(t *testing.T) {
	// Naming somebody in the query is not becoming them.
	s := farmerServer(t)
	r := withGrant("")
	r.URL.RawQuery = "farmer_id=F-OTHER"

	if _, err := s.actingScope(r, aFarmer()); !errors.Is(err, errNotMyFarmer) {
		t.Errorf("error = %v, want a refusal", err)
	}
}

func TestAnOfficersScopeIsNotTouchedByAFarmerIdInTheQuery(t *testing.T) {
	// An officer's reach is jurisdictions. A farmer id in the query is a
	// filter they are entitled to apply, and it must not become their scope.
	s := farmerServer(t)
	officer := &rbac.Principal{
		UserID: "u-1", Kind: rbac.KindOfficer,
		Grants: []rbac.Grant{{
			Role: rbac.RoleTehsildar, JurisdictionID: "11111111-1111-1111-1111-111111111111",
		}},
	}
	r := withGrant("")
	r.URL.RawQuery = "farmer_id=F-ANYBODY"

	scope, err := s.actingScope(r, officer)
	if err != nil {
		t.Fatal(err)
	}
	if scope.FarmerID != "" {
		t.Errorf("an officer's scope was narrowed to farmer %q by a query "+
			"parameter", scope.FarmerID)
	}
	if len(scope.JurisdictionIDs) == 0 {
		t.Error("the officer lost their jurisdictions")
	}
}

func TestAGrantAloneSaysWhichFarmer(t *testing.T) {
	// A grant names exactly one farmer, so presenting one is itself saying
	// which farmer the request is about. Without this every call site would
	// have to repeat the id in a query parameter beside the header, and the
	// one that forgot would act as the account's own farmer instead --
	// resolving the claim to "not found" after the field had been walked.
	s := farmerServer(t)
	grant, _, err := s.Auth.IssueFarmerGrant("acct-1", "F-OTHER", auth.GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.actingFarmer(withGrant(grant), aFarmer(), "")
	if err != nil {
		t.Fatalf("a grant with no query parameter beside it was refused: %v", err)
	}
	if got != "F-OTHER" {
		t.Errorf("acting for %q, want the farmer the grant names", got)
	}
}

func TestAnInvalidGrantAloneFallsBackToTheAccountsOwnFarmer(t *testing.T) {
	// A grant from another account, or an expired one, says nothing. It must
	// not become a way to act as somebody, and it must not break the ordinary
	// request either: the account's own farmer is what a session means with
	// nothing else said.
	s := farmerServer(t)
	stranger, _, err := s.Auth.IssueFarmerGrant("acct-99", "F-OTHER", auth.GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.actingFarmer(withGrant(stranger), aFarmer(), "")
	if err != nil {
		t.Fatalf("an unusable grant broke an ordinary request: %v", err)
	}
	if got != "F-OWN" {
		t.Errorf("acting for %q; another account's grant named the farmer", got)
	}
}
