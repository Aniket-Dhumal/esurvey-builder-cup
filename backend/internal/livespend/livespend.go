// Package livespend counts what live guidance costs and refuses it past a ceiling.
//
// # Why a cap exists
//
// The model is billed by the minute, and a session is held open by a farmer rather
// than by a request. A handset in a pocket with the screen on is a session; so is a
// farmer who put the phone down to look at their crop. The relay bounds one session
// -- eight minutes, ninety seconds of silence -- and bounds nothing about how many
// sessions a day.
//
// The caps are not there to save money in the ordinary case. A farmer walking four
// points and asking two questions is what the feature is for. They exist for the
// cases nobody intends: a handset in a reconnect loop, a device left in a vehicle, a
// claim reopened forty times.
//
// # Why cutting the assistant off is safe
//
// Because nothing depends on it. The scripted guidance, the walk, the spacing rule
// and the filing all work with no network at all, so a farmer who reaches the ceiling
// loses the assistant and keeps the claim. That is the whole reason the assistant was
// built as an addition rather than as a step: a budget can be enforced against it
// without enforcing anything against the farmer.
package livespend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Caps bound how much live guidance one farmer and one handset may use.
//
// Per day rather than per month: a monthly ceiling is reached on the eleventh and
// leaves nineteen days with no assistant, which is worse than a daily one that
// resets. A farmer who genuinely needs more comes back tomorrow, and a handset in a
// loop stops today.
type Caps struct {
	// PerFarmerSeconds is a day's worth for one farmer's own claims.
	//
	// Thirty minutes. A walk of four points with questions at each is five or six;
	// thirty is a farmer having a long and difficult day, which happens, and is
	// still far short of a device left running.
	PerFarmerSeconds int

	// PerAccountSeconds is a day's worth for one handset, across every farmer it
	// acts for. A household shares a phone, so this is the higher of the two.
	PerAccountSeconds int
}

// Default is what ships until somebody says otherwise.
//
// The figures are a judgement rather than a measurement: nobody has run a season of
// this yet, and the first month's usage is what should set them. Named so that
// changing them is a change to a constant rather than an archaeology exercise.
func Default() Caps {
	return Caps{
		PerFarmerSeconds:  30 * 60,
		PerAccountSeconds: 90 * 60,
	}
}

// ErrOverBudget means the ceiling has been reached.
//
// Distinguished from every other refusal so the handset can say the right thing: the
// assistant is finished for today, the walk is not, and nothing about the claim has
// changed.
var ErrOverBudget = errors.New("the assistant has reached today's limit")

// Store records sessions and answers whether another may start.
type Store struct {
	pool *db.Pool
	caps Caps
}

func New(pool *db.Pool) *Store {
	return &Store{pool: pool, caps: Default()}
}

// WithCaps overrides the ceilings.
func (s *Store) WithCaps(c Caps) *Store {
	s.caps = c
	return s
}

// Caps reports the ceilings in force, for a handset that wants to show them.
func (s *Store) Caps() Caps { return s.caps }

// Used is what has been spent today.
type Used struct {
	FarmerSeconds  int
	AccountSeconds int
}

// Remaining is what is left of the smaller of the two allowances.
//
// The smaller, because both apply: a farmer under their own ceiling on a handset that
// has reached its account ceiling has none left, and reporting the larger figure
// would promise time that will be refused.
func (s *Store) Remaining(u Used) int {
	farmer := s.caps.PerFarmerSeconds - u.FarmerSeconds
	account := s.caps.PerAccountSeconds - u.AccountSeconds
	if account < farmer {
		farmer = account
	}
	if farmer < 0 {
		return 0
	}
	return farmer
}

// UsedToday sums the day's sessions.
//
// Since local midnight, not a rolling twenty-four hours. A farmer told "you have half
// an hour a day" understands a day as the day they are having, and a rolling window
// would refuse them at nine in the morning for a session at ten the night before.
func (s *Store) UsedToday(
	ctx context.Context, farmerID, accountUserID string,
) (Used, error) {
	var u Used
	err := s.pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(seconds) FILTER (WHERE farmer_id = $1), 0)::int,
			COALESCE(SUM(seconds) FILTER (
				WHERE $2 <> '' AND account_user_id = $2::uuid), 0)::int
		  FROM live_session_spend
		 WHERE opened_at >= (date_trunc('day',
		           now() AT TIME ZONE 'Asia/Kolkata') AT TIME ZONE 'Asia/Kolkata')`,
		farmerID, accountUserID,
	).Scan(&u.FarmerSeconds, &u.AccountSeconds)
	if err != nil {
		return Used{}, fmt.Errorf("reading today's live usage: %w", err)
	}
	return u, nil
}

// SmallestUsefulSeconds is what is granted when the count could not be read.
//
// Enough to ask one question, not enough to matter if the count was wrong. A counting
// failure must not hand out unlimited time, and must not refuse a farmer standing in
// a field either.
const SmallestUsefulSeconds = 120

// MayStart says whether another session is allowed, and how long it may run.
//
// The remaining allowance is returned so the relay can bound the session to it rather
// than to its own eight minutes: a farmer with four minutes left should get four, not
// be cut off mid-sentence at eight.
func (s *Store) MayStart(
	ctx context.Context, farmerID, accountUserID string,
) (int, error) {
	used, err := s.UsedToday(ctx, farmerID, accountUserID)
	if err != nil {
		return SmallestUsefulSeconds, nil
	}
	left := s.Remaining(used)
	if left <= 0 {
		return 0, ErrOverBudget
	}
	return left, nil
}

// Record writes what a finished session cost.
//
// Called after the session rather than before: one that never connected cost nothing,
// and a row written on open would have to be corrected on every failure. The
// consequence is that a crash loses the record of one session, which is the right way
// round -- undercounting means a farmer gets slightly more than their allowance, and
// overcounting means one is refused time they never used.
func (s *Store) Record(ctx context.Context, e Event) error {
	if e.Seconds < 0 {
		e.Seconds = 0
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO live_session_spend (
			farmer_id, report_id, account_user_id,
			opened_at, closed_at, seconds,
			audio_bytes_in, audio_bytes_out, frames_in,
			ended_because, model
		) VALUES (
			$1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid,
			$4, $5, $6, $7, $8, $9, $10, $11
		)`,
		e.FarmerID, e.ReportID, e.AccountUserID,
		e.OpenedAt, e.ClosedAt, e.Seconds,
		e.AudioBytesIn, e.AudioBytesOut, e.FramesIn,
		e.EndedBecause, e.Model,
	)
	if err != nil {
		return fmt.Errorf("recording a live session: %w", err)
	}
	return nil
}

// Event is one finished session.
type Event struct {
	FarmerID      string
	ReportID      string
	AccountUserID string

	OpenedAt time.Time
	ClosedAt time.Time
	Seconds  int

	AudioBytesIn  int64
	AudioBytesOut int64
	FramesIn      int

	EndedBecause string
	Model        string
}
