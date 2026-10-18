// Package event owns survey events — a district's notification that a
// calamity occurred, and the areas it covers.
//
// The rule the whole package exists to enforce: a farmer may be surveyed
// against an event only if their land sits inside one of the talukas or
// circles that event names, and only while the event is open. Everything else
// here is bookkeeping around that sentence.
package event

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Errors.
var (
	ErrNotFound     = errors.New("survey event not found")
	ErrBadState     = errors.New("the event is not in a state that allows this")
	ErrNotEligible  = errors.New("the parcel is not covered by this event")
	ErrOutOfWindow  = errors.New("the incident date is outside the event window")
	ErrSurveyClosed = errors.New("surveying for this event has closed")
	ErrBadArea      = errors.New("invalid event area")
)

// State is an event's lifecycle position.
type State string

const (
	StateDraft    State = "draft"
	StateDeclared State = "declared"
	StateOpen     State = "open"
	// StatePaused suspends filing without ending it. A road is impassable, a
	// district wants collection stopped for two days, and the filing deadline
	// has not moved -- which is precisely what closing the event would say.
	StatePaused    State = "paused"
	StateClosed    State = "closed"
	StateComplete  State = "assessment_complete"
	StateCancelled State = "cancelled"
)

// Calamity types, matching the database enum.
var calamityTypes = []string{
	"flood", "cyclone", "hailstorm", "unseasonal_rain", "drought",
	"pest_attack", "landslide", "cloudburst", "frost", "fire",
}

// ValidCalamity reports whether c is a known calamity type.
func ValidCalamity(c string) bool { return slices.Contains(calamityTypes, c) }

// CalamityTypes returns the closed set, for a form to render.
func CalamityTypes() []string { return slices.Clone(calamityTypes) }

// Area is one taluka or circle an event covers.
type Area struct {
	JurisdictionID string     `json:"jurisdiction_id"`
	Level          rbac.Level `json:"level"`
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	// VillageCount is how many villages the area resolves to. An event named
	// over circles that have no villages mapped covers nobody, and the number
	// is the only way an officer would notice before farmers cannot file.
	VillageCount int `json:"village_count"`
}

// Crop is one crop a survey event covers.
//
// The code is AgriStack's, because the comparison that matters happens against
// a farmer's crop_sown_data and an equality test on a code beats a string
// match on names that differ by spelling, language and case between two
// systems. The name is carried so an officer reading the event does not have
// to know what 0201001 is.
type Crop struct {
	Code string `json:"crop_code"`
	Name string `json:"crop_name"`
}

