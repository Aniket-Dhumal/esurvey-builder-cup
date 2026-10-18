package satellite

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The wire names, pinned from this side.
//
// The Python models in satellite-intelligence/src/esurvey_sar/moisture.py
// carry the same lists in test_moisture.py. A field renamed on one side fails
// quietly on the other -- an unmarshalled field is a zero, and a zero NDMI is
// a plausible value for a stressed crop -- so each side asserts the list, and
// changing one without the other fails a test instead of an assessment.
func TestMoistureWireNames(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want []string
	}{
		{MoistureRequest{}, []string{"baseline_years", "end_date", "farmer_id", "field_id", "geometry", "start_date"}},
		{MoistureResult{}, []string{"area_ha", "baseline_years", "clear_threshold", "missions", "optical", "parcel_pixels", "ring_cropland_pixels", "soil"}},
		{OpticalPass{}, []string{"clear_fraction", "clear_pixels", "date", "ndmi", "ndvi", "ring_clear_pixels", "ring_ndmi", "ring_ndvi", "season_offset"}},
		{SoilPass{}, []string{"coverage", "date", "orbit", "relative_orbit", "vv_db"}},
	} {
		typ := reflect.TypeOf(tc.v)
		var got []string
		for k := 0; k < typ.NumField(); k++ {
			name := strings.Split(typ.Field(k).Tag.Get("json"), ",")[0]
			got = append(got, name)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s wire names = %v, want %v", typ.Name(), got, tc.want)
		}
	}
}

func TestAServiceWithNoMoistureModuleIsNotAFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"MODULE_NOT_ENABLED","message":"off"}}`))
	})
	_, err := c.Moisture(context.Background(), MoistureRequest{
		Geometry:  Geometry{Type: "Polygon", Coordinates: json.RawMessage(`[[[75.8,18.1]]]`)},
		StartDate: "2026-07-05", EndDate: "2026-10-03", BaselineYears: 3,
	})
	if !errors.Is(err, ErrMoistureUnavailable) {
		t.Fatalf("error = %v, want ErrMoistureUnavailable", err)
	}
}

// A missing mean stays missing. A pass that saw the parcel and no cropland in
// the ring must not arrive with a ring NDMI of 0.0.
func TestAMissingMeanIsNotAZero(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/submit") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"task_id":"m-1","status":"PROCESSING"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"COMPLETED","result":{
			"optical":[{"date":"2026-08-08","season_offset":0,"ndmi":0.2395,
				"ndvi":0.3886,"clear_fraction":0.867,"clear_pixels":26,
				"ring_ndmi":null,"ring_ndvi":null,"ring_clear_pixels":0}],
			"soil":[{"date":"2026-08-15","vv_db":-10.094,"orbit":"DESCENDING",
				"relative_orbit":63,"coverage":1.0}],
			"parcel_pixels":30,"ring_cropland_pixels":18020,
			"baseline_years":3,"clear_threshold":0.6,
			"missions":["SENTINEL-2","SENTINEL-1","ESA-WORLDCOVER"],"area_ha":1.0579
		}}`))
	})
	got, err := c.Moisture(context.Background(), MoistureRequest{
		Geometry:  Geometry{Type: "Polygon", Coordinates: json.RawMessage(`[[[75.8,18.1]]]`)},
		StartDate: "2026-07-05", EndDate: "2026-10-03", BaselineYears: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := got.Optical[0]
	if p.NDMI == nil || *p.NDMI != 0.2395 {
		t.Fatalf("ndmi = %v", p.NDMI)
	}
	if p.RingNDMI != nil {
		t.Fatalf("a null ring mean arrived as %v", *p.RingNDMI)
	}
	if got.Soil[0].RelativeOrbit == nil || *got.Soil[0].RelativeOrbit != 63 {
		t.Fatalf("relative orbit = %v", got.Soil[0].RelativeOrbit)
	}
}

func TestAFailedMeasurementCarriesItsCode(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/submit") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"task_id":"m-2","status":"PROCESSING"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"FAILED","error":"quota","error_code":"EE_QUOTA"}`))
	})
	_, err := c.Moisture(context.Background(), MoistureRequest{
		Geometry:  Geometry{Type: "Polygon", Coordinates: json.RawMessage(`[[[75.8,18.1]]]`)},
		StartDate: "2026-07-05", EndDate: "2026-10-03",
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "EE_QUOTA" {
		t.Fatalf("error = %v, want an EE_QUOTA API error", err)
	}
}
