package assessment

import (
	"github.com/agribid/esurvey-platform/backend/internal/ai"
)

// Verified is an agent assessment after this service's own checks.
//
// The agent applies equivalent guardrails internally. These are applied again
// because the agent is a separate deployable reached over HTTP: a
// misconfiguration, a rollback to an older prompt, or a swapped model could
// all change its behaviour without this service knowing. The backend owns the
// figure it publishes, so it re-derives what it can.
type Verified struct {
	DamagePercent *float64 `json:"damage_percent"`
	Severity      string   `json:"severity"`
	Confidence    float64  `json:"confidence"`

	InsufficientEvidence bool   `json:"insufficient_evidence"`
	Reasoning            string `json:"reasoning"`

	CropPresence          string              `json:"crop_presence"`
	CropPresenceReasoning string              `json:"crop_presence_reasoning"`
	FraudIndicators       []ai.FraudIndicator `json:"fraud_indicators"`

	EvidenceCited  []string `json:"evidence_cited"`
	Contradictions []string `json:"contradictions"`

	Model         string `json:"model"`
	PromptVersion string `json:"prompt_version"`

	// Specialists is the panel's own account of itself: which members
	// examined what, and which could not run. An officer reading a low
	// confidence deserves to see that it was low because the weather source
	// was unreachable, not to guess.
	Specialists []ai.Specialist `json:"specialists,omitempty"`
	// Degraded means the panel ran short-handed.
	Degraded       bool   `json:"degraded,omitempty"`
	DegradedReason string `json:"degraded_reason,omitempty"`

	// Fusion is the specification's composite, computed beside this figure
	// rather than instead of it. Nil until a ground vision score exists,
	// which is the ordinary case and is reported as an absent cross-check.
	Fusion *Fusion `json:"fusion,omitempty"`

	// AgentGuardrails is what the agent reported applying; BackendGuardrails
	// is what this service applied on top. Kept apart so a divergence between
	// the two is visible rather than merged away — if the backend is
	// routinely correcting the agent, something upstream is wrong.
	AgentGuardrails   []string `json:"agent_guardrails"`
	BackendGuardrails []string `json:"backend_guardrails"`
}