// Event is a district's calamity notification.
type Event struct {
	ID           string `json:"id"`
	Reference    string `json:"reference"`
	CalamityType string `json:"calamity_type"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`

	NotificationRef string `json:"notification_ref,omitempty"`

	DistrictID   string `json:"district_id"`
	DistrictCode string `json:"district_code"`
	DistrictName string `json:"district_name"`

	IncidentStart string `json:"incident_start"`
	IncidentEnd   string `json:"incident_end"`

	// Season and CropYear are derived from the incident date, not stored.
	//
	// # Why they are here at all
	//
	// Every crop loss report must state both -- validate refuses one without
	// a crop year -- and until now nothing told a client what they were. The
	// farmer app sent an empty string and every report it filed came back
	// 400, which the farmer met as a report that never arrived.
	//
	// # Why derived rather than a column
	//
	// A hailstorm on 20 August 2026 is a kharif 2026-27 event; the season is
	// a fact about when it happened, not a separate decision somebody makes.
	// Deriving it means it cannot drift from incident_start, and -- more
	// importantly -- it means there is one answer. Two clients each working
	// it out for themselves would eventually disagree, and a report filed at
	// a counter would carry a different season from the same event filed on a
	// phone.
	//
	// The limit is real and worth naming: an event that a Collector wants to
	// declare for a season the calendar does not agree with -- a late rabi
	// loss reported in a kharif month -- cannot be expressed. That needs a
	// column and a field on the declaration form, and it is a decision for
	// somebody who issues notifications rather than a default.
	Season   string `json:"season"`
	CropYear string `json:"crop_year"`

	SurveyOpensAt  *time.Time `json:"survey_opens_at,omitempty"`
	SurveyClosesAt time.Time  `json:"survey_closes_at"`

	// LossCategories are the categories a claim against this event may be
	// filed under, already narrowed to the ones its calamity permits.
	//
	// Filled by the API layer from report.CalamitiesFor rather than stored:
	// the rule lives with the reports package and duplicating it here is how
	// the two drift. Empty on an event nobody has narrowed yet.
	//
	// # Why the narrowing happens here and not on the handset
	//
	// A drought is not a post-harvest loss and a cyclone on cut crop lying in
	// the field is not a standing-crop one. The server refuses the mismatch,
	// and a form that offered the choice anyway would let a farmer pick it,
	// walk four points of their field, and be refused afterwards. An option a
	// farmer cannot complete must not be on the screen.
	LossCategories []LossCategoryOption `json:"loss_categories,omitempty"`

	State State `json:"state"`

	DeclaredBy string     `json:"declared_by,omitempty"`
	DeclaredAt *time.Time `json:"declared_at,omitempty"`

	Areas []Area `json:"areas"`

	// Crops the event covers. Empty means every crop, which is the correct
	// reading of a Collector who did not narrow it: a cyclone does not check
	// what was planted.
	Crops []Crop `json:"crops"`

	// VillageCount is the union across areas — the number of villages whose
	// farmers may be surveyed.
	VillageCount int `json:"village_count"`

	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SurveyOpen reports whether a survey may be recorded against the event now.
func (e *Event) SurveyOpen(now time.Time) bool {
	if e.State != StateOpen {
		return false
	}
	if e.SurveyOpensAt != nil && now.Before(*e.SurveyOpensAt) {
		return false
	}
	return now.Before(e.SurveyClosesAt)
}

// New is the input for creating an event.
type New struct {
	CalamityType    string
	Title           string
	Description     string
	NotificationRef string
	DistrictID      string
	IncidentStart   string
	IncidentEnd     string
	SurveyOpensAt   *time.Time
	SurveyClosesAt  time.Time
	// AreaCodes name the talukas and circles covered, by jurisdiction code.
	AreaCodes []string

	// Crops narrows the event to particular crops. Empty covers all of them.
	Crops []Crop

	CreatedBy string
}

// Store persists survey events.
type Store struct {
	pool *db.Pool

	// officers is the console's bell. Optional -- see notify.go, where the
	// announcements live and where the reasoning for who is told is written
	// out. A deployment without it records nothing.
	officers *notify.Officers

	// log records announcements that could not be written. Never a reason to
	// fail the transition that caused them.
	log *slog.Logger
}

// NewStore builds a store.
func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// WithLogger attaches a logger for the things this store declines to fail on.
func (s *Store) WithLogger(l *slog.Logger) *Store {
	s.log = l
	return s
}

const eventColumns = `
	e.id, e.reference, e.calamity_type::text, e.title, COALESCE(e.description, ''),
	COALESCE(e.notification_ref, ''),
	e.district_id, d.code, d.name,
	e.incident_start, e.incident_end,
	e.survey_opens_at, e.survey_closes_at, e.state::text,
	COALESCE(e.declared_by::text, ''), e.declared_at,
	COALESCE(e.created_by::text, ''), e.created_at`

func scanEvent(row pgx.Row) (*Event, error) {
	var e Event
	var start, end time.Time
	var state string
	err := row.Scan(
		&e.ID, &e.Reference, &e.CalamityType, &e.Title, &e.Description,
		&e.NotificationRef,
		&e.DistrictID, &e.DistrictCode, &e.DistrictName,
		&start, &end,
		&e.SurveyOpensAt, &e.SurveyClosesAt, &state,
		&e.DeclaredBy, &e.DeclaredAt,
		&e.CreatedBy, &e.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.IncidentStart = start.Format("2006-01-02")
	e.IncidentEnd = end.Format("2006-01-02")
	e.Season, e.CropYear = SeasonAndCropYear(start)
	e.State = State(state)
	return &e, nil
}

// Create writes a draft event with its areas.
//
// Draft, always. Declaring is a separate act by a named officer, because a
// notification is a thing a Collector signs rather than a side effect of
// filling in a form.
func (s *Store) Create(ctx context.Context, in New) (*Event, error) {
	if !ValidCalamity(in.CalamityType) {
		return nil, fmt.Errorf("%w: unknown calamity type %q", ErrBadArea, in.CalamityType)
	}
	if len(in.AreaCodes) == 0 {
		return nil, fmt.Errorf(
			"%w: an event must name at least one taluka or circle, or it covers nobody",
			ErrBadArea)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	ref, err := nextReference(ctx, tx, in.DistrictID, in.IncidentStart)
	if err != nil {
		return nil, err
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO survey_events (
			reference, calamity_type, title, description, notification_ref,
			district_id, incident_start, incident_end,
			survey_opens_at, survey_closes_at, created_by
		) VALUES (
			$1, $2::calamity_type, $3, NULLIF($4, ''), NULLIF($5, ''),
			$6, $7::date, $8::date, $9, $10, $11
		) RETURNING id`,
		ref, in.CalamityType, in.Title, in.Description, in.NotificationRef,
		in.DistrictID, in.IncidentStart, in.IncidentEnd,
		in.SurveyOpensAt, in.SurveyClosesAt, nullString(in.CreatedBy),
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("creating event: %w", err)
	}

	if err := addCrops(ctx, tx, id, in.Crops, in.CreatedBy); err != nil {
		return nil, err
	}

	if err := addAreas(ctx, tx, id, in.AreaCodes, in.CreatedBy); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// addCrops attaches the crops an event covers.
//
// No crops is not an error. An event that names none covers every crop, and a
// Collector declaring a cyclone across four talukas has no reason to enumerate
// what was planted in them.
func addCrops(ctx context.Context, tx pgx.Tx, eventID string, crops []Crop, by string) error {
	for _, c := range crops {
		code := strings.TrimSpace(c.Code)
		name := strings.TrimSpace(c.Name)
		if code == "" || name == "" {
			return fmt.Errorf(
				"%w: a crop needs both a code and a name; got %q / %q",
				ErrBadArea, c.Code, c.Name)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO survey_event_crops (event_id, crop_code, crop_name, added_by)
			VALUES ($1, $2, $3, $4) ON CONFLICT (event_id, crop_code) DO NOTHING`,
			eventID, code, name, nullString(by)); err != nil {
			return fmt.Errorf("attaching crop %s: %w", code, err)
		}
	}
	return nil
}

// addAreas resolves codes to jurisdictions and attaches them.
//
// The database trigger is the authority on whether an area is acceptable —
// the right level, inside the declaring district. This resolves the code and
// lets the trigger refuse, so that there is one rule rather than two that can
// disagree.
func addAreas(ctx context.Context, tx pgx.Tx, eventID string, codes []string, by string) error {
	for _, code := range codes {
		var jid string
		err := tx.QueryRow(ctx, `
			SELECT id FROM jurisdictions
			WHERE code = $1 AND level IN ('sub_district', 'circle')`, code).Scan(&jid)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no taluka or circle with code %q", ErrBadArea, code)
		}
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO survey_event_areas (event_id, jurisdiction_id, added_by)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			eventID, jid, nullString(by)); err != nil {
			return fmt.Errorf("%w: %s", ErrBadArea, cleanTriggerError(err))
		}
	}
	return nil
}

