package notify

import (
	"strings"
	"testing"
)

// What a farmer reads. The tests here are about the two things that would do
// real harm: a message that implies money, and a message in a language the
// recipient does not read.

func vars() Vars {
	return Vars{
		Reference: "CLR-2025-000041",
		Village:   "मालेगाव",
		Calamity:  "अवकाळी पाऊस व गारपीट",
		Deadline:  "२० ऑक्टोबर २०२५",
		Figure:    "६२%",
		Outcome:   "हरकत मान्य झाली",
	}
}

func allEvents() []string {
	return []string{
		EventReportAcknowledged, EventReportVerified, EventReportAdjudicated,
		EventSurveyOpened, EventSurveyClosing,
		EventGrievanceFiled, EventGrievanceDecided,
	}
}

func TestEveryEventHasAMessage(t *testing.T) {
	// An event code with no message written is a notification nobody
	// composed. Sending a generic string instead would be worse than sending
	// nothing.
	for _, code := range allEvents() {
		body, err := Render(code, vars())
		if err != nil {
			t.Errorf("%s: %v", code, err)
			continue
		}
		if strings.TrimSpace(body) == "" {
			t.Errorf("%s rendered empty", code)
		}
	}
}

func TestAnUnknownEventIsRefused(t *testing.T) {
	if _, err := Render("report.exploded", vars()); err == nil {
		t.Error("an unwritten event rendered a message")
	}
}

func TestMessagesAreInMarathi(t *testing.T) {
	// The recipient is a farmer in a Maharashtra village. English here is a
	// message that was technically delivered.
	for _, code := range allEvents() {
		body, err := Render(code, vars())
		if err != nil {
			t.Fatal(err)
		}
		devanagari := 0
		for _, r := range body {
			if r >= 0x0900 && r <= 0x097F {
				devanagari++
			}
		}
		if devanagari < 10 {
			t.Errorf("%s has almost no Devanagari:\n%s", code, body)
		}
	}
}

func TestNoMessagePromisesMoney(t *testing.T) {
	// This platform assesses loss. It does not disburse and does not know
	// what any figure is worth in rupees. A message that reads as a payment
	// notice is the most damaging thing it could send.
	forbidden := []string{"₹", "रुपये", "रक्कम मिळेल", "payment", "compensation", "मदत मिळेल"}

	for _, code := range allEvents() {
		body, err := Render(code, vars())
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range forbidden {
			// The adjudication message says explicitly that it is *not*
			// about an amount, which is the one legitimate mention.
			if strings.Contains(body, word) && !strings.Contains(body, "रकमेची नाही") {
				t.Errorf("%s mentions %q:\n%s", code, word, body)
			}
		}
	}
}

func TestEveryClaimMessageCarriesItsReference(t *testing.T) {
	// A farmer who gets an SMS and rings the taluka office has to be able to
	// say which claim it is about.
	for _, code := range []string{
		EventReportAcknowledged, EventReportVerified, EventReportAdjudicated,
		EventGrievanceFiled, EventGrievanceDecided,
	} {
		body, err := Render(code, vars())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, "CLR-2025-000041") {
			t.Errorf("%s does not carry the reference:\n%s", code, body)
		}
	}
}

func TestJoinDropsEmptyLines(t *testing.T) {
	// A blank line in an SMS reads as a broken message, and the recipient
	// cannot tell whether something was left out.
	got := join("first", "", "   ", "second")
	if got != "first\nsecond" {
		t.Errorf("join = %q", got)
	}
}

func TestMessagesFitASensibleNumberOfSegments(t *testing.T) {
	// Devanagari SMS is UCS-2: 70 characters per segment, 67 when
	// concatenated. Long messages cost more and are more likely to be
	// truncated by a gateway, so three segments is the working ceiling.
	const perSegment = 67
	for _, code := range allEvents() {
		body, err := Render(code, vars())
		if err != nil {
			t.Fatal(err)
		}
		segments := (len([]rune(body)) + perSegment - 1) / perSegment
		if segments > 3 {
			t.Errorf("%s is %d segments (%d chars):\n%s",
				code, segments, len([]rune(body)), body)
		}
	}
}
