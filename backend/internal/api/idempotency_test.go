package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/idempotency"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Database-backed: the guarantee is an INSERT only one request can win, and
// that is invisible above Postgres.

func idemServer(t *testing.T) (*Server, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the idempotency tests")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)
	return &Server{
		Idempotency: idempotency.New(pool),
		DB:          pool,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, pool
}

// A principal per test, so tests never share keys.
func principalFor(t *testing.T) *rbac.Principal {
	t.Helper()
	return &rbac.Principal{UserID: uuidish(t.Name())}
}

// uuidish turns a test name into a stable, valid uuid.
func uuidish(name string) string {
	h := uint64(1469598103934665603)
	for i := 0; i < len(name); i++ {
		h ^= uint64(name[i])
		h *= 1099511628211
	}
	hex := func(v uint64, n int) string {
		const digits = "0123456789abcdef"
		out := make([]byte, n)
		for i := n - 1; i >= 0; i-- {
			out[i] = digits[v&0xf]
			v >>= 4
		}
		return string(out)
	}
	return hex(h>>32, 8) + "-" + hex(h>>16, 4) + "-4" + hex(h>>4, 3) + "-8" + hex(h, 3) + "-" + hex(h*31, 12)
}

func request(p *rbac.Principal, key string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set(headerIdempotencyKey, key)
	}
	return r.WithContext(rbac.WithPrincipal(r.Context(), p))
}

