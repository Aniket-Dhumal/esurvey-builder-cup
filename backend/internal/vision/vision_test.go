package vision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// The guard between an unvalidated measurement and a compensation decision.
//
// The only vision implementation that exists is a classical baseline: colour
// thresholds and a gradient orientation, not the trained segmentation the
// specification asks for. The specification hands the ground signal half a
// compensation decision in clear weather and nearly two thirds under cloud, so
// letting an unvalidated estimate through carries real money on it.
//
// This file is about the refusal holding, and about the ordinary absences --
// no model, no video, unusable footage -- staying ordinary rather than turning
// into failures that stop an assessment.

// --- doubles ----------------------------------------------------------------

type fakeCaptures struct {
	list []survey.Capture
	body map[string]string
	err  error
}

func (f *fakeCaptures) ForReport(_ context.Context, _ string) ([]survey.Capture, error) {
	return f.list, f.err
}

func (f *fakeCaptures) Media(_ context.Context, id string) (io.ReadCloser, string, error) {
	b, ok := f.body[id]
	if !ok {
		return nil, "", errors.New("no such capture")
	}
	return io.NopCloser(strings.NewReader(b)), "video/mp4", nil
}

func at(min int) time.Time {
	return time.Date(2026, 9, 1, 10, min, 0, 0, time.UTC)
}

func video(id string, minute int) survey.Capture {
	return survey.Capture{
		ID: id, ContentType: "video/mp4",
		Kind: survey.KindStationVideo, CapturedAt: at(minute),
	}
}

// atStation is a clip taken at one of the directed points.
func atStation(id string, minute, station int) survey.Capture {
	c := video(id, minute)
	c.StationNo = &station
	return c
}

func still(id string, minute int) survey.Capture {
	return survey.Capture{ID: id, ContentType: "image/jpeg", CapturedAt: at(minute)}
}

// answer builds a service that returns one measurement.
func answer(t *testing.T, baseline bool, seen *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Clone()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stem_angle_degrees": 62.5,
			"classes": map[string]any{
				"healthy_foliage": 5000, "submerged": 3000,
				"necrotic": 2000, "background": 4000,
			},
			"quality": 0.52, "keyframes": 18, "baseline": baseline,
		})
	}))
}

func withCaptures() *fakeCaptures {
	return &fakeCaptures{
		list: []survey.Capture{video("clip-1", 5)},
		body: map[string]string{"clip-1": "pretend this is an mp4"},
	}
}

// --- the refusal ------------------------------------------------------------

func TestAnUnvalidatedMeasurementIsRefusedByDefault(t *testing.T) {
	srv := answer(t, true, nil)
	defer srv.Close()

	c := New(srv.URL, "", withCaptures(), false)

	m, err := c.Measure(context.Background(), "report-1", "Soybean")
	if !errors.Is(err, ErrBaselineRefused) {
		t.Fatalf("err = %v, want ErrBaselineRefused", err)
	}
	if len(m) != 0 {
		t.Error("a refused measurement was returned anyway")
	}
}

