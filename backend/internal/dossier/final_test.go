package dossier

import (
	"strings"
	"testing"
)

// A clip the assistant asked for reads as a directed observation.
//
// The distinction is the whole value of recording what was asked. A close-up of
// split pods the farmer chose to take is the farmer's selection of evidence; the
// same clip taken because the assistant asked for it answers a selective-evidence
// challenge. A reader cannot tell them apart unless the document says which.
func TestEvidenceSaysWhenAClipWasDirected(t *testing.T) {
	s := Evidence([]EvidenceItem{{
		Kind:       "station_video",
		CapturedAt: "2026-09-30 10:15",
		AccuracyM:  6,
		Geofence:   "inside",
		SHA256:     "abc",
		AskedFor:   "शेंगा फोडून आतले दाणे जवळून दाखवा",
	}})

	if len(s.Rows) != 1 {
		t.Fatalf("got %d rows", len(s.Rows))
	}
	if !strings.Contains(s.Rows[0].Effect.EN, "directed rather than chosen") {
		t.Fatalf("the row does not say the clip was directed: %q",
			s.Rows[0].Effect.EN)
	}
	// What was asked is quoted, so a reader can see what the direction was rather
	// than being told there was one.
	if !strings.Contains(s.Rows[0].Effect.EN, "शेंगा") {
		t.Fatalf("what was asked for is missing: %q", s.Rows[0].Effect.EN)
	}
	if s.Preamble.EN == "" {
		t.Fatal("nothing above the table says some evidence was directed")
	}
	// And the preamble must not overclaim: the assistant shapes clips, it does not
	// decide how many are required.
	if !strings.Contains(s.Preamble.EN, "does not decide how many") {
		t.Fatalf("the preamble does not bound what the assistant decides: %q",
			s.Preamble.EN)
	}
}

// A claim walked with the assistant off is not described as weaker for it.
func TestEvidenceSaysNothingAboutAnAssistantThatNeverSpoke(t *testing.T) {
	s := Evidence([]EvidenceItem{{
		Kind:       "station_video",
		CapturedAt: "2026-09-30 10:15",
		AccuracyM:  6,
		Geofence:   "inside",
		SHA256:     "abc",
	}})

	if s.Preamble.EN != "" {
		t.Fatalf("a preamble appeared for undirected evidence: %q", s.Preamble.EN)
	}
	if strings.Contains(s.Rows[0].Effect.EN, "assistant") {
		t.Fatalf("an absent assistant is mentioned: %q", s.Rows[0].Effect.EN)
	}
	// The boundary test is still stated, because that is what the row has always
	// been for.
	if !strings.Contains(s.Rows[0].Effect.EN, "boundary") {
		t.Fatalf("the geofence explanation was lost: %q", s.Rows[0].Effect.EN)
	}
}

// Mixed evidence counts only what was directed.
func TestEvidenceCountsOnlyTheDirectedClips(t *testing.T) {
	s := Evidence([]EvidenceItem{
		{Kind: "station_video", AskedFor: "मुळे दाखवा"},
		{Kind: "station_video"},
		{Kind: "station_video", AskedFor: "शेंगा दाखवा"},
		{Kind: "damage_closeup"},
	})
	if !strings.Contains(s.Preamble.EN, "2 of these clips") {
		t.Fatalf("preamble miscounts: %q", s.Preamble.EN)
	}
}
