package api

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// The lockout, which is what makes a date of birth safe to ask for.
//
// A Farmer ID is printed on documents and known around a village, so the date
// is carrying the whole weight of the check -- one low-entropy fact, and in
// this registry's own sample data it is 1 January. Roughly fourteen thousand
// candidate days is nothing to a script and not much to a patient relative.
//
// So the count is the control, and these are about the two ways it fails
// open: not counting, and not counting when the cache is unreachable.
//
// Set ESURVEY_TEST_REDIS_ADDR (and ESURVEY_TEST_REDIS_PASSWORD). Database 15,
// flushed.

func lockoutServer(t *testing.T) *Server {
	t.Helper()
	addr := os.Getenv("ESURVEY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set ESURVEY_TEST_REDIS_ADDR to run the lockout tests")
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
	return &Server{Redis: rdb, Log: slog.New(slog.DiscardHandler)}
}

func TestAnAccountIsStoppedAfterAHandfulOfWrongAnswers(t *testing.T) {
	s := lockoutServer(t)
	ctx := context.Background()

	locked, err := s.lookupLocked(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		t.Fatal("an account was locked out before it had answered anything")
	}

	// A farmer who misremembers the format has a few tries.
	for i := 0; i < lookupFailureBudget-1; i++ {
		if err := s.countLookupFailure(ctx, "acct-1"); err != nil {
			t.Fatal(err)
		}
		locked, err := s.lookupLocked(ctx, "acct-1")
		if err != nil {
			t.Fatal(err)
		}
		if locked {
			t.Fatalf("locked out after %d attempts; the budget is %d and a "+
				"farmer typing a date wrong is not an attacker",
				i+1, lookupFailureBudget)
		}
	}

	// And then does not.
	if err := s.countLookupFailure(ctx, "acct-1"); err != nil {
		t.Fatal(err)
	}
	locked, err = s.lookupLocked(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Errorf("still not locked out after %d wrong answers; without this "+
			"a year of dates is a few minutes' work", lookupFailureBudget)
	}
}

func TestOneAccountsLockoutDoesNotStopAnother(t *testing.T) {
	// A village shares a tower and an app. One person mistyping their date
	// must not shut the next person out of their own record.
	s := lockoutServer(t)
	ctx := context.Background()

	for i := 0; i < lookupFailureBudget+2; i++ {
		if err := s.countLookupFailure(ctx, "acct-1"); err != nil {
			t.Fatal(err)
		}
	}

	locked, err := s.lookupLocked(ctx, "acct-2")
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		t.Error("a second account was locked out by the first's attempts")
	}
}

func TestTheWindowIsNotExtendedByGuessingInIt(t *testing.T) {
	// ExpireNX, not Expire. Refreshing the window on every attempt would let
	// an account that keeps guessing hold its own lockout open for ever --
	// harmless -- but the same mistake the other way round, resetting the
	// count, is what would make the budget meaningless.
	s := lockoutServer(t)
	ctx := context.Background()

	if err := s.countLookupFailure(ctx, "acct-1"); err != nil {
		t.Fatal(err)
	}
	first, err := s.Redis.TTL(ctx, lookupFailureKey("acct-1")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if first <= 0 {
		t.Fatalf("the counter has no expiry (%v); it would never clear", first)
	}

	for i := 0; i < 3; i++ {
		if err := s.countLookupFailure(ctx, "acct-1"); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.Redis.Get(ctx, lookupFailureKey("acct-1")).Int()
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("count = %d after four attempts; every one of them has to "+
			"be counted", n)
	}

	again, err := s.Redis.TTL(ctx, lookupFailureKey("acct-1")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if again > first {
		t.Errorf("the window grew from %v to %v while being spent", first, again)
	}
}

func TestWithNoCounterTheLookupIsRefusedRatherThanAllowed(t *testing.T) {
	// The one place in this codebase that fails CLOSED. Everywhere else a
	// cache outage lets the request through, because a farmer blocked by a
	// Redis restart is a worse failure than the one being prevented. Here it
	// is the opposite: no counter means no lockout, and no lockout means an
	// unlimited guessing window on somebody's land records.
	s := &Server{Redis: nil, Log: slog.New(slog.DiscardHandler)}

	if _, err := s.lookupLocked(context.Background(), "acct-1"); err == nil {
		t.Error("with no counter the lookup reported itself unlocked; the " +
			"handler reads that as permission to check the date")
	}
}
