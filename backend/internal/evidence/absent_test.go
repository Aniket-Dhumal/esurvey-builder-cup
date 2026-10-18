package evidence

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/platform/saferr"
)

// The list of sources that could not be gathered is read carefully: it sits
// under "Could not be gathered" on the assessment screen, and an officer
// deciding whether a claim is short of evidence weighs it. A library's
// message landing there is not cosmetic -- it is a sentence that looks like a
// finding about the claim and is actually a stack trace in prose.
//
// The one that reached an officer, before this: the weather archive answered
// 502 with an HTML page and the Go JSON decoder's complaint about it went
// straight to the screen.
func TestLibraryErrorsNeverReachTheAbsentList(t *testing.T) {
	leaks := []string{
		`decoding the weather archive (http 502): invalid character '<' looking for beginning of value`,
		"json: cannot unmarshal number into Go struct field foo.bar of type string",
		"dial tcp 172.20.0.4:8200: connection refused",
		"context deadline exceeded",
		"pq: relation \"assessments\" does not exist",
		"github.com/agribid/esurvey-platform/backend/internal/weather.Fetch",
	}
	for _, msg := range leaks {
		if !saferr.LooksInternal(msg) {
			t.Errorf("this would reach an officer verbatim:\n  %s", msg)
		}
	}
}

// The other half, and the one a denylist gets wrong: the sentences that are
// written for an officer must survive untouched. A guard that swallowed these
// would replace the most useful thing on the screen -- why the evidence is
// missing and what it means for the claim -- with "it could not be reached".
func TestOfficerFacingReasonsSurvive(t *testing.T) {
	keep := []string{
		"no radar pass follows the incident yet; Sentinel-1 revisits every six to twelve days",
		"no threshold yield has been issued for Soybean in this unit for 2025-2026, so the percentage is not a shortfall against a statutory figure",
		"this parcel has no recorded boundary, so there is no area to measure backscatter over; the farmer can trace it from the app by walking the field edge",
		"no survey video has been captured for this report",
		"no radar pass precedes the incident, so there is nothing to compare the field against",
		`"Bajra" is not in the agronomic table, so no growth stage or susceptibility could be established for it`,
	}
	for _, msg := range keep {
		if saferr.LooksInternal(msg) {
			t.Errorf("this is written for an officer and must not be swallowed:\n  %s", msg)
		}
	}
}

// The replacement has to say something. An empty reason under a heading that
// promises one is worse than the leak it replaced.
func TestTheReplacementSaysSomething(t *testing.T) {
	const replacement = "it could not be reached; the assessment was made without it"
	if strings.TrimSpace(replacement) == "" {
		t.Fatal("the replacement reason is empty")
	}
	if saferr.LooksInternal(replacement) {
		t.Error("the replacement would itself be caught by the guard")
	}
}

// The guard, exercised through the real Assemble path rather than by calling
// the predicate directly.
//
// Worth its own test because the predicate being right and the predicate being
// *wired in* are different facts, and it was the wiring that was missing: the
// list has been sanitising HTTP responses at the API boundary for weeks while
// this surface, which is the one an officer actually reads, had no guard at
// all.
func TestAFailingSourceIsNotDescribedByItsLibrary(t *testing.T) {
	a := &Assembler{
		Log: quiet(),
		Radar: &fakeRadar{err: errors.New(
			`decoding the radar reply (http 502): invalid character '<' looking for beginning of value`)},
	}

	p := a.Assemble(context.Background(), input(), nil)

	var radar string
	for _, s := range p.Absent {
		if strings.HasPrefix(s, "radar:") {
			radar = s
		}
	}
	if radar == "" {
		t.Fatal("radar failed and was not recorded as absent")
	}
	for _, leak := range []string{"invalid character", "http 502", "decoding"} {
		if strings.Contains(radar, leak) {
			t.Errorf("the library's words reached the officer: %q contains %q",
				radar, leak)
		}
	}
	if !strings.Contains(radar, "could not be reached") {
		t.Errorf("the replacement says nothing useful: %q", radar)
	}
}
