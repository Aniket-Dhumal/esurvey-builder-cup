package api

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/assessment"
)

// The backstop on the way into the assessments table. It fired on every
// assessment while the Gemini key's IP restriction refused the panel.
func TestAProviderErrorNeverReachesTheRecord(t *testing.T) {
	leak := "Not examined: weather_specialist: 403 PERMISSION_DENIED. {'error': " +
		"{'message': 'originating IP address of the call (203.0.113.8)', " +
		"'metadata': {'consumer': 'projects/100000000001'}}}"
	pct := 40.0
	v := assessment.Verified{
		Reasoning:             "This assessment was produced without the reasoning panel. " + leak,
		CropPresenceReasoning: "the vegetation series peaks at 0.72, well above bare soil",
		DegradedReason:        leak,
		DamagePercent:         &pct,
		CropPresence:          "confirmed",
		Specialists: []ai.Specialist{
			{Agent: "weather_specialist", Error: leak},
			{Agent: "vision_specialist", Error: "no ground photographs were supplied"},
		},
	}

	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.withholdProviderErrors(context.Background(), &v)

	for name, text := range map[string]string{
		"reasoning":          v.Reasoning,
		"degraded_reason":    v.DegradedReason,
		"weather specialist": v.Specialists[0].Error,
	} {
		for _, frag := range []string{"203.0.113.8", "projects/", "PERMISSION_DENIED", "{"} {
			if strings.Contains(text, frag) {
				t.Errorf("%s still carries %q: %s", name, frag, text)
			}
		}
	}

	// Our own sentences, and every figure, are left exactly as they were.
	if v.Specialists[1].Error != "no ground photographs were supplied" {
		t.Errorf("an ordinary gap was rewritten: %q", v.Specialists[1].Error)
	}
	if !strings.Contains(v.CropPresenceReasoning, "0.72") {
		t.Errorf("genuine reasoning was withheld: %q", v.CropPresenceReasoning)
	}
	if v.DamagePercent == nil || *v.DamagePercent != 40 || v.CropPresence != "confirmed" {
		t.Error("evidence was altered along with the error text")
	}
}
