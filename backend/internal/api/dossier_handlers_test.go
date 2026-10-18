package api

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/dossier"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

func TestAbsentReasonPullsOneSourceOut(t *testing.T) {
	absent := []string{
		"radar: no Sentinel-1 pass covered the incident window",
		"ground vision: the farmer has not uploaded a survey video",
	}
	if got := absentReason(absent, "radar"); got != "no Sentinel-1 pass covered the incident window" {
		t.Fatalf("radar reason = %q", got)
	}
	if got := absentReason(absent, "weather"); got != "" {
		t.Fatalf("a source that was gathered should have no reason, got %q", got)
	}
}

// A finding recorded after seeing the other officer's is not a second opinion,
// and the document has to be able to say which is which.
func TestOfficerFindingsCarryWhetherTheOfficerWasBlind(t *testing.T) {
	pct := 45.0
	findings := []report.Finding{
		{Role: rbac.RoleTalathi, OfficerName: "A", Tier: report.TierVillagePair,
			DamagePercent: &pct, SubmittedAt: time.Now(), SawPeerFinding: false},
		{Role: rbac.RoleKrushiSahayak, OfficerName: "B", Tier: report.TierVillagePair,
			DamagePercent: &pct, SubmittedAt: time.Now(), SawPeerFinding: true},
		// A rung above the pair. Not an opinion on the figure, and listing it
		// beside two numbers would suggest three.
		{Role: rbac.RoleMandalOfficer, OfficerName: "C", Tier: report.TierArbitration,
			SubmittedAt: time.Now()},
	}
	got := officerFindings(findings)
	if len(got) != 2 {
		t.Fatalf("want the pair only, got %d rows", len(got))
	}
	if !got[0].Blind {
		t.Error("the first officer recorded without seeing the other and is not marked blind")
	}
	if got[1].Blind {
		t.Error("the second officer saw the first's figure and is marked blind anyway")
	}
}

func TestGeofenceOutsideTheParcelIsNotAnAccusation(t *testing.T) {
	no, dist := false, 18.0
	got := geofenceResult(survey.Capture{InsideParcel: &no, DistanceFromParcelM: &dist})
	if !strings.Contains(got, "18 m outside") {
		t.Fatalf("geofence line = %q", got)
	}
	for _, word := range []string{"fraud", "invalid", "rejected"} {
		if strings.Contains(strings.ToLower(got), word) {
			t.Errorf("the geofence line accuses the farmer: %q", got)
		}
	}
	untested := geofenceResult(survey.Capture{})
	if !strings.Contains(untested, "Not tested") {
		t.Fatalf("no boundary on record should say so, got %q", untested)
	}
}

// The whole point of the state: a document that is not final carries no
// figure, because a provisional number is the number everybody remembers.
func TestAProvisionalDossierPrintsNoFigure(t *testing.T) {
	pct := 62.0
	for _, tc := range []struct {
		name  string
		state dossier.State
		want  bool
	}{
		{"provisional", dossier.Provisional, false},
		{"final", dossier.Final, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := dossier.Build(dossier.Inputs{
				State:               tc.state,
				Reference:           "assessment-1",
				GeneratedAt:         time.Now(),
				AssessedLossPercent: &pct,
			})
			if got := d.AssessedLossPercent != nil; got != tc.want {
				t.Fatalf("figure present = %v, want %v", got, tc.want)
			}
			var buf bytes.Buffer
			if err := d.Print(&buf); err != nil {
				t.Fatalf("printing: %v", err)
			}
			if !strings.Contains(buf.String(), "<html") {
				t.Fatal("the rendered dossier is not a document")
			}
		})
	}
}

// Build parses DecidedAt with a fixed layout and silently drops anything else,
// so the layout this handler writes has to be the layout Build reads.
func TestTheDecisionDateSurvivesBuild(t *testing.T) {
	decided := time.Date(2026, 9, 3, 12, 0, 0, 0, ist)
	findings := []report.Finding{
		{Tier: report.TierVillagePair, SubmittedAt: decided},
	}
	d := adjudication(findings, report.Consensus{Route: report.RouteConsensus})
	if d == nil {
		t.Fatal("a completed pair has a decider")
	}
	pct := 40.0
	built := dossier.Build(dossier.Inputs{
		State: dossier.Final, AssessedLossPercent: &pct,
		Consensus: &dossier.Consensus{DecidedBy: d.by, DecidedAt: d.at},
	})
	if built.DecidedAt == nil {
		t.Fatalf("Build could not parse the date this handler wrote: %q", d.at)
	}
}
