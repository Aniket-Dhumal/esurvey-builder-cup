package satellite

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Radar is the platform's answer to its own blindness under monsoon cloud, so
// the thing most worth testing is what happens on a deployment where the
// engine does not have it: the assessment must proceed exactly as before.

func TestAnEngineWithNoRadarModuleIsNotAFailure(t *testing.T) {
	// A deployment where the endpoint does not exist should assess without
	// radar and say so. Reporting it as a failure would page somebody for a
	// feature that was never deployed.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no such module"}}`))
	})

	_, err := c.SAR(context.Background(), SARRequest{
		Geometry:  Geometry{Type: "Polygon", Coordinates: json.RawMessage(`[[[73.8,18.5]]]`)},
		StartDate: "2026-06-01", EndDate: "2026-08-01",
	})
	if !errors.Is(err, ErrRadarUnavailable) {
		t.Fatalf("error = %v, want ErrRadarUnavailable", err)
	}
}

func TestARadarSeriesComesBackWithItsOrbits(t *testing.T) {
	// Backscatter differs by several decibels between ascending and
	// descending passes over an unchanged field. A series that dropped the
	// orbit would let the analysis compare across them silently.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/submit") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"task_id":"t-1","status":"PROCESSING"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"COMPLETED","result":{
			"time_series":[
				{"date":"2026-07-01","vv_db":-8.5,"vh_db":-14.5,"orbit":"ASCENDING"},
				{"date":"2026-07-13","vv_db":-8.6,"vh_db":-14.6,"orbit":"ASCENDING"}
			],
			"missions":["SENTINEL-1A"]
		}}`))
	})

	got, err := c.SAR(context.Background(), SARRequest{
		Geometry:  Geometry{Type: "Polygon", Coordinates: json.RawMessage(`[[[73.8,18.5]]]`)},
		StartDate: "2026-06-01", EndDate: "2026-08-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TimeSeries) != 2 {
		t.Fatalf("passes = %d", len(got.TimeSeries))
	}
	if got.TimeSeries[0].Orbit != "ASCENDING" {
		t.Errorf("orbit = %q", got.TimeSeries[0].Orbit)
	}
	if got.TimeSeries[0].VVdB != -8.5 || got.TimeSeries[0].VHdB != -14.5 {
		t.Errorf("backscatter decoded as %+v", got.TimeSeries[0])
	}
	if len(got.Missions) == 0 {
		t.Error("no mission named; a figure should be traceable to a collection")
	}
}

func TestAWindowWithNoPassIsASuccessWithNothingToSay(t *testing.T) {
	// The same convention the NDVI module uses for a wholly clouded window.
	// A short window over a parcel may genuinely have had no pass.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/submit") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"task_id":"t-2","status":"PROCESSING"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"COMPLETED","result":{"time_series":[]}}`))
	})

	got, err := c.SAR(context.Background(), SARRequest{
		Geometry:  Geometry{Type: "Polygon", Coordinates: json.RawMessage(`[[[73.8,18.5]]]`)},
		StartDate: "2026-07-01", EndDate: "2026-07-05",
	})
	if err != nil {
		t.Fatalf("an empty window was reported as an error: %v", err)
	}
	if len(got.TimeSeries) != 0 {
		t.Errorf("passes = %d", len(got.TimeSeries))
	}
}

func TestAFailedRadarAnalysisCarriesTheEnginesCode(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/submit") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"task_id":"t-3","status":"PROCESSING"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"FAILED","error_code":"GEOMETRY_TOO_LARGE",
			"error":"the submitted boundary exceeds the area limit"}`))
	})

	_, err := c.SAR(context.Background(), SARRequest{
		Geometry:  Geometry{Type: "Polygon", Coordinates: json.RawMessage(`[[[73.8,18.5]]]`)},
		StartDate: "2026-06-01", EndDate: "2026-08-01",
	})
	if err == nil {
		t.Fatal("a failed analysis came back as success")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want *satellite.Error", err)
	}
	if apiErr.Code != "GEOMETRY_TOO_LARGE" {
		t.Errorf("code = %q; callers branch on the code, not on message text",
			apiErr.Code)
	}
}

func TestACompletedAnalysisWithNoResultIsAnError(t *testing.T) {
	// Returning an empty series here would record "no passes" for a parcel
	// the engine actually analysed.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/submit") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"task_id":"t-4","status":"PROCESSING"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
	})

	if _, err := c.SAR(context.Background(), SARRequest{
		Geometry:  Geometry{Type: "Polygon", Coordinates: json.RawMessage(`[[[73.8,18.5]]]`)},
		StartDate: "2026-06-01", EndDate: "2026-08-01",
	}); err == nil {
		t.Fatal("a completed analysis with no result was accepted")
	}
}
