package survey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/blob"
)

// Resumable upload, for evidence too large to send in one request.
//
// # Why this exists
//
// Five photographs are about two megabytes. Four fifteen-second clips are
// nearer fifty -- twenty-five times the payload this platform was built
// around, moving over 2G from a field, and required rather than optional.
//
// Submitted in one request, a connection that drops at 80% starts again at
// zero. At that size it is not a slow upload, it is a report that never
// completes: retried until the attempt budget runs out, after which the
// farmer is told their claim failed and has no way to see why.
//
// So the bytes arrive in chunks, each one acknowledged, and a pass that
// resumes continues from the last chunk the server actually has.
//
// # Why the parts live in the evidence store
//
// The API container runs on a read-only root filesystem with a small tmpfs,
// which is deliberate and worth keeping. Assembling a fifty-megabyte file on
// local disk would mean giving that up. The blob store is already durable,
// already sized for this, and already the thing that survives a container
// being replaced mid-upload -- which on a three-hour upload is not a remote
// possibility.

// Errors particular to uploading.
var (
	ErrUploadNotFound = errors.New("no such upload")

	// ErrBusy means every assembly slot is taken. Retryable: the handset
	// sends the last chunk again, which is harmless because the server
	// already has it, and assembly starts once a slot frees.
	ErrBusy = errors.New("the server is assembling other uploads; try again shortly")

	// ErrUploadRestarted means the assembled file did not match its declared
	// hash, and the upload has been reset so the next attempt starts clean.
	ErrUploadRestarted = errors.New("the upload did not match its checksum and has been restarted")
	ErrUploadMismatch  = errors.New("the assembled file is not the one that was declared")
	ErrChunkTooLarge   = errors.New("the chunk is larger than the size declared for this upload")
	ErrTooManyUploads  = errors.New("too many uploads are already open on this report")
)

// MaxOpenUploads bounds how many unfinished uploads one report may hold.
//
// # Why a limit exists at all
//
// Beginning an upload costs the server a row and a reservation; finishing one
// costs it up to sixty megabytes in the evidence store. Nothing bounded how
// many could be started, so one signed-in account could open uploads until the
// bucket holding every farmer's photographs filled up -- and the sweep only
// reclaims them after they expire, seven days later.
//
// The rate limiter slows that but does not stop it: three hundred requests a
// minute is three hundred reservations a minute.
//
// # Why eight
//
// A report needs four station clips and four station stills. Eight is exactly
// that, and a ninth open upload means either a client bug or somebody probing.
// Completed uploads do not count -- they have become captures, which are
// bounded by their own rules -- so an honest farmer redoing a station that
// failed is never refused.
const MaxOpenUploads = 8

// MinClipMs is the shortest clip that counts as a station's evidence.
//
// The app records fifteen seconds and nothing checked, so an interrupted
// recording -- a handset that stopped early, a farmer who moved off -- counted
// as a covered point (docs/EVIDENCE_REVALIDATION.md, V9). Five seconds is
// generous against a fifteen-second clip and still refuses a glance.
//
// Only a clip that says how long it is can be refused for being short: a
// handset that reports no duration is not punished for its firmware.
const MinClipMs = 5000

// MaxChunkBytes bounds one chunk.
//
// Two megabytes is a few seconds of video and about fifteen seconds of 2G. It
// is chosen to be small enough that losing one costs almost nothing and large
// enough that a fifty-megabyte clip is not five hundred round trips.
const MaxChunkBytes = 2 << 20

// UploadState is what a client needs to know to carry on.
type UploadState struct {
	ID       string `json:"id"`
	ReportID string `json:"report_id"`

	TotalBytes    int64 `json:"total_bytes"`
	ReceivedBytes int64 `json:"received_bytes"`
	ChunkSize     int   `json:"chunk_size"`

	// The index the server wants next. This is the whole resume protocol:
	// ask, get a number, send that chunk. A client that lost its reply asks
	// again and is told the same number rather than sending a duplicate.
	NextChunk int `json:"next_chunk"`

	// Set once the file is assembled, verified and recorded as a capture.
	CaptureID string `json:"capture_id,omitempty"`
}

