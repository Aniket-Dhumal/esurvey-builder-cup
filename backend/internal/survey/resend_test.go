package survey

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A photograph sent again.
//
// The reply to an upload is lost on a village connection far more often than
// the upload itself fails. The handset cannot tell the two apart, so it sends
// again -- and the platform used to record a second copy and then mark it as
// one photograph submitted for two plots.

func photo(reportID, farmID, content string) New {
	in := aCapture(reportID, farmID)
	in.Media = strings.NewReader(content)
	return in
}

func captureCount(t *testing.T, pool *pgxpool.Pool, reportID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM survey_captures WHERE report_id = $1`, reportID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAResentPhotographIsTheSamePhotograph(t *testing.T) {
	_, pool := testStations(t)
	store := uploadStore(t, pool)
	placeBoundary(t, pool, "ST-40")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	first, err := store.Submit(ctx, photo(reportID, "ST-40", "the hail-flattened soybean, frame 1"))
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if first.Replayed {
		t.Fatal("the first send was reported as a replay")
	}

	again, err := store.Submit(ctx, photo(reportID, "ST-40", "the hail-flattened soybean, frame 1"))
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if !again.Replayed || again.ID != first.ID {
		t.Fatalf("the resend created capture %s (replayed=%v), want %s", again.ID, again.Replayed, first.ID)
	}
	if n := captureCount(t, pool, reportID); n != 1 {
		t.Fatalf("%d captures for one photograph sent twice", n)
	}

	// And the photograph that exists was not marked as reused.
	if slices.Contains(first.GatesFailed, GateDistinctImage) {
		t.Fatal("the original photograph failed the distinct-image gate")
	}
}

// The foreground timer and the background worker, both sending the same
// photograph at the same instant.
func TestConcurrentResendsRecordOneCapture(t *testing.T) {
	_, pool := testStations(t)
	store := uploadStore(t, pool)
	placeBoundary(t, pool, "ST-41")
	reportID := makeReport(t, pool)

	const copies = 12
	var wg sync.WaitGroup
	ids := make([]string, copies)
	errs := make([]error, copies)
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := store.Submit(context.Background(), photo(reportID, "ST-41", "one photograph, many sends"))
			errs[i] = err
			if c != nil {
				ids[i] = c.ID
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("concurrent sends produced different captures: %v", ids)
		}
	}
	if n := captureCount(t, pool, reportID); n != 1 {
		t.Fatalf("%d captures from %d concurrent sends of one photograph", n, copies)
	}
}

// The gate still does its job: the same bytes on a different report are one
// photograph submitted for two plots.
func TestTheSamePhotographOnAnotherReportStillFailsTheGate(t *testing.T) {
	_, pool := testStations(t)
	store := uploadStore(t, pool)
	placeBoundary(t, pool, "ST-42")
	reportID := makeReport(t, pool)
	ctx := context.Background()

	var otherID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO crop_loss_reports
			(reference, survey_event_id, farmer_id, farmer_name,
			 village_lgd_code, village_name, sub_district_lgd_code,
			 sub_district_name, district_lgd_code, district_name,
			 season, crop_year, loss_category, calamity_type, occurred_at)
		SELECT 'ST-RPT-OTHER', survey_event_id, farmer_id, farmer_name,
		       village_lgd_code, village_name, sub_district_lgd_code,
		       sub_district_name, district_lgd_code, district_name,
		       season, crop_year, loss_category, calamity_type, occurred_at
		  FROM crop_loss_reports WHERE id = $1
		RETURNING id`, reportID).Scan(&otherID); err != nil {
		t.Fatalf("creating a second report: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM crop_loss_reports WHERE reference = 'ST-RPT-OTHER'`)
	})

	if _, err := store.Submit(ctx, photo(reportID, "ST-42", "a photograph used twice")); err != nil {
		t.Fatal(err)
	}
	second, err := store.Submit(ctx, photo(otherID, "ST-42", "a photograph used twice"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Replayed {
		t.Fatal("a photograph on a different report was treated as a resend")
	}
	if !slices.Contains(second.GatesFailed, GateDistinctImage) {
		t.Fatal("the same bytes on a different report passed the distinct-image gate")
	}
}

// --- the upload whose assembly failed ------------------------------------------

// Every byte arrived and assembly did not succeed. Before, nothing would ever
// finish it: the handset had no chunk left to send.
func TestAStrandedUploadIsFinishedByTheNextContact(t *testing.T) {
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-43")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(300_000)
	const chunk = 64 << 10
	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-43"),
		"stranded-clip", int64(len(body)), chunk, sumOf(body))
	if err != nil {
		t.Fatal(err)
	}

	// Store every chunk and advance the row, but stop short of assembly: the
	// state a transient failure during finishUpload leaves behind.
	for i := 0; i*chunk < len(body); i++ {
		end := min((i+1)*chunk, len(body))
		if err := store.blobs.Put(ctx, partKey(st.ID, i), "application/octet-stream", body[i*chunk:end]); err != nil {
			t.Fatal(err)
		}
	}
	chunks := (len(body) + chunk - 1) / chunk
	if _, err := pool.Exec(ctx, `
		UPDATE capture_uploads SET received_bytes = total_bytes, next_chunk = $2
		 WHERE id = $1`, st.ID, chunks); err != nil {
		t.Fatal(err)
	}

	// The handset's "where did I get to?"
	resumed, err := store.ResumeUpload(ctx, reportID, st.ID)
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}
	if !resumed.Complete() {
		t.Fatal("an upload with every byte in was not finished when the handset asked")
	}

	// And beginning it again with the same key is also enough.
	again, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-43"),
		"stranded-clip", int64(len(body)), chunk, sumOf(body))
	if err != nil || !again.Complete() || again.CaptureID != resumed.CaptureID {
		t.Fatalf("a repeated begin did not return the finished capture: %+v, %v", again, err)
	}
}

