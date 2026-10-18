package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
)

// The rule that decides whose record a farmer can read.
//
// Everything about the farmer-facing registry route rests on sameMobile. If it
// answers true too readily, any signed-in farmer can pull the personal record
// of anyone whose Farmer ID they can read off a document -- which in a village
// is everyone.

func TestAMobileMatchesAcrossTheTwoConventions(t *testing.T) {
	// AgriStack carries a bare ten-digit number; the account holds E.164.
	// This is the ordinary case, and a strict string comparison would refuse
	// every farmer in the system.
	if !sameMobile("9527464552", "+919527464552") {
		t.Error("the registry's ten digits and the account's E.164 are the " +
			"same subscriber and must match")
	}
}

func TestADifferentSubscriberNeverMatches(t *testing.T) {
	if sameMobile("9527464552", "+919999999999") {
		t.Error("two different numbers matched; this is the whole control")
	}
}

func TestAnAbsentNumberNeverMatches(t *testing.T) {
	// The failure that would matter most in practice. Farmer accounts created
	// by enrolment may carry no mobile, and registry records for farmers who
	// never gave one carry no mobile either. If blank matched blank, the first
	// such account would open every such record.
	cases := []struct{ registry, account string }{
		{"", "+919527464552"},
		{"9527464552", ""},
		{"", ""},
	}
	for _, c := range cases {
		if sameMobile(c.registry, c.account) {
			t.Errorf("sameMobile(%q, %q) matched; absent is not a value",
				c.registry, c.account)
		}
	}
}

func TestAnUnreadableNumberNeverMatches(t *testing.T) {
	// Normalisation failing must fall to "no", not to "compare the raw
	// strings". A record with a junk mobile should be unreachable, not
	// reachable by anyone whose account holds the same junk.
	for _, junk := range []string{"not a number", "12", "++++"} {
		if sameMobile(junk, junk) {
			t.Errorf("two identical unreadable numbers (%q) matched", junk)
		}
	}
}

// --- the projection ---------------------------------------------------------

// loadReferenceRecord reads the real mock farmer, so the projection is tested
// against the record the platform actually serves rather than a fixture that
// can drift away from it.
func loadReferenceRecord(t *testing.T) *agristack.FarmerRecord {
	t.Helper()

	dir := filepath.Join("..", "..", "..", "mock_files")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("mock_files is not reachable from here: %v", err)
	}

	src, err := agristack.NewFileSource(dir)
	if err != nil {
		t.Fatalf("reading the mock registry: %v", err)
	}
	ids := src.FarmerIDs()
	if len(ids) == 0 {
		t.Skip("no farmer records in mock_files")
	}
	rec, err := src.FarmerRecord(context.Background(), ids[0])
	if err != nil {
		t.Fatalf("reading %s: %v", ids[0], err)
	}
	return rec
}

func TestTheHandsetIsNeverSentIdentityData(t *testing.T) {
	// The omissions are the reason the projection exists, so they are asserted
	// on the encoded bytes rather than on the struct. A field added to the
	// view later, or an embedded struct that quietly carries the whole record
	// through, fails here -- which is exactly when it should.
	//
	// These handsets are shared and borrowed. A caste category cached on one
	// is a liability with no matching use on any farmer screen.
	rec := loadReferenceRecord(t)

	forbidden := []string{
		"aadhaar",
		"aadhaar_hash",
		"caste",
		"dob",
		"gender",
		"khata",
		"joint_owner",
		"identifier_name",
		"mobile",
	}

	// A test that asserts an absence passes just as happily against an empty
	// string. So first prove every term is really in the record being
	// projected from -- otherwise this test would keep passing after someone
	// renamed the registry's fields, and would be guarding nothing.
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("encoding the record: %v", err)
	}
	for _, f := range forbidden {
		if !strings.Contains(strings.ToLower(string(raw)), f) {
			t.Fatalf("%q is not in the registry record, so excluding it from "+
				"the projection proves nothing; fix this list", f)
		}
	}

	body, err := json.Marshal(projectForFarmer(rec))
	if err != nil {
		t.Fatalf("encoding the projection: %v", err)
	}
	got := string(body)

	for _, f := range forbidden {
		if strings.Contains(strings.ToLower(got), f) {
			t.Errorf("the projection carries %q to the handset:\n%s", f, got)
		}
	}
}

func TestTheProjectionCarriesWhatTheReportNeeds(t *testing.T) {
	rec := loadReferenceRecord(t)
	view := projectForFarmer(rec)

	if view.FarmerID == "" || view.FarmerName == "" {
		t.Error("the farmer cannot confirm a record with no name on it")
	}
	if view.VillageLGDCode == "" {
		t.Error("the village code routes the report to an office")
	}
	if len(view.Parcels) == 0 {
		t.Fatal("the reference record has land; the projection dropped it")
	}

	p := view.Parcels[0]
	switch {
	case p.FarmID == "":
		t.Error("farm_id keys the boundary and the geofence")
	case p.SurveyNumber == "":
		t.Error("the survey number is what a farmer recognises the plot by")
	case p.OwnerExtent == "":
		t.Error("owner extent is the insurable area and must survive")
	}
}

func TestTheOwnersOwnScriptSurvives(t *testing.T) {
	// A farmer confirming their parcel should see the name as the 7/12 spells
	// it, not a transliteration of it. If this ever comes through empty the
	// screen silently falls back to Latin and nobody notices.
	rec := loadReferenceRecord(t)
	view := projectForFarmer(rec)

	if len(view.Parcels) == 0 {
		t.Skip("no parcels on the reference record")
	}
	ror := view.Parcels[0].OwnerNameROR
	if ror == "" {
		t.Fatal("owner_name_ror is empty")
	}
	var devanagari bool
	for _, r := range ror {
		if r >= 0x0900 && r <= 0x097F {
			devanagari = true
			break
		}
	}
	if !devanagari {
		t.Errorf("owner_name_ror %q carries no Devanagari; the farmer is "+
			"being shown a transliteration of their own name", ror)
	}
}

func TestCropsAreAttachedToTheParcelTheyWereSownOn(t *testing.T) {
	// The registry returns land and crops as two arrays joined by farm_id.
	// Doing that join here means it happens once rather than in every screen,
	// and a crop attached to the wrong survey number would put the wrong crop
	// on a claim.
	rec := loadReferenceRecord(t)
	view := projectForFarmer(rec)

	var seen int
	for _, p := range view.Parcels {
		for _, c := range p.Crops {
			seen++
			var belongs bool
			for _, src := range rec.Crops {
				if src.FarmID == p.FarmID && src.CropName == c.CropName {
					belongs = true
				}
			}
			if !belongs {
				t.Errorf("crop %q is on parcel %s and the registry does not "+
					"put it there", c.CropName, p.FarmID)
			}
		}
	}
	if seen == 0 {
		t.Error("the reference record has a sown crop and the projection " +
			"dropped it; the report would ask the farmer to type it in")
	}
}

func TestAParcelWithNoGeometryStillProjects(t *testing.T) {
	// Live AgriStack returns plot_geometry: null for every farmer we can
	// reach. That absence is what sends the farmer to the boundary-tracing
	// screen, so it has to survive to the handset as null rather than
	// crashing the projection on the way.
	rec := loadReferenceRecord(t)
	for i := range rec.Parcels {
		rec.Parcels[i].PlotGeometry = nil
	}

	view := projectForFarmer(rec)
	if len(view.Parcels) == 0 {
		t.Fatal("parcels disappeared when their geometry did")
	}
	if view.Parcels[0].PlotGeometry != nil {
		t.Error("a parcel with no geometry must project as null")
	}
}