// nextReference builds SE-<year>-<district>-<seq>.
//
// Sequenced per district per year and unique in the schema, so two Collectors
// declaring at once cannot collide and a retry is visible rather than silent.
func nextReference(ctx context.Context, tx pgx.Tx, districtID, incidentStart string) (string, error) {
	var districtCode string
	if err := tx.QueryRow(ctx,
		`SELECT code FROM jurisdictions WHERE id = $1 AND level = 'district'`,
		districtID).Scan(&districtCode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("%w: no such district", ErrBadArea)
		}
		return "", err
	}

	// From a counter row, not a count: two events created in the same district
	// in the same instant would otherwise be given the same reference and one
	// refused by the unique index. See migration 0034, and 0033 for the same
	// fix on claim references, where it was measured failing.
	year := incidentStart[:4]
	var n int
	if err := tx.QueryRow(ctx, `
		INSERT INTO event_reference_counters (district_id, year, last_number)
		VALUES ($1, $2, 1)
		ON CONFLICT (district_id, year) DO UPDATE
		   SET last_number = event_reference_counters.last_number + 1
		RETURNING last_number`,
		districtID, year).Scan(&n); err != nil {
		return "", err
	}

	return fmt.Sprintf("SE-%s-%s-%03d", year, districtCode, n), nil
}

