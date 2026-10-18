package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/blob"
	"github.com/agribid/esurvey-platform/backend/internal/idempotency"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/store"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// A station clip, over real HTTP, through the middleware a handset meets.
//
// The store-level tests prove assembly and resume; this one proves the same
// path with routing, the token, the app-version gate and the error mapping in
// between -- which is where "the app treats a 404 as a refusal" lived
// (docs/SYNC_SCALE_REVIEW.md, H2).

func e2eServer(t *testing.T) (*httptest.Server, *pgxpool.Pool, *survey.Store, string, string) {
	t.Helper()
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the upload end-to-end test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)

	clean := func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM capture_uploads WHERE report_id IN
			(SELECT id FROM crop_loss_reports WHERE farmer_id = 'F-E2E-1')`)
		_, _ = pool.Exec(c, `DELETE FROM survey_captures WHERE report_id IN
			(SELECT id FROM crop_loss_reports WHERE farmer_id = 'F-E2E-1')`)
		_, _ = pool.Exec(c, `DELETE FROM assessments WHERE farmer_id = 'F-E2E-1'`)
		_, _ = pool.Exec(c, `DELETE FROM crop_loss_reports WHERE farmer_id = 'F-E2E-1'`)
		_, _ = pool.Exec(c, `DELETE FROM survey_events WHERE reference = 'SE-E2E-1'`)
		// The two accounts are left in place: the audit trail refers to them,
		// and deleting an account an audit entry names is refused -- rightly.
		// They are reused rather than recreated.
		_, _ = pool.Exec(c, `DELETE FROM jurisdictions WHERE code LIKE 'E2E-%'`)
		_, _ = pool.Exec(c, `DELETE FROM report_reference_counters WHERE district_lgd_code = 'E2E-D1'`)
	}
	clean()
	t.Cleanup(clean)

	// A place, an open event, and one filed report to attach evidence to.
	ids := map[string]string{}
	for _, j := range []struct{ level, code, name, parent string }{
		{"state", "E2E-S1", "E2E State", ""},
		{"district", "E2E-D1", "E2E District", "E2E-S1"},
		{"sub_district", "E2E-T1", "E2E Taluka", "E2E-D1"},
		{"village", "E2E-V1", "E2E Village", "E2E-T1"},
	} {
		var parent any
		if j.parent != "" {
			parent = ids[j.parent]
		}
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO jurisdictions (level, code, name, parent_id)
			VALUES ($1::jurisdiction_level, $2, $3, $4::uuid) RETURNING id`,
			j.level, j.code, j.name, parent).Scan(&id); err != nil {
			t.Fatalf("seeding %s: %v", j.code, err)
		}
		ids[j.code] = id
	}

	// An open event must name who declared it.
	var officerID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, full_name, kind, status, password_hash)
		VALUES ('e2e.officer@esurvey-test.in', 'e2e.officer@esurvey-test.in', 'E2E Officer', 'officer', 'active',
		        '$argon2id$v=19$m=65536,t=3,p=4$c2VlZHNlZWRzZWVk$notarealhash')
		ON CONFLICT (username) DO UPDATE SET full_name = EXCLUDED.full_name
		RETURNING id`).Scan(&officerID); err != nil {
		t.Fatalf("seeding the officer: %v", err)
	}

	var eventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO survey_events (reference, calamity_type, title, district_id,
			incident_start, incident_end, survey_closes_at, state,
			declared_by, declared_at)
		VALUES ('SE-E2E-1', 'hailstorm', 'E2E hailstorm', $1,
			current_date - 5, current_date - 4, now() + interval '30 days', 'open',
			$2, now())
		RETURNING id`, ids["E2E-D1"], officerID).Scan(&eventID); err != nil {
		t.Fatalf("seeding the event: %v", err)
	}

	var reportID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO crop_loss_reports (
			reference, survey_event_id, farmer_id, farmer_name,
			village_lgd_code, village_name, sub_district_lgd_code,
			sub_district_name, district_lgd_code, district_name,
			season, crop_year, loss_category, calamity_type, occurred_at,
			reported_channel, declaration_accepted, documents_promised,
			farmer_signature_kind, farmer_signed_at, state)
		VALUES ('CLR-E2E-1', $1, 'F-E2E-1', 'E2E Farmer',
			'E2E-V1', 'E2E Village', 'E2E-T1', 'E2E Taluka', 'E2E-D1', 'E2E District',
			'kharif', '2026-27', 'standing_crop', 'hailstorm', now() - interval '4 days',
			'farmer_app', true, '{}', 'thumb', now(), 'submitted')
		RETURNING id`, eventID).Scan(&reportID); err != nil {
		t.Fatalf("seeding the report: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO crop_loss_report_items (report_id, line_no, survey_number,
			farm_id, crop_name, area_sown_ha, area_affected_ha, damage_percent)
		VALUES ($1, 1, '453', 'E2E-FARM-1', 'Soybean', 1.2, 1.0, 60)`,
		reportID); err != nil {
		t.Fatalf("seeding the line: %v", err)
	}

	// The farmer's own account: captures record who sent them.
	var farmerUserID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (kind, status, full_name, mobile_e164, agristack_farmer_id)
		VALUES ('farmer', 'active', 'E2E Farmer', '+919000000001', 'F-E2E-1')
		ON CONFLICT (agristack_farmer_id) DO UPDATE SET full_name = EXCLUDED.full_name
		RETURNING id`).Scan(&farmerUserID); err != nil {
		t.Fatalf("seeding the farmer account: %v", err)
	}

	dir, err := blob.NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	seed, err := auth.GenerateSeed()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := auth.NewKeyring("esurvey", "esurvey-api", seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	captures := survey.NewStore(pool, dir)
	srv := &Server{
		DB:          pool,
		Log:         log,
		Reports:     report.NewStore(pool),
		Store:       store.New(pool),
		Captures:    captures,
		Keys:        keys,
		Audit:       audit.New(pool, log),
		Idempotency: idempotency.New(pool),
	}

	token, _, err := keys.Issue(&rbac.Principal{
		UserID:   farmerUserID,
		Kind:     rbac.KindFarmer,
		FarmerID: "F-E2E-1",
		Grants:   []rbac.Grant{{Role: rbac.RoleFarmer}},
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, pool, captures, reportID, token
}

func TestAClipSurvivesEverythingBetweenTheHandsetAndTheStore(t *testing.T) {
	ts, pool, captures, reportID, token := e2eServer(t)
	ctx := context.Background()

	clip := make([]byte, 5<<20)
	for i := range clip {
		clip[i] = byte(i % 251)
	}
	sum := sha256.Sum256(clip)
	digest := hex.EncodeToString(sum[:])
	const chunk = 2 << 20

	do := func(method, path string, body []byte, contentType string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-App-Version", "1.1.0")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		answer, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, answer
	}

	begin := func() map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"upload_key":   "e2e-station-1",
			"content_type": "video/mp4",
			"total_bytes":  len(clip),
			"chunk_size":   chunk,
			"sha256":       digest,
			"kind":         "station_video",
			"station_no":   1,
			"captured_at":  time.Now().Add(-time.Hour).Format(time.RFC3339),
			"lat":          18.17,
			"lon":          75.8131,
			"duration_ms":  15000,

			// What the handset's encoder agreed to. Sent here because the
			// handler parsed these three and then dropped them: the fields
			// existed on the request struct, the columns existed on the table,
			// and nothing joined the two, so every clip stored a null and the
			// office could not tell a large claim caused by the phone from one
			// caused by a longer walk.
			"encoding_profile":     "preferred",
			"encoding_fps":         15,
			"encoding_bitrate_bps": 2500000,
		})
		res, answer := do(http.MethodPost, "/api/v1/reports/"+reportID+"/uploads",
			body, "application/json")
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("begin: %d %s", res.StatusCode, answer)
		}
		var st map[string]any
		if err := json.Unmarshal(answer, &st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	send := func(uploadID string, index int) (*http.Response, []byte) {
		t.Helper()
		from := index * chunk
		to := min(from+chunk, len(clip))
		return do(http.MethodPut,
			fmt.Sprintf("/api/v1/reports/%s/uploads/%s/chunks/%d", reportID, uploadID, index),
			clip[from:to], "application/octet-stream")
	}

	st := begin()
	uploadID := st["id"].(string)

	// Two chunks arrive, then the farmer is out of signal for a fortnight.
	for i := 0; i < 2; i++ {
		if res, answer := send(uploadID, i); res.StatusCode != http.StatusOK {
			t.Fatalf("chunk %d: %d %s", i, res.StatusCode, answer)
		}
	}

	// Housekeeping runs. An upload still moving must survive it.
	if _, err := captures.SweepAbandonedUploads(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if res, answer := do(http.MethodGet,
		"/api/v1/reports/"+reportID+"/uploads/"+uploadID, nil, ""); res.StatusCode != http.StatusOK {
		t.Fatalf("an upload that moved today was swept: %d %s", res.StatusCode, answer)
	}

	// Now it really is abandoned: the sweeper runs a fortnight later.
	if _, err := pool.Exec(ctx,
		`UPDATE capture_uploads SET expires_at = now() - interval '1 day' WHERE id = $1`,
		uploadID); err != nil {
		t.Fatal(err)
	}
	if _, err := captures.SweepAbandonedUploads(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	// The handset, which knows nothing of any of this, sends its next chunk.
	res, answer := send(uploadID, 2)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("a chunk for a swept upload: %d %s", res.StatusCode, answer)
	}
	var refusal struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(answer, &refusal)
	if refusal.Error.Code != "UPLOAD_NOT_FOUND" {
		t.Fatalf("code %q; the app tells this apart from a refusal", refusal.Error.Code)
	}

	// So it begins again under the same key -- which is what the app does --
	// and finishes.
	st = begin()
	uploadID = st["id"].(string)
	if n := int(st["next_chunk"].(float64)); n != 0 {
		t.Fatalf("a fresh upload wants chunk %d, not 0", n)
	}
	var last []byte
	for i := 0; i*chunk < len(clip); i++ {
		res, answer = send(uploadID, i)
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
			t.Fatalf("chunk %d: %d %s", i, res.StatusCode, answer)
		}
		last = answer
	}

	var final struct {
		CaptureID string `json:"capture_id"`
	}
	if err := json.Unmarshal(last, &final); err != nil {
		t.Fatal(err)
	}
	if final.CaptureID == "" {
		t.Fatalf("the clip never became a capture: %s", last)
	}

	stored, err := captures.Get(ctx, final.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SHA256 != digest || stored.ByteSize != len(clip) {
		t.Fatalf("stored %d bytes with hash %s; sent %d with %s",
			stored.ByteSize, stored.SHA256, len(clip), digest)
	}

	// And what the phone said it recorded at arrived with it.
	var profile *string
	var fps, bitrate *int
	if err := pool.QueryRow(ctx,
		`SELECT encoding_profile, encoding_fps, encoding_bitrate_bps
		   FROM survey_captures WHERE id = $1`, final.CaptureID,
	).Scan(&profile, &fps, &bitrate); err != nil {
		t.Fatal(err)
	}
	if profile == nil || *profile != "preferred" {
		t.Fatalf("encoding profile %v; the handset sent \"preferred\"", profile)
	}
	if fps == nil || *fps != 15 {
		t.Fatalf("encoding fps %v; the handset sent 15", fps)
	}
	if bitrate == nil || *bitrate != 2500000 {
		t.Fatalf("encoding bitrate %v; the handset sent 2500000", bitrate)
	}
}
