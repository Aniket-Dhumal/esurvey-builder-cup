package config

import (
	"strings"
	"testing"
	"time"
)

// Configuration is where a deployment quietly becomes insecure. Every check
// below guards something that would otherwise fail open: a signing seed that
// regenerates on restart, a Redis with no password, one-time codes printed to
// a log, or a session cookie sent over plaintext.

func TestStagingIsHardenedLikeProduction(t *testing.T) {
	// A staging environment weaker than production tests something other than
	// what will be shipped, and it is usually the one pointed at real data
	// first.
	for _, e := range []Environment{Production, Staging} {
		if !e.Hardened() {
			t.Errorf("%q is not hardened", e)
		}
	}
	if Development.Hardened() {
		t.Error("development is hardened; nobody would be able to run it locally")
	}
}

// --- the parsers ------------------------------------------------------------

// capture collects what fail() was asked to report, so a parser can be tested
// without the process exiting.
func capture() (fail func(string, ...any), messages *[]string) {
	msgs := []string{}
	return func(format string, args ...any) {
		msgs = append(msgs, format)
	}, &msgs
}

func TestIntValRejectsNonNumbers(t *testing.T) {
	// Silently falling back to a default would mean a deployment running with
	// a connection limit nobody chose.
	t.Setenv("ESURVEY_TEST_INT", "not-a-number")
	fail, msgs := capture()

	intVal("ESURVEY_TEST_INT", 10, fail)
	if len(*msgs) == 0 {
		t.Error("a non-numeric value was accepted silently")
	}
}

func TestIntValUsesTheFallbackWhenUnset(t *testing.T) {
	fail, msgs := capture()
	if got := intVal("ESURVEY_TEST_UNSET_INT", 42, fail); got != 42 {
		t.Errorf("got %d, want the fallback 42", got)
	}
	if len(*msgs) > 0 {
		t.Errorf("an unset variable was reported as an error: %v", *msgs)
	}
}

func TestBoolValRejectsNonBooleans(t *testing.T) {
	// "yes" is the likely mistake, and reading it as false would silently
	// disable whatever it was meant to enable.
	t.Setenv("ESURVEY_TEST_BOOL", "yes")
	fail, msgs := capture()

	boolVal("ESURVEY_TEST_BOOL", false, fail)
	if len(*msgs) == 0 {
		t.Error(`"yes" was accepted as a boolean`)
	}
}

func TestDurValRejectsBareNumbers(t *testing.T) {
	// "15" is ambiguous — nanoseconds, by Go's rules, which is never what
	// anyone meant by a token lifetime.
	t.Setenv("ESURVEY_TEST_DUR", "15")
	fail, msgs := capture()

	dur("ESURVEY_TEST_DUR", time.Minute, fail)
	if len(*msgs) == 0 {
		t.Error(`"15" was accepted as a duration`)
	}
}

func TestDurValAcceptsRealDurations(t *testing.T) {
	t.Setenv("ESURVEY_TEST_DUR_OK", "12h")
	fail, msgs := capture()

	if got := dur("ESURVEY_TEST_DUR_OK", time.Minute, fail); got != 12*time.Hour {
		t.Errorf("got %v, want 12h", got)
	}
	if len(*msgs) > 0 {
		t.Errorf("a valid duration was reported: %v", *msgs)
	}
}

func TestSplitListIgnoresBlanks(t *testing.T) {
	// The CORS allowlist. A trailing comma producing an empty origin would be
	// an entry that matches nothing — or worse, something.
	got := splitList(" https://a.example , , https://b.example ,")
	if len(got) != 2 {
		t.Fatalf("got %d entries: %q", len(got), got)
	}
	for _, o := range got {
		if strings.TrimSpace(o) == "" {
			t.Error("a blank origin survived")
		}
	}
	if got[0] != "https://a.example" {
		t.Errorf("entries are not trimmed: %q", got[0])
	}
}

func TestSplitListOnEmptyIsEmpty(t *testing.T) {
	// An unset allowlist must be no origins, never one empty origin.
	for _, in := range []string{"", "   ", ","} {
		if got := splitList(in); len(got) != 0 {
			t.Errorf("splitList(%q) = %q, want empty", in, got)
		}
	}
}

func TestStrFallsBackOnlyWhenUnset(t *testing.T) {
	t.Setenv("ESURVEY_TEST_STR", "set")
	if got := str("ESURVEY_TEST_STR", "fallback"); got != "set" {
		t.Errorf("got %q, want the set value", got)
	}
	if got := str("ESURVEY_TEST_STR_UNSET", "fallback"); got != "fallback" {
		t.Errorf("got %q, want the fallback", got)
	}
}