// Get returns one event with its areas.
func (s *Store) Get(ctx context.Context, id string) (*Event, error) {
	e, err := scanEvent(s.pool.QueryRow(ctx, `
		SELECT `+eventColumns+`
		FROM survey_events e JOIN jurisdictions d ON d.id = e.district_id
		WHERE e.id = $1`, id))
	if err != nil {
		return nil, err
	}
	if _, err := s.withCrops(ctx, e); err != nil {
		return nil, err
	}
	return s.withAreas(ctx, e)
}

// ByReference looks an event up by the identifier printed on paper.
func (s *Store) ByReference(ctx context.Context, ref string) (*Event, error) {
	e, err := scanEvent(s.pool.QueryRow(ctx, `
		SELECT `+eventColumns+`
		FROM survey_events e JOIN jurisdictions d ON d.id = e.district_id
		WHERE e.reference = $1`, strings.ToUpper(strings.TrimSpace(ref))))
	if err != nil {
		return nil, err
	}
	if _, err := s.withCrops(ctx, e); err != nil {
		return nil, err
	}
	return s.withAreas(ctx, e)
}

// withCrops loads the crops an event covers.
//
// Empty is a meaningful answer and is kept as an empty slice rather than nil,
// because "covers every crop" and "we did not look" must not serialise alike
// to a console deciding whether to show a crop filter.
func (s *Store) withCrops(ctx context.Context, e *Event) (*Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT crop_code, crop_name FROM survey_event_crops
		WHERE event_id = $1 ORDER BY crop_name`, e.ID)
	if err != nil {
		return nil, fmt.Errorf("reading event crops: %w", err)
	}
	defer rows.Close()

	e.Crops = []Crop{}
	for rows.Next() {
		var c Crop
		if err := rows.Scan(&c.Code, &c.Name); err != nil {
			return nil, err
		}
		e.Crops = append(e.Crops, c)
	}
	return e, rows.Err()
}

func (s *Store) withAreas(ctx context.Context, e *Event) (*Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.level::text, j.code, j.name,
		       (SELECT count(*) FROM jurisdictions v
		         WHERE v.level = 'village'
		           AND (v.id = j.id OR j.id = ANY (v.ancestor_ids)))
		FROM survey_event_areas a
		JOIN jurisdictions j ON j.id = a.jurisdiction_id
		WHERE a.event_id = $1
		ORDER BY j.level::text, j.name`, e.ID)
	if err != nil {
		return nil, fmt.Errorf("reading event areas: %w", err)
	}
	defer rows.Close()

	e.Areas = []Area{}
	for rows.Next() {
		var a Area
		var level string
		if err := rows.Scan(&a.JurisdictionID, &level, &a.Code, &a.Name, &a.VillageCount); err != nil {
			return nil, err
		}
		a.Level = rbac.Level(level)
		e.Areas = append(e.Areas, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The union, not the sum: a circle inside a selected taluka would
	// otherwise be counted twice.
	if err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT v.id)
		FROM survey_event_areas a
		JOIN jurisdictions v
		  ON v.level = 'village'
		 AND (v.id = a.jurisdiction_id OR a.jurisdiction_id = ANY (v.ancestor_ids))
		WHERE a.event_id = $1`, e.ID).Scan(&e.VillageCount); err != nil {
		return nil, err
	}

	return e, nil
}

// ListFilter narrows an event listing.
type ListFilter struct {
	// JurisdictionIDs restricts to events whose district is at or above the
	// caller's posting, so a Talathi sees the events covering their village
	// and a Collector sees their district's.
	JurisdictionIDs []string
	State           string

	// Villages narrows to events covering at least one of these LGD village
	// codes, by the same village_in_event rule a filing is checked against.
	//
	// For a farmer, who holds no jurisdiction and so was sent every open event
	// in the state. The handset filtered by district, which cannot see the
	// talukas and circles an event actually covers -- so it offered an event
	// in the farmer's district that did not cover their village, and the
	// claim was filled in, walked and photographed before the office refused
	// it: "This plot is outside the survey area." Seen on a handset, on the
	// first real run.
	Villages []string

	Page page.Params
}

// eventVisible is the jurisdiction test, written once so the count and the
// page cannot drift apart -- a total computed against different predicates
// than the rows is a pager that offers a page which then comes back empty.
const eventVisible = `($1::uuid[] IS NULL
       OR d.id = ANY($1)
       OR d.ancestor_ids && $1
       OR EXISTS (SELECT 1 FROM unnest($1::uuid[]) AS caller(id)
                  JOIN jurisdictions cj ON cj.id = caller.id
                  WHERE d.id = ANY (cj.ancestor_ids)))`

// eventSearch matches the reference an officer was given and the district
// they know it by.
const eventSearch = `($5::text = '' OR e.reference ILIKE $5 OR d.name ILIKE $5)`

// GetVisible returns one event if its district is related to the caller's
// postings by the same rule as List: the district inside the caller's area,
// or the caller's area inside the district. An event elsewhere answers as not
// found, so an id cannot be used to read another state's declarations.
func (s *Store) GetVisible(ctx context.Context, id string, jurisdictionIDs []string) (*Event, error) {
	if len(jurisdictionIDs) == 0 {
		return nil, ErrNotFound
	}
	e, err := scanEvent(s.pool.QueryRow(ctx, `
		SELECT `+eventColumns+`
		FROM survey_events e JOIN jurisdictions d ON d.id = e.district_id
		WHERE `+eventVisible+` AND e.id = $2`, jurisdictionIDs, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return e, nil
}

// List returns events visible to the caller, newest first.
func (s *Store) List(ctx context.Context, f ListFilter) (page.Result[*Event], error) {
	jur := nullableUUIDs(f.JurisdictionIDs)
	search := ""
	if f.Page.Query != "" {
		search = page.Like(f.Page.Query)
	}

	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM survey_events e
		JOIN jurisdictions d ON d.id = e.district_id
		WHERE `+eventVisible+`
		  AND ($2::text = '' OR e.state::text = $2)
		  AND `+strings.ReplaceAll(eventSearch, "$5", "$3")+`
		  AND `+coversAny("$4"),
		jur, f.State, search, villageList(f.Villages)).Scan(&total); err != nil {
		return page.Result[*Event]{}, fmt.Errorf("counting events: %w", err)
	}
	if total == 0 {
		return page.Wrap([]*Event{}, 0, f.Page), nil
	}

	// An event is visible when the caller's posting is inside the declaring
	// district, or is the district, or contains it. A Talathi must see the
	// event that covers their village; a state officer sees all of theirs.
	rows, err := s.pool.Query(ctx, `
		SELECT `+eventColumns+`
		FROM survey_events e
		JOIN jurisdictions d ON d.id = e.district_id
		WHERE `+eventVisible+`
		  AND ($2::text = '' OR e.state::text = $2)
		  AND `+eventSearch+`
		  AND `+coversAny("$6")+`
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $3 OFFSET $4`,
		jur, f.State, f.Page.Limit, f.Page.Offset, search, villageList(f.Villages))
	if err != nil {
		return page.Result[*Event]{}, fmt.Errorf("listing events: %w", err)
	}
	defer rows.Close()

	out := []*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return page.Result[*Event]{}, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return page.Result[*Event]{}, err
	}

	// One query per event to load its areas. Bounded by the page size, which
	// is why the page size is bounded: this was previously bounded only by
	// whatever the caller asked for.
	for _, e := range out {
		if _, err := s.withAreas(ctx, e); err != nil {
			return page.Result[*Event]{}, err
		}
	}
	return page.Wrap(out, total, f.Page), nil
}

