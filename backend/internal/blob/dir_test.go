package blob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	d, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	want := []byte("a photograph of a flattened field")
	if err := d.Put(ctx, "ab/abcdef.jpg", "image/jpeg", want); err != nil {
		t.Fatal(err)
	}

	got, err := ReadAll(ctx, d, "ab/abcdef.jpg", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("read back %q", got)
	}
}

func TestMissingIsDistinguishable(t *testing.T) {
	// "The evidence is gone" and "the store is down" call for different
	// responses from an officer's screen; collapsing them lets an outage read
	// as a missing photograph.
	d, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Get(context.Background(), "no/such.jpg")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAKeyCannotEscapeTheStore(t *testing.T) {
	// Not reachable today -- keys are content hashes -- but one refactor away
	// from being untrue, and the failure is writing attacker-named files
	// anywhere the process can reach.
	root := t.TempDir()
	d, err := NewDir(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for _, key := range []string{
		"../escaped.jpg",
		"../../escaped.jpg",
		"ab/../../escaped.jpg",
		`..\escaped.jpg`,
		"/etc/passwd",
		"ab//x.jpg",
		"./x.jpg",
		"",
	} {
		if err := d.Put(ctx, key, "image/jpeg", []byte("x")); err == nil {
			t.Errorf("key %q was accepted", key)
		}
		rc, err := d.Get(ctx, key)
		if err == nil {
			rc.Close()
			t.Errorf("key %q was readable", key)
		}
	}

	// And nothing landed beside the root.
	outside, _ := filepath.Glob(filepath.Join(filepath.Dir(root), "escaped.jpg"))
	if len(outside) > 0 {
		t.Errorf("a file escaped the store: %v", outside)
	}
}

func TestAReaderNeverSeesAPartialWrite(t *testing.T) {
	// Written to a temp name and renamed. A truncated image that passes as
	// evidence is not recoverable the way a stray temp file is.
	root := t.TempDir()
	d, err := NewDir(root)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 512<<10)
	for i := range body {
		body[i] = byte(i)
	}
	if err := d.Put(context.Background(), "cd/big.jpg", "image/jpeg", body); err != nil {
		t.Fatal(err)
	}

	// No leftovers from the rename dance.
	var partials []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && strings.HasPrefix(info.Name(), ".partial-") {
			partials = append(partials, p)
		}
		return nil
	})
	if len(partials) > 0 {
		t.Errorf("temporary files left behind: %v", partials)
	}

	got, err := ReadAll(context.Background(), d, "cd/big.jpg", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(body) {
		t.Errorf("read %d bytes, wrote %d", len(got), len(body))
	}
}

func TestPuttingTheSameKeyTwiceIsFine(t *testing.T) {
	// Keys are content hashes, so a re-submitted photograph is the same bytes
	// at the same key. That must not be an error.
	d, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	body := []byte("same bytes")
	for i := 0; i < 2; i++ {
		if err := d.Put(ctx, "ef/dup.jpg", "image/jpeg", body); err != nil {
			t.Fatalf("write %d: %v", i+1, err)
		}
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	// Retrying a cleanup should not fail.
	d, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Put(ctx, "01/x.jpg", "image/jpeg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := d.Delete(ctx, "01/x.jpg"); err != nil {
			t.Errorf("delete %d: %v", i+1, err)
		}
	}
}

func TestCheckNoticesAnUnusableDirectory(t *testing.T) {
	// A read-only remount or a full disk is how this store realistically
	// fails, and both are silent until the first capture of the day is lost.
	root := t.TempDir()
	d, err := NewDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Check(context.Background()); err != nil {
		t.Fatalf("a fresh directory failed its own check: %v", err)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Skipf("could not remove the root to simulate a failure: %v", err)
	}
	if err := d.Check(context.Background()); err == nil {
		t.Error("a missing evidence directory passed the check")
	}
}

func TestDescribeNamesTheLocation(t *testing.T) {
	root := t.TempDir()
	d, err := NewDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Describe(), root) {
		t.Errorf("Describe() = %q, does not name the directory", d.Describe())
	}
}
