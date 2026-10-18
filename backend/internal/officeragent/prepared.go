package officeragent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Holding a document between the sentence that asked for it and the click that
// downloads it.
//
// # What this is for
//
// The assistant prepares an export inside a conversation and the officer
// downloads it a moment later, from a button beside the answer. Those are two
// requests, so something has to hold the bytes in between. That is the whole of
// what this does.
//
// # Why the bytes, and not the recipe
//
// Keeping the request instead -- the format, the columns, the filters -- and
// re-rendering on download would be a few hundred bytes of state rather than a
// few hundred kilobytes. It is the wrong answer. The sentence the officer is
// reading says forty-two claims, and a document rendered again ten minutes later
// may hold forty-three: the queue moves. A statement whose own covering sentence
// miscounts it is worse than no statement, because the officer cannot tell which
// of the two numbers to believe. So the document is rendered once, and what is
// downloaded is the document that was described.
//
// # Why Redis rather than this process's memory
//
// Because the API is two replicas minimum behind one load balancer, and nothing
// routes a download to the replica that happened to prepare it -- see
// docs/PRODUCTION_TOPOLOGY.md, which puts it plainly: everything the API
// coordinates is in Postgres or Redis, not in a process's memory. In process
// memory the button beside the answer would fail about half the time, and not
// reproducibly. That is not a trade-off, it is a defect.
//
// Redis is also where this platform already keeps this exact kind of fact:
// transient state whose TTL is the enforcement rather than a cleanup job. See
// internal/platform/cache.
//
// What in-process memory would have bought is worth naming, because it is the
// cost of this choice. That Redis runs noeviction, sized for sessions and
// one-time codes, and a full one is an error on the sign-in path -- so a
// prepared document is bounded twice, by count and by bytes, and the worst one
// officer can pin is MaxPreparedBytesPerOfficer. An operator should read that
// number as the per-officer line in the Redis budget and raise REDIS_MAXMEMORY
// before putting this in front of a state.
//
// # Why the officer is in the key
//
// Not checked after the lookup. Composed into it. There is no accessor here that
// takes an id on its own, so another officer holding somebody else's id reads a
// key that was never written and is told there is no such export. A check can be
// forgotten at a second call site; a key that cannot be built without the owner
// cannot be.

// PreparedTTL is how long a prepared document waits to be fetched.
//
// Ten minutes. The download happens in the same breath as reading the answer, so
// this is sized for an interruption -- a phone call, somebody at the desk -- and
// not for a morning. Longer would squat on the Redis budget for a click that is
// not coming, and would hand an officer a statement of a queue that has since
// moved; asking again is one sentence and gives them the figures as they are now.
const PreparedTTL = 10 * time.Minute

// MaxPreparedPerOfficer bounds how many documents one officer may have waiting.
//
// Six is a conversation that changed its mind five times -- make that Excel, add
// the village, only Barshi -- which is exactly the conversation this feature is
// for. Past it the oldest goes, and in a conversation like that the oldest is the
// one that was already corrected.
const MaxPreparedPerOfficer = 6

// MaxPreparedBytesPerOfficer is the other half of the bound, and the half that
// matters to the deployment.
//
// Three mebibytes, from measurement rather than estimate. A document at the row
// cap, every column, Marathi names throughout, weighs:
//
//	xlsx     47 KB      docx    48 KB
//	pptx    169 KB      csv    230 KB
//	pdf     423 KB   (rendered by a real Gotenberg, not a stub)
//
// So the largest a thousand-row statement can be is the PDF at 423 KB, and the
// six documents above come to 2.5 MB. Three mebibytes covers that and refuses
// nothing this service can produce.
//
// It was eight, on an estimate of "one or two megabytes as a PDF" that turned
// out to be four times the truth. That mattered: this is the most one officer
// can pin in a Redis that runs noeviction, is sized for sessions and codes, and
// signs people out when it fills. A cap three times larger than anything that
// can be stored under it is not caution, it is a hundred and twenty-eight
// officers' worth of headroom nobody needed.
//
// A single document larger than this is refused outright rather than stored:
// there would be nothing left to evict to make room for it.
const MaxPreparedBytesPerOfficer = 3 << 20

// measuredLargestDocumentBytes is the biggest document measured at the row cap.
//
// The PDF above. Kept as a number so the budget's arithmetic is checkable rather
// than asserted in a comment -- see the test that multiplies it by the document
// count. Remeasure it if the exporter learns a new format or the row cap moves;
// TestTheMeasuredFiguresStillHoldAgainstARealConverter does that against a
// Gotenberg when one is configured.
const measuredLargestDocumentBytes = 433560

