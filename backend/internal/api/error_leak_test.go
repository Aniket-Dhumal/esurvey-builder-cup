package api

import (
	"net/http"
	"strings"
	"testing"
)

// What a client is allowed to be told when something fails.
//
// Eighty writeError call sites hand through an err.Error(), and those errors
// were written by the standard library for a Go programmer. Two things go
// wrong when one reaches a client. An officer is asked to act on a sentence
// about struct fields, which they cannot. And an unauthenticated caller learns
// the names and shapes of our request types, which is a map of the API drawn
// for them at no cost.
//
// The guard is in writeError rather than at the call sites, so these tests are
// about the guard.

func TestInternalErrorsNeverReachTheClient(t *testing.T) {
	// Every one of these is a real error string produced by a dependency on a
	// path that ends in writeError.
	internal := []string{
		"json: cannot unmarshal number into Go struct field officerLoginRequest.username of type string",
		"unexpected EOF",
		"invalid character 'x' looking for beginning of value",
		`strconv.Atoi: parsing "abc": invalid syntax`,
		"sql: no rows in result set",
		`pq: duplicate key value violates unique constraint "users_username_key"`,
		"context deadline exceeded",
		"dial tcp 127.0.0.1:5432: connect: connection refused",
		"open /app/reference_data/lgd.csv: no such file or directory",
		"runtime error: index out of range [3] with length 2",
		"github.com/jackc/pgx/v5: closed pool",
	}

	for _, msg := range internal {
		if !looksInternal(msg) {
			t.Errorf("this would reach a client verbatim:\n  %s", msg)
		}
	}
}

func TestAuthoredMessagesArePassedThrough(t *testing.T) {
	// The API's own prose is its contract with the programs that call it, and
	// the guard must not eat it. A denylist that swallowed these would be
	// worse than the leak: the client would lose the one thing that says which
	// field was wrong.
	authored := []string{
		"farmer_id is required",
		"no such account",
		"the credentials were not accepted",
		"a code was sent recently; wait before requesting another",
		"both village authorities must record a finding before this can be adjudicated",
		"status must be active, suspended or deactivated",
		"an account cannot suspend itself; ask another administrator",
	}

	for _, msg := range authored {
		if looksInternal(msg) {
			t.Errorf("the guard would replace an authored message:\n  %s", msg)
		}
	}
}

func TestEveryFallbackTellsThePersonWhatToDo(t *testing.T) {
	statuses := []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge,
		http.StatusTooManyRequests, http.StatusUnprocessableEntity,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		599, // an unmapped status still has to say something
	}

	for _, s := range statuses {
		got := fallbackFor(s)
		if got == "" {
			t.Errorf("status %d has no message", s)
			continue
		}
		// A message that is not a sentence is a label, and a label leaves the
		// reader with nowhere to go.
		if !strings.HasSuffix(got, ".") {
			t.Errorf("status %d is not a sentence: %q", s, got)
		}
		if u := strings.ToUpper(got[:1]); u != got[:1] {
			t.Errorf("status %d does not start with a capital: %q", s, got)
		}
		if looksInternal(got) {
			t.Errorf("status %d fallback trips its own guard: %q", s, got)
		}
	}
}

func TestEmptyMessageBecomesTheFallback(t *testing.T) {
	// The two 5xx sites that used to send err.Error() now log it and pass "".
	// That must not produce an error body with an empty message field.
	if got := fallbackFor(http.StatusInternalServerError); got == "" {
		t.Fatal("a 500 with no message would tell the client nothing")
	}
}
