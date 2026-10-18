package survey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/blob"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Resumable upload.
//
// The behaviour worth testing is not "a file arrives" -- it is what happens
// when it does not: a connection that drops halfway, an acknowledgement that
// is lost so a chunk is sent twice, a file that assembles into something other
// than what was promised. Those are the ordinary conditions on the connections
// this runs over, not the exceptional ones.

func uploadStore(t *testing.T, pool *pgxpool.Pool) *Store {
	t.Helper()
	dir, err := blob.NewDir(t.TempDir())
	if err != nil {
		t.Fatalf("evidence store: %v", err)
	}
	return NewStore(pool, dir)
}

// aClip is a body big enough to need several chunks.
func aClip(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func videoCapture(reportID, farmID string) New {
	in := aCapture(reportID, farmID)
	in.ContentType = "video/mp4"
	in.Kind = "station_video"
	station := 1
	duration := 15000
	in.StationNo = &station
	in.DurationMs = &duration
	in.Media = nil
	return in
}

// send pushes chunks until the upload completes, returning the final state.
func send(t *testing.T, s *Store, reportID, uploadID string, body []byte, chunkSize int) *UploadState {
	t.Helper()
	ctx := context.Background()

	var st *UploadState
	for i := 0; i*chunkSize < len(body); i++ {
		end := (i + 1) * chunkSize
		if end > len(body) {
			end = len(body)
		}
		var err error
		st, err = s.AppendChunk(ctx, reportID, uploadID, i, bytes.NewReader(body[i*chunkSize:end]))
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	return st
}

func TestAClipArrivesInPiecesAndBecomesOneCapture(t *testing.T) {
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-20")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(500_000)
	const chunk = 64 << 10

	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-20"),
		"clip-1", int64(len(body)), chunk, sumOf(body))
	if err != nil {
		t.Fatalf("beginning the upload: %v", err)
	}
	if st.NextChunk != 0 {
		t.Fatalf("a fresh upload wants chunk %d, not 0", st.NextChunk)
	}

	final := send(t, store, reportID, st.ID, body, chunk)
	if !final.Complete() {
		t.Fatalf("every byte was sent and the upload is not complete: %+v", final)
	}

	capture, err := store.Get(ctx, final.CaptureID)
	if err != nil {
		t.Fatalf("reading the capture: %v", err)
	}
	if capture.ByteSize != len(body) {
		t.Errorf("the capture is %d bytes, the clip was %d", capture.ByteSize, len(body))
	}
	if capture.SHA256 != sumOf(body) {
		t.Error("the stored capture is not the clip that was uploaded")
	}
}

func TestAnInterruptedUploadResumesWhereItStopped(t *testing.T) {
	// The reason the whole mechanism exists. A farmer walks out of signal
	// three chunks in; when they walk back, the server must want chunk four
	// and not chunk zero.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-21")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(300_000)
	const chunk = 50_000

	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-21"),
		"clip-2", int64(len(body)), chunk, sumOf(body))
	if err != nil {
		t.Fatalf("beginning the upload: %v", err)
	}

	// Three chunks land, then the signal goes.
	for i := 0; i < 3; i++ {
		if _, err := store.AppendChunk(ctx, reportID, st.ID, i,
			bytes.NewReader(body[i*chunk:(i+1)*chunk])); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}

	// The app comes back and asks where it got to.
	resumed, err := store.UploadState(ctx, reportID, st.ID)
	if err != nil {
		t.Fatalf("asking where the upload got to: %v", err)
	}
	if resumed.NextChunk != 3 {
		t.Fatalf("the server wants chunk %d after three landed; the farmer "+
			"would resend what it already has", resumed.NextChunk)
	}
	if resumed.ReceivedBytes != 3*chunk {
		t.Errorf("received %d bytes, expected %d", resumed.ReceivedBytes, 3*chunk)
	}

	// And the rest goes through.
	for i := 3; i*chunk < len(body); i++ {
		end := (i + 1) * chunk
		if end > len(body) {
			end = len(body)
		}
		st, err = store.AppendChunk(ctx, reportID, st.ID, i,
			bytes.NewReader(body[i*chunk:end]))
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	if !st.Complete() {
		t.Fatal("the resumed upload never completed")
	}
}

func TestAResentChunkIsNotStoredTwice(t *testing.T) {
	// A handset whose acknowledgement was lost resends. Treating that as an
	// error would strand the upload on exactly the connection that caused it;
	// treating it as new bytes would corrupt the file.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-22")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(120_000)
	const chunk = 40_000

	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-22"),
		"clip-3", int64(len(body)), chunk, sumOf(body))
	if err != nil {
		t.Fatalf("beginning the upload: %v", err)
	}

	if _, err := store.AppendChunk(ctx, reportID, st.ID, 0,
		bytes.NewReader(body[0:chunk])); err != nil {
		t.Fatalf("first send: %v", err)
	}

	// The same chunk again, because the reply never arrived.
	again, err := store.AppendChunk(ctx, reportID, st.ID, 0,
		bytes.NewReader(body[0:chunk]))
	if err != nil {
		t.Fatalf("a resent chunk was refused: %v", err)
	}
	if again.ReceivedBytes != int64(chunk) {
		t.Errorf("a resent chunk was counted twice: %d bytes received, expected %d",
			again.ReceivedBytes, chunk)
	}
	if again.NextChunk != 1 {
		t.Errorf("the server wants chunk %d after a duplicate; it should still want 1",
			again.NextChunk)
	}
}

