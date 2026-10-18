package agristack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The trial procedure is: drop a registry response into a directory. Every
// test here is a way that procedure can go wrong in someone's hands.

const bareRecord = `{
  "state_lgd_code": "27",
  "FarmerData": {"farmer_id": "F1", "farmer_name": "Test Farmer"},
  "land_data": [{
    "farmer_id": "F1", "farm_id": "MH1", "survey_number": "453",
    "village_lgd_code": "562111", "district_lgd_code": "496",
    "owner_extent": "1.6", "area_unit": "Hectare"
  }],
  "crop_sown_data": [{
    "farm_id": "MH1", "survey_number": "453", "season": "Kharif",
    "crop_name": "Soybean", "sowing_date": "20/06/2025",
    "sown_area_integer": 1, "sown_area_decimal": "6", "sown_area_unit": "Hectare"
  }]
}`

func envelope(t *testing.T, record string) string {
	t.Helper()
	return `{"message":{"search_response":[{"data":{"reg_records":` + record + `}}]}}`
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return p
}

// noThrottle removes the stat interval so a test does not have to sleep for a
// second to observe a reload.
func noThrottle(s *FileSource) *FileSource {
	s.minInterval = 0
	return s
}

func TestAnEmptyDirectoryIsNotAFailure(t *testing.T) {
	// The service must start before any record has been placed. Refusing to
	// boot means an operator cannot set the platform up and then load it.
	s, err := NewFileSource(t.TempDir())
	if err != nil {
		t.Fatalf("an empty registry directory refused to start: %v", err)
	}
	if got := len(s.FarmerIDs()); got != 0 {
		t.Errorf("farmers = %d, want 0", got)
	}

	_, err = s.FarmerRecord(context.Background(), "F1")
	if err == nil {
		t.Fatal("a lookup against an empty directory succeeded")
	}
	// The message has to say what to do, not just that nothing was found.
	if !contains(err.Error(), "place a registry response") {
		t.Errorf("unhelpful error for an empty directory: %v", err)
	}
}

func TestARecordDroppedInIsPickedUpWithoutARestart(t *testing.T) {
	// The whole reason records load from a directory.
	dir := t.TempDir()
	s, err := NewFileSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	noThrottle(s)

	if _, err := s.FarmerRecord(context.Background(), "F1"); err == nil {
		t.Fatal("found a farmer before the file existed")
	}

	write(t, dir, "farmer_F1.json", envelope(t, bareRecord))

	rec, err := s.FarmerRecord(context.Background(), "F1")
	if err != nil {
		t.Fatalf("a record placed in the directory was not picked up: %v", err)
	}
	if rec.Farmer.FarmerName != "Test Farmer" {
		t.Errorf("farmer name = %q", rec.Farmer.FarmerName)
	}
}

func TestAnUpdatedRecordIsPickedUp(t *testing.T) {
	// An operator correcting a record mid-trial must not have to restart.
	dir := t.TempDir()
	write(t, dir, "farmer_F1.json", envelope(t, bareRecord))
	s, err := NewFileSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	noThrottle(s)

	var updated map[string]any
	if err := json.Unmarshal([]byte(bareRecord), &updated); err != nil {
		t.Fatal(err)
	}
	updated["FarmerData"].(map[string]any)["farmer_name"] = "Corrected Name"
	body, _ := json.Marshal(updated)

	// Size differs, so the change is visible even where mtime is coarse.
	write(t, dir, "farmer_F1.json", envelope(t, string(body)))

	rec, err := s.FarmerRecord(context.Background(), "F1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Farmer.FarmerName != "Corrected Name" {
		t.Errorf("a corrected record was not picked up: name = %q", rec.Farmer.FarmerName)
	}
}

func TestOneBadFileDoesNotUnloadTheGoodOnes(t *testing.T) {
	// A bad paste during a trial must cost one farmer, not all of them.
	dir := t.TempDir()
	write(t, dir, "good.json", envelope(t, bareRecord))
	write(t, dir, "broken.json", `{"message": {"search_response": [`)

	s, err := NewFileSource(dir)
	if err != nil {
		t.Fatalf("a directory with one bad file refused to load: %v", err)
	}
	if _, err := s.FarmerRecord(context.Background(), "F1"); err != nil {
		t.Errorf("the good record was lost with the bad one: %v", err)
	}

	st := s.Status()
	if len(st.Problems) != 1 {
		t.Fatalf("problems = %d, want 1", len(st.Problems))
	}
	// Named, so the operator knows which paste to fix.
	if st.Problems[0].File != "broken.json" {
		t.Errorf("problem file = %q", st.Problems[0].File)
	}
}

func TestTheThreeShapesAnOperatorMightHave(t *testing.T) {
	// Whichever shape their tooling produced should work; rejecting two of
	// three turns a trial into a debugging session.
	for name, body := range map[string]string{
		"on-seek envelope":   envelope(t, bareRecord),
		"bare record":        bareRecord,
		"array of records":   "[" + bareRecord + "]",
		"array of envelopes": "[" + envelope(t, bareRecord) + "]",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "record.json", body)
			s, err := NewFileSource(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.FarmerRecord(context.Background(), "F1"); err != nil {
				t.Errorf("%s was not accepted: %v", name, err)
			}
		})
	}
}