// A corrupt assembly is restarted, not refused for ever under the same key.
func TestACorruptUploadRestartsCleanly(t *testing.T) {
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-44")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(200_000)
	const chunk = 64 << 10
	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-44"),
		"corrupt-clip", int64(len(body)), chunk, sumOf(body))
	if err != nil {
		t.Fatal(err)
	}

	bad := bytes.Clone(body)
	bad[100_000] ^= 0xFF
	var lastErr error
	for i := 0; i*chunk < len(bad); i++ {
		end := min((i+1)*chunk, len(bad))
		_, lastErr = store.AppendChunk(ctx, reportID, st.ID, i, bytes.NewReader(bad[i*chunk:end]))
	}
	if !errors.Is(lastErr, ErrUploadRestarted) {
		t.Fatalf("final chunk error = %v, want ErrUploadRestarted", lastErr)
	}

	cur, err := store.UploadState(ctx, reportID, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.NextChunk != 0 || cur.ReceivedBytes != 0 {
		t.Fatalf("the corrupt upload was not reset: %+v", cur)
	}

	// Sending the right bytes again under the same key now succeeds.
	final := send(t, store, reportID, st.ID, body, chunk)
	if !final.Complete() {
		t.Fatal("the restarted upload did not complete")
	}
}

// Assembly is bounded. With every slot taken, a finishing upload is told the
// server is busy rather than holding another fifty-megabyte buffer.
func TestAssemblyIsBoundedAndBusyIsRetryable(t *testing.T) {
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-45")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)

	for i := 0; i < AssemblyLimit; i++ {
		store.assembling <- struct{}{}
	}

	body := aClip(100_000)
	st, err := store.BeginUpload(context.Background(), videoCapture(reportID, "ST-45"),
		"busy-clip", int64(len(body)), len(body), sumOf(body))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = store.AppendChunk(ctx, reportID, st.ID, 0, bytes.NewReader(body))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("error = %v, want ErrBusy with every slot taken", err)
	}

	// Free the slots: the same upload finishes on the next contact, with the
	// chunk it already sent.
	for i := 0; i < AssemblyLimit; i++ {
		<-store.assembling
	}
	final, err := store.ResumeUpload(context.Background(), reportID, st.ID)
	if err != nil || !final.Complete() {
		t.Fatalf("the upload did not finish once a slot freed: %+v, %v", final, err)
	}
}
