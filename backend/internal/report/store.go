package report

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"strings"
)

const reportColumns = `
	r.id, r.reference, r.survey_event_id, COALESCE(e.reference, ''),
	r.farmer_id, r.farmer_name,
	COALESCE(r.farmer_address, ''), COALESCE(r.farmer_mobile, ''),
	COALESCE(r.farmer_email, ''),
	r.village_lgd_code, r.village_name,
	COALESCE(r.circle_code, ''), COALESCE(r.circle_name, ''),
	r.sub_district_lgd_code, r.sub_district_name,
	r.district_lgd_code, r.district_name,
	r.season::text, r.crop_year,
	r.loss_category::text, r.calamity_type::text, r.occurred_at,
	r.reported_at, COALESCE(r.reported_by::text, ''), r.reported_channel,
	r.filed_late, COALESCE(r.late_reason, ''), r.filed_at,
	r.arrived_after_close,
	r.declaration_accepted, r.documents_promised,
	COALESCE(r.farmer_signature_kind, ''), r.farmer_signed_at,
	COALESCE(r.acknowledged_by::text, ''), COALESCE(ack.full_name, ''),
	r.acknowledged_at, r.state, r.created_at,
	r.adjudicated_percent, r.adjudicated_area_ha, r.adjudicated_at,
	COALESCE(r.approval_level::text, ''),
	r.capture_count, r.stations_captured`

const reportFrom = `
	FROM crop_loss_reports r
	LEFT JOIN survey_events e ON e.id = r.survey_event_id
	LEFT JOIN users ack ON ack.id = r.acknowledged_by`