func TestResumingByKeyDoesNotStartASecondUpload(t *testing.T) {
	// The app retries the opening request after losing its reply. Without the
	// upload key that would begin a second upload of the same clip, and the
	// farmer would send fifty megabytes twice over a connection that can
	// barely carry it once.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-23")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(80_000)
	in := videoCapture(reportID, "ST-23")

	first, err := store.BeginUpload(ctx, in, "clip-4", int64(len(body)), 40_000, sumOf(body))
	if err != nil {
		t.Fatalf("beginning the upload: %v", err)
	}
	if _, err := store.AppendChunk(ctx, reportID, first.ID, 0,
		bytes.NewReader(body[0:40_000])); err != nil {
		t.Fatalf("first chunk: %v", err)
	}

	second, err := store.BeginUpload(ctx, in, "clip-4", int64(len(body)), 40_000, sumOf(body))
	if err != nil {
		t.Fatalf("re-beginning the upload: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("the same upload key produced two uploads: %s and %s",
			first.ID, second.ID)
	}
	if second.NextChunk != 1 {
		t.Errorf("resuming forgot the chunk already sent; wants %d, should want 1",
			second.NextChunk)
	}
}

func TestAFileThatAssemblesIntoSomethingElseIsRefused(t *testing.T) {
	// The declared hash is checked against the assembled bytes. It is the only
	// way to catch a chunk that arrived intact but wrong, and the file has
	// crossed three hours and possibly two networks by the time it is whole.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-24")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(90_000)
	const chunk = 30_000

	// Declared as the hash of something else entirely.
	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-24"),
		"clip-5", int64(len(body)), chunk, sumOf(aClip(11)))
	if err != nil {
		t.Fatalf("beginning the upload: %v", err)
	}

	// A wrong checksum fails every time. It is restarted a bounded number of
	// times -- a chunk damaged in transit is fixed by that -- and then refused,
	// so the handset stops sending fifty megabytes into a loop.
	var lastErr error
	for attempt := 0; attempt <= MaxUploadRestarts; attempt++ {
		for i := 0; i*chunk < len(body); i++ {
			_, lastErr = store.AppendChunk(ctx, reportID, st.ID, i,
				bytes.NewReader(body[i*chunk:(i+1)*chunk]))
		}
		if attempt < MaxUploadRestarts && !errors.Is(lastErr, ErrUploadRestarted) {
			t.Fatalf("attempt %d: got %v, want ErrUploadRestarted", attempt, lastErr)
		}
	}
	if !errors.Is(lastErr, ErrUploadMismatch) {
		t.Fatalf("after %d restarts got %v, want ErrUploadMismatch", MaxUploadRestarts, lastErr)
	}
}

