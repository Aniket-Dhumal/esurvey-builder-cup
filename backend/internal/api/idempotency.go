package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/idempotency"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Retrying a write without doing it twice.
//
// # What the handset sends
//
// An Idempotency-Key header, chosen when the work was queued and never changed
// -- the queued row's own id. The first request with it executes; any later one
// replays that answer with Idempotency-Replayed: true.
//
// Without the header the request behaves exactly as it always did, so every
// build of the app already in farmers' hands keeps working.
//
// # Why the body is not part of the match
//
// The draft standard compares a fingerprint of the request body and refuses a
// mismatch. Two things make that wrong here.
//
// A photograph goes up as multipart, and the multipart boundary is chosen at
// random for every attempt, so the same photograph sent twice never has the
// same body. And an app update can change how a queued row is serialised -- a
// date normalised differently on the way out -- which would turn a farmer's
// retry into a permanent refusal of a claim they filed before the update.
//
// The key is scoped to the account that chose it, so the only way to reuse a
// key for different content is for that account's own app to do it. Method
// and route are still matched, which catches the realistic bug -- one key
// used for two different operations -- without stranding anybody's queue.
//
// # Why a 5xx is not recorded
//
// It is a statement about the server at one moment. Recording it would make a
// thirty-second outage permanent for every operation that happened to land in
// it. The claim is released and the retry executes.

const (
	headerIdempotencyKey = "Idempotency-Key"
	headerReplayed       = "Idempotency-Replayed"
)

// A key is what the app generates: a UUID or a row id. Restricted so nothing
// odd reaches a log line or a query plan.
var validKey = regexp.MustCompile(`^[A-Za-z0-9_\-:.]{8,128}$`)

// idempotent wraps a write so a retry with the same key replays its answer.
//
// Applied inside require(), because the key is scoped to the principal and the
// principal is only known once the request is authenticated.
func (s *Server) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
		if key == "" || s.Idempotency == nil {
			next(w, r)
			return
		}
		if !validKey.MatchString(key) {
			writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_INVALID",
				"the Idempotency-Key header must be 8 to 128 letters, digits, "+
					"hyphens, underscores, colons or dots")
			return
		}

		p := rbac.FromContext(r.Context())
		if p == nil || p.UserID == "" {
			next(w, r)
			return
		}

		// The full path, report id included: one key reused for a photograph of
		// a different report is two operations, and is refused as a mismatch
		// rather than answered with the other report's photograph.
		route := r.Method + " " + r.URL.Path

		claim, err := s.Idempotency.Begin(r.Context(), p.UserID, key,
			r.Method, route, "")
		switch {
		case errors.Is(err, idempotency.ErrInProgress):
			// Two copies of one retry. The client waits and asks again, and
			// the second copy finds the first one's answer.
			w.Header().Set("Retry-After", "3")
			writeError(w, http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS",
				"this request is already being processed; try again shortly")
			return
		case errors.Is(err, idempotency.ErrMismatch):
			writeError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
				"this Idempotency-Key was already used for a different request")
			return
		case err != nil:
			// The record is unavailable. Executing without it risks a
			// duplicate, refusing risks nothing: the client keeps the work and
			// retries, which is what it does with any 503.
			s.Log.Error("idempotency store unavailable", "error", err)
			w.Header().Set("Retry-After", "10")
			writeError(w, http.StatusServiceUnavailable, "IDEMPOTENCY_UNAVAILABLE", "")
			return
		}

		if claim.Replay != nil {
			rec := claim.Replay
			if rec.ContentType != "" {
				w.Header().Set("Content-Type", rec.ContentType)
			}
			w.Header().Set(headerReplayed, "true")
			w.WriteHeader(rec.Status)
			_, _ = w.Write(rec.Body)
			return
		}

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)

		// Recorded on a context that outlives the request: a client that gave
		// up and hung up has still caused the write, and its retry has to find
		// the answer.
		ctx := context.WithoutCancel(r.Context())
		if !replayable(rec.status) {
			if err := s.Idempotency.Release(ctx, p.UserID, key); err != nil {
				s.Log.Warn("releasing an idempotency key", "error", err)
			}
			return
		}
		if err := s.Idempotency.Complete(ctx, p.UserID, key, rec.status,
			rec.Header().Get("Content-Type"), rec.body.Bytes()); err != nil {
			// The write happened and its answer went out. A retry will see an
			// unfinished claim and, once it is stale, execute again -- which
			// is why the writes that matter most also carry their own unique
			// constraint.
			s.Log.Error("recording an idempotent answer", "error", err)
		}
	}
}

// recorder passes the response through and keeps a copy of it.
type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
	overflow    bool
}

// Unwrap lets a refusal's reason pass through to the request log. Without it
// every idempotent route -- which is every route the farmer's queue writes to
// -- hid its refusals behind this wrapper, and those are the ones a handset
// cannot explain.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if !r.overflow {
		if r.body.Len()+len(b) > idempotency.MaxBody {
			r.overflow = true
			r.body.Reset()
		} else {
			r.body.Write(b)
		}
	}
	return r.ResponseWriter.Write(b)
}

// replayable is whether an answer is one a retry must be told again.
//
// Only a request that did its work is recorded. A retry of it has to find that
// answer rather than doing the work twice.
//
// A refusal did nothing, so running it again duplicates nothing, and replaying
// it would make it permanent: a claim refused by a server fault that is since
// fixed stays refused forever, because its retries never reach the fixed code.
// A 5xx, a 408 and a 429 are about the moment and are equally not recorded.
func replayable(status int) bool {
	return status >= 200 && status < 400
}
