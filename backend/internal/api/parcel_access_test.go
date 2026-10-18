package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Who may be told where a parcel is.
//
// A boundary and its sampling points are the shape and position of an
// identified person's land. parcel:read is held by every farmer and carries no
// jurisdiction, so the routes that return them answered for any farm id that
// was asked for -- and a farm id is an AgriStack identifier printed on
// documents and known around a village. "You need the id" was never a control.

// oneFarmer is a registry holding two farmers, each with one parcel.
type oneFarmer struct{}

func (oneFarmer) FarmerIDs() []string { return []string{"F-MINE", "F-THEIRS"} }

func (oneFarmer) FarmerRecord(_ context.Context, farmerID string) (*agristack.FarmerRecord, error) {
	switch farmerID {
	case "F-MINE":
		return &agristack.FarmerRecord{
			Farmer:  agristack.Farmer{FarmerID: "F-MINE", MobileNo: "9527464552"},
			Parcels: []agristack.Parcel{{FarmID: "FARM-MINE", VillageLGDCode: "V1"}},
		}, nil
	case "F-THEIRS":
		return &agristack.FarmerRecord{
			Farmer:  agristack.Farmer{FarmerID: "F-THEIRS", MobileNo: "9999999999"},
			Parcels: []agristack.Parcel{{FarmID: "FARM-THEIRS", VillageLGDCode: "V1"}},
		}, nil
	}
	return nil, agristack.ErrNotFound
}

func parcelServer(t *testing.T) *Server {
	t.Helper()
	seed, err := auth.GenerateSeed()
	if err != nil {
		t.Fatal(err)
	}
	k, err := auth.NewKeyring("esurvey", "esurvey-api", seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Registry: oneFarmer{}, Auth: &auth.Service{Keys: k}}
}

// asksFor builds a request for one parcel, as one farmer.
func asksFor(farmID, farmerQuery, grant string) *http.Request {
	url := "/api/v1/parcels/" + farmID + "/boundary"
	if farmerQuery != "" {
		url += "?farmer_id=" + farmerQuery
	}
	r := httptest.NewRequest(http.MethodGet, url, nil)
	r.SetPathValue("farmID", farmID)
	if grant != "" {
		r.Header.Set("X-Farmer-Grant", grant)
	}
	return r
}

// withPrincipal attaches the caller the middleware would have resolved.
func withPrincipal(r *http.Request, p *rbac.Principal) *http.Request {
	return r.WithContext(rbac.WithPrincipal(r.Context(), p))
}

func theFarmer() *rbac.Principal {
	return &rbac.Principal{
		UserID: "acct-1", Kind: rbac.KindFarmer, FarmerID: "F-MINE",
	}
}

func TestAFarmerMayBeToldWhereTheirOwnParcelIs(t *testing.T) {
	s := parcelServer(t)
	r := withPrincipal(asksFor("FARM-MINE", "", ""), theFarmer())
	w := httptest.NewRecorder()

	if err := s.mayReadParcel(w, r); err != nil {
		t.Fatalf("a farmer was refused their own parcel: %v", err)
	}
}

func TestAFarmerIsNotToldWhereSomebodyElsesParcelIs(t *testing.T) {
	// The finding. Knowing the farm id was enough.
	s := parcelServer(t)
	r := withPrincipal(asksFor("FARM-THEIRS", "", ""), theFarmer())
	w := httptest.NewRecorder()

	if err := s.mayReadParcel(w, r); err == nil {
		t.Error("a farmer read the position of another farmer's land")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 -- a caller who supplied the id "+
			"learns nothing from being told it exists", w.Code)
	}
}

func TestNamingAnotherFarmerInTheQueryDoesNotHelp(t *testing.T) {
	// The query parameter is how an officer names a farmer. A farmer setting
	// it must not thereby become that farmer.
	s := parcelServer(t)
	r := withPrincipal(asksFor("FARM-THEIRS", "F-THEIRS", ""), theFarmer())
	w := httptest.NewRecorder()

	if err := s.mayReadParcel(w, r); err == nil {
		t.Error("a farmer reached another's parcel by naming them in the query")
	}
}

func TestAGrantReachesTheParcelsOfTheFarmerItNames(t *testing.T) {
	// The household case. One mobile number acts for several farmers, and the
	// second farmer's own parcel is theirs to see.
	s := parcelServer(t)
	grant, _, err := s.Auth.IssueFarmerGrant("acct-1", "F-THEIRS", auth.GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	r := withPrincipal(asksFor("FARM-THEIRS", "F-THEIRS", grant), theFarmer())
	w := httptest.NewRecorder()
	if err := s.mayReadParcel(w, r); err != nil {
		t.Errorf("a confirmed farmer's own parcel was refused: %v", err)
	}
}

func TestAnotherAccountsGrantDoesNotReachAParcel(t *testing.T) {
	s := parcelServer(t)
	grant, _, err := s.Auth.IssueFarmerGrant("acct-99", "F-THEIRS", auth.GrantTTL)
	if err != nil {
		t.Fatal(err)
	}

	r := withPrincipal(asksFor("FARM-THEIRS", "F-THEIRS", grant), theFarmer())
	w := httptest.NewRecorder()
	if err := s.mayReadParcel(w, r); err == nil {
		t.Error("a grant issued to another account reached a parcel")
	}
}