func TestAnUnvalidatedMeasurementIsUsableWhenExplicitlyPermitted(t *testing.T) {
	srv := answer(t, true, nil)
	defer srv.Close()

	c := New(srv.URL, "", withCaptures(), true)

	m, err := c.Measure(context.Background(), "report-1", "Soybean")
	if err != nil {
		t.Fatalf("permitted and still refused: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("%d measurements; the fixture has one clip", len(m))
	}
	one := m[0].Measurements

	// The whole contract, decoded field for field. A silent zero here would
	// read downstream as an undamaged field.
	if one.StemAngleDegrees != 62.5 {
		t.Errorf("stem angle = %v, want 62.5", one.StemAngleDegrees)
	}
	if one.Classes.Submerged != 3000 || one.Classes.Necrotic != 2000 {
		t.Errorf("classes did not survive decoding: %+v", one.Classes)
	}
	if one.Quality != 0.52 || one.Keyframes != 18 {
		t.Errorf("quality/keyframes = %v/%v, want 0.52/18", one.Quality, one.Keyframes)
	}
}

func TestATrainedModelIsAcceptedWithoutThePermission(t *testing.T) {
	// The permission is about unvalidated baselines, not about vision as such.
	// A model that does not label itself a baseline must work in production
	// without anyone setting a development flag.
	srv := answer(t, false, nil)
	defer srv.Close()

	c := New(srv.URL, "", withCaptures(), false)

	if _, err := c.Measure(context.Background(), "report-1", "Soybean"); err != nil {
		t.Fatalf("a trained model's measurement was refused: %v", err)
	}
}

// --- the ordinary absences --------------------------------------------------

func TestNoModuleConfiguredIsUnavailableNotAnError(t *testing.T) {
	c := New("", "", withCaptures(), false)

	if _, err := c.Measure(context.Background(), "report-1", "Soybean"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}

	// And a nil client, which is what a deployment without one holds.
	var nilClient *Client
	if _, err := nilClient.Measure(context.Background(), "report-1", "Soybean"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a nil client returned %v, want ErrUnavailable", err)
	}
}

func TestAServiceWithNoModelAnswersUnavailable(t *testing.T) {
	// The load-bearing 404, the same convention the radar module uses.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, "", withCaptures(), true)
	if _, err := c.Measure(context.Background(), "report-1", "Soybean"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestAReportWithNoVideoIsNotAFailure(t *testing.T) {
	// Most reports have photographs and no clip. Nil measurements with a nil
	// error, so the assembler records it absent rather than as a fault.
	srv := answer(t, true, nil)
	defer srv.Close()

	caps := &fakeCaptures{list: []survey.Capture{still("photo-1", 4)}}
	c := New(srv.URL, "", caps, true)

	m, err := c.Measure(context.Background(), "report-1", "Soybean")
	if err != nil {
		t.Fatalf("a report with no video reported an error: %v", err)
	}
	if len(m) != 0 {
		t.Error("a measurement was invented for a report with no video")
	}
}

func TestUnusableFootageIsExplainedNotSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	c := New(srv.URL, "", withCaptures(), true)

	_, err := c.Measure(context.Background(), "report-1", "Soybean")
	if err == nil {
		t.Fatal("unusable footage produced no error")
	}
	// The officer reads this on the dossier, so it has to say what happened.
	if !strings.Contains(err.Error(), "could not be measured") {
		t.Errorf("err = %q, which does not explain itself", err)
	}
}

// --- choosing the clip ------------------------------------------------------

func TestEveryDirectedPointIsMeasured(t *testing.T) {
	// The walk sends a farmer to four points because corners differ. Measuring
	// one of the four -- which this did -- threw away the difference the walk
	// was made to capture, while the farmer still paid to upload all four
	// (docs/EVIDENCE_REVALIDATION.md, V1).
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stem_angle_degrees": 10.0,
			"classes":            map[string]any{"healthy_foliage": 9000},
			"quality":            0.5, "keyframes": 12, "baseline": false,
		})
	}))
	defer srv.Close()

	caps := &fakeCaptures{
		list: []survey.Capture{
			atStation("s2", 2, 2), still("photo", 9),
			atStation("s4", 4, 4), atStation("s1", 1, 1), atStation("s3", 3, 3),
		},
		body: map[string]string{
			"s1": "STATION-ONE", "s2": "STATION-TWO",
			"s3": "STATION-THREE", "s4": "STATION-FOUR",
		},
	}
	c := New(srv.URL, "", caps, false)

	clips, err := c.Measure(context.Background(), "report-1", "Soybean")
	if err != nil {
		t.Fatal(err)
	}
	if len(clips) != 4 {
		t.Fatalf("%d points measured; the walk was four", len(clips))
	}
	// In walk order, so the record reads the way the farmer walked.
	for i, want := range []int{1, 2, 3, 4} {
		if clips[i].StationNo != want {
			t.Errorf("clip %d is station %d, want %d", i, clips[i].StationNo, want)
		}
	}
	for _, want := range []string{"STATION-ONE", "STATION-TWO", "STATION-THREE", "STATION-FOUR"} {
		found := false
		for _, body := range sent {
			if strings.Contains(body, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was never sent to the vision service", want)
		}
	}
}

