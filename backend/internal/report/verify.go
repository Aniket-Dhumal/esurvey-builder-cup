package report

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"strings"
)

// Verification of a report by the village's allocated authorities.
//
// The Talathi verifies the land record, ownership and survey-number identity.
// The Krushi Sahayak verifies the crop, its condition and whether the loss is
// agronomically plausible. They are a pair, not a sequence, and they record
// blind to each other.

// Errors.
var (
	ErrNotAssigned     = errors.New("you are not the allocated authority for this report")
	ErrAlreadyRecorded = errors.New("you have already recorded a finding on this report")
	ErrPairIncomplete  = errors.New("both village authorities must record a finding first")
	ErrNoAuthority     = errors.New("no authority is posted to this village")
)

// Tier is the level a finding was recorded at.
type Tier string

const (
	// TierVillagePair is the Talathi and the Krushi Sahayak.
	TierVillagePair Tier = "village_pair"
	// TierArbitration is the Circle Officer resolving their disagreement.
	TierArbitration Tier = "arbitration"
	// TierEscalation is the Tehsildar.
	TierEscalation Tier = "escalation"
)

// Outcome is what an officer found.
type Outcome string

const (
	OutcomeConfirmed       Outcome = "confirmed"
	OutcomePartlyConfirmed Outcome = "partly_confirmed"
	OutcomeRejected        Outcome = "rejected"
	OutcomeNotTraceable    Outcome = "not_traceable"
)

// Route is how a report reaches its adjudication.
type Route string

const (
	RouteConsensus   Route = "consensus"
	RouteArbitration Route = "arbitration"
	RouteEscalation  Route = "escalation"
)

// Divergence thresholds between the two village findings.
//
// From the specification's dual-officer framework. The numbers are percentage
// points of assessed damage, not a ratio: two officers who say 10% and 14% are
// four points apart and agreeing, and two who say 60% and 64% are also four
// points apart and also agreeing.
const (
	// ConsensusThreshold — at or under this the pair agree, and the Circle
	// Officer may adjudicate on their mean.
	ConsensusThreshold = 5.0
	// ArbitrationThreshold — between the two, the Circle Officer must decide
	// and record why. Above it, the Tehsildar does.
	ArbitrationThreshold = 12.0
)

// VerificationSLA is how long an assigned officer has.
//
// Seven days, matching the interval the intimation form allows for completing
// information that could not be given at once.
const VerificationSLA = 7 * 24 * time.Hour

// Authority is an officer allocated to a village.
type Authority struct {
	UserID            string     `json:"user_id"`
	Role              rbac.Role  `json:"role"`
	FullName          string     `json:"full_name"`
	JurisdictionLevel rbac.Level `json:"jurisdiction_level"`
}