func TestAFileThatIsNotARecordIsReportedByName(t *testing.T) {
	// Silently ignoring it is how an operator ends up staring at a directory
	// with the file plainly in it, wondering why the farmer is missing.
	dir := t.TempDir()
	write(t, dir, "notes.json", `{"some": "other json"}`)

	s, err := NewFileSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if len(st.Problems) != 1 || st.Problems[0].File != "notes.json" {
		t.Fatalf("problems = %+v, want notes.json reported", st.Problems)
	}
	if !contains(st.Problems[0].Reason, "farmer record") {
		t.Errorf("reason does not say what is wrong: %q", st.Problems[0].Reason)
	}
}

func TestARemovedFileUnloadsItsFarmer(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "farmer_F1.json", envelope(t, bareRecord))
	s, err := NewFileSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	noThrottle(s)

	if _, err := s.FarmerRecord(context.Background(), "F1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "farmer_F1.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FarmerRecord(context.Background(), "F1"); err == nil {
		t.Error("a farmer stayed loaded after its file was removed")
	}
}

func TestWhateverTheRegistryDirectoryHoldsParses(t *testing.T) {
	// Not a named farmer any more.
	//
	// This used to read one curated record -- a captured on-seek response with
	// a hand-added boundary, from when no live response carried geometry --
	// and assert its parcel and its 1.6 hectares. That record has been removed
	// now that a real export supplies geometry, so what is worth checking is
	// the thing that actually breaks: a file dropped into this directory by
	// hand, for a trial, parsing into records the platform can use.
	//
	// Skipped where the directory is absent -- the container image build
	// copies only backend/ -- and where it holds no records, which is the
	// ordinary state of a fresh clone.
	const dir = "../../../mock_files"
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("mock_files is not reachable from here: %v", err)
	}

	s, err := NewFileSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := s.FarmerIDs()
	if len(ids) == 0 {
		t.Skip("no registry records in mock_files; nothing to parse")
	}

	// Whatever is there must not merely load: it has to carry the fields the
	// assessment path reads, or the first farmer to file finds out instead.
	withGeometry, withCrops := 0, 0
	for _, id := range ids {
		rec, err := s.FarmerRecord(context.Background(), id)
		if err != nil {
			t.Fatalf("farmer %s did not load: %v", id, err)
		}
		if rec.Farmer.FarmerID != id {
			t.Errorf("record filed under %s carries farmer id %q",
				id, rec.Farmer.FarmerID)
		}
		if rec.Farmer.VillageLGDCode == "" {
			t.Errorf("farmer %s has no village, so nothing can place them", id)
		}
		// Never the whole number, whatever a registry sends.
		if n := len(rec.Farmer.Aadhaar); n > 4 {
			t.Errorf("farmer %s carries %d Aadhaar digits; only the last four "+
				"may be kept", id, n)
		}
		for i := range rec.Parcels {
			p := &rec.Parcels[i]
			if p.FarmID == "" {
				t.Errorf("farmer %s has a parcel with no farm id", id)
			}
			if p.PlotGeometry != nil && len(p.PlotGeometry.Coordinates) > 0 {
				withGeometry++
			}
			if _, err := p.ExtentHectares(); err != nil && p.OwnerExtent != "" {
				t.Errorf("parcel %s has an unreadable extent %q: %v",
					p.FarmID, p.OwnerExtent, err)
			}
		}
		withCrops += len(rec.Crops)
	}

	t.Logf("%d farmers, %d parcels with geometry, %d crop rows",
		len(ids), withGeometry, withCrops)
}

// --- derived values --------------------------------------------------------

func TestExtentUnits(t *testing.T) {
	cases := []struct {
		unit  string
		value string
		want  float64
		ok    bool
	}{
		{"Hectare", "1.6", 1.6, true},
		{"", "1.6", 1.6, true},
		{"Acre", "1", 0.404686, true},
		{"Guntha", "40", 0.404686, true},
		{"Bigha", "1", 0, false},
		// A zero extent divides into every per-hectare figure downstream.
		{"Hectare", "0", 0, false},
		{"Hectare", "-1", 0, false},
		{"Hectare", "not a number", 0, false},
	}
	for _, c := range cases {
		p := Parcel{OwnerExtent: c.value, AreaUnit: c.unit}
		got, err := p.ExtentHectares()
		if c.ok != (err == nil) {
			t.Errorf("%q %s: err = %v, want ok=%v", c.value, c.unit, err, c.ok)
			continue
		}
		if c.ok && !nearly(got, c.want) {
			t.Errorf("%q %s = %v, want %v", c.value, c.unit, got, c.want)
		}
	}
}

func TestSowingDateAcceptsBothFormats(t *testing.T) {
	// An export that has already normalised to ISO must not be reparsed as
	// DD/MM/YYYY -- that silently transposes the day and the year.
	cases := map[string]string{
		"20/06/2025": "2025-06-20",
		"2025-06-20": "2025-06-20",
		"01/12/2025": "2025-12-01",
	}
	for in, want := range cases {
		c := CropSown{SowingDate: in}
		got, err := c.SowingDateISO()
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}

	for _, bad := range []string{"", "June 2025", "31/31/2025"} {
		c := CropSown{SowingDate: bad}
		if _, err := c.SowingDateISO(); err == nil {
			t.Errorf("%q was accepted as a date", bad)
		}
	}
}

func nearly(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
