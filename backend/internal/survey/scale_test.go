package survey

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Finishing many videos at once, at the size the app allows. The API runs
// with a 512 MiB memory limit, so the peak heap here is the number that
// decides whether a busy morning is an OOM kill. Set ESURVEY_SCALE=1.
func TestManyVideosFinishingAtOnce(t *testing.T) {
	if os.Getenv("ESURVEY_SCALE") == "" {
		t.Skip("set ESURVEY_SCALE=1 to assemble full-size videos")
	}
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-20")
	store := uploadStore(t, pool)
	ctx := context.Background()

	const (
		clips    = 8
		clipSize = MaxVideoBytes - 1<<20 // a full-length clip at the limit
	)
	// One report, eight clips: exactly the per-report cap on open uploads.
	reports := []string{makeReport(t, pool)}
	body := aClip(clipSize)
	// Distinct bytes per clip, or the distinct-image gate would refuse them.
	bodies := make([][]byte, clips)
	for i := range bodies {
		b := append([]byte(nil), body...)
		copy(b, []byte(fmt.Sprintf("clip-%04d", i)))
		bodies[i] = b
	}

	// Every chunk but the last goes in first, so the assemblies start together.
	type pending struct {
		report, id string
		last       int
		body       []byte
	}
	var ups []pending
	for i := 0; i < clips; i++ {
		rep := reports[0]
		st, err := store.BeginUpload(ctx, videoCapture(rep, "ST-20"),
			fmt.Sprintf("scale-clip-%d", i), int64(clipSize), MaxChunkBytes, sumOf(bodies[i]))
		if err != nil {
			t.Fatalf("begin %d: %v", i, err)
		}
		chunks := (clipSize + MaxChunkBytes - 1) / MaxChunkBytes
		for c := 0; c < chunks-1; c++ {
			if _, err := store.AppendChunk(ctx, rep, st.ID, c,
				bytes.NewReader(bodies[i][c*MaxChunkBytes:(c+1)*MaxChunkBytes])); err != nil {
				t.Fatalf("chunk %d/%d: %v", i, c, err)
			}
		}
		ups = append(ups, pending{rep, st.ID, chunks - 1, bodies[i]})
	}
	bodies, body = nil, nil
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	var peak atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peak.Load() {
				peak.Store(m.HeapInuse)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	var busy atomic.Int32
	var wg sync.WaitGroup
	start := time.Now()
	for _, u := range ups {
		wg.Add(1)
		go func(u pending) {
			defer wg.Done()
			from := u.last * MaxChunkBytes
			for {
				st, err := store.AppendChunk(ctx, u.report, u.id, u.last,
					bytes.NewReader(u.body[from:]))
				if errors.Is(err, ErrBusy) {
					busy.Add(1)
					continue
				}
				if err != nil {
					t.Errorf("finishing %s: %v", u.id, err)
					return
				}
				if !st.Complete() {
					t.Errorf("%s not complete after its last chunk", u.id)
				}
				return
			}
		}(u)
	}
	wg.Wait()
	close(stop)

	t.Logf("%d clips of %d MiB finished in %s; %d busy refusals",
		clips, clipSize>>20, time.Since(start).Round(time.Millisecond), busy.Load())
	t.Logf("peak heap in use while assembling: %d MiB, of which %d MiB is the "+
		"assembly itself (the test holds the clips too); container limit 512 MiB",
		peak.Load()>>20, (peak.Load()-base.HeapInuse)>>20)
	// Streamed, the assembly holds a chunk or two per clip. It measured 956
	// MiB when it held whole files (docs/SYNC_SCALE_REVIEW.md, C2).
	if grew := int64(peak.Load()) - int64(base.HeapInuse); grew > 128<<20 {
		t.Errorf("assembling %d clips grew the heap by %d MiB; want under 128 MiB",
			clips, grew>>20)
	}
}

// An upload still receiving chunks must not be swept as abandoned. The expiry
// used to be fixed at begin (docs/SYNC_SCALE_REVIEW.md, H2).
func TestAnUploadStillMovingIsNotSwept(t *testing.T) {
	_, pool := testStations(t)
	placeBoundary(t, pool, "ST-20")
	reportID := makeReport(t, pool)
	store := uploadStore(t, pool)
	ctx := context.Background()

	body := aClip(300_000)
	const chunk = 100_000
	st, err := store.BeginUpload(ctx, videoCapture(reportID, "ST-20"),
		"slow-clip", int64(len(body)), chunk, sumOf(body))
	if err != nil {
		t.Fatal(err)
	}
	// Started eight days ago, on a connection that comes and goes.
	if _, err := pool.Exec(ctx, `UPDATE capture_uploads
		SET created_at = now() - interval '8 days',
		    expires_at = now() - interval '1 day' WHERE id = $1`, st.ID); err != nil {
		t.Fatal(err)
	}
	// A chunk arrives today.
	if _, err := store.AppendChunk(ctx, reportID, st.ID, 0,
		bytes.NewReader(body[:chunk])); err != nil {
		t.Fatal(err)
	}

	if _, err := store.SweepAbandonedUploads(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UploadState(ctx, reportID, st.ID); err != nil {
		t.Fatalf("an upload that received a chunk today was swept: %v", err)
	}
}