// Verify re-checks an agent result against the evidence that produced it.
//
// The panel applies equivalent guardrails internally. They are applied again
// here for the same reason as before: the panel is a separate deployable
// reached over HTTP, and a rollback, a swapped model or a degraded run could
// change its behaviour without this service knowing.
func Verify(r ai.Result, req ai.AssessRequest) Verified {
	v := Verified{
		DamagePercent:         r.DamagePercent,
		Severity:              r.Severity,
		Confidence:            r.Confidence,
		InsufficientEvidence:  r.InsufficientEvidence,
		Reasoning:             r.Reasoning,
		CropPresence:          r.CropPresence,
		CropPresenceReasoning: r.CropPresenceReasoning,
		FraudIndicators:       r.FraudIndicators,
		EvidenceCited:         r.EvidenceCited,
		Contradictions:        r.Contradictions,
		Model:                 r.Model,
		PromptVersion:         r.PromptVersion,
		AgentGuardrails:       r.GuardrailsApplied,
	}

	var applied []string

	// The hard rule. Sentinel-2 at 10 m over a smallholding establishes that a
	// crop existed and roughly when it grew. It cannot measure how badly that
	// crop was damaged, so a figure produced without a field photograph is
	// discarded however confident it sounded.
	if !ai.HasGroundImagery(req.Images) && v.DamagePercent != nil {
		v.DamagePercent = nil
		v.Severity = ""
		v.InsufficientEvidence = true
		applied = append(applied, "damage_requires_ground_imagery")
	}

	// A refusal must not carry a figure out with it.
	if v.InsufficientEvidence && (v.DamagePercent != nil || v.Severity != "") {
		v.DamagePercent = nil
		v.Severity = ""
		applied = append(applied, "cleared_figure_on_refusal")
	}

	// Damage to a crop that never existed is incoherent.
	if v.CropPresence == ai.PresenceAbsent && v.DamagePercent != nil {
		v.DamagePercent = nil
		v.Severity = ""
		v.InsufficientEvidence = true
		applied = append(applied, "damage_cleared_no_crop_present")
	}

	if v.DamagePercent != nil {
		clamped := clamp(*v.DamagePercent, 0, 100)
		if clamped != *v.DamagePercent {
			applied = append(applied, "damage_percent_clamped")
		}
		v.DamagePercent = &clamped

		// Severity must agree with the figure it accompanies; the number is
		// what downstream code reads.
		derived := severityFor(clamped)
		if v.Severity != derived {
			v.Severity = derived
			applied = append(applied, "severity_derived_from_damage_percent")
		}
	}

	// Nothing without ground truth may present as a confident assessment.
	if !ai.HasGroundImagery(req.Images) && v.Confidence > 0.6 {
		v.Confidence = 0.6
		applied = append(applied, "confidence_capped_no_ground_truth")
	}
	v.Confidence = clamp(v.Confidence, 0, 1)

	// Cross-check the crop-presence finding against the series itself, so
	// "nothing grew here" does not depend on a language model noticing.
	if present, known := vegetationEverPresent(req.Satellite); known {
		switch {
		case !present && v.CropPresence == ai.PresenceConfirmed:
			v.CropPresence = ai.PresenceInconclusive
			applied = append(applied, "presence_downgraded_series_at_bare_soil")
		case present && v.CropPresence == ai.PresenceAbsent:
			v.CropPresence = ai.PresenceInconclusive
			applied = append(applied, "presence_downgraded_series_shows_vegetation")
		}
	}

	if v.CropPresence == "" {
		v.CropPresence = ai.PresenceInconclusive
	}

	v.BackendGuardrails = applied
	return v
}

// vegetationEverPresent reports whether the series ever rose meaningfully
// above bare soil. The second return is false when there are too few
// observations to say.
func vegetationEverPresent(s ai.SatelliteEvidence) (present bool, known bool) {
	if len(s.TimeSeries) < 2 {
		return false, false
	}
	peak := s.TimeSeries[0].NDVI
	for _, p := range s.TimeSeries[1:] {
		if p.NDVI > peak {
			peak = p.NDVI
		}
	}
	return peak >= vegetationPresentNDVI, true
}

// CriticalFraudIndicators returns only the indicators that warrant blocking a
// claim pending investigation.
func CriticalFraudIndicators(f []ai.FraudIndicator) []ai.FraudIndicator {
	var out []ai.FraudIndicator
	for _, i := range f {
		if i.Severity == ai.FraudCritical {
			out = append(out, i)
		}
	}
	return out
}

// TriageFor routes a verified assessment for human review.
//
// Every claim reaches at least standard audit. Nothing fast-tracks: with no
// ground truth, no assessment here is safe to approve unreviewed, whatever
// the confidence arithmetic says.
func TriageFor(v Verified) string {
	if len(CriticalFraudIndicators(v.FraudIndicators)) > 0 {
		return TriageRed
	}

	// The instruments and the panel disagree about the same field by a quarter
	// of the whole scale. One of them is wrong about land somebody is being
	// paid for, and neither this service nor the panel can settle which from
	// here. That is the specification's RED_ANOMALY_ESCALATE, and it is the
	// reason the composite is computed at all.
	if v.Fusion.Diverges(v.DamagePercent) {
		return TriageRed
	}
	if v.CropPresence == ai.PresenceAbsent {
		return TriageRed
	}
	if v.InsufficientEvidence || v.Confidence < 0.45 {
		return TriageRed
	}
	return TriageAmber
}

func severityFor(damagePercent float64) string {
	switch {
	case damagePercent >= 50:
		return SeverityCatastrophic
	case damagePercent >= 33:
		return SeveritySevere
	case damagePercent >= 20:
		return SeverityModerate
	default:
		return SeverityNegligible
	}
}
