package blob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// These run against a real bucket, so they are opt-in:
//
//	gcloud auth application-default login
//	ESURVEY_TEST_BUCKET=esurvey-evidence-501512 go test ./internal/blob/ -run GCS -v
//
// Skipped otherwise. An evidence store is not something to verify only by
// reading the code -- the failure modes that matter (wrong region, no
// permission, public by accident) are all properties of the live bucket.
func gcsForTest(t *testing.T) *GCS {
	t.Helper()
	bucket := os.Getenv("ESURVEY_TEST_BUCKET")
	if bucket == "" {
		t.Skip("set ESURVEY_TEST_BUCKET to run the Cloud Storage tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if g, err := NewGCS(ctx, bucket, "_test"); err == nil {
		t.Cleanup(func() { _ = g.Close() })
		return g
	}

	// No Application Default Credentials. Rather than skip -- which would
	// leave the only code path that talks to Cloud Storage unverified -- fall
	// back to a token borrowed from the operator's existing gcloud session.
	//
	// Deliberately not a service-account key. A downloaded key to this bucket
	// would be a long-lived, silently copyable credential to every farmer
	// photograph in the system; a token printed by gcloud expires in an hour
	// and is written nowhere. This is a test convenience and must never
	// become how the service authenticates -- see the comment on GCS.
	g, err := gcsFromGcloudToken(ctx, bucket, "_test")
	if err != nil {
		t.Skipf("no usable credentials for %s: %v", bucket, err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func gcsFromGcloudToken(ctx context.Context, bucket, prefix string) (*GCS, error) {
	out, err := exec.CommandContext(ctx, "gcloud", "auth", "print-access-token").Output()
	if err != nil {
		return nil, fmt.Errorf("gcloud has no active session: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return nil, errors.New("gcloud returned an empty token")
	}

	client, err := storage.NewClient(ctx, option.WithTokenSource(
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
	))
	if err != nil {
		return nil, err
	}
	// Constructed directly because this file is in package blob. Keeping it
	// here rather than adding an exported constructor means the production
	// API still offers exactly one way to authenticate.
	return &GCS{client: client, bucket: bucket, prefix: prefix}, nil
}

func TestGCSCheckReachesTheBucket(t *testing.T) {
	g := gcsForTest(t)
	if err := g.Check(context.Background()); err != nil {
		t.Fatalf("bucket is not usable by this identity: %v", err)
	}
}

func TestGCSRoundTrip(t *testing.T) {
	g := gcsForTest(t)
	ctx := context.Background()

	key := "ab/roundtrip.txt"
	want := []byte("a photograph of a flattened field")
	if err := g.Put(ctx, key, "text/plain", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	t.Cleanup(func() { _ = g.Delete(context.Background(), key) })

	got, err := ReadAll(ctx, g, key, 1<<20)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("read back %q", got)
	}
}

func TestGCSMissingIsDistinguishable(t *testing.T) {
	// The same contract the local store keeps: absent evidence and an
	// unreachable store are different facts.
	g := gcsForTest(t)
	_, err := g.Get(context.Background(), "no/such-object-exists.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGCSDeleteIsIdempotent(t *testing.T) {
	g := gcsForTest(t)
	ctx := context.Background()
	key := "ab/deleteme.txt"
	if err := g.Put(ctx, key, "text/plain", []byte("x")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := g.Delete(ctx, key); err != nil {
			t.Errorf("delete %d: %v", i+1, err)
		}
	}
}

func TestGCSPrefixIsApplied(t *testing.T) {
	// The prefix is what keeps a trial's evidence out of production's inside
	// one bucket. If it were ignored, that separation would be imaginary.
	g := gcsForTest(t)
	if want := "/_test"; g.Describe()[len(g.Describe())-len(want):] != want {
		t.Errorf("Describe() = %q, does not carry the prefix", g.Describe())
	}
}