func TestAPointRecordedTwiceUsesTheSecondTake(t *testing.T) {
	// A farmer who redid a station meant the second one.
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stem_angle_degrees": 10.0,
			"classes":            map[string]any{"healthy_foliage": 9000},
			"quality":            0.5, "keyframes": 12, "baseline": false,
		})
	}))
	defer srv.Close()

	caps := &fakeCaptures{
		list: []survey.Capture{atStation("first", 1, 2), atStation("second", 6, 2)},
		body: map[string]string{"first": "FIRST-TAKE", "second": "SECOND-TAKE"},
	}
	c := New(srv.URL, "", caps, false)

	clips, err := c.Measure(context.Background(), "report-1", "Soybean")
	if err != nil {
		t.Fatal(err)
	}
	if len(clips) != 1 {
		t.Fatalf("%d clips for one station", len(clips))
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "SECOND-TAKE") {
		t.Errorf("sent %v; want the second take alone", sent)
	}
}

func TestOneUnreadableClipDoesNotCostTheOthers(t *testing.T) {
	srv := answer(t, false, nil)
	defer srv.Close()

	caps := &fakeCaptures{
		list: []survey.Capture{atStation("good", 1, 1), atStation("gone", 2, 2)},
		// "gone" has no body: the store cannot read it.
		body: map[string]string{"good": "GOOD-BYTES"},
	}
	c := New(srv.URL, "", caps, false)

	clips, err := c.Measure(context.Background(), "report-1", "Soybean")
	if err != nil {
		t.Fatalf("one unreadable clip failed the whole walk: %v", err)
	}
	if len(clips) != 1 || clips[0].StationNo != 1 {
		t.Fatalf("clips = %+v; want the readable station alone", clips)
	}
}

func TestAStoreFailureIsReportedNotIgnored(t *testing.T) {
	srv := answer(t, false, nil)
	defer srv.Close()

	caps := &fakeCaptures{err: errors.New("the database is unreachable")}
	c := New(srv.URL, "", caps, true)

	if _, err := c.Measure(context.Background(), "report-1", "Soybean"); err == nil {
		t.Fatal("a store failure was swallowed and read as no video")
	}
}

// --- the caller token -------------------------------------------------------

func TestTheCallerTokenIsSent(t *testing.T) {
	// The endpoint decodes attacker-controlled video with a native library, so
	// the token is what keeps it from being reachable by anything else.
	var seen http.Header
	srv := answer(t, false, &seen)
	defer srv.Close()

	c := New(srv.URL, "the-shared-secret", withCaptures(), false)
	if _, err := c.Measure(context.Background(), "report-1", "Soybean"); err != nil {
		t.Fatal(err)
	}

	if got := seen.Get("X-API-Key"); got != "the-shared-secret" {
		t.Errorf("X-API-Key = %q, want the configured token", got)
	}
	if ct := seen.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart/form-data", ct)
	}
}

// --- what the service is told about the footage ------------------------------