// Complete reports whether there is nothing left to send.
func (u UploadState) Complete() bool { return u.CaptureID != "" }

// BeginUpload starts or resumes an upload.
//
// Keyed by the client's own upload_key, so a handset that retried after losing
// the reply resumes the upload it started rather than beginning a second one.
// Without that, every dropped connection would leave a half-finished upload
// behind and the farmer would send the same video several times over the
// connection least able to carry it once.
func (s *Store) BeginUpload(ctx context.Context, in New, key string, totalBytes int64, chunkSize int, sum string) (*UploadState, error) {
	if key == "" {
		return nil, fmt.Errorf("%w: an upload key is required", ErrBadMedia)
	}
	limit, ok := acceptedMedia[in.ContentType]
	if !ok {
		return nil, fmt.Errorf("%w: %s is not a media type this system accepts",
			ErrBadMedia, in.ContentType)
	}
	if totalBytes <= 0 || totalBytes > int64(limit) {
		return nil, fmt.Errorf("%w: %d bytes is outside the limit for %s",
			ErrTooLarge, totalBytes, in.ContentType)
	}
	if chunkSize <= 0 || chunkSize > MaxChunkBytes {
		chunkSize = MaxChunkBytes
	}
	if len(sum) != 64 {
		return nil, fmt.Errorf("%w: a sha256 of the whole file is required", ErrBadMedia)
	}
	if !ValidPosition(in.Lat, in.Lon) {
		return nil, fmt.Errorf("%w: the capture has no usable position", ErrBadMedia)
	}
	if IsVideo(in.ContentType) && in.DurationMs != nil && *in.DurationMs < MinClipMs {
		return nil, fmt.Errorf(
			"%w: a %.1f second clip is too short to survey a point; record it again",
			ErrBadMedia, float64(*in.DurationMs)/1000)
	}

	// A resume. The phone begins again on every pass -- that is how it asks
	// where it got to -- and the gates below were judged when this upload was
	// first begun. Running the PostGIS geofence and the open-upload count again
	// on every resume of every clip, on the morning after an event, is load
	// that learns nothing. Resuming also counts as activity for expiry.
	var resumed UploadState
	switch err := s.pool.QueryRow(ctx, `
		UPDATE capture_uploads
		   SET updated_at = now(),
		       expires_at = GREATEST(expires_at, now() + interval '7 days')
		 WHERE report_id = $1 AND upload_key = $2
		RETURNING id, report_id, total_bytes, received_bytes, chunk_size,
		          next_chunk, COALESCE(capture_id::text, '')`,
		in.ReportID, key).Scan(&resumed.ID, &resumed.ReportID, &resumed.TotalBytes,
		&resumed.ReceivedBytes, &resumed.ChunkSize, &resumed.NextChunk, &resumed.CaptureID); {
	case err == nil:
		return s.finishIfReady(ctx, &resumed)
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("resuming the upload: %w", err)
	}

	// The gates that can be judged before a single byte arrives are judged
	// now. Refusing a fifty-megabyte upload after it has finished, for a
	// reason that was knowable before it started, is the most expensive
	// possible way to say no to somebody on a village connection.
	gates := runGates(ctx, s.pool, in, "")
	if gates.inside != nil && !*gates.inside {
		return nil, fmt.Errorf("%w: %.0f m from the boundary",
			ErrOutsideParcel, *gates.distance)
	}
	if in.IsMocked != nil && *in.IsMocked {
		return nil, ErrMockedPosition
	}
	if gates.nearestOther != nil && *gates.nearestOther < gates.spacing {
		return nil, fmt.Errorf("%w: %.0f m from the last one, and this crop "+
			"needs %.0f m", ErrSamePosition, *gates.nearestOther, gates.spacing)
	}
	if gates.takenBy != nil {
		return nil, takenError(gates.takenBy)
	}
	if gates.away != nil && *gates.away > UnmappedFieldRadiusM {
		return nil, fmt.Errorf("%w: %.0f m from this claim's other evidence",
			ErrAwayFromField, *gates.away)
	}

	// Counted before the insert, and excluding this key so a resumed upload is
	// never refused for being one of the uploads it already is.
	var open int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM capture_uploads
		WHERE report_id = $1 AND capture_id IS NULL AND upload_key <> $2`,
		in.ReportID, key).Scan(&open); err != nil {
		return nil, fmt.Errorf("counting open uploads: %w", err)
	}
	if open >= MaxOpenUploads {
		return nil, fmt.Errorf("%w: %d already open", ErrTooManyUploads, open)
	}

	var st UploadState
	err := s.pool.QueryRow(ctx, `
		INSERT INTO capture_uploads (
			report_id, upload_key, content_type, total_bytes, chunk_size,
			sha256, temp_path, kind, line_no, caption, station_no,
			captured_at, lat, lon, accuracy_m, altitude_m, heading_deg,
			duration_ms, is_mocked, device_label, started_by
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, '', $7, $8, NULLIF($9, ''), $10,
			$11, $12, $13, $14, NULL, $15,
			$16, $17, NULLIF($18, ''), $19
		)
		-- Resuming counts as activity, like a chunk arriving.
		ON CONFLICT (report_id, upload_key) DO UPDATE
			SET updated_at = now(),
			    expires_at = GREATEST(capture_uploads.expires_at, now() + interval '7 days')
		RETURNING id, report_id, total_bytes, received_bytes, chunk_size,
		          next_chunk, COALESCE(capture_id::text, '')`,
		in.ReportID, key, in.ContentType, totalBytes, chunkSize,
		sum, string(in.Kind), in.LineNo, in.Caption, in.StationNo,
		in.CapturedAt, in.Lat, in.Lon, in.AccuracyM, in.HeadingDeg,
		in.DurationMs, in.IsMocked, in.DeviceLabel, nullString(in.CapturedBy),
	).Scan(&st.ID, &st.ReportID, &st.TotalBytes, &st.ReceivedBytes,
		&st.ChunkSize, &st.NextChunk, &st.CaptureID)
	if err != nil {
		return nil, fmt.Errorf("starting the upload: %w", err)
	}
	return s.finishIfReady(ctx, &st)
}

// finishIfReady assembles an upload that has every byte and no capture yet.
//
// # The upload this rescues
//
// Assembly can fail after the last chunk is recorded: the evidence store
// blinks, the database times out, no slot is free. The bytes are all in and
// next_chunk has moved past the end, so the handset has no chunk left to send
// and the server, until this, had nothing left to do. The upload sat complete
// in every respect except the one that mattered, and the video never became
// evidence.
//
// Now any contact with the upload -- resuming it, asking its state, resending
// its last chunk -- finishes it.
func (s *Store) finishIfReady(ctx context.Context, st *UploadState) (*UploadState, error) {
	if st.Complete() || st.ReceivedBytes < st.TotalBytes {
		return st, nil
	}
	return s.finishUpload(ctx, st)
}

// UploadState reads where an upload has got to.
func (s *Store) UploadState(ctx context.Context, reportID, uploadID string) (*UploadState, error) {
	var st UploadState
	err := s.pool.QueryRow(ctx, `
		SELECT id, report_id, total_bytes, received_bytes, chunk_size,
		       next_chunk, COALESCE(capture_id::text, '')
		FROM capture_uploads WHERE id = $1 AND report_id = $2`,
		uploadID, reportID).Scan(&st.ID, &st.ReportID, &st.TotalBytes,
		&st.ReceivedBytes, &st.ChunkSize, &st.NextChunk, &st.CaptureID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUploadNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading the upload: %w", err)
	}
	return &st, nil
}

// AppendChunk stores one chunk and returns what the server wants next.
//
// Sending a chunk the server already has is not an error. A handset whose
// acknowledgement was lost will resend, and answering "I already have that,
// send this instead" is the behaviour that makes the retry safe -- treating it
// as a fault would strand the upload on exactly the connection that caused it.
func (s *Store) AppendChunk(
	ctx context.Context, reportID, uploadID string, index int, body io.Reader,
) (*UploadState, error) {
	st, err := s.UploadState(ctx, reportID, uploadID)
	if err != nil {
		return nil, err
	}
	if st.Complete() {
		return st, nil
	}
	if st.ReceivedBytes >= st.TotalBytes {
		// Every byte is in and assembly has not succeeded yet.
		return s.finishUpload(ctx, st)
	}
	if index != st.NextChunk {
		// Already have it, or too far ahead. Either way the answer is the
		// same: here is the number I actually want.
		return st, nil
	}

	chunk, err := io.ReadAll(io.LimitReader(body, int64(st.ChunkSize)+1))
	if err != nil {
		return nil, fmt.Errorf("reading the chunk: %w", err)
	}
	if len(chunk) > st.ChunkSize {
		return nil, ErrChunkTooLarge
	}
	if len(chunk) == 0 {
		return nil, fmt.Errorf("%w: the chunk is empty", ErrBadMedia)
	}
	if st.ReceivedBytes+int64(len(chunk)) > st.TotalBytes {
		return nil, fmt.Errorf("%w: more bytes than were declared", ErrTooLarge)
	}

	// Written before the row moves on. The reverse would advance next_chunk
	// past bytes that are not stored, and the gap would only surface at
	// assembly -- by which time the farmer has uploaded the whole file.
	//
	// Keyed on st.ID, the uuid the database returned, and never on the string
	// the caller sent. Today the two are the same -- a path value that is not
	// a uuid fails the lookup above -- but the object key is a filesystem path
	// under blob.Dir, and a key built from caller input is one refactor away
	// from being a traversal.
	if err := s.blobs.Put(ctx, partKey(st.ID, index), "application/octet-stream", chunk); err != nil {
		return nil, fmt.Errorf("storing chunk %d: %w", index, err)
	}

	err = s.pool.QueryRow(ctx, `
		UPDATE capture_uploads
		SET received_bytes = received_bytes + $3, next_chunk = next_chunk + 1,
		    updated_at = now(),
		    -- An upload is abandoned when it stops moving, not when it is old.
		    -- The expiry was fixed at begin, so a video trickling in over a
		    -- week of patchy signal was swept while chunks were still arriving
		    -- (docs/SYNC_SCALE_REVIEW.md, H2).
		    expires_at = GREATEST(expires_at, now() + interval '7 days')
		WHERE id = $1 AND report_id = $2 AND next_chunk = $4
		RETURNING id, report_id, total_bytes, received_bytes, chunk_size,
		          next_chunk, COALESCE(capture_id::text, '')`,
		uploadID, reportID, len(chunk), index,
	).Scan(&st.ID, &st.ReportID, &st.TotalBytes, &st.ReceivedBytes,
		&st.ChunkSize, &st.NextChunk, &st.CaptureID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another request took this chunk first. The part is already stored
		// under the same key with the same bytes, so nothing is lost.
		return s.UploadState(ctx, reportID, uploadID)
	}
	if err != nil {
		return nil, fmt.Errorf("recording chunk %d: %w", index, err)
	}

	if st.ReceivedBytes >= st.TotalBytes {
		return s.finishUpload(ctx, st)
	}
	return st, nil
}

// partKey is where one chunk waits. Under a prefix of its own so an abandoned
// upload can be swept without touching evidence.
func partKey(uploadID string, index int) string {
	return fmt.Sprintf("uploads/%s/%08d", uploadID, index)
}

// finishUpload assembles the parts, checks them against what was declared, and
// records the capture.
func (s *Store) finishUpload(ctx context.Context, st *UploadState) (*UploadState, error) {
	// A slot, or a prompt answer that there is none. See AssemblyLimit.
	wait, cancel := context.WithTimeout(ctx, AssemblyWait)
	defer cancel()
	select {
	case s.assembling <- struct{}{}:
		defer func() { <-s.assembling }()
	case <-wait.Done():
		return nil, ErrBusy
	}

	// Another request may have finished it while this one waited.
	if cur, err := s.UploadState(ctx, st.ReportID, st.ID); err == nil && cur.Complete() {
		return cur, nil
	}

	var (
		in         New
		kind       string
		sum        string
		lineNo     *int
		caption    string
		device     string
		capturedBy *string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT kind, sha256, content_type, line_no, COALESCE(caption, ''),
		       station_no, captured_at, lat, lon, accuracy_m, heading_deg,
		       duration_ms, is_mocked, COALESCE(device_label, ''),
		       started_by::text
		FROM capture_uploads WHERE id = $1`, st.ID).Scan(
		&kind, &sum, &in.ContentType, &lineNo, &caption,
		&in.StationNo, &in.CapturedAt, &in.Lat, &in.Lon, &in.AccuracyM,
		&in.HeadingDeg, &in.DurationMs, &in.IsMocked, &device, &capturedBy)
	if err != nil {
		return nil, fmt.Errorf("reading the upload metadata: %w", err)
	}

	// First pass: hash the parts as a stream. Nothing is held but the chunk
	// being read.
	//
	// Checked against the hash the handset declared before a byte was sent.
	// A file stitched together from chunks that arrived over three hours and
	// two network changes deserves to be verified before it is called
	// evidence -- and a mismatch here is the only way to catch a chunk that
	// arrived intact but wrong.
	h := sha256.New()
	n, err := io.Copy(h, s.parts(ctx, st))
	if err != nil {
		return nil, err
	}
	if n != st.TotalBytes {
		return nil, fmt.Errorf("%w: assembled %d bytes, expected %d",
			ErrUploadMismatch, n, st.TotalBytes)
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		// Reset, not refused -- up to a point. The handset resumes by upload
		// key, so leaving the corrupt parts in place would hand the same
		// broken upload back to every retry, for ever; starting from chunk
		// zero is the retry that can succeed when a chunk was damaged in
		// transit. A checksum that is wrong in itself fails every time, so
		// after MaxUploadRestarts the upload is refused for good.
		restarted, err := s.resetUpload(ctx, st)
		if err != nil {
			return nil, err
		}
		if !restarted {
			return nil, ErrUploadMismatch
		}
		return nil, ErrUploadRestarted
	}

	in.ReportID = st.ReportID
	in.Kind = Kind(kind)
	in.LineNo = lineNo
	in.Caption = caption
	in.DeviceLabel = device
	if capturedBy != nil {
		in.CapturedBy = *capturedBy
	}

	// Fetched now rather than carried through the upload: the parcel is what
	// the report says it is, and a boundary corrected while the video was in
	// flight should be the one the geofence uses.
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(i.farm_id, '')
		FROM crop_loss_report_items i
		WHERE i.report_id = $1
		ORDER BY i.line_no LIMIT 1`, st.ReportID).Scan(&in.FarmID)

	// When the loss happened, and the window the event covers.
	//
	// The photograph path has carried these from the first day; this one never
	// did, so the strongest evidence in the system was the only evidence never
	// checked against the date of the calamity it was offered for
	// (docs/EVIDENCE_REVALIDATION.md, V5). Read here rather than at begin
	// because that is where the gates run.
	//
	// Neither gate refuses a clip: both are recorded, and a clip filmed before
	// the storm reaches an officer saying so.
	var windowStart, windowEnd *time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT r.occurred_at, e.incident_start, e.incident_end
		  FROM crop_loss_reports r
		  LEFT JOIN survey_events e ON e.id = r.survey_event_id
		 WHERE r.id = $1`, st.ReportID).
		Scan(&in.IncidentAt, &windowStart, &windowEnd); err != nil {
		// A report that cannot be read here is a report the capture will fail
		// against anyway; the gates simply go unrecorded.
		in.IncidentAt = time.Time{}
	}
	if windowStart != nil && windowEnd != nil {
		in.WindowStart = windowStart.Format("2006-01-02")
		in.WindowEnd = windowEnd.Format("2006-01-02")
	}

	if _, err := checkNew(in); err != nil {
		return nil, err
	}
	// Second pass, only if the gates pass: stream the parts into the evidence
	// store under the content hash already verified.
	capture, err := s.record(ctx, in, sum, st.TotalBytes,
		func(ctx context.Context, key string) error {
			if streamer, ok := s.blobs.(blob.Streamer); ok {
				return streamer.PutStream(ctx, key, in.ContentType, s.parts(ctx, st), st.TotalBytes)
			}
			// A store that cannot stream takes the bytes whole. Only test
			// doubles lack PutStream; both real stores have it.
			body, err := io.ReadAll(s.parts(ctx, st))
			if err != nil {
				return err
			}
			return s.blobs.Put(ctx, key, in.ContentType, body)
		})
	if err != nil {
		return nil, err
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE capture_uploads SET capture_id = $2 WHERE id = $1`,
		st.ID, capture.ID); err != nil {
		return nil, fmt.Errorf("completing the upload: %w", err)
	}
	st.CaptureID = capture.ID

	// The parts have served their purpose. Left behind they would double the
	// storage of every video in the system.
	for i := 0; i < st.NextChunk; i++ {
		_ = s.blobs.Delete(ctx, partKey(st.ID, i))
	}

	return st, nil
}

// parts reads an upload's chunks back in order, one open at a time.
func (s *Store) parts(ctx context.Context, st *UploadState) io.Reader {
	return &partReader{ctx: ctx, s: s, st: st}
}

type partReader struct {
	ctx  context.Context
	s    *Store
	st   *UploadState
	next int
	cur  io.ReadCloser
}

func (p *partReader) Read(b []byte) (int, error) {
	for {
		if p.cur == nil {
			if p.next >= p.st.NextChunk {
				return 0, io.EOF
			}
			rc, err := p.s.blobs.Get(p.ctx, partKey(p.st.ID, p.next))
			if err != nil {
				return 0, fmt.Errorf("chunk %d is missing from the store: %w", p.next, err)
			}
			p.cur = struct {
				io.Reader
				io.Closer
			}{io.LimitReader(rc, int64(p.st.ChunkSize)), rc}
			p.next++
		}
		n, err := p.cur.Read(b)
		if err == io.EOF {
			p.cur.Close()
			p.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

// SweepAbandonedUploads removes uploads that were started and never finished.
//
// A farmer who begins a video and never regains signal leaves one behind, and
// on a district-wide event there will be many. Their parts are the largest
// objects this platform stores, so nothing else is worth sweeping first.
func (s *Store) SweepAbandonedUploads(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, next_chunk FROM capture_uploads
		WHERE capture_id IS NULL AND expires_at < $1`, now)
	if err != nil {
		return 0, fmt.Errorf("finding abandoned uploads: %w", err)
	}

	type doomed struct {
		id    string
		parts int
	}
	var list []doomed
	for rows.Next() {
		var d doomed
		if err := rows.Scan(&d.id, &d.parts); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, d := range list {
		// Objects first. A row deleted before its parts leaves bytes nothing
		// points at, which is the one outcome that cannot be cleaned up
		// later.
		for i := 0; i < d.parts; i++ {
			_ = s.blobs.Delete(ctx, partKey(d.id, i))
		}
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM capture_uploads WHERE id = $1`, d.id); err != nil {
			return 0, fmt.Errorf("removing abandoned upload %s: %w", d.id, err)
		}
	}
	return len(list), nil
}

// MaxUploadRestarts is how many times a checksum failure restarts an upload
// before it is refused.
const MaxUploadRestarts = 2

// resetUpload discards an upload's parts and starts it again from chunk zero,
// unless it has already been restarted MaxUploadRestarts times. Reports
// whether it restarted.
func (s *Store) resetUpload(ctx context.Context, st *UploadState) (bool, error) {
	for i := 0; i < st.NextChunk; i++ {
		_ = s.blobs.Delete(ctx, partKey(st.ID, i))
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE capture_uploads
		   SET received_bytes = 0, next_chunk = 0, restarts = restarts + 1,
		       updated_at = now()
		 WHERE id = $1 AND capture_id IS NULL AND restarts < $2`,
		st.ID, MaxUploadRestarts)
	if err != nil {
		return false, fmt.Errorf("restarting a corrupt upload: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ResumeUpload reads where an upload has got to, and finishes it if every
// byte is already in. The handset's "where did I get to?" is also the moment a
// stranded upload is rescued.
func (s *Store) ResumeUpload(ctx context.Context, reportID, uploadID string) (*UploadState, error) {
	st, err := s.UploadState(ctx, reportID, uploadID)
	if err != nil {
		return nil, err
	}
	return s.finishIfReady(ctx, st)
}