// Transition moves an event to a new state.
//
// The permitted moves are listed rather than inferred, so that adding one is a
// visible change to this table and not an emergent property of some condition
// elsewhere.
var transitions = map[State][]State{
	StateDraft:    {StateDeclared, StateCancelled},
	StateDeclared: {StateOpen, StateCancelled},
	StateOpen:     {StatePaused, StateClosed, StateCancelled},
	// Resuming returns to open. A paused event can also be closed directly:
	// a district that suspended filing and then decided the window was over
	// should not have to resume it for a moment first.
	StatePaused:    {StateOpen, StateClosed, StateCancelled},
	StateClosed:    {StateComplete, StateOpen}, // reopening a deadline is a real act
	StateComplete:  {},
	StateCancelled: {},
}

// Transition applies a state change by a named officer.
func (s *Store) Transition(ctx context.Context, id string, to State, by string) (*Event, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(transitions[e.State], to) {
		return nil, fmt.Errorf("%w: cannot move an event from %s to %s",
			ErrBadState, e.State, to)
	}
	// A declaration names the officer making it. The database enforces this
	// too -- declared_has_declarer -- but a check constraint surfaces as an
	// SQLSTATE, and this is a rule an officer should be told about in words.
	if to == StateDeclared && strings.TrimSpace(by) == "" {
		return nil, fmt.Errorf(
			"%w: a declaration must name the officer making it", ErrBadState)
	}
	if to == StateOpen && e.VillageCount == 0 {
		return nil, fmt.Errorf(
			"%w: the named areas resolve to no villages, so no farmer could be "+
				"surveyed; check that circles have been mapped for this taluka",
			ErrBadState)
	}

	switch to {
	case StateDeclared:
		_, err = s.pool.Exec(ctx, `
			UPDATE survey_events
			SET state = $2::survey_event_state, declared_by = $3, declared_at = now()
			WHERE id = $1`, id, string(to), nullString(by))
	case StateClosed:
		_, err = s.pool.Exec(ctx, `
			UPDATE survey_events
			SET state = $2::survey_event_state, closed_by = $3, closed_at = now()
			WHERE id = $1`, id, string(to), nullString(by))
	default:
		_, err = s.pool.Exec(ctx, `
			UPDATE survey_events SET state = $2::survey_event_state WHERE id = $1`,
			id, string(to))
	}
	if err != nil {
		return nil, fmt.Errorf("changing event state: %w", err)
	}

	fresh, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	// Tell the officers who have to act on it. After the state change rather
	// than inside it: an event that opened correctly and could not be
	// announced is still an open event, and refusing the transition to save
	// the notification would be the wrong way round.
	if err := s.announce(ctx, fresh, to); err != nil && s.log != nil {
		s.log.Warn("announcing an event state change",
			"event", fresh.Reference, "to", string(to), "error", err)
	}
	return fresh, nil
}