func scanReport(row pgx.Row) (*Report, error) {
	var r Report
	var season, category, state, level string
	err := row.Scan(
		&r.ID, &r.Reference, &r.SurveyEventID, &r.SurveyEventReference,
		&r.FarmerID, &r.FarmerName,
		&r.FarmerAddress, &r.FarmerMobile, &r.FarmerEmail,
		&r.Place.VillageCode, &r.Place.VillageName,
		&r.Place.CircleCode, &r.Place.CircleName,
		&r.Place.SubDistrictCode, &r.Place.SubDistrictName,
		&r.Place.DistrictCode, &r.Place.DistrictName,
		&season, &r.CropYear,
		&category, &r.CalamityType, &r.OccurredAt,
		&r.ReportedAt, &r.ReportedBy, &r.ReportedChannel,
		&r.FiledLate, &r.LateReason, &r.FiledAt, &r.ArrivedAfterClose,
		&r.DeclarationAccepted, &r.DocumentsPromised,
		&r.FarmerSignatureKind, &r.FarmerSignedAt,
		&r.AcknowledgedBy, &r.AcknowledgedByName,
		&r.AcknowledgedAt, &state, &r.CreatedAt,
		&r.AdjudicatedPercent, &r.AdjudicatedAreaHa, &r.AdjudicatedAt,
		&level, &r.CaptureCount, &r.StationsCaptured,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Season = Season(season)
	r.LossCategory = LossCategory(category)
	r.State = State(state)
	r.ApprovalLevel = Level(level)
	if r.DocumentsPromised == nil {
		r.DocumentsPromised = []string{}
	}
	return &r, nil
}

// awaitingPredicate narrows a listing to one rung of the approval ladder.
//
// # Why finished reports are excluded
//
// approval_level records the rung a report reached, and it keeps recording it
// after the District has signed: the report stops climbing but does not stop
// having climbed. Filtering on the column alone therefore leaves every
// finished report sitting in the queue of the level that finished it, where
// an officer can select it, press approve, and be told the report is closed.
//
// A queue that never empties is a queue nobody trusts. The rung says where a
// report got to; the state says whether anything is still owed on it, and a
// queue is a question about the second.
func awaitingPredicate(param string) string {
	return `(` + param + `::text = '' OR (
		r.approval_level::text = ` + param + `
		AND r.state NOT IN ('adjudicated', 'closed', 'withdrawn')))`
}

// scopePredicate is the jurisdiction filter, written once.
//
// A farmer sees their own reports; an officer sees the ones in villages
// beneath their posting. Both are pushed into the query rather than applied to
// a loaded slice, for the same reason as everywhere else: a filter after the
// fact is one forgotten early return away from a leak.
func scopePredicate(jurParam, farmerParam string) string {
	return `(
		(` + farmerParam + `::text IS NOT NULL AND r.farmer_id = ` + farmerParam + `)
		OR (` + jurParam + `::uuid[] IS NOT NULL AND EXISTS (
			SELECT 1 FROM jurisdictions v
			WHERE v.level = 'village' AND v.code = r.village_lgd_code
			  AND (v.id = ANY(` + jurParam + `) OR v.ancestor_ids && ` + jurParam + `)
		))
	)`
}

// Get returns one report with its crop lines, if it lies inside the scope.
//
// Out of scope and non-existent answer identically, so that a listing of
// reference numbers cannot be turned into a map of which villages have filed.
func (s *Store) Get(ctx context.Context, id string, scope rbac.Scope) (*Report, error) {
	if scope.Empty() {
		return nil, ErrNotFound
	}

	r, err := scanReport(s.pool.QueryRow(ctx,
		`SELECT `+reportColumns+reportFrom+
			` WHERE r.id = $1 AND `+scopePredicate("$2", "$3"),
		id, nullableUUIDs(scope.JurisdictionIDs), nullString(scope.FarmerID)))
	if err != nil {
		return nil, err
	}
	return s.withItems(ctx, r)
}

// ByReference looks a report up by the number printed on the acknowledgement.
func (s *Store) ByReference(ctx context.Context, ref string, scope rbac.Scope) (*Report, error) {
	if scope.Empty() {
		return nil, ErrNotFound
	}
	r, err := scanReport(s.pool.QueryRow(ctx,
		`SELECT `+reportColumns+reportFrom+
			` WHERE r.reference = $1 AND `+scopePredicate("$2", "$3"),
		ref, nullableUUIDs(scope.JurisdictionIDs), nullString(scope.FarmerID)))
	if err != nil {
		return nil, err
	}
	return s.withItems(ctx, r)
}

func (s *Store) withItems(ctx context.Context, r *Report) (*Report, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT line_no, survey_number, COALESCE(sub_survey_number, ''),
		       COALESCE(farm_id, ''), crop_name,
		       area_sown_ha, area_affected_ha, damage_percent
		FROM crop_loss_report_items
		WHERE report_id = $1 ORDER BY line_no`, r.ID)
	if err != nil {
		return nil, fmt.Errorf("reading report lines: %w", err)
	}
	defer rows.Close()

	r.Items = []Item{}
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.LineNo, &i.SurveyNumber, &i.SubSurveyNumber,
			&i.FarmID, &i.CropName,
			&i.AreaSownHa, &i.AreaAffectedHa, &i.DamagePercent); err != nil {
			return nil, err
		}
		r.Items = append(r.Items, i)
	}
	return r, rows.Err()
}

// Filter narrows a listing.
type Filter struct {
	Scope    rbac.Scope
	EventID  string
	FarmerID string
	State    string

	// AwaitingLevel narrows to reports sitting at one rung of the approval
	// ladder. What turns the report list into an approval queue.
	AwaitingLevel string

	Page page.Params
}

// worklistCursor encodes where a page of the worklist stopped.
//
// The sort key, not the row: NOT contested, then when it was reported, then
// its id -- the same three values the next page compares against.
func worklistCursor(last Summary) string {
	notContested := "t"
	if last.Contested {
		notContested = "f"
	}
	return page.Cursor(notContested,
		last.ReportedAt.Format(time.RFC3339Nano), last.ID)
}

// searchPredicate is what a free-text search matches on.
//
// Four columns, because an officer arrives with whatever they happen to have:
// a reference number read out over the phone, a name, a village, or the last
// digits of a mobile from a call log. Making them type into the right box
// first would be a worse search than none.
//
// Every one of these is a substring match, which is why migration 0008 exists
// -- a B-tree cannot serve `%barshi%`, so without trigram indexes each of
// these is a sequential scan.
const searchPredicate = `(
	$8::text = '' OR
	r.reference ILIKE $8 OR
	r.farmer_name ILIKE $8 OR
	r.village_name ILIKE $8 OR
	r.farmer_mobile ILIKE $8
)`

// Summary is a report without its crop lines, for a worklist.
type Summary struct {
	ID              string    `json:"id"`
	Reference       string    `json:"reference"`
	FarmerID        string    `json:"farmer_id"`
	FarmerName      string    `json:"farmer_name"`
	VillageName     string    `json:"village_name"`
	VillageCode     string    `json:"village_lgd_code"`
	CircleName      string    `json:"circle_name,omitempty"`
	SubDistrictName string    `json:"sub_district_name"`
	LossCategory    string    `json:"loss_category"`
	CalamityType    string    `json:"calamity_type"`
	OccurredAt      time.Time `json:"occurred_at"`
	ReportedAt      time.Time `json:"reported_at"`
	FiledLate       bool      `json:"filed_late"`
	State           string    `json:"state"`

	Lines          int     `json:"lines"`
	AreaSownHa     float64 `json:"area_sown_ha"`
	AreaAffectedHa float64 `json:"area_affected_ha"`
	DamagePercent  float64 `json:"damage_percent"`

	// Where this report is on the approval ladder, and the two flags that
	// change how it may be handled there. Carried on the summary because the
	// queue is built from summaries and an officer must see, before opening
	// anything, which rows they cannot simply tick.
	// AssessedPercent is the figure the ladder is actually carrying: what the
	// last standing rung passed up, or failing that what the village pair
	// found. DamagePercent above is the farmer's own declaration, weighted
	// across their crop lines, and the two are different claims about the same
	// field -- 48.8% declared against 63.5% assessed, on the reports in front
	// of me while writing this.
	//
	// Both belong on the row. Showing only the declaration in an approval
	// queue puts the wrong number under an officer's cursor: the dialog opens
	// pre-filled with it, approving looks like a no-op, and the server -- which
	// compares against the assessed figure -- either refuses for a missing
	// reason or, if one is typed, records a fifteen-point cut nobody meant.
	AssessedPercent *float64 `json:"assessed_percent,omitempty"`

	ApprovalLevel string `json:"approval_level,omitempty"`
	Contested     bool   `json:"contested"`
	CropMismatch  bool   `json:"crop_mismatch"`
	RegistryCrop  string `json:"registry_crop_name,omitempty"`
	ReturnedCount int    `json:"returned_count"`

	// How much evidence the claim rests on. Both counters sit on the report
	// row itself, maintained by a trigger as captures arrive, so carrying them
	// on the summary costs the queue nothing.
	//
	// They are on the row rather than only in the dossier because an officer
	// approving in a batch never opens the dossier: before this, a claim shot
	// from one corner and a claim walked to all four points looked identical
	// in the queue (docs/EVIDENCE_REVALIDATION.md, V8).
	CaptureCount     int `json:"capture_count"`
	StationsCaptured int `json:"stations_captured"`

	// ArrivedAfterClose means the claim was completed before the event
	// stopped taking claims and delivered after it. On the row because a
	// district that has closed its books needs to see, in the queue, which
	// figures arrived after they did.
	ArrivedAfterClose bool `json:"arrived_after_close"`
}

// List returns summaries, newest first, restricted to the scope.
//
// The totals are computed in SQL rather than by loading every line. The
// weighted percentage is affected-area-weighted, not a mean of the line
// percentages: a total loss on a tenth of a hectare and a scratch on four
// hectares do not average to half.
func (s *Store) List(ctx context.Context, f Filter) (page.Result[Summary], error) {
	if f.Scope.Empty() {
		return page.Wrap([]Summary{}, 0, f.Page), nil
	}

	jur := nullableUUIDs(f.Scope.JurisdictionIDs)
	farmer := nullString(f.Scope.FarmerID)
	search := ""
	if f.Page.Query != "" {
		search = page.Like(f.Page.Query)
	}

	// The count runs against the same predicates as the page, and without the
	// lateral join that computes the crop-line totals -- those cost work per
	// row and no part of a count needs them.
	// Bounded, for the same reason as the audit log: this is the listing an
	// officer lives in, and a district's report count grows without limit
	// across seasons. See page.CountCeiling.
	// The count is for the numbered pager, and a cursor pager has no numbers:
	// it offers "next", which NextCursor answers. Counting on every cursor
	// page would be the expensive half of a page that exists to be cheap.
	var total int
	if f.Page.Cursor != "" {
		total = -1
	} else if err := s.pool.QueryRow(ctx, page.CountSQL(`
		SELECT 1 FROM crop_loss_reports r
		WHERE `+scopePredicate("$1", "$2")+`
		  AND ($3::text = '' OR r.survey_event_id::text = $3)
		  AND ($4::text = '' OR r.farmer_id = $4)
		  AND ($5::text = '' OR r.state = $5)
		  AND `+awaitingPredicate("$7")+`
		  AND `+strings.ReplaceAll(searchPredicate, "$8", "$6")),
		jur, farmer, f.EventID, f.FarmerID, f.State, search,
		f.AwaitingLevel).Scan(&total); err != nil {
		return page.Result[Summary]{}, fmt.Errorf("counting reports: %w", err)
	}
	if total == 0 {
		return page.Wrap([]Summary{}, 0, f.Page), nil
	}

	// Paging by cursor where the caller sent one: an offset makes the database
	// walk the rows it skips, which is why offsets are refused past
	// page.MaxOffset. A cursor continues from the last row of the previous
	// page at the same cost however deep it goes. See page.Params.Cursor.
	after := "TRUE"
	offset := f.Page.Offset
	var cursorContested bool
	var cursorReported time.Time
	var cursorID string
	if f.Page.Cursor != "" {
		parts, ok := page.DecodeCursor(f.Page.Cursor, 3)
		if !ok {
			return page.Result[Summary]{}, fmt.Errorf(
				"%w: that cursor is not one this listing issued", ErrBadInput)
		}
		at, err := time.Parse(time.RFC3339Nano, parts[1])
		if err != nil {
			return page.Result[Summary]{}, fmt.Errorf(
				"%w: that cursor is not one this listing issued", ErrBadInput)
		}
		// The cursor carries the sort key, which is NOT contested.
		cursorContested, cursorReported, cursorID = parts[0] == "t", at, parts[2]
		// Everything after the last row the caller saw, in this listing's own
		// order, as one ascending row comparison -- which is what the index
		// can answer. See migration 0038 for why the key is NOT contested.
		after = `((NOT r.contested), r.reported_at, r.id) >
		         ($10::boolean, $11::timestamptz, $12::uuid)`
		offset = 0
	}

	args := []any{jur, farmer, f.EventID, f.FarmerID, f.State,
		f.Page.Limit, offset, search, f.AwaitingLevel}
	if f.Page.Cursor != "" {
		args = append(args, cursorContested, cursorReported, cursorID)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.reference, r.farmer_id, r.farmer_name,
		       r.village_name, r.village_lgd_code,
		       COALESCE(r.circle_name, ''), r.sub_district_name,
		       r.loss_category::text, r.calamity_type::text,
		       r.occurred_at, r.reported_at, r.filed_late, r.state,
		       COALESCE(t.lines, 0), COALESCE(t.sown, 0), COALESCE(t.affected, 0),
		       CASE WHEN COALESCE(t.sown, 0) > 0
		            THEN t.weighted / t.sown ELSE 0 END,
		       -- The ladder's standing figure, in the same order the store's
		       -- own standing() resolves it, so the queue and the decision
		       -- cannot disagree about what is being approved.
		       COALESCE(
		         (SELECT a.percent_after FROM report_approvals a
		           WHERE a.report_id = r.id AND a.decision = 'approved'
		             AND a.superseded_at IS NULL
		           ORDER BY a.decided_at DESC LIMIT 1),
		         r.adjudicated_percent,
		         (SELECT avg(v.damage_percent) FROM report_verifications v
		           WHERE v.report_id = r.id AND v.tier = 'village_pair'
		             AND v.superseded_at IS NULL AND v.damage_percent IS NOT NULL)),
		       COALESCE(r.approval_level::text, ''), r.contested,
		       r.crop_mismatch, COALESCE(r.registry_crop_name, ''),
		       r.returned_count,
		       r.capture_count, r.stations_captured,
		       r.arrived_after_close
		FROM crop_loss_reports r
		LEFT JOIN LATERAL (
			SELECT count(*) AS lines,
			       sum(i.area_sown_ha) AS sown,
			       sum(i.area_affected_ha) AS affected,
			       sum(i.area_affected_ha * i.damage_percent) AS weighted
			FROM crop_loss_report_items i WHERE i.report_id = r.id
		) t ON true
		WHERE `+scopePredicate("$1", "$2")+`
		  AND ($3::text = '' OR r.survey_event_id::text = $3)
		  AND ($4::text = '' OR r.farmer_id = $4)
		  AND ($5::text = '' OR r.state = $5)
		  AND `+awaitingPredicate("$9")+`
		  AND `+searchPredicate+`
		  AND `+after+`
		-- Contested first, then oldest. A queue sorted only by age buries the
		-- reports two officers disagreed about under the ones nobody did, and
		-- those are exactly the ones a supervisor is for.
		ORDER BY (NOT r.contested), r.reported_at ASC, r.id ASC
		LIMIT $6 OFFSET $7`,
		args...)
	if err != nil {
		return page.Result[Summary]{}, fmt.Errorf("listing reports: %w", err)
	}
	defer rows.Close()

	out := []Summary{}
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.ID, &s.Reference, &s.FarmerID, &s.FarmerName,
			&s.VillageName, &s.VillageCode, &s.CircleName, &s.SubDistrictName,
			&s.LossCategory, &s.CalamityType, &s.OccurredAt, &s.ReportedAt,
			&s.FiledLate, &s.State,
			&s.Lines, &s.AreaSownHa, &s.AreaAffectedHa, &s.DamagePercent,
			&s.AssessedPercent, &s.ApprovalLevel, &s.Contested, &s.CropMismatch, &s.RegistryCrop,
			&s.ReturnedCount,
			&s.CaptureCount, &s.StationsCaptured,
			&s.ArrivedAfterClose); err != nil {
			return page.Result[Summary]{}, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return page.Result[Summary]{}, err
	}
	if total < 0 {
		// A cursor page: no count was taken, so nothing here may claim one.
		res := page.Result[Summary]{
			Items: out, Limit: f.Page.Limit, Query: f.Page.Query,
		}
		if res.Items == nil {
			res.Items = []Summary{}
		}
		if len(out) == f.Page.Limit {
			last := out[len(out)-1]
			res.NextCursor = worklistCursor(last)
			res.HasMore = true
		}
		return res, nil
	}

	res := page.Wrap(out, total, f.Page)
	if len(out) == f.Page.Limit {
		last := out[len(out)-1]
		res.NextCursor = worklistCursor(last)
	}
	return res, nil
}