// Assignment is one officer asked to verify one report.
type Assignment struct {
	ID          string     `json:"id"`
	ReportID    string     `json:"report_id"`
	OfficerID   string     `json:"officer_id"`
	OfficerName string     `json:"officer_name,omitempty"`
	Role        rbac.Role  `json:"role"`
	Tier        Tier       `json:"tier"`
	AssignedAt  time.Time  `json:"assigned_at"`
	DueAt       time.Time  `json:"due_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Overdue     bool       `json:"overdue"`
}

// Finding is one officer's recorded verification.
type Finding struct {
	ID          string    `json:"id"`
	OfficerID   string    `json:"officer_id"`
	OfficerName string    `json:"officer_name,omitempty"`
	Role        rbac.Role `json:"role"`
	Tier        Tier      `json:"tier"`

	Outcome        Outcome  `json:"outcome"`
	DamagePercent  *float64 `json:"damage_percent"`
	AreaAffectedHa *float64 `json:"area_affected_ha"`
	Findings       string   `json:"findings"`

	VisitedAt   *time.Time `json:"visited_at,omitempty"`
	SubmittedAt time.Time  `json:"submitted_at"`

	SawPeerFinding     bool `json:"saw_peer_finding"`
	SawModelAssessment bool `json:"saw_model_assessment"`

	// Revised means this officer already had a finding on this report and
	// replaced it after reading their colleague's. Distinct from
	// SawPeerFinding, which is only "recorded second" -- an officer who filed
	// after their colleague and an officer who moved towards them are
	// different things, and a screen that renders both as "recorded second"
	// hides the one worth seeing.
	Revised bool `json:"revised"`

	Items []VerifiedItem `json:"items,omitempty"`
}

// VerifiedItem is one survey number as the officer found it.
type VerifiedItem struct {
	LineNo         int      `json:"line_no"`
	CropName       string   `json:"crop_name,omitempty"`
	AreaSownHa     *float64 `json:"area_sown_ha,omitempty"`
	AreaAffectedHa *float64 `json:"area_affected_ha,omitempty"`
	DamagePercent  *float64 `json:"damage_percent,omitempty"`
}

// Consensus is the result of comparing the two village findings.
type Consensus struct {
	// Complete is false until both authorities have recorded.
	Complete bool `json:"complete"`

	FirstPercent  *float64 `json:"first_percent"`
	SecondPercent *float64 `json:"second_percent"`

	// Divergence is the gap in percentage points. Nil where either officer
	// rejected the claim or could not trace the parcel, since there is no
	// figure to compare.
	Divergence *float64 `json:"divergence"`

	// Mean is the pair's average, which the Circle Officer may adjudicate on
	// when they agree.
	Mean *float64 `json:"mean"`

	Route Route `json:"route"`
	// AdjudicatedBy names the role that must decide.
	AdjudicatedBy rbac.Role `json:"adjudicated_by"`
	Explanation   string    `json:"explanation"`
}

// Reconcile applies the consensus rule to the pair's findings.
//
// A pure function of the two findings, so the rule that decides who adjudicates
// thousands of claims is one testable expression rather than a condition spread
// across a handler.
func Reconcile(findings []Finding) Consensus {
	var pair []Finding
	for _, f := range findings {
		if f.Tier == TierVillagePair {
			pair = append(pair, f)
		}
	}

	if len(pair) < 2 {
		return Consensus{
			Complete:    false,
			Route:       RouteConsensus,
			Explanation: "waiting for both village authorities to record a finding",
		}
	}

	a, b := pair[0], pair[1]

	// A rejection or an untraceable parcel is not a number to average. Either
	// one sends the report up: an officer saying "there is no loss here" and
	// another saying "there is 40%" is the sharpest disagreement the system
	// can see, and averaging it would bury exactly the case a supervisor
	// needs to look at.
	if a.DamagePercent == nil || b.DamagePercent == nil {
		c := Consensus{
			Complete:      true,
			FirstPercent:  a.DamagePercent,
			SecondPercent: b.DamagePercent,
			Route:         RouteEscalation,
			AdjudicatedBy: rbac.RoleTehsildar,
		}
		switch {
		case a.DamagePercent == nil && b.DamagePercent == nil:
			c.Explanation = fmt.Sprintf(
				"both authorities recorded %s and %s; no loss figure to reconcile",
				a.Outcome, b.Outcome)
		default:
			c.Explanation = "one authority found a loss and the other did not; " +
				"the disagreement cannot be averaged"
		}
		return c
	}

	first, second := *a.DamagePercent, *b.DamagePercent
	divergence := math.Abs(first - second)
	mean := (first + second) / 2

	c := Consensus{
		Complete:      true,
		FirstPercent:  &first,
		SecondPercent: &second,
		Divergence:    &divergence,
		Mean:          &mean,
	}

	switch {
	case divergence <= ConsensusThreshold:
		c.Route = RouteConsensus
		c.AdjudicatedBy = rbac.RoleMandalOfficer
		c.Explanation = fmt.Sprintf(
			"the authorities are %.1f points apart, within the %.0f-point "+
				"consensus band; the Circle Officer may adjudicate on their mean of %.1f%%",
			divergence, ConsensusThreshold, mean)
	case divergence <= ArbitrationThreshold:
		c.Route = RouteArbitration
		c.AdjudicatedBy = rbac.RoleMandalOfficer
		c.Explanation = fmt.Sprintf(
			"the authorities are %.1f points apart; the Circle Officer must "+
				"arbitrate and record a reason", divergence)
	default:
		c.Route = RouteEscalation
		c.AdjudicatedBy = rbac.RoleTehsildar
		c.Explanation = fmt.Sprintf(
			"the authorities are %.1f points apart, beyond the %.0f-point "+
				"arbitration band; the Tehsildar decides",
			divergence, ArbitrationThreshold)
	}
	return c
}

// Adjudication is the final figure and what the officer was shown.
type Adjudication struct {
	ID          string    `json:"id"`
	OfficerID   string    `json:"officer_id"`
	OfficerName string    `json:"officer_name,omitempty"`
	Role        rbac.Role `json:"role"`

	ShownFirstPercent  *float64 `json:"shown_first_percent"`
	ShownSecondPercent *float64 `json:"shown_second_percent"`
	ShownModelPercent  *float64 `json:"shown_model_percent"`
	Divergence         *float64 `json:"divergence"`
	Route              Route    `json:"route"`

	AdjudicatedPercent float64 `json:"adjudicated_percent"`
	AdjudicatedAreaHa  float64 `json:"adjudicated_area_ha"`
	Reason             string  `json:"reason"`

	DecidedAt time.Time `json:"decided_at"`
}

// --- store -----------------------------------------------------------------

// Authorities returns the officers allocated to a village.
func (s *Store) Authorities(ctx context.Context, villageCode string) ([]Authority, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, officer_role, full_name, jurisdiction_level
		 FROM village_authorities($1)`, villageCode)
	if err != nil {
		return nil, fmt.Errorf("reading village authorities: %w", err)
	}
	defer rows.Close()

	var out []Authority
	for rows.Next() {
		var a Authority
		var role, level string
		if err := rows.Scan(&a.UserID, &role, &a.FullName, &level); err != nil {
			return nil, err
		}
		a.Role, a.JurisdictionLevel = rbac.Role(role), rbac.Level(level)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Allocate assigns a report to its village's authorities.
//
// One Talathi and one Krushi Sahayak. Where several hold the posting, the one
// with the fewest open assignments takes it -- an officer returning from leave
// should not be handed a month of backlog because they happen to sort first.
//
// Returns ErrNoAuthority when the village has neither, which is a real and
// visible gap rather than a silent one: a report nobody was asked to verify
// would otherwise sit in submitted forever.
func (s *Store) Allocate(ctx context.Context, reportID, villageCode, by string) ([]Assignment, error) {
	authorities, err := s.Authorities(ctx, villageCode)
	if err != nil {
		return nil, err
	}
	if len(authorities) == 0 {
		return nil, fmt.Errorf("%w: village %s has no Talathi or Krushi Sahayak posted",
			ErrNoAuthority, villageCode)
	}

	// Pick one per role, least loaded first.
	chosen := map[rbac.Role]Authority{}
	for _, role := range []rbac.Role{rbac.RoleTalathi, rbac.RoleKrushiSahayak} {
		var best Authority
		bestLoad := -1
		for _, a := range authorities {
			if a.Role != role {
				continue
			}
			var load int
			if err := s.pool.QueryRow(ctx, `
				SELECT count(*) FROM report_assignments
				WHERE officer_id = $1 AND completed_at IS NULL
				  AND superseded_at IS NULL`, a.UserID).Scan(&load); err != nil {
				return nil, err
			}
			if bestLoad < 0 || load < bestLoad {
				best, bestLoad = a, load
			}
		}
		if bestLoad >= 0 {
			chosen[role] = best
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	due := time.Now().UTC().Add(VerificationSLA)
	for _, a := range chosen {
		if _, err := tx.Exec(ctx, `
			INSERT INTO report_assignments
				(report_id, officer_id, role, tier, assigned_by, due_at)
			VALUES ($1, $2, $3::officer_role, 'village_pair', $4, $5)
			ON CONFLICT (report_id, officer_id, tier) DO NOTHING`,
			reportID, a.UserID, string(a.Role), nullString(by), due); err != nil {
			return nil, fmt.Errorf("assigning %s: %w", a.Role, err)
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE crop_loss_reports SET state = 'assigned'
		 WHERE id = $1 AND state IN ('submitted', 'acknowledged')`, reportID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Assignments(ctx, reportID)
}

// Assignments lists who has been asked to verify a report.
func (s *Store) Assignments(ctx context.Context, reportID string) ([]Assignment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.report_id, a.officer_id, u.full_name, a.role::text, a.tier,
		       a.assigned_at, a.due_at, a.completed_at
		FROM report_assignments a
		JOIN users u ON u.id = a.officer_id
		WHERE a.report_id = $1 AND a.superseded_at IS NULL
		ORDER BY a.tier, a.role::text`, reportID)
	if err != nil {
		return nil, fmt.Errorf("reading assignments: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	out := []Assignment{}
	for rows.Next() {
		var a Assignment
		var role string
		if err := rows.Scan(&a.ID, &a.ReportID, &a.OfficerID, &a.OfficerName,
			&role, &a.Tier, &a.AssignedAt, &a.DueAt, &a.CompletedAt); err != nil {
			return nil, err
		}
		a.Role = rbac.Role(role)
		a.Overdue = a.CompletedAt == nil && now.After(a.DueAt)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Allocation is an officer's standing on one report.
//
// Assigned and Completed are separate because the two refusals they produce
// mean opposite things. "You were never asked to verify this" sends an officer
// to find out why; "you already did" is the blind working as designed. Folding
// them into one boolean told an officer who had just recorded a finding that
// they were not the village's authority, which is both wrong and alarming.
type Allocation struct {
	Tier      Tier
	Assigned  bool
	Completed bool
}

// AllocationFor reports an officer's standing on a report.
func (s *Store) AllocationFor(ctx context.Context, reportID, officerID string) (Allocation, error) {
	var tier string
	var completed *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT tier, completed_at FROM report_assignments
		WHERE report_id = $1 AND officer_id = $2 AND superseded_at IS NULL
		ORDER BY completed_at NULLS FIRST
		LIMIT 1`, reportID, officerID).Scan(&tier, &completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Allocation{}, nil
	}
	if err != nil {
		return Allocation{}, err
	}
	return Allocation{Tier: Tier(tier), Assigned: true, Completed: completed != nil}, nil
}

// validFinding is what a finding must carry whichever way it arrives -- first
// time, or as an amendment after the pair have read each other.
func validFinding(in NewFinding) error {
	switch {
	case len(in.Findings) < 10:
		return fmt.Errorf(
			"%w: record what you saw; a figure with no reasoning cannot be "+
				"argued with, and a panchnama that cannot be argued with is not one",
			ErrBadInput)
	case in.Outcome == OutcomeConfirmed || in.Outcome == OutcomePartlyConfirmed:
		if in.DamagePercent == nil {
			return fmt.Errorf("%w: a confirmed loss needs a percentage", ErrBadInput)
		}
	case in.Outcome == OutcomeRejected || in.Outcome == OutcomeNotTraceable:
		if in.DamagePercent != nil {
			return fmt.Errorf(
				"%w: %s carries no loss percentage", ErrBadInput, in.Outcome)
		}
	default:
		return fmt.Errorf("%w: unknown outcome %q", ErrBadInput, in.Outcome)
	}
	return nil
}

// NewFinding is an officer's submitted verification.
type NewFinding struct {
	ReportID  string
	OfficerID string
	Role      rbac.Role
	Tier      Tier

	Outcome        Outcome
	DamagePercent  *float64
	AreaAffectedHa *float64
	Findings       string
	VisitedAt      *time.Time

	SawModelAssessment bool

	Items []VerifiedItem
}

// RecordFinding writes one officer's verification.
//
// saw_peer_finding is computed here rather than taken from the caller: it is
// true exactly when a finding already exists at this tier, which the server
// knows and the client cannot be trusted to report about itself.
func (s *Store) RecordFinding(ctx context.Context, in NewFinding) (*Finding, error) {
	if err := validFinding(in); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var peerCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM report_verifications
		WHERE report_id = $1 AND tier = $2 AND officer_id <> $3`,
		in.ReportID, string(in.Tier), in.OfficerID).Scan(&peerCount); err != nil {
		return nil, err
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO report_verifications (
			report_id, officer_id, role, tier, outcome,
			damage_percent, area_affected_ha, findings, visited_at,
			saw_peer_finding, saw_model_assessment
		) VALUES ($1, $2, $3::officer_role, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id`,
		in.ReportID, in.OfficerID, string(in.Role), string(in.Tier), string(in.Outcome),
		in.DamagePercent, in.AreaAffectedHa, in.Findings, in.VisitedAt,
		peerCount > 0, in.SawModelAssessment,
	).Scan(&id)
	if err != nil {
		if isUnique(err) {
			return nil, ErrAlreadyRecorded
		}
		return nil, fmt.Errorf("%w: %s", ErrBadInput, cleanErr(err))
	}

	for _, it := range in.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO report_verification_items (
				verification_id, line_no, crop_name,
				area_sown_ha, area_affected_ha, damage_percent
			) VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6)`,
			id, it.LineNo, it.CropName,
			it.AreaSownHa, it.AreaAffectedHa, it.DamagePercent); err != nil {
			return nil, fmt.Errorf("%w: line %d: %s", ErrBadInput, it.LineNo, cleanErr(err))
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE report_assignments SET completed_at = now()
		WHERE report_id = $1 AND officer_id = $2 AND tier = $3
		  AND superseded_at IS NULL`,
		in.ReportID, in.OfficerID, string(in.Tier)); err != nil {
		return nil, err
	}

	// under_verification once one is in, verified once the pair are.
	next := "under_verification"
	if in.Tier == TierVillagePair && peerCount >= 1 {
		next = "verified"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports SET state = $2
		WHERE id = $1 AND state IN ('assigned', 'under_verification', 'acknowledged', 'submitted')`,
		in.ReportID, next); err != nil {
		return nil, err
	}

	// The pair is complete. Either they agreed, in which case that is the
	// decision, or they did not, in which case one officer is asked to settle
	// it.
	//
	// No climb either way. Nothing above the village approves a claim the pair
	// agreed on; see the note at the head of ladder.go for what that costs and
	// why it was chosen.
	if next == "verified" {
		findings, err := s.findingsTx(ctx, tx, in.ReportID)
		if err != nil {
			return nil, err
		}
		c := Reconcile(findings)

		if !contestedBy(c) {
			if err := s.decideOnThePair(ctx, tx, in.ReportID, c); err != nil {
				return nil, err
			}
		} else if err := s.routeDisagreement(ctx, tx, in.ReportID); err != nil {
			return nil, err
		}
	}

	// Only when the pair is complete. Telling a farmer "verified" after the
	// first of two officers has filed would be telling them the answer is
	// settled while it is still one opinion.
	if next == "verified" {
		if rep := s.target(ctx, tx, in.ReportID); rep != nil {
			v := notifyVars(rep)
			v.Figure = figure(in.DamagePercent)
			s.tell(ctx, tx, notify.EventReportVerified, rep, v)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	findings, err := s.Findings(ctx, in.ReportID)
	if err != nil {
		return nil, err
	}
	for i := range findings {
		if findings[i].ID == id {
			return &findings[i], nil
		}
	}
	return nil, ErrNotFound
}

// Findings returns every recorded verification for a report.
// findingsSQL is shared by the pooled and in-transaction readers.
//
// Superseded findings are excluded. A report the Circle sent back has the
// pair's original findings kept for the record, and Reconcile counts village
// pair findings to decide whether the pair is complete -- so leaving the old
// ones in would make a returned report look like it had four village officers
// and reconcile the wrong two.
const findingsSQL = `
	SELECT v.id, v.officer_id, u.full_name, v.role::text, v.tier, v.outcome,
	       v.damage_percent, v.area_affected_ha, v.findings, v.visited_at,
	       v.saw_peer_finding, v.saw_model_assessment, v.submitted_at,
	       EXISTS (
	           SELECT 1 FROM report_verifications e
	            WHERE e.report_id = v.report_id
	              AND e.officer_id = v.officer_id
	              AND e.tier = v.tier
	              AND e.superseded_reason = 'amended'
	       )
	FROM report_verifications v
	JOIN users u ON u.id = v.officer_id
	WHERE v.report_id = $1 AND v.superseded_at IS NULL
	ORDER BY v.submitted_at`

// findingsTx reads the standing findings inside a transaction, so a decision
// on the ladder sees the same rows it is about to act on.
func (s *Store) findingsTx(ctx context.Context, tx pgx.Tx, reportID string) ([]Finding, error) {
	rows, err := tx.Query(ctx, findingsSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("reading findings: %w", err)
	}
	defer rows.Close()
	return scanFindings(rows)
}

// ContestedDivergence is the gap in percentage points past which the pair are
// treated as disagreeing.
//
// The same 25 points the assessment's own cross-check uses between the
// instruments and the panel. Two officers standing in the same field who are
// a quarter of the whole scale apart have not made the same observation, and
// somebody should read that report rather than tick it.
const ContestedDivergence = 25.0

// contestedBy reports whether a reconciliation should mark the report
// contested.
//
// A missing figure counts. An officer who refused the claim outright against
// one who found forty per cent is the sharpest disagreement the system can
// see, and Reconcile deliberately leaves Divergence nil in that case rather
// than inventing a number to subtract.
func contestedBy(c Consensus) bool {
	if !c.Complete {
		return false
	}
	if c.FirstPercent == nil || c.SecondPercent == nil {
		return true
	}
	return c.Divergence != nil && *c.Divergence > ContestedDivergence
}

// scanFindings reads the rows findingsSQL returns, without their line items.
func scanFindings(rows pgx.Rows) ([]Finding, error) {
	out := []Finding{}
	for rows.Next() {
		var f Finding
		var role, outcome string
		if err := rows.Scan(&f.ID, &f.OfficerID, &f.OfficerName, &role, &f.Tier,
			&outcome, &f.DamagePercent, &f.AreaAffectedHa, &f.Findings,
			&f.VisitedAt, &f.SawPeerFinding, &f.SawModelAssessment,
			&f.SubmittedAt, &f.Revised); err != nil {
			return nil, err
		}
		f.Role, f.Outcome = rbac.Role(role), Outcome(outcome)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) Findings(ctx context.Context, reportID string) ([]Finding, error) {
	rows, err := s.pool.Query(ctx, findingsSQL, reportID)
	if err != nil {
		return nil, fmt.Errorf("reading findings: %w", err)
	}
	out, err := scanFindings(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}

	for i := range out {
		items, err := s.findingItems(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

func (s *Store) findingItems(ctx context.Context, verificationID string) ([]VerifiedItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT line_no, COALESCE(crop_name, ''), area_sown_ha, area_affected_ha,
		       damage_percent
		FROM report_verification_items
		WHERE verification_id = $1 ORDER BY line_no`, verificationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []VerifiedItem{}
	for rows.Next() {
		var i VerifiedItem
		if err := rows.Scan(&i.LineNo, &i.CropName, &i.AreaSownHa,
			&i.AreaAffectedHa, &i.DamagePercent); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// NewAdjudication is the final decision.
type NewAdjudication struct {
	ReportID  string
	OfficerID string
	Role      rbac.Role

	AdjudicatedPercent float64
	AdjudicatedAreaHa  float64
	Reason             string

	ShownModelPercent *float64
}

// Adjudicate records the final figure.
//
// The consensus is recomputed here rather than trusted from the request, so
// what is stored as "what the officer was shown" is what the server would have
// shown them.
func (s *Store) Adjudicate(ctx context.Context, in NewAdjudication) (*Adjudication, error) {
	if len(in.Reason) < 10 {
		return nil, fmt.Errorf(
			"%w: record why this figure and not the officers' own", ErrBadInput)
	}
	if in.AdjudicatedPercent < 0 || in.AdjudicatedPercent > 100 {
		return nil, fmt.Errorf("%w: the figure is out of range", ErrBadInput)
	}

	findings, err := s.Findings(ctx, in.ReportID)
	if err != nil {
		return nil, err
	}
	c := Reconcile(findings)
	if !c.Complete {
		return nil, ErrPairIncomplete
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO report_adjudications (
			report_id, officer_id, role,
			shown_first_percent, shown_second_percent, shown_model_percent,
			divergence, route,
			adjudicated_percent, adjudicated_area_ha, reason
		) VALUES ($1, $2, $3::officer_role, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id`,
		in.ReportID, in.OfficerID, string(in.Role),
		c.FirstPercent, c.SecondPercent, in.ShownModelPercent,
		c.Divergence, string(c.Route),
		in.AdjudicatedPercent, in.AdjudicatedAreaHa, in.Reason,
	).Scan(&id)
	if err != nil {
		if isUnique(err) {
			return nil, fmt.Errorf(
				"%w: this report has already been adjudicated", ErrBadState)
		}
		return nil, fmt.Errorf("%w: %s", ErrBadInput, cleanErr(err))
	}

	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports
		SET state = 'adjudicated', adjudicated_percent = $2,
		    adjudicated_area_ha = $3, adjudicated_at = now()
		WHERE id = $1`,
		in.ReportID, in.AdjudicatedPercent, in.AdjudicatedAreaHa); err != nil {
		return nil, err
	}

	// The decision itself. The message says in as many words that this is a
	// record of loss and not of money, because it is the one a farmer is
	// most likely to read as a payment notice.
	if rep := s.target(ctx, tx, in.ReportID); rep != nil {
		v := notifyVars(rep)
		pct := in.AdjudicatedPercent
		v.Figure = figure(&pct)
		s.tell(ctx, tx, notify.EventReportAdjudicated, rep, v)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Adjudication(ctx, in.ReportID)
}

// Revise records a new adjudicated figure after an appeal was upheld.
//
// # Why this is not an update
//
// report_adjudications is append-only by trigger, and rightly: who decided
// what, on what was in front of them, and when, is exactly what an inquiry
// asks afterwards. A revision is therefore a new decision naming the one it
// replaces, and the chain reads in both directions.
//
// # Why it demands the appeal
//
// The schema requires a grievance on any revision. An adjudicated figure may
// change only through a process the farmer can see and could themselves have
// started -- never by an officer revisiting their own decision off the
// record.
func (s *Store) Revise(ctx context.Context, in Revision) (*Adjudication, error) {
	if len(strings.TrimSpace(in.Reason)) < 10 {
		return nil, fmt.Errorf(
			"%w: record why the figure changed", ErrBadInput)
	}
	if in.RevisedPercent < 0 || in.RevisedPercent > 100 {
		return nil, fmt.Errorf("%w: the figure is out of range", ErrBadInput)
	}

	current, err := s.Adjudication(ctx, in.ReportID)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := s.revise(ctx, tx, in, current); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Adjudication(ctx, in.ReportID)
}

// revise does the work inside a caller's transaction, so that deciding an
// appeal and revising the figure it was about either both happen or neither
// does. A grievance recorded as upheld beside a report still carrying the old
// number is the exact inconsistency this whole change exists to remove.
func (s *Store) revise(
	ctx context.Context, tx pgx.Tx, in Revision, current *Adjudication,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO report_adjudications (
			report_id, officer_id, role,
			shown_first_percent, shown_second_percent, shown_model_percent,
			divergence, route,
			adjudicated_percent, adjudicated_area_ha, reason,
			supersedes_id, grievance_id
		) VALUES ($1, $2, $3::officer_role, $4, $5, $6, $7, $8, $9, $10, $11,
		          $12, $13)`,
		in.ReportID, in.OfficerID, string(in.Role),
		current.ShownFirstPercent, current.ShownSecondPercent,
		current.ShownModelPercent, current.Divergence, current.Route,
		in.RevisedPercent, in.RevisedAreaHa, strings.TrimSpace(in.Reason),
		current.ID, in.GrievanceID,
	)
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf(
				"%w: this decision has already been revised", ErrBadState)
		}
		return fmt.Errorf("%w: %s", ErrBadInput, cleanErr(err))
	}

	// Lifted onto the report, because that is what every rollup, worklist and
	// assessment report reads.
	_, err = tx.Exec(ctx, `
		UPDATE crop_loss_reports
		SET adjudicated_percent = $2, adjudicated_area_ha = $3,
		    adjudicated_at = now()
		WHERE id = $1`,
		in.ReportID, in.RevisedPercent, in.RevisedAreaHa)
	return err
}

// Revision is a figure changed by an upheld appeal.
type Revision struct {
	ReportID    string
	GrievanceID string
	OfficerID   string
	Role        rbac.Role

	RevisedPercent float64
	RevisedAreaHa  float64
	Reason         string
}

// Adjudication returns a report's final decision, if it has one.
func (s *Store) Adjudication(ctx context.Context, reportID string) (*Adjudication, error) {
	var a Adjudication
	var role string
	err := s.pool.QueryRow(ctx, `
		SELECT j.id, j.officer_id, u.full_name, j.role::text,
		       j.shown_first_percent, j.shown_second_percent, j.shown_model_percent,
		       j.divergence, j.route,
		       j.adjudicated_percent, j.adjudicated_area_ha, j.reason, j.decided_at
		FROM report_adjudications j
		JOIN users u ON u.id = j.officer_id
		WHERE j.report_id = $1
		  -- The end of the chain: the decision nothing has replaced. A report
		  -- whose appeal was upheld has two adjudications and only one current
		  -- figure, and every caller wants the current one.
		  AND NOT EXISTS (
		      SELECT 1 FROM report_adjudications later
		      WHERE later.supersedes_id = j.id
		  )`, reportID).Scan(
		&a.ID, &a.OfficerID, &a.OfficerName, &role,
		&a.ShownFirstPercent, &a.ShownSecondPercent, &a.ShownModelPercent,
		&a.Divergence, &a.Route,
		&a.AdjudicatedPercent, &a.AdjudicatedAreaHa, &a.Reason, &a.DecidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Role = rbac.Role(role)
	return &a, nil
}

// Worklist is what one officer has been asked to do.
type WorklistEntry struct {
	Assignment
	Reference    string    `json:"reference"`
	FarmerName   string    `json:"farmer_name"`
	VillageName  string    `json:"village_name"`
	SurveyNumber string    `json:"survey_number,omitempty"`
	CalamityType string    `json:"calamity_type"`
	OccurredAt   time.Time `json:"occurred_at"`
	ReportState  string    `json:"report_state"`
	PeerRecorded bool      `json:"peer_recorded"`
}

// Worklist returns an officer's open assignments, oldest deadline first.
//
// peer_recorded is shown and the peer's figure is not: an officer should know
// their colleague has been, so they do not both drive out on the same morning,
// without learning what they concluded.
// Worklist is what one officer has been asked to verify.
//
// Bounded, because after a calamity a Talathi's list is not the handful it is
// on an ordinary week. Overdue is counted across the whole list rather than
// the page: an officer told "3 overdue" while looking at page one, when page
// four holds nine more, has been told something false.
func (s *Store) Worklist(
	ctx context.Context, officerID string, includeDone bool, p page.Params,
) (page.Result[WorklistEntry], int, error) {
	search := ""
	if p.Query != "" {
		search = page.Like(p.Query)
	}

	const where = `a.officer_id = $1 AND a.superseded_at IS NULL
		  AND ($2 OR a.completed_at IS NULL)
		  AND ($5::text = '' OR r.reference ILIKE $5
		                     OR r.farmer_name ILIKE $5
		                     OR r.village_name ILIKE $5)`

	var total, overdue int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE a.completed_at IS NULL AND a.due_at < now())
		FROM report_assignments a
		JOIN crop_loss_reports r ON r.id = a.report_id
		WHERE `+strings.ReplaceAll(where, "$5", "$3"),
		officerID, includeDone, search).Scan(&total, &overdue); err != nil {
		return page.Result[WorklistEntry]{}, 0, fmt.Errorf("counting worklist: %w", err)
	}
	if total == 0 {
		return page.Wrap([]WorklistEntry{}, 0, p), 0, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.report_id, a.officer_id, u.full_name, a.role::text, a.tier,
		       a.assigned_at, a.due_at, a.completed_at,
		       r.reference, r.farmer_name, r.village_name,
		       r.calamity_type::text, r.occurred_at, r.state,
		       EXISTS (SELECT 1 FROM report_verifications v
		               WHERE v.report_id = a.report_id AND v.tier = a.tier
		                 AND v.officer_id <> a.officer_id)
		FROM report_assignments a
		JOIN users u ON u.id = a.officer_id
		JOIN crop_loss_reports r ON r.id = a.report_id
		WHERE `+where+`
		ORDER BY a.completed_at NULLS FIRST, a.due_at, a.id
		LIMIT $3 OFFSET $4`,
		officerID, includeDone, p.Limit, p.Offset, search)
	if err != nil {
		return page.Result[WorklistEntry]{}, 0, fmt.Errorf("reading worklist: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	out := []WorklistEntry{}
	for rows.Next() {
		var e WorklistEntry
		var role string
		if err := rows.Scan(&e.ID, &e.ReportID, &e.OfficerID, &e.OfficerName,
			&role, &e.Tier, &e.AssignedAt, &e.DueAt, &e.CompletedAt,
			&e.Reference, &e.FarmerName, &e.VillageName,
			&e.CalamityType, &e.OccurredAt, &e.ReportState, &e.PeerRecorded); err != nil {
			return page.Result[WorklistEntry]{}, 0, err
		}
		e.Role = rbac.Role(role)
		e.Overdue = e.CompletedAt == nil && now.After(e.DueAt)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return page.Result[WorklistEntry]{}, 0, err
	}
	return page.Wrap(out, total, p), overdue, nil
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// decideOnThePair writes the figure the two village officers agreed on.
//
// # Why the mean and not one of them
//
// They are two readings of the same field, within the consensus band of each
// other. Picking either would be picking a person, and there is no ground on
// which to prefer one -- Reconcile has already established that the difference
// between them is not a disagreement. The mean is what the Circle Officer used
// to be permitted to adjudicate on for exactly this case, and it is the same
// arithmetic now that nobody above the village is asked.
//
// A pair who both refused the claim have no figure to average, and none is
// written: the claim is decided at nil, which is what a refusal is.
func (s *Store) decideOnThePair(
	ctx context.Context, tx pgx.Tx, reportID string, c Consensus,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET state = 'adjudicated',
		       adjudicated_percent = $2,
		       adjudicated_at = now(),
		       contested = false,
		       approval_level = NULL,
		       approval_level_since = NULL,
		       updated_at = now()
		 WHERE id = $1`, reportID, c.Mean); err != nil {
		return fmt.Errorf("deciding on the pair's finding: %w", err)
	}
	return nil
}

// routeDisagreement sends a contested claim to the one officer who settles it.
//
// The Circle Officer where one is posted over the claim's village, and the
// Tehsildar where none is. Not a sequence: only one of them is asked, and the
// record has to show which.
//
// # Why the routing is by posting and not by how far apart they are
//
// It used to escalate on magnitude -- the Circle Officer for a moderate
// disagreement, the Tehsildar for a wide one. That distinction assumed a
// ladder to climb. With one settling officer the only question that matters is
// whether anybody holds the Circle over this village, because a claim routed
// to a vacant posting waits for ever.
//
// A lookup that fails sends the claim to the Tehsildar. The Tehsil is always
// staffed in a district running an event, and a claim parked at an empty
// Circle is worse than one parked a level up.
func (s *Store) routeDisagreement(
	ctx context.Context, tx pgx.Tx, reportID string,
) error {
	to := LevelTehsil
	if s.circleIsStaffed(ctx, tx, reportID) {
		to = LevelCircle
	}

	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET approval_level = $2::approval_level,
		       approval_level_since = now(),
		       contested = true,
		       updated_at = now()
		 WHERE id = $1 AND approval_level IS NULL`, reportID, string(to)); err != nil {
		return fmt.Errorf("routing a disagreement to the %s: %w", to, err)
	}

	// Never fatal: a claim that was routed correctly and could not be
	// announced is still a claim that was routed.
	if err := s.arrived(ctx, tx, reportID, to); err != nil {
		s.noticeFailed("announcing a disagreement to settle", reportID, err)
	}
	return nil
}

// circleIsStaffed reports whether any active Circle Officer covers this
// claim's village.
func (s *Store) circleIsStaffed(
	ctx context.Context, tx pgx.Tx, reportID string,
) bool {
	if s.officers == nil {
		return false
	}
	var villageID string
	if err := tx.QueryRow(ctx, `
		SELECT v.id
		  FROM crop_loss_reports r
		  JOIN jurisdictions v
		    ON v.code = r.village_lgd_code AND v.level = 'village'
		 WHERE r.id = $1`, reportID).Scan(&villageID); err != nil {
		return false
	}
	to, err := s.officers.Recipients(ctx, tx, villageID, LevelCircle.Roles())
	return err == nil && len(to) > 0
}
