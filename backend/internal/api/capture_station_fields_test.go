package api

import (
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// The fields that tie a photograph to the point it was taken at.
//
// They were absent from this end of the multipart upload for the whole of the
// walk's life, and nothing failed: the claim was filed, the four clips were
// scored, the map was drawn, and the photographs sat in the store belonging to
// no point. A silent loss is the only kind worth a test here.

func form(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestAPhotographsPointIsRead(t *testing.T) {
	var in survey.New
	readStationFields(form(map[string]string{
		"station_no":       "3",
		"station_offset_m": "4.5",
		"is_mocked":        "false",
	}), &in)

	if in.StationNo == nil || *in.StationNo != 3 {
		t.Fatalf("the station number was not read: %v", in.StationNo)
	}
	if in.StationOffsetM == nil || *in.StationOffsetM != 4.5 {
		t.Errorf("the offset was not read: %v", in.StationOffsetM)
	}
	if in.IsMocked == nil || *in.IsMocked {
		t.Errorf("a genuine fix was not recorded as genuine: %v", in.IsMocked)
	}
}

func TestAPhotographOutsideTheWalkCarriesNoPoint(t *testing.T) {
	// A damage closeup the farmer took wherever they liked. Filing it against
	// station 0 would put it on the map of the walk as though the office had
	// sent them there.
	var in survey.New
	readStationFields(form(nil), &in)

	if in.StationNo != nil {
		t.Errorf("a photograph with no station was given one: %v", *in.StationNo)
	}
	if in.StationOffsetM != nil {
		t.Errorf("an offset was invented: %v", *in.StationOffsetM)
	}
	// The gate distinguishes "the handset said the fix was genuine" from "the
	// handset did not say", and an absent field must stay the second.
	if in.IsMocked != nil {
		t.Errorf("a handset that said nothing was recorded as saying %v",
			*in.IsMocked)
	}
}

func TestAStationNumberThatIsNotANumberIsIgnored(t *testing.T) {
	var in survey.New
	readStationFields(form(map[string]string{
		"station_no": "0",
		"is_mocked":  "yes please",
	}), &in)

	if in.StationNo != nil {
		t.Errorf("station 0 was accepted as a point: %v", *in.StationNo)
	}
	if in.IsMocked != nil {
		t.Errorf("an unreadable value became a fact: %v", *in.IsMocked)
	}
}
