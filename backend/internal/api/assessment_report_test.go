package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The farmer's copy of the assessment, once the claim is decided -- and not a
// moment before.
//
// The officers examine a claim blind to each other, and the farmer is shown a
// loss figure only once both village officers have recorded. The copy has to
// hold to that, and so does every other way a farmer might reach an
// assessment.
func TestTheFarmerGetsTheAssessmentReportOnceTheClaimIsDecided(t *testing.T) {
	ts, pool, _, reportID, token := e2eServer(t)
	ctx := context.Background()

	var assessmentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO assessments (
			farmer_id, farm_id, survey_number, village_lgd_code,
			sowing_date, incident_date, owner_extent_hectares,
			crop_name, assessment, satellite, terrain,
			triage_tier, crop_presence, damage_percent, crop_loss_report_id
		) VALUES (
			'F-E2E-1', 'E2E-FARM-1', '453', 'E2E-V1',
			'2026-06-20', current_date - 5, 1.2,
			'Soybean', '{"damage_percent": 71, "reasoning": "hail"}'::jsonb,
			'{}'::jsonb, '{}'::jsonb,
			'GREEN_ROUTINE', 'confirmed', 71, $1
		) RETURNING id`, reportID).Scan(&assessmentID); err != nil {
		t.Fatalf("seeding the assessment: %v", err)
	}

	get := func(path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}

	copyPath := "/api/v1/reports/" + reportID + "/assessment-report"

	// Undecided, but assessed: the farmer reads it.
	//
	// They see what each instrument found about their own field before the
	// officers decide, which is when a correction is still cheap. What they do
	// not see is a decided figure -- the document is stamped provisional and
	// dossier.Build refuses to print one on a provisional page.
	code, body := get(copyPath)
	if code != http.StatusOK {
		t.Fatalf("an assessed claim was withheld from its own farmer: %d %s",
			code, body)
	}
	if !strings.Contains(body, "PROVISIONAL") {
		t.Error("an undecided copy is not stamped provisional, so a farmer " +
			"could read it as the decision")
	}

	// The assessment routes themselves stay shut: a farmer reads their claim's
	// report, not the assessment store.
	for _, path := range []string{
		"/api/v1/assessments",
		"/api/v1/assessments/" + assessmentID,
		"/api/v1/assessments/" + assessmentID + "/dossier",
	} {
		if code, _ := get(path); code != http.StatusForbidden {
			t.Errorf("%s answered a farmer with %d; the computer's figure "+
				"would reach them before the officers have recorded", path, code)
		}
	}

	// Decided.
	if _, err := pool.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET state = 'adjudicated', adjudicated_percent = 40,
		       adjudicated_area_ha = 1.0, adjudicated_at = now()
		 WHERE id = $1`, reportID); err != nil {
		t.Fatalf("deciding the claim: %v", err)
	}

	code, body = get(copyPath)
	if code != http.StatusOK {
		t.Fatalf("decided claim: %d %s", code, body)
	}
	if !strings.Contains(body, "<html") {
		t.Fatal("the copy is not a document")
	}
	if !strings.Contains(body, "CLR-E2E-1") {
		t.Error("the copy does not carry the claim number the farmer tracks it by")
	}
	// The decided figure is what the final document stands on, not the
	// computer's 71.
	if !strings.Contains(body, `<p class="figure big">40%</p>`) {
		t.Error("the decided figure is not the figure the farmer's copy stands on")
	}
	if strings.Contains(body, `<p class="figure big">71%</p>`) {
		t.Error("the copy prints the computer's estimate as the decided loss")
	}
}

func TestAnotherFarmersClaimHasNoAssessmentReport(t *testing.T) {
	ts, pool, _, reportID, token := e2eServer(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		UPDATE crop_loss_reports SET farmer_id = 'F-E2E-OTHER', state = 'closed'
		 WHERE id = $1`, reportID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM crop_loss_reports WHERE farmer_id = 'F-E2E-OTHER'`)
	})

	req, _ := http.NewRequest(http.MethodGet,
		ts.URL+"/api/v1/reports/"+reportID+"/assessment-report", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("another farmer's decided claim answered %d, want 404", res.StatusCode)
	}
}

func TestADecidedClaimWithNoAssessmentStillHasItsReport(t *testing.T) {
	// Claims decided before assessments ran automatically have none. The
	// document is still the record of the decision, and says in its own
	// section that no computer assessment was run rather than being refused.
	ts, pool, _, reportID, token := e2eServer(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET state = 'adjudicated', adjudicated_percent = 55,
		       adjudicated_area_ha = 1.0, adjudicated_at = now()
		 WHERE id = $1`, reportID); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet,
		ts.URL+"/api/v1/reports/"+reportID+"/assessment-report", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	page := string(body)
	if !strings.Contains(page, `<p class="figure big">55%</p>`) {
		t.Error("the decided figure is missing")
	}
	if !strings.Contains(page, "No computer assessment was run on this claim") {
		t.Error("the report does not say that no computer assessment was run")
	}
}
