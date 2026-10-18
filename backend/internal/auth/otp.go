package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Farmer authentication is a one-time code to a registered handset.
//
// A farmer has no password to remember and no office to reset one at. The
// mobile number is already in the registry, which makes the handset the only
// credential the system can check without asking them to hold something new.
//
// Three limits, each closing a different hole:
//
//   - attempts per code, so a six-digit secret cannot be guessed;
//   - a resend cooldown, so the endpoint is not a free SMS gun pointed at a
//     stranger's phone;
//   - a daily cap per number, so it is not a slow one either.
//
// Only the hash of a code is stored, for the same reason passwords are
// hashed: whoever can read Redis should not thereby be able to sign in.

// OTP errors.
var (
	ErrOTPCooldown    = errors.New("a code was sent recently")
	ErrOTPDailyCap    = errors.New("too many codes requested for this number today")
	ErrOTPNotFound    = errors.New("no code was requested, or it has expired")
	ErrOTPAttempts    = errors.New("too many incorrect attempts")
	ErrOTPIncorrect   = errors.New("incorrect code")
	ErrUnknownHandset = errors.New("no account is registered to that number")

	// ErrOfficerHandset is an officer's number at the farmer sign-in.
	//
	// Returned only where Service.NameOfficerHandsets is set, because saying
	// so out loud turns this route into a way to ask "is this number a
	// revenue officer's" for any number somebody cares to type -- and the
	// answer is worth having if you want to phish or SIM-swap the people who
	// approve claims. Silence costs an officer who opens the wrong app a
	// confusing minute; speech costs every officer in the state a small,
	// permanent increase in how findable they are.
	ErrOfficerHandset = errors.New("that number signs in on the officer console")

	// ErrChannelDown is the code not being sendable now, for a reason that
	// passes: the gateway is unreachable, rate limiting, or out of credit.
	// Worth trying again in a few minutes, and the farmer should be told to.
	ErrChannelDown = errors.New("codes cannot be sent just now")

	// ErrUndeliverable is the code not being sendable at all until somebody
	// fixes the deployment: no registered template, an unknown sender header,
	// credentials the gateway refuses.
	//
	// Separated from ErrChannelDown because the advice differs and the farmer
	// is the one who pays for the wrong advice: "try again in a few minutes"
	// against a missing DLT template is an instruction to stand in a field
	// pressing a button that cannot work.
	ErrUndeliverable = errors.New("codes cannot be sent to this handset")
)

// Sender delivers a code to a handset.
//
// A failure should be returned as, or wrapped around, ErrChannelDown or
// ErrUndeliverable. Anything else is reported to the farmer as an unexplained
// server fault, which is true but useless to them.
type Sender interface {
	Send(ctx context.Context, mobileE164, code string) error
	// Name identifies the channel in logs and health output.
	Name() string
}

// LogSender writes codes to the service log.
//
// Development only, and the configuration loader refuses to start a hardened
// environment with it selected: a one-time code in a log file is a credential
// in a log file.
type LogSender struct{ Log *slog.Logger }

func (s LogSender) Name() string { return "log" }

func (s LogSender) Send(_ context.Context, mobileE164, code string) error {
	s.Log.Warn("one-time code delivered to the log; development only",
		"mobile", maskMobile(mobileE164), "code", code)
	return nil
}

type otpRecord struct {
	Hash      string    `json:"h"`
	Attempts  int       `json:"a"`
	IssuedAt  time.Time `json:"i"`
	ExpiresAt time.Time `json:"e"`
}

// OTP issues and verifies one-time codes.
type OTP struct {
	rdb    *redis.Client
	sender Sender

	// purpose separates one flow's codes from another's. A farmer signing in
	// and an officer proving a second factor share a Redis and may even share
	// a number; without this they would share a cooldown and a daily cap, and
	// an officer signing in for the eleventh time would lock a farmer out.
	purpose string

	length      int
	ttl         time.Duration
	maxAttempts int
	cooldown    time.Duration
	dailyCap    int
}

// NewOTP builds the code service.
func NewOTP(
	rdb *redis.Client, sender Sender,
	length int, ttl time.Duration, maxAttempts int,
	cooldown time.Duration, dailyCap int,
) *OTP {
	return &OTP{
		rdb: rdb, sender: sender,
		length: length, ttl: ttl, maxAttempts: maxAttempts,
		cooldown: cooldown, dailyCap: dailyCap,
	}
}

// For returns the same service keeping its codes under their own keys.
//
// dailyCap is per purpose too: ten codes a day is right for a farmer, who
// signs in rarely, and wrong for an officer who signs in every morning.
func (o *OTP) For(purpose string, dailyCap int) *OTP {
	copied := *o
	copied.purpose = purpose
	if dailyCap > 0 {
		copied.dailyCap = dailyCap
	}
	return &copied
}

// Channel names the delivery mechanism in use.
func (o *OTP) Channel() string { return o.sender.Name() }