// Prepared is one document the conversation produced, as the console is told
// about it.
//
// No bytes and no address. The id is what the download route takes, and the rest
// is what a button says: this file, this many claims, this big.
type Prepared struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Format   string `json:"format"`
	Rows     int    `json:"rows"`
	Bytes    int    `json:"bytes"`
}

// PreparedFile is a document that is waiting, bytes and all.
type PreparedFile struct {
	Prepared
	ContentType string
	Body        []byte
}

// ErrNoPreparedExport means there is nothing under that id for this officer.
//
// One answer for expired, evicted, never existed and belongs to somebody else --
// deliberately, and for the reason get_claim gives: distinguishing them would
// tell an officer that a document exists which is not theirs.
var ErrNoPreparedExport = errors.New("no prepared export with that id")

// PreparedTooLargeError is the refusal a document too big to park gets.
//
// Phrased with both numbers because the caller is usually the model, which can
// act on it in the same turn: fewer columns, or a narrower set of claims.
type PreparedTooLargeError struct {
	Bytes int
	Limit int
}

func (e PreparedTooLargeError) Error() string {
	return fmt.Sprintf("that document came to %s, and a prepared download may "+
		"be at most %s; ask for fewer columns, or narrow the claims",
		humanBytes(int64(e.Bytes)), humanBytes(int64(e.Limit)))
}

// Exports holds prepared documents, each under the officer who prepared it.
type Exports struct {
	kv  preparedKV
	now func() time.Time
}

// NewRedisExports is the one a deployment uses. See the file comment for why.
func NewRedisExports(rdb *redis.Client) *Exports {
	return newExports(redisKV{rdb: rdb}, time.Now)
}

// NewMemoryExports keeps prepared documents in this process.
//
// The policy below -- the TTL, the caps, the eviction, the owner in the key -- is
// the same whichever backing is used, which is what makes this worth having: it
// is how that policy is tested without a server, and it is all a single-process
// run needs. A deployment of more than one replica must not use it.
func NewMemoryExports() *Exports {
	return newExports(newMemoryKV(time.Now), time.Now)
}

// newExports is the constructor with the clock in it, for the tests of the TTL.
func newExports(kv preparedKV, now func() time.Time) *Exports {
	return &Exports{kv: kv, now: now}
}

// Keep files a finished document under the officer who asked for it.
//
// [format] is what it is, which Export itself does not carry: it holds a content
// type and a filename, and the console wants the word the officer used.
func (x *Exports) Keep(
	ctx context.Context, owner string, format Format, out *Export,
) (Prepared, error) {
	if owner == "" {
		// There is nothing to key it under, and a blank owner would file every
		// such document where every other blank owner could read it.
		return Prepared{}, errors.New("a prepared export needs an owner")
	}
	if len(out.Bytes) > MaxPreparedBytesPerOfficer {
		return Prepared{}, PreparedTooLargeError{
			Bytes: len(out.Bytes), Limit: MaxPreparedBytesPerOfficer,
		}
	}

	id, err := preparedID()
	if err != nil {
		return Prepared{}, err
	}
	p := Prepared{
		ID:       id,
		Filename: out.Filename,
		Format:   string(format),
		Rows:     out.Rows,
		Bytes:    len(out.Bytes),
	}

	stored, err := encodePrepared(PreparedFile{
		Prepared: p, ContentType: out.ContentType, Body: out.Bytes,
	})
	if err != nil {
		return Prepared{}, err
	}

	notes, err := x.index(ctx, owner)
	if err != nil {
		return Prepared{}, err
	}
	notes, err = x.evict(ctx, owner, notes, len(stored))
	if err != nil {
		return Prepared{}, err
	}

	// The index is written before the document, so that a failure leaves nothing
	// stored rather than a document nothing is accounting for. The opposite
	// leftover -- a note for a document that was never written -- costs an
	// eviction decision made against a few kilobytes that are not there, for ten
	// minutes, and is what the pruning below is written to tolerate.
	notes = append(notes, preparedNote{
		ID: id, Bytes: len(stored), At: x.now(),
	})
	indexed, err := json.Marshal(notes)
	if err != nil {
		return Prepared{}, fmt.Errorf("recording the prepared export: %w", err)
	}
	if err := x.kv.set(ctx, indexKey(owner), indexed, PreparedTTL); err != nil {
		return Prepared{}, fmt.Errorf("recording the prepared export: %w", err)
	}
	if err := x.kv.set(ctx, docKey(owner, id), stored, PreparedTTL); err != nil {
		return Prepared{}, fmt.Errorf("storing the prepared export: %w", err)
	}
	return p, nil
}