func TestTheServiceIsToldTheCropAndTheStation(t *testing.T) {
	// Both change what the same frames mean. Necrosis in a ripening cereal
	// looks like a healthy harvest, and a reading from one corner is not a
	// reading of the field. Measured without them on the first live run, the
	// model said so itself: "The crop was not declared."
	var fields struct {
		crop    string
		station string
		clips   int
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Errorf("parsing the upload: %v", err)
		}
		fields.crop = r.FormValue("crop_name")
		fields.station = r.FormValue("station_no")
		fields.clips++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stem_angle_degrees": 4.0,
			"classes": map[string]any{
				"healthy_foliage": 8000, "submerged": 0,
				"necrotic": 0, "background": 2000,
			},
			"quality": 0.8, "keyframes": 8, "baseline": false,
		})
	}))
	defer srv.Close()

	captures := &fakeCaptures{
		list: []survey.Capture{atStation("clip-3", 5, 3)},
		body: map[string]string{"clip-3": "pretend this is an mp4"},
	}
	c := New(srv.URL, "", captures, false)

	if _, err := c.Measure(context.Background(), "report-1", "Soybean"); err != nil {
		t.Fatalf("measuring: %v", err)
	}
	if fields.clips != 1 {
		t.Fatalf("%d clips posted, want 1", fields.clips)
	}
	if fields.crop != "Soybean" {
		t.Errorf("crop_name = %q; the model judged without knowing the crop", fields.crop)
	}
	if fields.station != "3" {
		t.Errorf("station_no = %q, want 3", fields.station)
	}
}

func TestAnUndeclaredCropIsOmittedRatherThanSentEmpty(t *testing.T) {
	// A parcel whose crop AgriStack does not carry is ordinary. The field is
	// left out so the service applies its own default rather than being told
	// the crop is the empty string.
	var sawCropField bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Errorf("parsing the upload: %v", err)
		}
		_, sawCropField = r.MultipartForm.Value["crop_name"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stem_angle_degrees": 4.0,
			"classes": map[string]any{
				"healthy_foliage": 8000, "submerged": 0,
				"necrotic": 0, "background": 2000,
			},
			"quality": 0.8, "keyframes": 8, "baseline": false,
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "", withCaptures(), false)
	if _, err := c.Measure(context.Background(), "report-1", ""); err != nil {
		t.Fatalf("measuring: %v", err)
	}
	if sawCropField {
		t.Error("an empty crop_name was sent; the field should be omitted")
	}
}

func TestTheCropCheckReachesTheAssessment(t *testing.T) {
	// The service's answer about the crop's identity, as the platform must
	// read it. Measured live on 18 September 2026: a soybean field declared
	// as sugarcane came back false at 0.95 with a botanical reason.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stem_angle_degrees": 0.0,
			"classes": map[string]any{
				"healthy_foliage": 40000, "submerged": 0,
				"necrotic": 0, "background": 40000,
			},
			"quality": 0.8, "keyframes": 8, "baseline": false,
			"crop_declared":            "Sugarcane",
			"crop_seen":                "soybean",
			"crop_matches":             false,
			"crop_mismatch_confidence": 0.95,
			"crop_mismatch_detail":     "broad trifoliate leaves, low bushy habit",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "", withCaptures(), false)
	clips, err := c.Measure(context.Background(), "report-1", "Sugarcane")
	if err != nil {
		t.Fatalf("measuring: %v", err)
	}
	if len(clips) != 1 {
		t.Fatalf("%d clips", len(clips))
	}
	got := clips[0].Crop
	if !got.Mismatched() {
		t.Fatal("a field of a different crop did not come back as a mismatch")
	}
	if got.Seen != "soybean" || got.Declared != "Sugarcane" || got.Confidence != 0.95 {
		t.Errorf("crop check = %+v", got)
	}
}

func TestAServiceThatSaysNothingAboutTheCropAccusesNobody(t *testing.T) {
	// The classical baseline cannot tell one crop from another and its answer
	// carries none of these fields. Absent must read as silence, never as
	// "the crop matches" and never as a mismatch.
	srv := answer(t, false, nil)
	defer srv.Close()

	c := New(srv.URL, "", withCaptures(), false)
	clips, err := c.Measure(context.Background(), "report-1", "Soybean")
	if err != nil {
		t.Fatalf("measuring: %v", err)
	}
	if clips[0].Crop.Matches != nil {
		t.Errorf("a service that said nothing produced a verdict: %+v", clips[0].Crop)
	}
	if clips[0].Crop.Mismatched() {
		t.Error("silence read as a mismatch")
	}
}