// transitions lists the moves an officer may make by hand.
//
// It had said acknowledged -> inspected -> closed, and "inspected" is not a
// state this system has: the database CHECK rejects it, so that move returned
// a constraint violation rather than a refusal. The verification workflow
// replaced it -- allocation writes assigned, filing findings writes
// under_verification and then verified, adjudication writes adjudicated --
// and this table was never brought forward with it.
//
// Those four are absent from the map's values on purpose. They are reached by
// doing the work, not by selecting a status: a report becomes verified
// because two officers filed findings, and offering a button that says so
// without them would make the audit trail a claim rather than a record. What
// remains here is what a human legitimately decides: submit, acknowledge,
// close a finished report, withdraw an unfinished one.
var transitions = map[State][]State{
	StateDraft:             {StateSubmitted, StateWithdrawn},
	StateSubmitted:         {StateAcknowledged, StateWithdrawn},
	StateAcknowledged:      {StateWithdrawn},
	StateAssigned:          {StateWithdrawn},
	StateUnderVerification: {},
	StateVerified:          {StateClosed},
	StateAdjudicated:       {StateClosed},
	StateClosed:            {},
	StateWithdrawn:         {},
}

// TerminalStates are the ones no further work is expected on.
var TerminalStates = []State{StateClosed, StateWithdrawn}