// Fetch hands back a prepared document, and leaves it where it is.
//
// # Why a download is not one-shot
//
// Deleting on the first read looks like the tidier choice: the bytes go as soon
// as they have been used, and an id that leaked into a log opens nothing
// afterwards. It is the wrong choice for this office. A download of several
// hundred kilobytes over a taluka line gets interrupted and the browser retries
// it; an antivirus fetches the file before the browser does; an officer
// double-clicks the button. In every one of those the second request is the one
// that matters, and one-shot turns each into "that export is gone" for a document
// the officer was just told was ready.
//
// It would buy little against theft in any case. The id is not a capability on
// its own: the key is composed from the officer's own user id, and the route in
// front of this wants their session and report:read. What bounds the exposure is
// the ten minutes and the caps, not the single use.
func (x *Exports) Fetch(
	ctx context.Context, owner, id string,
) (*PreparedFile, error) {
	if owner == "" || !validPreparedID(id) {
		// An id that cannot have been issued cannot be stored under one, so this
		// is not found rather than a complaint about its shape -- and nothing
		// built from a path parameter reaches a key.
		return nil, ErrNoPreparedExport
	}
	raw, err := x.kv.get(ctx, docKey(owner, id))
	if err != nil {
		return nil, fmt.Errorf("reading the prepared export: %w", err)
	}
	if raw == nil {
		return nil, ErrNoPreparedExport
	}
	return decodePrepared(raw)
}

// preparedNote is one entry in an officer's index of what is waiting.
//
// Only what the caps are decided on: how big it was and when it was prepared.
// The filename and the row count are in the document's own header, where the
// download reads them.
type preparedNote struct {
	ID    string    `json:"id"`
	Bytes int       `json:"bytes"`
	At    time.Time `json:"at"`
}

