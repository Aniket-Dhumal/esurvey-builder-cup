package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Dir stores objects as files under a root directory.
//
// The development and single-node deployment. Keys are split on the first two
// characters into subdirectories: a flat directory of a hundred thousand
// photographs is slow to list and unpleasant to inspect on every filesystem
// worth naming.
type Dir struct {
	root string
}

// NewDir prepares root and returns a store over it.
func NewDir(root string) (*Dir, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating evidence directory %s: %w", root, err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &Dir{root: abs}, nil
}

// path resolves a key to a file, rejecting anything that is not already a
// plain relative path.
//
// Rejected rather than normalised, which is the important part. Cleaning
// "../x.jpg" into "x.jpg" would keep it inside the root -- safe -- while
// silently mapping two different keys onto one file. For a content-addressed
// evidence store that collision is the whole problem: one photograph would
// overwrite another and both rows would still look correct.
//
// Keys are built by this codebase from content hashes, so nothing here is
// reachable today. The check is present because that is one refactor away
// from being untrue.
func (d *Dir) path(key string) (string, error) {
	if key == "" {
		return "", errors.New("blob: empty key")
	}
	if strings.ContainsAny(key, `\:`) {
		return "", fmt.Errorf("blob: key %q contains a path separator or drive", key)
	}
	if strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("blob: key %q is absolute", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("blob: key %q is not a plain relative path", key)
		}
	}

	full := filepath.Join(d.root, filepath.FromSlash(key))
	// Belt and braces: whatever the rules above missed, the result must still
	// be under the root.
	if !strings.HasPrefix(full, d.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("blob: key %q escapes the store", key)
	}
	return full, nil
}

func (d *Dir) Put(_ context.Context, key, _ string, body []byte) error {
	full, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("creating evidence directory: %w", err)
	}

	// Already stored. A resent photograph and a resent chunk are both the
	// same bytes under the same key, and writing them again spends a disk
	// write -- and on the morning after an event, a great many of them -- to
	// arrive where things already are.
	if same, _ := sameContent(full, body); same {
		return nil
	}

	// Written to a temporary name and renamed, so a reader never observes a
	// half-written photograph. A crash mid-write leaves a stray temp file,
	// which is recoverable; a truncated image that passes as evidence is not.
	tmp, err := os.CreateTemp(filepath.Dir(full), ".partial-*")
	if err != nil {
		return fmt.Errorf("creating evidence file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("writing evidence: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flushing evidence: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing evidence: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("setting evidence permissions: %w", err)
	}
	if err := renameSettled(tmpName, full, body); err != nil {
		// Two writers of the same object. Every key here is either a content
		// hash or a chunk that is only ever rewritten with the same bytes, so
		// an object already present with identical bytes is this write,
		// already done -- not a failure.
		//
		// Linux renames over an existing file atomically and never reaches
		// this. Windows refuses a rename onto a file another writer has just
		// placed, which is what twelve copies of one resent photograph
		// arriving together produced.
		if same, _ := sameContent(full, body); same {
			return nil
		}
		return fmt.Errorf("storing evidence: %w", err)
	}
	return nil
}

// PutStream writes an object from a reader. See Streamer.
func (d *Dir) PutStream(_ context.Context, key, _ string, r io.Reader, size int64) error {
	full, err := d.path(key)
	if err != nil {
		return err
	}
	if info, err := os.Stat(full); err == nil && info.Size() == size {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("creating evidence directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".partial-*")
	if err != nil {
		return fmt.Errorf("creating evidence file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	n, err := io.Copy(tmp, r)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("writing evidence: %w", err)
	}
	if n != size {
		tmp.Close()
		return fmt.Errorf("writing evidence: wrote %d bytes, expected %d", n, size)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flushing evidence: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing evidence: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("setting evidence permissions: %w", err)
	}
	var rerr error
	for attempt := 0; attempt < 6; attempt++ {
		if rerr = os.Rename(tmpName, full); rerr == nil {
			return nil
		}
		// A concurrent writer of the same content-addressed object.
		if info, err := os.Stat(full); err == nil && info.Size() == size {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 15 * time.Millisecond)
	}
	return fmt.Errorf("storing evidence: %w", rerr)
}

// renameSettled renames, tolerating a concurrent writer of the same bytes.
//
// On Windows a rename onto a path that another writer is renaming onto at the
// same instant fails with a sharing violation that clears within milliseconds.
// A handful of short retries, each first checking whether the other writer
// already put the identical bytes there, turns that race into the success it
// is. On Linux the first rename succeeds and none of this runs.
func renameSettled(tmp, full string, body []byte) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if err = os.Rename(tmp, full); err == nil {
			return nil
		}
		if same, _ := sameContent(full, body); same {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 15 * time.Millisecond)
	}
	return err
}

// sameContent reports whether the file at path holds exactly body.
func sameContent(path string, body []byte) (bool, error) {
	info, err := os.Stat(path)
	if err != nil || info.Size() != int64(len(body)) {
		return false, err
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return bytes.Equal(existing, body), nil
}

func (d *Dir) Get(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := d.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("opening evidence: %w", err)
	}
	return f, nil
}

func (d *Dir) Delete(_ context.Context, key string) error {
	full, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing evidence: %w", err)
	}
	return nil
}

func (d *Dir) Describe() string { return "local directory " + d.root }

// Remote reports whether the evidence survives this machine. It does not.
//
// A working configuration and a fragile one: every photograph a claim rests on
// lives on one server's disk, and losing that server loses the evidence for
// every open claim. The operator console says so rather than reporting a
// directory as though it were storage.
func (d *Dir) Remote() bool { return false }

// Check confirms the directory is still writable.
//
// A read-only remount or a full disk is the realistic way this store fails,
// and both are silent until the first capture of the day is lost.
func (d *Dir) Check(_ context.Context) error {
	f, err := os.CreateTemp(d.root, ".health-*")
	if err != nil {
		return fmt.Errorf("evidence directory is not writable: %w", err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