func cleanKeys(t *testing.T, pool *pgxpool.Pool, principal string) {
	t.Helper()
	_, _ = pool.Exec(context.Background(), `DELETE FROM idempotency_keys WHERE principal_id = $1`, principal)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM idempotency_keys WHERE principal_id = $1`, principal)
	})
}

func TestARetryReplaysTheFirstAnswerWithoutExecutingAgain(t *testing.T) {
	s, pool := idemServer(t)
	p := principalFor(t)
	cleanKeys(t, pool, p.UserID)

	var runs atomic.Int32
	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		n := runs.Add(1)
		writeJSON(w, http.StatusCreated, map[string]any{"id": "report-1", "run": n})
	})

	first := httptest.NewRecorder()
	h(first, request(p, "draft-local-0001"))
	second := httptest.NewRecorder()
	h(second, request(p, "draft-local-0001"))

	if runs.Load() != 1 {
		t.Fatalf("the handler ran %d times for one key", runs.Load())
	}
	if second.Code != http.StatusCreated || second.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d %q, want %d %q", second.Code, second.Body.String(),
			first.Code, first.Body.String())
	}
	if second.Header().Get(headerReplayed) != "true" {
		t.Fatal("the replay was not marked as one")
	}
	if first.Header().Get(headerReplayed) != "" {
		t.Fatal("the first answer was marked as a replay")
	}
}

// A 5xx is about the moment. A retry must execute, not be handed the failure.
func TestAServerFailureIsNotReplayed(t *testing.T) {
	s, pool := idemServer(t)
	p := principalFor(t)
	cleanKeys(t, pool, p.UserID)

	var runs atomic.Int32
	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) == 1 {
			writeError(w, http.StatusServiceUnavailable, "BUSY", "")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
	})

	h(httptest.NewRecorder(), request(p, "draft-local-0002"))
	retry := httptest.NewRecorder()
	h(retry, request(p, "draft-local-0002"))

	if runs.Load() != 2 || retry.Code != http.StatusCreated {
		t.Fatalf("runs = %d, retry = %d; a 503 must not become the permanent answer",
			runs.Load(), retry.Code)
	}
}

// The foreground timer, the background worker, and a proxy retry, all at once.
func TestConcurrentCopiesOfOneRetryExecuteOnce(t *testing.T) {
	s, pool := idemServer(t)
	p := principalFor(t)
	cleanKeys(t, pool, p.UserID)

	var runs atomic.Int32
	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		runs.Add(1)
		time.Sleep(150 * time.Millisecond) // a write that takes a moment
		writeJSON(w, http.StatusCreated, map[string]any{"id": "report-1"})
	})

	const copies = 20
	codes := make([]int, copies)
	var wg sync.WaitGroup
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h(rec, request(p, "draft-local-0003"))
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	if runs.Load() != 1 {
		t.Fatalf("%d concurrent copies executed the write %d times", copies, runs.Load())
	}
	for _, c := range codes {
		if c != http.StatusCreated && c != http.StatusConflict {
			t.Fatalf("unexpected status %d among %v", c, codes)
		}
	}

	// And once the first has finished, every later copy gets its answer.
	after := httptest.NewRecorder()
	h(after, request(p, "draft-local-0003"))
	if after.Code != http.StatusCreated || after.Header().Get(headerReplayed) != "true" {
		t.Fatalf("after the race: %d replayed=%q", after.Code, after.Header().Get(headerReplayed))
	}
}

func TestAKeyIsScopedToTheAccountThatChoseIt(t *testing.T) {
	s, pool := idemServer(t)
	a := &rbac.Principal{UserID: uuidish(t.Name() + "-a")}
	b := &rbac.Principal{UserID: uuidish(t.Name() + "-b")}
	cleanKeys(t, pool, a.UserID)
	cleanKeys(t, pool, b.UserID)

	var runs atomic.Int32
	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		runs.Add(1)
		writeJSON(w, http.StatusCreated, map[string]any{"owner": rbac.FromContext(r.Context()).UserID})
	})
	h(httptest.NewRecorder(), request(a, "same-key-0001"))
	other := httptest.NewRecorder()
	h(other, request(b, "same-key-0001"))

	if runs.Load() != 2 {
		t.Fatal("one account's key answered another account's request")
	}
	if !strings.Contains(other.Body.String(), b.UserID) {
		t.Fatalf("account b was handed account a's answer: %s", other.Body.String())
	}
}

func TestAKeyReusedForADifferentOperationIsRefused(t *testing.T) {
	s, pool := idemServer(t)
	p := principalFor(t)
	cleanKeys(t, pool, p.UserID)

	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]any{})
	})
	h(httptest.NewRecorder(), request(p, "draft-local-0004"))

	other := httptest.NewRequest(http.MethodPost, "/api/v1/grievances", strings.NewReader(`{}`))
	other.Header.Set(headerIdempotencyKey, "draft-local-0004")
	other = other.WithContext(rbac.WithPrincipal(other.Context(), p))
	rec := httptest.NewRecorder()
	h(rec, other)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for a key reused on another route", rec.Code)
	}
}

func TestNoKeyBehavesExactlyAsBefore(t *testing.T) {
	s, _ := idemServer(t)
	p := principalFor(t)
	var runs atomic.Int32
	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		runs.Add(1)
		w.WriteHeader(http.StatusCreated)
	})
	h(httptest.NewRecorder(), request(p, ""))
	h(httptest.NewRecorder(), request(p, ""))
	if runs.Load() != 2 {
		t.Fatal("a request without a key was deduplicated; old app builds would break")
	}
}

// A process that died mid-request leaves a claim that never completes. It must
// not block the retry for ever.
func TestAStaleClaimIsTakenOver(t *testing.T) {
	s, pool := idemServer(t)
	p := principalFor(t)
	cleanKeys(t, pool, p.UserID)

	if _, err := pool.Exec(context.Background(), `
		INSERT INTO idempotency_keys (principal_id, key, method, route, created_at)
		VALUES ($1, 'draft-local-0005', 'POST', 'POST /api/v1/reports', now() - interval '10 minutes')`,
		p.UserID); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int32
	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		runs.Add(1)
		writeJSON(w, http.StatusCreated, map[string]any{})
	})
	rec := httptest.NewRecorder()
	h(rec, request(p, "draft-local-0005"))
	if runs.Load() != 1 || rec.Code != http.StatusCreated {
		t.Fatalf("a stale claim blocked the retry: runs=%d status=%d", runs.Load(), rec.Code)
	}
}

func TestAMalformedKeyIsRefused(t *testing.T) {
	s, _ := idemServer(t)
	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a malformed key reached the handler")
	})
	rec := httptest.NewRecorder()
	h(rec, request(principalFor(t), "bad key with spaces"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSweepRemovesOnlyOldKeys(t *testing.T) {
	s, pool := idemServer(t)
	p := principalFor(t)
	cleanKeys(t, pool, p.UserID)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO idempotency_keys (principal_id, key, method, route, created_at)
		VALUES ($1, 'old-key-00001', 'POST', 'x', now() - interval '40 days'),
		       ($1, 'new-key-00001', 'POST', 'x', now())`, p.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Idempotency.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var keys []string
	rows, _ := pool.Query(ctx, `SELECT key FROM idempotency_keys WHERE principal_id = $1`, p.UserID)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) != 1 || keys[0] != "new-key-00001" {
		t.Fatalf("after sweep: %v", keys)
	}
}

// A refusal is not the permanent answer: the refused operation wrote nothing,
// and once the cause is fixed its retry has to reach the fixed code.
func TestARefusalIsNotReplayed(t *testing.T) {
	s, pool := idemServer(t)
	p := principalFor(t)
	cleanKeys(t, pool, p.UserID)

	var runs atomic.Int32
	h := s.idempotent(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) == 1 {
			writeError(w, http.StatusBadRequest, "INVALID_REPORT", "refused by a fault since fixed")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
	})

	h(httptest.NewRecorder(), request(p, "draft-local-0400"))
	retry := httptest.NewRecorder()
	h(retry, request(p, "draft-local-0400"))

	if runs.Load() != 2 || retry.Code != http.StatusCreated {
		t.Fatalf("runs = %d, retry = %d; a 400 must not become the permanent answer",
			runs.Load(), retry.Code)
	}
	if retry.Header().Get(headerReplayed) != "" {
		t.Fatal("the retry was answered from the record")
	}
}