// Request issues a code and delivers it.
//
// Returns the cooldown remaining alongside ErrOTPCooldown so a client can
// display a countdown rather than a bare refusal.
func (o *OTP) Request(ctx context.Context, mobileE164 string) (time.Duration, error) {
	cdKey := o.key("otpcd:", mobileE164)
	if ttl, err := o.rdb.TTL(ctx, cdKey).Result(); err == nil && ttl > 0 {
		return ttl, ErrOTPCooldown
	}

	// The daily counter expires at the end of the day rather than 24 hours
	// after the first request, so the cap is a day rather than a rolling
	// window a farmer cannot reason about.
	dayKey := o.key("otpday:", mobileE164) + ":" +
		time.Now().UTC().Format("2006-01-02")
	count, err := o.rdb.Incr(ctx, dayKey).Result()
	if err != nil {
		return 0, fmt.Errorf("counting daily codes: %w", err)
	}
	if count == 1 {
		o.rdb.Expire(ctx, dayKey, 25*time.Hour)
	}
	if int(count) > o.dailyCap {
		return 0, ErrOTPDailyCap
	}

	code, err := numericCode(o.length)
	if err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	rec := otpRecord{
		Hash:      hashCode(mobileE164, code),
		IssuedAt:  now,
		ExpiresAt: now.Add(o.ttl),
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return 0, err
	}

	// Stored before delivery. A code that reached the handset but not the
	// store is unusable and looks to the farmer like the system is broken;
	// the reverse merely wastes one unused record.
	if err := o.rdb.Set(ctx, o.key("otp:", mobileE164), body, o.ttl).Err(); err != nil {
		return 0, fmt.Errorf("storing code: %w", err)
	}

	if err := o.sender.Send(ctx, mobileE164, code); err != nil {
		o.rdb.Del(ctx, o.key("otp:", mobileE164))
		return 0, fmt.Errorf("delivering code: %w", err)
	}

	o.rdb.Set(ctx, cdKey, "1", o.cooldown)
	return 0, nil
}

// Verify checks a submitted code and consumes it on success.
func (o *OTP) Verify(ctx context.Context, mobileE164, code string) error {
	key := o.key("otp:", mobileE164)

	raw, err := o.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return ErrOTPNotFound
	}
	if err != nil {
		return fmt.Errorf("reading code: %w", err)
	}

	var rec otpRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return fmt.Errorf("decoding code record: %w", err)
	}

	if rec.Attempts >= o.maxAttempts {
		o.rdb.Del(ctx, key)
		return ErrOTPAttempts
	}

	if subtle.ConstantTimeCompare(
		[]byte(hashCode(mobileE164, strings.TrimSpace(code))),
		[]byte(rec.Hash),
	) != 1 {
		rec.Attempts++
		if body, mErr := json.Marshal(rec); mErr == nil {
			// Keep the original expiry: a wrong guess must not extend the
			// life of the code it was guessing at.
			o.rdb.Set(ctx, key, body, time.Until(rec.ExpiresAt))
		}
		if rec.Attempts >= o.maxAttempts {
			o.rdb.Del(ctx, key)
			return ErrOTPAttempts
		}
		return ErrOTPIncorrect
	}

	// Single use.
	o.rdb.Del(ctx, key)
	return nil
}

// key names one Redis entry for this service's purpose.
func (o *OTP) key(prefix, mobile string) string {
	if o.purpose == "" {
		return prefix + mobile
	}
	return prefix + o.purpose + ":" + mobile
}

// hashCode binds a code to the number it was issued for, so a code observed
// for one handset cannot be replayed against another.
func hashCode(mobile, code string) string {
	sum := sha256.Sum256([]byte(mobile + ":" + code))
	return hex.EncodeToString(sum[:])
}

// numericCode returns a uniformly distributed decimal code.
//
// crypto/rand throughout: math/rand seeded from the clock would make codes
// predictable from the time a request was made.
func numericCode(length int) (string, error) {
	digits := make([]byte, length)
	for i := range digits {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", fmt.Errorf("generating code: %w", err)
		}
		digits[i] = byte('0' + n.Int64())
	}
	return string(digits), nil
}

// maskMobile keeps the last two digits, which is enough for a farmer to
// recognise their own number and not enough for a log reader to learn it.
func maskMobile(m string) string {
	if len(m) < 4 {
		return "****"
	}
	return strings.Repeat("*", len(m)-2) + m[len(m)-2:]
}

// NormalizeMobile converts an Indian mobile number to E.164.
//
// The registry stores bare ten-digit numbers; a farmer may type any of the
// usual variants. Normalising at the edge is what keeps one handset from
// becoming two accounts.
func NormalizeMobile(raw string) (string, error) {
	var digits strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()

	switch {
	case strings.HasPrefix(raw, "+91") && len(d) == 12:
		d = d[2:]
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		d = d[2:]
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		d = d[1:]
	}

	if len(d) != 10 || d[0] < '6' {
		return "", fmt.Errorf("%q is not a valid Indian mobile number", raw)
	}
	return "+91" + d, nil
}
