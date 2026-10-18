// Package blob stores farmer-captured evidence.
//
// # Why this is an interface
//
// Evidence is the one thing this platform holds that cannot be recomputed. A
// satellite series can be re-fetched, an assessment can be re-run, a report
// can be re-filed; a photograph of a flattened field on the day it was
// flattened exists once. Where those bytes live is therefore a deployment
// decision -- a volume on a laptop during development, an encrypted bucket in
// the region the data is legally required to stay in for anything real -- and
// the code that runs the gates should not have to know which.
//
// # What every implementation must guarantee
//
//   - Keys are content-addressed by the caller. The same bytes written twice
//     land in the same place, so a re-submitted photograph does not become a
//     second piece of evidence.
//   - A Put that returns nil means the bytes are durable. Not buffered, not
//     queued -- readable by a different process.
//   - A Get for a missing key returns ErrNotFound and not a generic failure,
//     because "the evidence is gone" and "the store is down" call for
//     different responses from an officer's screen.
package blob

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound means the key is not in the store.
//
// Distinguished from every other failure on purpose: evidence that is absent
// is a fact about a claim, and evidence that is unreachable is a fact about
// the platform. Collapsing them would let an outage read as a missing
// photograph, which is the reading that harms a farmer.
var ErrNotFound = errors.New("blob: not found")

// Store holds opaque bytes under caller-chosen keys.
type Store interface {
	// Put writes body under key. Writing the same key twice with the same
	// bytes must succeed and be a no-op in effect.
	Put(ctx context.Context, key, contentType string, body []byte) error

	// Get opens the object. The caller closes it.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Delete removes the object. Deleting a key that is not there is not an
	// error -- retrying a cleanup should not fail.
	Delete(ctx context.Context, key string) error

	// Describe names where this store is, for the readiness endpoint. An
	// operator looking at a system holding evidence should be able to see
	// where it is being kept without reading the configuration.
	Describe() string

	// Check reports whether the store is actually usable right now, rather
	// than merely configured. Called from readiness.
	Check(ctx context.Context) error
}

// Streamer is a Store that can write an object from a reader, without the
// caller holding the whole object in memory.
//
// # Why this exists
//
// A station clip is up to sixty megabytes. Assembling one from its chunks into
// a byte slice, then handing that slice to Put, measured 956 MiB of heap for
// four clips finishing together -- against an API container limited to 512 MiB
// (docs/SYNC_SCALE_REVIEW.md, C2). Streamed, the same work holds one chunk.
//
// size is the exact length the reader will produce. An object already stored
// under key with that size is left alone: every streamed key is a content
// hash, so the same key and size is the same object.
type Streamer interface {
	PutStream(ctx context.Context, key, contentType string, r io.Reader, size int64) error
}

// ReadAll is Get plus a bounded read, which is what most callers want.
func ReadAll(ctx context.Context, s Store, key string, limit int64) ([]byte, error) {
	rc, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}