// Eligibility is the answer to "may this parcel be surveyed against this
// event, now".
type Eligibility struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason,omitempty"`
}

// CheckParcel decides whether a parcel and incident date fit an event.
//
// Three independent conditions, reported separately because the remedy
// differs: the wrong area is a mistake by whoever chose the event, a date
// outside the window is a mis-stated incident, and a closed survey is a
// deadline nobody can fix from here.
func (s *Store) CheckParcel(
	ctx context.Context, eventID, villageCode, incidentDate string, now time.Time,
) (Eligibility, error) {
	e, err := s.Get(ctx, eventID)
	if err != nil {
		return Eligibility{}, err
	}

	if !e.SurveyOpen(now) {
		return Eligibility{Reason: fmt.Sprintf(
			"surveying is not open for %s (state %s, closes %s)",
			e.Reference, e.State, e.SurveyClosesAt.Format("2006-01-02"))}, nil
	}

	if incidentDate < e.IncidentStart || incidentDate > e.IncidentEnd {
		return Eligibility{Reason: fmt.Sprintf(
			"incident date %s is outside the event window %s to %s",
			incidentDate, e.IncidentStart, e.IncidentEnd)}, nil
	}

	var covered bool
	if err := s.pool.QueryRow(ctx,
		`SELECT village_in_event($1, $2)`, eventID, villageCode).Scan(&covered); err != nil {
		return Eligibility{}, fmt.Errorf("checking event coverage: %w", err)
	}
	if !covered {
		return Eligibility{Reason: fmt.Sprintf(
			"village %s is not in any taluka or circle covered by %s",
			villageCode, e.Reference)}, nil
	}

	return Eligibility{Eligible: true}, nil
}

