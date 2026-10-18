package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"cloud.google.com/go/storage"
)

// GCS stores evidence in a Cloud Storage bucket.
//
// # Why no credentials appear here
//
// Authentication is Application Default Credentials, and deliberately nothing
// else. On a developer machine that is `gcloud auth application-default
// login`; on GKE or Cloud Run it is the workload's own identity. What it is
// never is a downloaded service-account key sitting in the repository or an
// environment variable, because a long-lived key to the bucket holding
// identified farmers' photographs is the single worst secret this system
// could have, and it leaks the way all such keys leak.
//
// # Why the bucket is not configured here either
//
// Encryption, versioning, public-access prevention, retention and region are
// properties of the bucket, set once when it is created and auditable
// afterwards. Setting them from application code would mean every deployment
// could quietly weaken them. This code assumes a correctly built bucket and
// says so on startup if it cannot reach one; see docs/EVIDENCE_STORAGE.md for
// what "correctly built" means and the commands that produce it.
type GCS struct {
	client *storage.Client
	bucket string
	prefix string
}

// NewGCS opens a client for bucket. prefix, if given, is prepended to every
// key -- useful for keeping a trial's evidence separate from production's in
// one bucket without relying on anyone remembering to.
func NewGCS(ctx context.Context, bucket, prefix string) (*GCS, error) {
	if bucket == "" {
		return nil, errors.New("blob: no bucket named")
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to Cloud Storage: %w", err)
	}
	return &GCS{client: client, bucket: bucket, prefix: prefix}, nil
}

func (g *GCS) object(key string) *storage.ObjectHandle {
	name := key
	if g.prefix != "" {
		name = g.prefix + "/" + key
	}
	return g.client.Bucket(g.bucket).Object(name)
}

func (g *GCS) Put(ctx context.Context, key, contentType string, body []byte) error {
	// A write must not hang a request forever. Evidence upload happens while
	// a farmer stands in a field on a phone connection; a stuck write that
	// never returns is worse than a failure they can retry.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	w := g.object(key).NewWriter(ctx)
	w.ContentType = contentType

	// Written once and never rewritten. The key is a content hash, so a
	// second write of the same key is the same bytes -- and if it somehow is
	// not, overwriting evidence silently is exactly what must not happen.
	// DoesNotExist turns that into a failed precondition we can recognise.
	w.ChunkSize = 1 << 20

	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("uploading evidence: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("completing evidence upload: %w", err)
	}
	return nil
}

// PutStream writes an object from a reader. See Streamer.
//
// The writer buffers one ChunkSize at a time, so a sixty-megabyte clip costs
// four megabytes of memory rather than sixty.
func (g *GCS) PutStream(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	obj := g.object(key)
	if attrs, err := obj.Attrs(ctx); err == nil && attrs.Size == size {
		return nil
	}

	w := obj.NewWriter(ctx)
	w.ContentType = contentType
	w.ChunkSize = 4 << 20

	n, err := io.Copy(w, r)
	if err != nil {
		_ = w.Close()
		return fmt.Errorf("uploading evidence: %w", err)
	}
	if n != size {
		_ = w.Close()
		return fmt.Errorf("uploading evidence: wrote %d bytes, expected %d", n, size)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("completing evidence upload: %w", err)
	}
	return nil
}

func (g *GCS) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := g.object(key).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading evidence: %w", err)
	}
	return r, nil
}

func (g *GCS) Delete(ctx context.Context, key string) error {
	err := g.object(key).Delete(ctx)
	if err == nil || errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return fmt.Errorf("removing evidence: %w", err)
}

// Remote reports whether the evidence survives this machine. It does.
func (g *GCS) Remote() bool { return true }

func (g *GCS) Describe() string {
	if g.prefix != "" {
		return "gs://" + g.bucket + "/" + g.prefix
	}
	return "gs://" + g.bucket
}

// Check confirms the bucket is reachable and this identity may use it.
//
// Done by reading the bucket's metadata rather than writing an object: it
// proves reachability and authorisation without leaving anything behind in a
// store whose whole point is that its contents are evidence.
func (g *GCS) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := g.client.Bucket(g.bucket).Attrs(ctx); err != nil {
		return fmt.Errorf("evidence bucket %s: %w", g.bucket, err)
	}
	return nil
}

// Close releases the client.
func (g *GCS) Close() error { return g.client.Close() }
