package report

import (
	"context"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Where a claim has got to, and what it is waiting for.
//
// # Why this is computed rather than stored
//
// Every fact here is already recorded somewhere: the claim's state, whether
// its evidence arrived, how the assessment ended, which officers have filed,
// which rung it sits at. A `progress` column would be a sixth copy of all of
// that, and the one that goes stale -- and it would go stale on the screen a
// farmer is looking at to find out whether anybody is doing anything.
//
// # Why it returns keys and not sentences
//
// The handset renders this with no signal, in Marathi, from strings it already
// carries. Sending prose would mean a farmer's progress screen was blank
// offline, which is the one place it is most likely to be read. The console
// renders the same keys in its own words.
//
// # What the farmer sees
//
// It was decided on 2026-09-16 that the farmer is shown friendly stages, the
// remarks each officer recorded, and the loss percentage -- the last two only
// once both village officers have filed.
//
// That gate is not presentation. The Talathi and the Krushi Sahayak record
// blind to each other, deliberately: two officers who can see each other's
// figure produce one figure twice. Releasing the first officer's remark to the
// farmer before the second has filed would make the farmer the channel between
// them, and the blind would be gone without anybody deciding to remove it.

// Stage keys, in the order they happen.
const (
	StageReceived = "received"
	StageChecked  = "checked"
	StageField    = "field"
	StageDecision = "decision"
	StageClosed   = "closed"
)

// How a stage stands.
const (
	StageDone    = "done"
	StageCurrent = "current"
	StagePending = "pending"
	// StageAttention is current, and not moving on its own. Something has to
	// happen that is not happening: a village with nobody posted, evidence
	// that stopped arriving. Shown differently so a claim that is stuck does
	// not look like a claim that is merely in progress.
	StageAttention = "attention"
)

// Why a stage is where it is. Rendered by the reader; never prose here.
const (
	WhyEvidenceArriving  = "evidence_arriving"
	WhyEvidenceShort     = "evidence_short"
	WhyAssessing         = "assessing"
	WhyAssessmentSkipped = "assessment_unavailable"
	WhyNoAuthority       = "no_authority"
	WhyAwaitingBoth      = "awaiting_both_officers"
	WhyAwaitingOne       = "awaiting_second_officer"
	WhyWithCircle        = "with_circle"
	WhyWithTehsil        = "with_tehsil"
	WhyWithDistrict      = "with_district"
	WhyReturned          = "returned_for_more"
	WhyWithdrawn         = "withdrawn"
)

// Stage is one step of the claim's progress.
type Stage struct {
	Key   string     `json:"key"`
	State string     `json:"state"`
	At    *time.Time `json:"at,omitempty"`
	Why   string     `json:"why,omitempty"`
}

// Remark is what one officer wrote, and what they found.
type Remark struct {
	Role string `json:"role"`
	// By is the officer's name. Present for officers reading the claim and
	// omitted for the farmer: the remark is what a farmer needs in order to
	// understand the decision, and the person's name mostly tells them who to
	// go and find.
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at"`
	Text    string    `json:"text"`
	Percent *float64  `json:"percent,omitempty"`
}

// Progress is the whole answer for one claim.
type Progress struct {
	Reference string  `json:"reference"`
	Stages    []Stage `json:"stages"`

	// AssessedPercent is the figure the claim currently carries, released only
	// once both village officers have recorded. Nil before that, and nil where
	// no figure has been reached.
	AssessedPercent *float64 `json:"assessed_percent,omitempty"`

	// Remarks, under the same gate.
	Remarks []Remark `json:"remarks,omitempty"`

	// EvidenceShort means the claim went on without all the evidence the
	// handset promised.
	EvidenceShort bool `json:"evidence_short,omitempty"`

	// Stuck is what is stopping the claim, for an officer. Never sent to a
	// farmer: "village RV-V1 has no Talathi posted" is the District's problem
	// to solve and not something a farmer can act on, and it reads as an
	// excuse rather than an explanation.
	Stuck string `json:"stuck,omitempty"`

	// Contested means the two village officers disagreed by more than the
	// arbitration threshold. Officers only.
	Contested bool `json:"contested,omitempty"`
}

// ProgressFor computes where a claim stands, for whoever is asking.
//
// The scope is the same one that decides whether they may see the claim at
// all, so a farmer gets their own and an officer gets the ones in their
// villages. Whether the reader is a farmer decides what is released, not what
// is computed: see forFarmer.
func (s *Store) ProgressFor(ctx context.Context, id string, scope rbac.Scope, farmer bool) (*Progress, error) {
	r, err := s.Get(ctx, id, scope)
	if err != nil {
		return nil, err
	}

	findings, err := s.Findings(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	approvals, err := s.Approvals(ctx, r.ID)
	if err != nil {
		return nil, err
	}

	var extra struct {
		evidenceCompleteAt *time.Time
		evidenceIncomplete bool
		assessmentState    string
		pipelineError      *string
		contested          bool
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT evidence_complete_at, evidence_incomplete, assessment_state,
		       pipeline_error, contested
		  FROM crop_loss_reports WHERE id = $1`, r.ID).
		Scan(&extra.evidenceCompleteAt, &extra.evidenceIncomplete,
			&extra.assessmentState, &extra.pipelineError, &extra.contested); err != nil {
		return nil, fmt.Errorf("reading claim progress: %w", err)
	}

	c := Reconcile(findings)

	p := &Progress{
		Reference:     r.Reference,
		EvidenceShort: extra.evidenceIncomplete,
		Contested:     extra.contested,
	}
	if extra.pipelineError != nil {
		p.Stuck = *extra.pipelineError
	}

	p.Stages = stagesOf(r, extra.evidenceCompleteAt, extra.evidenceIncomplete,
		extra.assessmentState, extra.pipelineError != nil, c,
		countVillageFilings(findings), approvals)

	// The gate. Both village officers have recorded, so neither can be
	// influenced by what the other said any more.
	if c.Complete {
		p.Remarks = remarksOf(findings, approvals)
		if r.AdjudicatedPercent != nil {
			p.AssessedPercent = r.AdjudicatedPercent
		} else if c.Mean != nil {
			p.AssessedPercent = c.Mean
		}
	}

	if farmer {
		p.forFarmer()
	}
	return p, nil
}

// forFarmer strips what is the office's own business.
//
// Not a rendering choice. Which rung a claim sits at, that its two verifiers
// disagreed, and that a village has nobody posted to it are all true and all
// things a farmer can do nothing with -- and the first two name officers to go
// and press.
func (p *Progress) forFarmer() {
	p.Stuck = ""
	p.Contested = false
	for i := range p.Remarks {
		p.Remarks[i].By = ""
	}
}

// stagesOf lays the claim's life out in five steps.
func stagesOf(
	r *Report,
	evidenceCompleteAt *time.Time,
	evidenceShort bool,
	assessmentState string,
	stuck bool,
	c Consensus,
	villageFilings int,
	approvals []Approval,
) []Stage {
	received := Stage{Key: StageReceived, State: StageDone, At: &r.ReportedAt}
	checked := Stage{Key: StageChecked, State: StagePending}
	field := Stage{Key: StageField, State: StagePending}
	decision := Stage{Key: StageDecision, State: StagePending}
	closed := Stage{Key: StageClosed, State: StagePending}

	// Withdrawn stops everything where it stands. Said once rather than
	// leaving four pending stages that will never happen.
	if r.State == StateWithdrawn {
		received.Why = WhyWithdrawn
		return []Stage{received, checked, field, decision, closed}
	}

	// 1. Received. Complete only once the evidence the handset promised has
	// arrived -- a claim whose photographs are still uploading has been taken
	// but not yet received.
	if evidenceCompleteAt == nil {
		received.State = StageCurrent
		received.Why = WhyEvidenceArriving
		return []Stage{received, checked, field, decision, closed}
	}
	received.At = evidenceCompleteAt
	if evidenceShort {
		received.Why = WhyEvidenceShort
	}

	// 2. Checked: the assessment, which nobody had to ask for.
	switch assessmentState {
	case "done":
		checked.State = StageDone
	case "skipped":
		checked.State = StageDone
		checked.Why = WhyAssessmentSkipped
	default:
		checked.State = StageCurrent
		checked.Why = WhyAssessing
		if stuck {
			checked.State = StageAttention
		}
		return []Stage{received, checked, field, decision, closed}
	}

	// 3. The field, which is where the claim is actually decided.
	switch {
	case c.Complete:
		field.State = StageDone
	case r.State == StateAcknowledged:
		// Assessed, not yet with anybody. Ordinarily a moment; an age when the
		// village has nobody posted to it.
		field.State = StageCurrent
		field.Why = WhyNoAuthority
		if stuck {
			field.State = StageAttention
		}
		return []Stage{received, checked, field, decision, closed}
	case villageFilings == 1:
		field.State = StageCurrent
		field.Why = WhyAwaitingOne
		return []Stage{received, checked, field, decision, closed}
	default:
		field.State = StageCurrent
		field.Why = WhyAwaitingBoth
		return []Stage{received, checked, field, decision, closed}
	}

	// 4. The ladder, and the figure at the end of it.
	if r.AdjudicatedPercent != nil && r.State != StateVerified {
		decision.State = StageDone
		decision.At = r.AdjudicatedAt
	} else {
		decision.State = StageCurrent
		switch r.ApprovalLevel {
		case LevelCircle:
			decision.Why = WhyWithCircle
		case LevelTehsil:
			decision.Why = WhyWithTehsil
		case LevelDistrict:
			decision.Why = WhyWithDistrict
		}
		for _, a := range approvals {
			if a.Decision == DecisionReturned && a.SupersededAt == nil {
				decision.Why = WhyReturned
				break
			}
		}
		return []Stage{received, checked, field, decision, closed}
	}

	// 5. Finished.
	//
	// Pending rather than current while the claim is merely decided. Closing
	// is an administrative act somebody performs, not a consequence of the
	// decision -- a decided claim may still be appealed -- so "current" here
	// would show a farmer a step that is being worked on when nothing is
	// happening and nothing is owed.
	if r.State == StateClosed {
		closed.State = StageDone
	}
	return []Stage{received, checked, field, decision, closed}
}

// countVillageFilings is how many of the pair have recorded.
//
// Consensus says whether both have; this tells "one has filed" from "neither
// has", which is the difference between a claim half done and one not started
// -- and the only one of the two a farmer can read as progress.
func countVillageFilings(findings []Finding) int {
	n := 0
	for _, f := range findings {
		if f.Tier == TierVillagePair {
			n++
		}
	}
	return n
}

// remarksOf is what each officer wrote, oldest first.
//
// The village pair, then every standing rung of the ladder. A superseded
// finding is left out: it was replaced, and showing a farmer a figure that no
// longer stands would be showing them a decision nobody made.
func remarksOf(findings []Finding, approvals []Approval) []Remark {
	var out []Remark
	for _, f := range findings {
		if f.Findings == "" {
			continue
		}
		out = append(out, Remark{
			Role: string(f.Role), By: f.OfficerName,
			At: f.SubmittedAt, Text: f.Findings, Percent: f.DamagePercent,
		})
	}
	for _, a := range approvals {
		if a.SupersededAt != nil || a.Reason == "" {
			continue
		}
		out = append(out, Remark{
			Role: string(a.Role), By: a.Officer,
			At: a.DecidedAt, Text: a.Reason, Percent: a.PercentAfter,
		})
	}
	return out
}