// cleanTriggerError strips the plpgsql prefix so the message the trigger
// raised reaches the caller as a sentence rather than a stack trace.
func cleanTriggerError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "ERROR: "); i >= 0 {
		msg = msg[i+len("ERROR: "):]
	}
	if i := strings.Index(msg, " (SQLSTATE"); i >= 0 {
		msg = msg[:i]
	}
	return msg
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableUUIDs(ids []string) any {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// SeasonAndCropYear places an incident in the agricultural calendar.
//
// Kharif is sown with the monsoon and harvested before winter; rabi is sown
// after it and harvested in spring; summer is the short irrigated crop
// between them. The month boundaries are the ordinary Maharashtra ones and
// are approximate at the edges -- a late kharif harvest in early November is
// still kharif to the farmer who sowed it.
//
// The crop year runs July to June, so a loss in February 2027 belongs to
// 2026-27 and not to 2027-28. Getting this backwards would file a claim
// against the wrong year's threshold yield, which is the figure the whole
// assessment is expressed against.
func SeasonAndCropYear(incident time.Time) (season, cropYear string) {
	m := int(incident.Month())

	switch {
	case m >= 6 && m <= 10:
		season = "kharif"
	case m >= 11 || m <= 3:
		season = "rabi"
	default:
		season = "summer"
	}

	y := incident.Year()
	if m < 6 {
		y--
	}
	return season, fmt.Sprintf("%d-%02d", y, (y+1)%100)
}

// --- editing an event's coverage -------------------------------------------

// ErrAreaInUse is a circle that cannot be removed because farmers have already
// filed against it.
var ErrAreaInUse = errors.New("that area cannot be removed")

// AddAreas extends an event's coverage.
//
// A calamity does not respect the moment a notification was signed: a
// Collector learns on day three that two more circles were hit. Before this,
// the only way to say so was to declare a second event over the same storm,
// which split one calamity's reports across two references and two deadlines.
//
// The area rules -- taluka or circle, inside the declaring district, event
// still running -- are enforced by the trigger in migration 0026 rather than
// here, so that they hold for every write path there will ever be.
func (s *Store) AddAreas(ctx context.Context, eventID string, codes []string, by string) (*Event, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("%w: name at least one taluka or circle to add", ErrBadArea)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := addAreas(ctx, tx, eventID, codes, by); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, eventID)
}

