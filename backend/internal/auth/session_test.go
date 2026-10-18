package auth

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Refresh rotation against a real Redis: the guarantees are WATCH and
// transactions, which a fake would only pretend to have.
//
// Set ESURVEY_TEST_REDIS_ADDR (and ESURVEY_TEST_REDIS_PASSWORD). Tests use
// database 15 and flush it.

func testSessions(t *testing.T) (*Sessions, *redis.Client) {
	t.Helper()
	addr := os.Getenv("ESURVEY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set ESURVEY_TEST_REDIS_ADDR to run the session tests")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("ESURVEY_TEST_REDIS_PASSWORD"),
		DB:       15,
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("no redis: %v", err)
	}
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rdb.FlushDB(context.Background()).Err()
		_ = rdb.Close()
	})
	return NewSessions(rdb), rdb
}

const ttl = 30 * 24 * time.Hour

func start(t *testing.T, s *Sessions) *Issued {
	t.Helper()
	is, err := s.Start(context.Background(), "user-1", "1.2.3.4", "test", ttl)
	if err != nil {
		t.Fatal(err)
	}
	return is
}

func rotate(t *testing.T, s *Sessions, token string) (*Issued, error) {
	t.Helper()
	is, _, err := s.Rotate(context.Background(), token, "1.2.3.4", "test", ttl)
	return is, err
}

func liveTokens(t *testing.T, rdb *redis.Client, family string) int64 {
	t.Helper()
	n, err := rdb.SCard(context.Background(), keyPrefixFamily+family).Result()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnOrdinaryRotationIssuesASuccessor(t *testing.T) {
	s, rdb := testSessions(t)
	t0 := start(t, s)

	t1, err := rotate(t, s, t0.Token)
	if err != nil {
		t.Fatal(err)
	}
	if t1.Token == t0.Token || t1.FamilyID != t0.FamilyID {
		t.Fatal("the successor is not a new token in the same family")
	}
	if n := liveTokens(t, rdb, t0.FamilyID); n != 1 {
		t.Fatalf("%d live tokens in the family; want 1", n)
	}
}

func TestAReplyLostOnTheWayIsForgiven(t *testing.T) {
	// The server rotated; the phone never heard and presents the old token.
	s, rdb := testSessions(t)
	t0 := start(t, s)

	lost, err := rotate(t, s, t0.Token)
	if err != nil {
		t.Fatal(err)
	}
	_ = lost // never delivered

	again, err := rotate(t, s, t0.Token)
	if err != nil {
		t.Fatalf("a retry after a lost reply was refused: %v", err)
	}
	if n := liveTokens(t, rdb, t0.FamilyID); n != 1 {
		t.Fatalf("%d live tokens after the retry; want 1", n)
	}
	// The undelivered successor is gone as a live token...
	if exists, _ := rdb.Exists(context.Background(), keyPrefixToken+hashToken(lost.Token)).Result(); exists != 0 {
		t.Fatal("the successor that never arrived is still live alongside its replacement")
	}
	// ...and the one that was delivered works.
	if _, err := rotate(t, s, again.Token); err != nil {
		t.Fatalf("the replacement does not rotate: %v", err)
	}
}

func TestTwoProcessesOnOnePhoneDoNotSignTheFarmerOut(t *testing.T) {
	// The worker rotates t0 -> w. The app, still holding t0, refreshes: a.
	// The worker then refreshes with w, which a replaced.
	s, rdb := testSessions(t)
	t0 := start(t, s)

	w, err := rotate(t, s, t0.Token)
	if err != nil {
		t.Fatal(err)
	}
	a, err := rotate(t, s, t0.Token)
	if err != nil {
		t.Fatalf("the app's refresh signed the farmer out: %v", err)
	}
	w2, err := rotate(t, s, w.Token)
	if err != nil {
		t.Fatalf("the worker's next refresh signed the farmer out: %v", err)
	}
	_ = a
	if n := liveTokens(t, rdb, t0.FamilyID); n != 1 {
		t.Fatalf("%d live tokens; want 1", n)
	}
	if _, err := rotate(t, s, w2.Token); err != nil {
		t.Fatalf("the surviving token does not rotate: %v", err)
	}
}

func TestAnOldTokenWhoseSuccessorWasUsedIsTheft(t *testing.T) {
	s, rdb := testSessions(t)
	t0 := start(t, s)

	t1, err := rotate(t, s, t0.Token)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := rotate(t, s, t1.Token) // the farmer carried on normally
	if err != nil {
		t.Fatal(err)
	}

	// Somebody presents t0: its successor has been used, so this is not a lost
	// reply.
	if _, err := rotate(t, s, t0.Token); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("got %v, want ErrRefreshReused", err)
	}
	if n := liveTokens(t, rdb, t0.FamilyID); n != 0 {
		t.Fatalf("%d live tokens after a reuse; the family should be revoked", n)
	}
	if _, err := rotate(t, s, t2.Token); err == nil {
		t.Fatal("the current token still works after the family was revoked")
	}
}

func TestTheGraceDoesNotLastForEver(t *testing.T) {
	s, rdb := testSessions(t)
	t0 := start(t, s)
	t1, err := rotate(t, s, t0.Token)
	if err != nil {
		t.Fatal(err)
	}

	// Four days ago.
	old := tombstone{
		family: t0.FamilyID,
		next:   hashToken(t1.Token),
		at:     time.Now().Add(-96 * time.Hour),
	}
	if err := rdb.Set(context.Background(), keyPrefixUsed+hashToken(t0.Token),
		old.String(), time.Hour).Err(); err != nil {
		t.Fatal(err)
	}

	if _, err := rotate(t, s, t0.Token); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("got %v, want ErrRefreshReused past the grace window", err)
	}
}

func TestSimultaneousRefreshesLeaveOneLiveToken(t *testing.T) {
	// Rotation used to read and then write; two refreshes at the same instant
	// could both mint.
	s, rdb := testSessions(t)
	t0 := start(t, s)

	const copies = 20
	var wg sync.WaitGroup
	errs := make([]error, copies)
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = rotate(t, s, t0.Token)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("refresh %d: %v", i, err)
		}
	}
	if n := liveTokens(t, rdb, t0.FamilyID); n != 1 {
		t.Fatalf("%d live tokens after %d simultaneous refreshes; want 1", n, copies)
	}
}

func TestASpentTokenIsNotRememberedForAMonth(t *testing.T) {
	s, rdb := testSessions(t)
	t0 := start(t, s)
	if _, err := rotate(t, s, t0.Token); err != nil {
		t.Fatal(err)
	}
	left, err := rdb.TTL(context.Background(), keyPrefixUsed+hashToken(t0.Token)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if left > ReuseGrace || left <= 0 {
		t.Fatalf("tombstone kept for %s; want at most %s", left, ReuseGrace)
	}
	v, _ := rdb.Get(context.Background(), keyPrefixUsed+hashToken(t0.Token)).Result()
	if len(v) > 32+1+64+1+len(strconv.FormatInt(time.Now().Unix(), 10)) {
		t.Fatalf("tombstone is %d bytes: %q", len(v), v)
	}
}