// Terminal reports whether a report has finished its life.
func (s State) Terminal() bool { return slices.Contains(TerminalStates, s) }

// Transition moves a report, recording the officer where the move needs one.
//
// Acknowledgement is the पोहोच पावती: a named officer, at a recorded time,
// confirming the report reached the administration. It is the farmer's proof
// that they filed, so it is not a status flag the system sets on its own.
func (s *Store) Transition(
	ctx context.Context, id string, to State, by string, scope rbac.Scope,
) (*Report, error) {
	r, err := s.Get(ctx, id, scope)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(transitions[r.State], to) {
		return nil, fmt.Errorf("%w: cannot move a report from %s to %s",
			ErrBadState, r.State, to)
	}
	if to == StateSubmitted && !r.DeclarationAccepted {
		return nil, fmt.Errorf(
			"%w: the farmer's declaration and mark are missing", ErrBadState)
	}

	// A transaction, so the message the farmer is entitled to commits with
	// the acknowledgement itself. Acknowledging and then dying before the
	// message is queued leaves a farmer holding a receipt nobody told them
	// about, and nothing later can detect it.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if to == StateAcknowledged {
		_, err = tx.Exec(ctx, `
			UPDATE crop_loss_reports
			SET state = $2, acknowledged_by = $3, acknowledged_at = now()
			WHERE id = $1`, id, string(to), nullString(by))
	} else {
		_, err = tx.Exec(ctx,
			`UPDATE crop_loss_reports SET state = $2 WHERE id = $1`,
			id, string(to))
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadState, cleanErr(err))
	}

	// The पोहोच पावती. This is the one message a farmer is owed by the act of
	// filing, and the only proof they have that the report reached anyone.
	if to == StateAcknowledged {
		s.tell(ctx, tx, notify.EventReportAcknowledged, r, notifyVars(r))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, id, scope)
}

func nullableUUIDs(ids []string) any {
	if len(ids) == 0 {
		return nil
	}
	return ids
}
