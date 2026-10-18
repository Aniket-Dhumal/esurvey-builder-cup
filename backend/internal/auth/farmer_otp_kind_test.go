package auth

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
)

// The farmer sign-in route is for farmers.
//
// An officer signs in with a password, and every account carries a mobile
// number. If a code sent to that number could also open the account, the
// password stops being what protects a district's authority -- and the code
// travels over SMS, or in development into a log file. LoginOfficer has
// refused the mirror case (a farmer at the password route) since it was
// written; these tests hold the other half.
//
// Both a database and a Redis are needed: the accounts are real rows and the
// codes are real Redis records, and a fake of either would only prove the fake
// agrees with itself.

// capturedSender keeps the code instead of delivering it, which is the only
// way a test can complete a sign-in.
type capturedSender struct{ code string }

func (c *capturedSender) Name() string { return "test" }
func (c *capturedSender) Send(_ context.Context, _, code string) error {
	c.code = code
	return nil
}

func otpService(t *testing.T) (*Service, *capturedSender, *pgxpool.Pool) {
	t.Helper()

	dbURL := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the farmer sign-in tests")
	}
	addr := os.Getenv("ESURVEY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set ESURVEY_TEST_REDIS_ADDR to run the farmer sign-in tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)

	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("ESURVEY_TEST_REDIS_PASSWORD"),
		DB:       15,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("no redis: %v", err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rdb.FlushDB(context.Background()).Err()
		_ = rdb.Close()
	})

	// The accounts these tests use, removed before and after so a failed run
	// does not poison the next one.
	//
	// Matched on the login as well as the handset. Keying on the number alone
	// was enough until a test could take the number off an account -- that row
	// then survived every cleanup and collided with the next run on its
	// username, which is how this was found. The invalid domain is reserved
	// for testing and cannot belong to a real officer.
	const mine = `mobile_e164 IN ('+919000000001', '+919000000002', '+919000000003')
		OR username LIKE '%@test.invalid'`
	clean := func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id IN
			(SELECT id FROM users WHERE `+mine+`)`)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE `+mine)
	}
	clean()
	t.Cleanup(clean)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := audit.New(pool, log)
	t.Cleanup(func() { _ = rec.Close(context.Background()) })

	sender := &capturedSender{}
	svc := &Service{
		Users:    identity.NewStore(pool),
		Sessions: NewSessions(rdb),
		Throttle: NewThrottle(rdb, 5, time.Hour, 15*time.Minute),
		OTP:      NewOTP(rdb, sender, 6, 5*time.Minute, 5, 0, 10),
		Keys:     testKeyring(t),
		Audit:    rec,
		Log:      log,

		AccessTTL:         15 * time.Minute,
		OfficerRefreshTTL: time.Hour,
		FarmerRefreshTTL:  24 * time.Hour,
	}
	return svc, sender, pool
}

// makeAccount writes one account of the given kind. SQL rather than the
// store's constructors because CreateOfficer takes no mobile number -- which
// is the point: an officer's number arrives by other routes, and this test is
// about what happens once it is there.
func makeAccount(t *testing.T, pool *pgxpool.Pool, kind, name, mobile string) {
	t.Helper()
	// An officer's login is their email address, lower case, and the table
	// enforces both (migration 0046). A farmer has neither.
	var username, email, hash any
	if kind == "officer" {
		username = "officer" + mobile[len(mobile)-4:] + "@test.invalid"
		email = username
		hash = dummyHash
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO users (kind, status, full_name, mobile_e164, username, email, password_hash)
		VALUES ($1::user_kind, 'active', $2, $3, $4, $5, $6)`,
		kind, name, mobile, username, email, hash)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnOfficersHandsetIsSentNoCode(t *testing.T) {
	svc, sender, pool := otpService(t)
	const mobile = "+919000000001"
	makeAccount(t, pool, "officer", "Test Officer", mobile)

	// Success, because the route never says whether a number is known.
	if err := svc.RequestFarmerOTP(context.Background(), mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatalf("the route should refuse silently, got %v", err)
	}
	if sender.code != "" {
		t.Fatal("a code was sent to an officer's handset")
	}
}

func TestAnOfficerIsToldWhereToSignInWhereThatIsAllowed(t *testing.T) {
	svc, sender, pool := otpService(t)
	const mobile = "+919000000001"
	makeAccount(t, pool, "officer", "Test Officer Three", mobile)

	// A trial or a laptop: the people typing numbers are the team, and four
	// silent refusals is how this was found.
	svc.NameOfficerHandsets = true
	err := svc.RequestFarmerOTP(context.Background(), mobile, Caller{IP: "1.2.3.4"})
	if !errors.Is(err, ErrOfficerHandset) {
		t.Fatalf("the officer was not told where to sign in: %v", err)
	}
	if sender.code != "" {
		t.Fatal("a code was sent to an officer's handset")
	}
}

func TestAHardenedDeploymentSaysNothingAboutOfficers(t *testing.T) {
	svc, sender, pool := otpService(t)
	const mobile = "+919000000002"
	makeAccount(t, pool, "officer", "Test Officer Four", mobile)

	// The default, and what production runs: the same sentence would answer
	// "is this number a revenue officer's" for any number typed at it.
	svc.NameOfficerHandsets = false
	if err := svc.RequestFarmerOTP(context.Background(), mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatalf("the refusal should be silent, got %v", err)
	}
	if sender.code != "" {
		t.Fatal("a code was sent to an officer's handset")
	}
}

func TestAnOfficersCodeIsNotASession(t *testing.T) {
	svc, sender, pool := otpService(t)
	const mobile = "+919000000002"
	makeAccount(t, pool, "officer", "Test Officer Two", mobile)

	// A code issued directly, as though the account had been a farmer when it
	// was asked for: the verify step must not lean on the request step having
	// refused.
	if _, err := svc.OTP.Request(context.Background(), mobile); err != nil {
		t.Fatal(err)
	}
	if sender.code == "" {
		t.Fatal("the test sender captured no code")
	}

	_, _, err := svc.VerifyFarmerOTP(
		context.Background(), mobile, sender.code, Caller{IP: "1.2.3.4"})
	if !errors.Is(err, ErrOTPIncorrect) {
		t.Fatalf("a correct code opened an officer account: %v", err)
	}
}

func TestAFarmersHandsetStillSignsIn(t *testing.T) {
	svc, sender, pool := otpService(t)
	const mobile = "+919000000001"
	makeAccount(t, pool, "farmer", "Test Farmer", mobile)

	ctx := context.Background()
	if err := svc.RequestFarmerOTP(ctx, mobile, Caller{IP: "1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	if sender.code == "" {
		t.Fatal("no code was sent to a farmer's handset")
	}

	tokens, user, err := svc.VerifyFarmerOTP(ctx, mobile, sender.code, Caller{IP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("a farmer could not sign in: %v", err)
	}
	if tokens.AccessToken == "" {
		t.Fatal("no access token")
	}
	if user.FullName != "Test Farmer" {
		t.Fatalf("signed in as %q", user.FullName)
	}
}
