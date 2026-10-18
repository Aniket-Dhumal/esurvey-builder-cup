package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

func TestVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		have, want string
		below      bool
	}{
		{"1.0.0", "1.1.0", true},
		{"1.1.0", "1.1.0", false},
		{"1.2.0", "1.1.9", false},
		{"1.1.0+42", "1.1.0", false},
		{"1.0.9+100", "1.1.0", true},
		{"2", "1.9.9", false},
		{"v1.0.1", "1.0.2", true},
		// Unparseable is never below: a formatting slip must not lock a
		// farmer out of sending their claim.
		{"banana", "1.0.0", false},
		{"1.0.0", "banana", false},
	} {
		if got := versionBelow(tc.have, tc.want); got != tc.below {
			t.Errorf("versionBelow(%q, %q) = %v, want %v", tc.have, tc.want, got, tc.below)
		}
	}
}

func TestAnOldBuildIsToldToUpdateAndOnlyForWrites(t *testing.T) {
	s := &Server{MinAppVersion: "1.2.0"}
	var reached bool
	h := s.requireCurrentApp(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	try := func(method, path, version string) int {
		reached = false
		r := httptest.NewRequest(method, path, nil)
		if version != "" {
			r.Header.Set(headerAppVersion, version)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if reached {
			return 0
		}
		return rec.Code
	}

	if code := try(http.MethodPost, "/api/v1/reports", "1.1.0"); code != http.StatusUpgradeRequired {
		t.Fatalf("an old build filing a report got %d, want 426", code)
	}
	if code := try(http.MethodPost, "/api/v1/reports", "1.2.0"); code != 0 {
		t.Fatal("a current build was refused")
	}
	// Reads, sign-in and the status report stay open to an old build.
	if code := try(http.MethodGet, "/api/v1/reports", "1.1.0"); code != 0 {
		t.Fatal("an old build was refused a read")
	}
	if code := try(http.MethodPost, "/api/v1/auth/farmer/verify", "1.1.0"); code != 0 {
		t.Fatal("an old build was refused sign-in")
	}
	if code := try(http.MethodPost, "/api/v1/devices/sync-status", "1.1.0"); code != 0 {
		t.Fatal("an old build was refused its status report")
	}
	// No header at all from the console: a browser is not a farmer's build.
	if code := try(http.MethodPost, "/api/v1/reports", ""); code != 0 {
		t.Fatal("a request with no version header and no farmer was refused")
	}
}

// A farmer's build from before the header existed is older than any minimum.
// Letting it through meant builds already in the field could never be stopped.
func TestAFarmerBuildWithNoVersionIsOlderThanAnyMinimum(t *testing.T) {
	s := &Server{MinAppVersion: "1.2.0"}
	h := s.requireCurrentApp(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	send := func(kind rbac.Kind) int {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", nil)
		r = r.WithContext(rbac.WithPrincipal(r.Context(), &rbac.Principal{UserID: "u", Kind: kind}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if code := send(rbac.KindFarmer); code != http.StatusUpgradeRequired {
		t.Fatalf("a farmer's unversioned build filing got %d, want 426", code)
	}
	if code := send(rbac.KindOfficer); code == http.StatusUpgradeRequired {
		t.Fatal("an officer's browser was told to update an app it does not have")
	}
}

func TestADeviceReportIsUpsertedAndAnEmptiedQueueLandsAtOnce(t *testing.T) {
	s, pool := idemServer(t)
	p := &rbac.Principal{UserID: uuidish(t.Name())}
	device := "device-" + strings.ReplaceAll(uuidish(t.Name()), "-", "")[:16]
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM device_sync_status WHERE device_id = $1`, device)
	})

	send := func(body map[string]any) int {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/devices/sync-status", strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(rbac.WithPrincipal(r.Context(), p))
		rec := httptest.NewRecorder()
		s.reportSyncStatus(rec, r)
		return rec.Code
	}

	old := time.Now().Add(-30 * time.Hour)
	if code := send(map[string]any{
		"device_id": device, "app_version": "1.2.0", "platform": "android",
		"pending_reports": 1, "pending_captures": 4, "oldest_pending_at": old,
	}); code != http.StatusNoContent {
		t.Fatalf("status = %d", code)
	}

	// Seconds later the queue drains. Throttled or not, that must land.
	if code := send(map[string]any{
		"device_id": device, "app_version": "1.2.0", "platform": "android",
	}); code != http.StatusNoContent {
		t.Fatalf("status = %d", code)
	}

	var reports, captures int
	var oldest *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT pending_reports, pending_captures, oldest_pending_at
		  FROM device_sync_status WHERE device_id = $1`, device).Scan(&reports, &captures, &oldest); err != nil {
		t.Fatal(err)
	}
	if reports != 0 || captures != 0 || oldest != nil {
		t.Fatalf("an emptied queue did not land: reports=%d captures=%d oldest=%v", reports, captures, oldest)
	}

	// A clock a year in the future is a wrong clock, not a fresh queue.
	future := time.Now().Add(400 * 24 * time.Hour)
	send(map[string]any{
		"device_id": device, "app_version": "1.2.0", "platform": "android",
		"pending_reports": 2, "oldest_pending_at": future,
	})
	if err := pool.QueryRow(ctx, `SELECT oldest_pending_at FROM device_sync_status WHERE device_id = $1`,
		device).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != nil {
		t.Fatalf("a future timestamp was stored: %v", oldest)
	}
}