// RemoveAreas narrows an event's coverage.
//
// # Why a circle with reports cannot be removed
//
// Because a farmer already filed against it. Removing it would leave the
// event covering ground its own area list does not name, and every report
// from there defending itself against a document that no longer includes it.
// The alternative -- withdrawing those reports -- would let one edit destroy a
// claim, which is not a power a coverage correction should carry.
//
// So removal is refused, and refused with the count, because "you cannot"
// without "nineteen reports hold it" leaves a Collector with no next step.
// The remedy for coverage that turned out to be wrong after farmers have
// filed is not an edit; it is the adjudication those reports already get.
func (s *Store) RemoveAreas(ctx context.Context, eventID string, codes []string, by string) (*Event, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("%w: name at least one taluka or circle to remove", ErrBadArea)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	for _, code := range codes {
		var jid, name string
		err := tx.QueryRow(ctx, `
			SELECT id, name FROM jurisdictions
			 WHERE code = $1 AND level IN ('sub_district', 'circle')`, code).Scan(&jid, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: no taluka or circle with code %q", ErrBadArea, code)
		}
		if err != nil {
			return nil, err
		}

		// Reports filed from any village at or beneath this node. Counted
		// through ancestor_ids rather than by walking down, so removing a
		// taluka sees the reports filed from the circles inside it.
		var n int
		// Joined on the village's LGD code, which is what a report carries --
		// it records the place as the code and name it was filed under, so
		// that the report still reads correctly if the tree is later
		// reorganised underneath it.
		if err := tx.QueryRow(ctx, `
			SELECT count(*)
			  FROM crop_loss_reports r
			  JOIN jurisdictions v
			    ON v.code = r.village_lgd_code AND v.level = 'village'
			 WHERE r.survey_event_id = $1
			   AND (v.id = $2 OR $2 = ANY(v.ancestor_ids))`, eventID, jid).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, fmt.Errorf(
				"%w: %d report%s already filed from %s. Removing it would leave "+
					"those claims outside the event they were filed against",
				ErrAreaInUse, n, plural(n), name)
		}

		tag, err := tx.Exec(ctx,
			`DELETE FROM survey_event_areas WHERE event_id = $1 AND jurisdiction_id = $2`,
			eventID, jid)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrBadArea, cleanTriggerError(err))
		}
		if tag.RowsAffected() == 0 {
			return nil, fmt.Errorf("%w: %s is not covered by this event", ErrBadArea, name)
		}
	}

	// An event covering nothing covers nobody, and the state machine already
	// refuses to open one. Refused here too rather than allowed and caught
	// later, because a running event emptied of areas is a live event no
	// farmer can file against and no screen explains.
	var left int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM survey_event_areas WHERE event_id = $1`, eventID).Scan(&left); err != nil {
		return nil, err
	}
	if left == 0 {
		return nil, fmt.Errorf(
			"%w: that would leave the event covering no area at all. Cancel it "+
				"instead, which says what happened", ErrBadArea)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, eventID)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// coversAny is the village filter: nothing when no villages are given, and
// otherwise events whose areas cover at least one of them.
//
// village_in_event is the function a filing is refused by, so the list a
// farmer chooses from and the check their claim meets are the same rule.
func coversAny(param string) string {
	return `(cardinality(` + param + `::text[]) = 0 OR EXISTS (
		SELECT 1 FROM unnest(` + param + `::text[]) AS v(code)
		 WHERE village_in_event(e.id, v.code)))`
}

// villageList is never nil, so the array parameter always has a type.
func villageList(codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// LossCategoryOption is one category a claim may be filed under, ready to
// render.
//
// Bilingual here rather than in the handset, so the two apps and the console
// cannot disagree about what a category is called in Marathi.
type LossCategoryOption struct {
	Code string `json:"code"`
	Mr   string `json:"mr"`
	En   string `json:"en"`
}