// index reads an officer's entries, oldest first, with the expired ones dropped.
//
// Expiry is computed from the time each was prepared rather than probed for,
// which is exact -- the TTL is a constant -- and costs no round trip per entry.
// The authority on whether a document is still there is the document's own key,
// which is the only thing Fetch reads.
func (x *Exports) index(ctx context.Context, owner string) ([]preparedNote, error) {
	raw, err := x.kv.get(ctx, indexKey(owner))
	if err != nil {
		return nil, fmt.Errorf("reading the prepared exports: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	var notes []preparedNote
	if err := json.Unmarshal(raw, &notes); err != nil {
		// An index this cannot read is an index to start again from. Refusing the
		// officer their export over it would be a worse answer than applying the
		// caps to the documents prepared from here on.
		return nil, nil
	}

	cutoff := x.now().Add(-PreparedTTL)
	kept := make([]preparedNote, 0, len(notes))
	for _, n := range notes {
		if n.At.After(cutoff) {
			kept = append(kept, n)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].At.Before(kept[j].At) })
	return kept, nil
}

// evict makes room for one more document, oldest first.
//
// Oldest rather than largest, because this is a conversation: the officer who has
// asked for six documents has corrected themselves five times, and the one they
// are least likely to want is the one they asked for first.
func (x *Exports) evict(
	ctx context.Context, owner string, notes []preparedNote, incoming int,
) ([]preparedNote, error) {
	total := 0
	for _, n := range notes {
		total += n.Bytes
	}

	for len(notes) > 0 &&
		(len(notes)+1 > MaxPreparedPerOfficer ||
			total+incoming > MaxPreparedBytesPerOfficer) {
		oldest := notes[0]
		if err := x.kv.del(ctx, docKey(owner, oldest.ID)); err != nil {
			return nil, fmt.Errorf("making room for the prepared export: %w", err)
		}
		notes = notes[1:]
		total -= oldest.Bytes
	}
	return notes, nil
}

// preparedDocPrefix and preparedIndexPrefix keep the two kinds of key apart.
//
// Distinct words rather than one prefix and a positional difference, so that no
// officer id, however strange, can make a document key read as an index key.
const (
	preparedDocPrefix   = "agentexp:doc:"
	preparedIndexPrefix = "agentexp:idx:"
)

func docKey(owner, id string) string { return preparedDocPrefix + owner + ":" + id }
func indexKey(owner string) string   { return preparedIndexPrefix + owner }

// preparedID is the name a prepared document is fetched by.
//
// Sixteen bytes from crypto/rand, hex. It is not a secret -- the owner is in the
// key and the route wants a session -- but it must be unguessable all the same,
// because an id that can be enumerated turns "only the officer who prepared it"
// into "the officer who prepared it, and anyone who counts".
func preparedID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("naming the prepared export: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// validPreparedID reports whether a string is the shape this issues.
//
// Checked before a key is built from it. The id arrives as a path parameter,
// which is a caller speaking, and nothing a caller sends should reach a key
// without having been looked at.
func validPreparedID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// preparedHeader is what is stored in front of the bytes.
type preparedHeader struct {
	Prepared
	ContentType string `json:"content_type"`
}

// encodePrepared puts a document and what it is into one value.
//
// A JSON header, a newline, then the bytes exactly as they will be served. One
// value rather than two keys because a header without its body, or a body whose
// header has expired, are states nothing here would know what to do with. The
// separator is safe: JSON escapes a newline inside any string it holds, so the
// first one in the value is always the end of the header.
func encodePrepared(f PreparedFile) ([]byte, error) {
	head, err := json.Marshal(preparedHeader{
		Prepared: f.Prepared, ContentType: f.ContentType,
	})
	if err != nil {
		return nil, fmt.Errorf("describing the prepared export: %w", err)
	}
	out := make([]byte, 0, len(head)+1+len(f.Body))
	out = append(out, head...)
	out = append(out, '\n')
	return append(out, f.Body...), nil
}

func decodePrepared(raw []byte) (*PreparedFile, error) {
	cut := bytes.IndexByte(raw, '\n')
	if cut < 0 {
		return nil, errors.New("a prepared export was stored without its header")
	}
	var head preparedHeader
	if err := json.Unmarshal(raw[:cut], &head); err != nil {
		return nil, fmt.Errorf("reading what the prepared export is: %w", err)
	}
	return &PreparedFile{
		Prepared:    head.Prepared,
		ContentType: head.ContentType,
		Body:        raw[cut+1:],
	}, nil
}

// preparedKV is the little that the policy above needs of a store.
//
// Get, set with a lifetime, delete. Narrow on purpose: everything that decides
// what an officer may hold is written once, above, and what differs between a
// deployment and a test is only where the bytes sit.
type preparedKV interface {
	// get returns nil, nil when there is nothing under the key.
	get(ctx context.Context, key string) ([]byte, error)
	set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	del(ctx context.Context, keys ...string) error
}

// redisKV is the backing a deployment gets.
type redisKV struct{ rdb *redis.Client }

func (k redisKV) get(ctx context.Context, key string) ([]byte, error) {
	out, err := k.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (k redisKV) set(
	ctx context.Context, key string, value []byte, ttl time.Duration,
) error {
	return k.rdb.Set(ctx, key, value, ttl).Err()
}

func (k redisKV) del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return k.rdb.Del(ctx, keys...).Err()
}

// memoryKV is the backing a test gets, and a single process could.
//
// The clock is shared with the store above it, so that a test which moves time
// forward moves it for the expiry and for the index's pruning together.
type memoryKV struct {
	now func() time.Time

	mu   sync.Mutex
	held map[string]memoryEntry
}

type memoryEntry struct {
	value []byte
	until time.Time
}

func newMemoryKV(now func() time.Time) *memoryKV {
	return &memoryKV{now: now, held: map[string]memoryEntry{}}
}

func (k *memoryKV) get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	e, ok := k.held[key]
	if !ok {
		return nil, nil
	}
	if !k.now().Before(e.until) {
		// Dropped on the way past rather than by a sweeper. Redis does the same
		// thing, and a backing that kept expired values until something came
		// looking for them would not be the same policy.
		delete(k.held, key)
		return nil, nil
	}
	return e.value, nil
}

func (k *memoryKV) set(
	_ context.Context, key string, value []byte, ttl time.Duration,
) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.held[key] = memoryEntry{value: value, until: k.now().Add(ttl)}
	return nil
}

func (k *memoryKV) del(_ context.Context, keys ...string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, key := range keys {
		delete(k.held, key)
	}
	return nil
}