func TestAnUploadFromOutsideTheParcelIsRefusedBeforeAnyBytesMove(t *testing.T) {
	// Refusing after fifty megabytes have arrived, for a reason knowable
	// before the first byte, is the most expensive possible way to say no to
	// somebody on a village connection.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-25")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)

	in := videoCapture(reportID, "ST-25")
	in.Lat = 18.1800 // a kilometre away

	body := aClip(1000)
	_, err := store.BeginUpload(context.Background(), in, "clip-6",
		int64(len(body)), 500, sumOf(body))
	if !errors.Is(err, ErrOutsideParcel) {
		t.Fatalf("got %v, want ErrOutsideParcel", err)
	}
}

func TestAnAbandonedUploadIsSweptAway(t *testing.T) {
	// A farmer who starts a video and never regains signal leaves one behind.
	// On a district-wide event there will be many, and their parts are the
	// largest objects this platform stores.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-26")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(60_000)
	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-26"),
		"clip-7", int64(len(body)), 20_000, sumOf(body))
	if err != nil {
		t.Fatalf("beginning the upload: %v", err)
	}
	if _, err := store.AppendChunk(ctx, reportID, st.ID, 0,
		bytes.NewReader(body[0:20_000])); err != nil {
		t.Fatalf("first chunk: %v", err)
	}

	// Nothing more ever arrives, and the expiry passes.
	swept, err := store.SweepAbandonedUploads(ctx, time.Now().Add(8*24*time.Hour))
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if swept == 0 {
		t.Fatal("the abandoned upload was not swept")
	}

	if _, err := store.UploadState(ctx, reportID, st.ID); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("the swept upload is still there: %v", err)
	}
}

func TestAFinishedUploadIsNotSwept(t *testing.T) {
	// The sweep must not take an upload that completed. Its capture points at
	// evidence, and the row is what tells a retrying client "already done"
	// rather than letting it upload the clip again.
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-27")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(40_000)
	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-27"),
		"clip-8", int64(len(body)), 20_000, sumOf(body))
	if err != nil {
		t.Fatalf("beginning the upload: %v", err)
	}
	final := send(t, store, reportID, st.ID, body, 20_000)
	if !final.Complete() {
		t.Fatal("the upload did not complete")
	}

	if _, err := store.SweepAbandonedUploads(ctx, time.Now().Add(8*24*time.Hour)); err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if _, err := store.UploadState(ctx, reportID, st.ID); err != nil {
		t.Errorf("a completed upload was swept: %v", err)
	}
}

// A clip is evidence for a calamity that happened on a date, and until now it
// was the only evidence never checked against that date: the photograph path
// carried the incident and the event window, and the upload path did not
// (docs/EVIDENCE_REVALIDATION.md, V5).
func TestAClipIsCheckedAgainstTheDateOfTheLoss(t *testing.T) {
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-25")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(200_000)
	const chunk = 100_000

	in := videoCapture(reportID, "ST-25")
	// Filmed a fortnight before the loss the report is about.
	in.CapturedAt = time.Now().Add(-14 * 24 * time.Hour)

	st, err := store.BeginUpload(ctx, in, "stale-clip", int64(len(body)), chunk, sumOf(body))
	if err != nil {
		t.Fatalf("beginning: %v", err)
	}
	final := send(t, store, reportID, st.ID, body, chunk)
	if !final.Complete() {
		t.Fatalf("the upload did not finish: %+v", final)
	}

	capture, err := store.Get(ctx, final.CaptureID)
	if err != nil {
		t.Fatal(err)
	}

	var ran, failed bool
	for _, g := range capture.GatesPassed {
		if g == GateAfterIncident {
			ran = true
		}
	}
	for _, g := range capture.GatesFailed {
		if g == GateAfterIncident {
			ran, failed = true, true
		}
	}
	if !ran {
		t.Fatalf("the incident gate never ran on a clip: passed=%v failed=%v",
			capture.GatesPassed, capture.GatesFailed)
	}
	if !failed {
		t.Error("a clip filmed before the loss passed the incident gate")
	}
	// And disqualified, as the same footage would be on the photograph path:
	// a clip filmed before the storm is not evidence of the storm, and must
	// not reach the panel as though it were.
	if capture.UsableAsEvidence() {
		t.Error("a clip filmed before the loss was still offered to the panel")
	}
}
