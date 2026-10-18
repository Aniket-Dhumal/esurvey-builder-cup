// Package saferr tells an error written for a programmer from one written for
// a person.
//
// # Why this is not in the api package any more
//
// It started there, guarding writeError so that a library's message could not
// reach an HTTP client. That covered every route and missed everything else,
// and "everything else" turned out to include the one surface where an error
// is *expected* and read carefully: the evidence packet's list of what could
// not be gathered.
//
// An officer opening an assessment was shown
//
//	weather: decoding the weather archive (http 502): invalid character '<'
//	looking for beginning of value
//
// which is the Go JSON decoder describing an HTML error page. It sat under a
// heading called "Could not be gathered", where an officer deciding whether a
// claim is short of evidence would read it and take it seriously.
//
// One list in one place, so the two callers cannot drift.
package saferr

import (
	"regexp"
	"strings"
)

// Markers are the fingerprints of an error that came out of a library rather
// than out of us.
//
// Matching on the message is a denylist, and denylists leak. It is used here
// because the alternative -- auditing every call site that formats an error
// into user-facing prose, and trusting that the next one is written correctly
// -- leaks worse, and because this catches every future call site for free.
var Markers = []string{
	"json:", "strconv.", "sql:", "pq:", "pgx",
	"cannot unmarshal", "Go struct field", "invalid character",
	"unexpected EOF", "runtime error", "goroutine",
	"context deadline exceeded", "context canceled",
	"dial tcp", "connection refused", "no such host", "i/o timeout",
	"no such file", "permission denied",
	"github.com/", "gopkg.in/", "/app/",
	// Bare status codes in parentheses read as a fault the reader caused.
	// "(http 502)" is true and useless; the sentence around it should say
	// what it means for the claim.
	"(http 5", "(http 4",
}

// LooksInternal reports whether a message was written for a programmer.
func LooksInternal(message string) bool {
	for _, m := range Markers {
		if strings.Contains(message, m) {
			return true
		}
	}
	return false
}

// Provider errors.
//
// A second family, separate from Markers because it is caught somewhere else
// and needs a different remedy. Markers catch Go libraries describing
// themselves; these catch a remote API's error body -- the reasoning service
// once stored Google's whole 403 response, with the calling server's public
// IP and the project number, in every assessment's reasoning while an API key
// restriction refused it. The AI service now describes failures instead of
// repeating them (failures.py); this is the backstop on the way into the
// database, for whatever a future change lets through.
var providerFingerprint = regexp.MustCompile(
	`\b\d{1,3}(?:\.\d{1,3}){3}\b` + // an IPv4 address
		`|projects/\d+` + // a Google Cloud project number
		`|AIza[0-9A-Za-z_\-]{20,}` + // a Google API key
		`|googleapis\.com` +
		`|PERMISSION_DENIED|RESOURCE_EXHAUSTED|UNAUTHENTICATED` +
		`|API_KEY_[A-Z_]+` +
		`|callerIp|ErrorInfo|@type` +
		`|\{\s*['"]error['"]\s*:` +
		`|Traceback \(most recent call last\)`)

// LooksLikeProviderError reports whether text carries a remote API's error.
func LooksLikeProviderError(text string) bool {
	return text != "" && providerFingerprint.MatchString(text)
}

// Withheld replaces text that carries a provider error with a sentence.
//
// The whole text, not the matching part. A partly redacted provider error is
// still a provider error in front of a farmer, and the surrounding sentence
// was usually built around it.
func Withheld(text, replacement string) (string, bool) {
	if !LooksLikeProviderError(text) {
		return text, false
	}
	return replacement, true
}
