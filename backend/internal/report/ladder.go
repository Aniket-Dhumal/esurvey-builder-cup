package report

import (
	"errors"
	"fmt"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Who decides a claim, and who settles it when the deciders disagree.
//
// # The shape
//
// The village pair decide. The Talathi and the Krushi Sahayak each accept or
// edit the figure the assessment produced, and when they agree that is the
// decision: the claim is adjudicated on their finding and nothing above the
// village approves it.
//
// When they disagree the claim goes to one officer to settle: the Circle
// Officer, or the Tehsildar where no Circle Officer is posted over that
// village. Whoever settles it writes the final figure. There is no climb --
// the claim does not pass from Circle to Tehsil to District, because only one
// of them is being asked a question.
//
// The Tehsil and the District read and aggregate. The District Magistrate
// declares the calamity, staffs the district, and hears appeals; they approve
// no claim.
//
// # What this replaced, and the cost of replacing it
//
// Every claim used to climb all four rungs unconditionally, on the argument
// that a Circle Officer who never saw a claim cannot be asked why it was paid,
// and that in a scheme where the money is public and the pressure is local,
// "nobody above the village looked at it" is the condition under which local
// capture happens.
//
// That argument has not been refuted; it has been weighed against volume and
// lost. A district event produces claims by the lakh and four sign-offs each
// is four sign-offs nobody performs honestly. The decision (2026-09-28) is
// that the pair decide and the supervisors read.
//
// The cost is real and is recorded here rather than left to be discovered: the
// two village officers are both shown the assessment's own percentage,
// pre-filled, and are blind only to each other. In the ordinary case the
// machine's figure becomes the paid figure on two confirmations, and the first
// supervisor to see it sees it afterwards. What stands in place of the old
// ladder is the audit trail, the farmer's own copy of the assessment, and the
// appeal -- not a second pair of eyes before payment.

// Level is a rung of the ladder.
type Level string

const (
	// LevelVillagePair is the Talathi and the Krushi Sahayak, who record
	// findings rather than approvals: their rung is complete when both have
	// recorded, not when one has signed.
	LevelVillagePair Level = "village_pair"

	// LevelCircle settles a disagreement between the pair.
	LevelCircle Level = "circle"

	// LevelTehsil settles one where no Circle Officer is posted over the
	// village. Not a rung above the Circle: an alternative to it.
	LevelTehsil Level = "tehsil"

	// LevelDistrict is kept as a value, and is no longer a rung.
	//
	// Claims filed before 2026-09-28 may still be sitting at it, and a value
	// the database holds and this package does not know is a claim that cannot
	// be read. Settle() accepts it for exactly that reason. Nothing routes a
	// claim here any more.
	LevelDistrict Level = "district"
)

// ladder is the order, and the only place it is written down.
//
// Three entries, and only the first two are places a claim waits: the village
// pair where it is decided, and the Circle or the Tehsil where a disagreement
// is settled. The District is not here at all -- it approves nothing.
var ladder = []Level{LevelVillagePair, LevelCircle, LevelTehsil}

// Ladder returns the rungs in order.
func Ladder() []Level { return append([]Level(nil), ladder...) }

// Valid reports whether l is a level a claim may be recorded at.
//
// Includes the district, which is no longer a rung: the column holds it on
// claims filed before it was removed, and a value the database carries that
// this package calls invalid is a claim nobody can read or settle.
func (l Level) Valid() bool {
	if l == LevelDistrict {
		return true
	}
	for _, x := range ladder {
		if x == l {
			return true
		}
	}
	return false
}

// levelRoles names who acts at each rung.
//
// The village pair is two roles because it is two people, and the rung is not
// complete until both have recorded a finding.
var levelRoles = map[Level][]rbac.Role{
	LevelVillagePair: {rbac.RoleTalathi, rbac.RoleKrushiSahayak},
	LevelCircle:      {rbac.RoleMandalOfficer},
	LevelTehsil:      {rbac.RoleTehsildar},
	// The District Magistrate is absent. They declare the calamity, staff the
	// district and hear appeals; they do not decide claims. A claim left at
	// 'district' by an earlier build is settled by the Tehsildar -- see
	// SettlesLeftovers.
}

// Roles are the roles that act at a rung.
func (l Level) Roles() []rbac.Role { return append([]rbac.Role(nil), levelRoles[l]...) }

// LevelFor is the rung a role acts at.
//
// The State Officer is deliberately absent: the state ministry reads
// aggregates and approves nothing, so there is no rung for it to occupy. A
// system administrator is absent for the same reason in reverse — being able
// to administer the platform is not being accountable for a payment. The
// District Magistrate joined them on 2026-09-28.
func LevelFor(role rbac.Role) (Level, bool) {
	for lvl, roles := range levelRoles {
		for _, r := range roles {
			if r == role {
				return lvl, true
			}
		}
	}
	return "", false
}

// Errors the ladder raises. Each is a refusal an officer will read.
var (
	ErrNotYourRung    = errors.New("this report is not waiting at your level")
	ErrNoRung         = errors.New("your role does not approve reports")
	ErrReasonRequired = errors.New(
		"changing the damage percentage requires a reason")
	ErrContestedReason = errors.New(
		"the two village authorities disagreed, so this report needs a written " +
			"reason before it can move up")
	ErrContestedBulk = errors.New(
		"the two village authorities disagreed, so this report has to be " +
			"opened and decided on its own")
	ErrPairNotComplete = errors.New(
		"both village authorities must record a finding before this report " +
			"can be approved")
	ErrAlreadyClosed = errors.New("this report is closed")
)

// Standing is what the ladder needs to know about a report to judge a move.
//
// A struct rather than the report itself, so the rule below is a pure function
// of a handful of facts and can be tested without a database, a farmer or a
// parcel.
type Standing struct {
	// At is the rung the report is waiting at. Empty means the village pair
	// have not both recorded yet.
	At Level

	// PairComplete is whether both village authorities have a finding on
	// record that has not been superseded.
	//
	// # Why this is a fact and not an inference
	//
	// It used to be inferred: an empty At meant the pair had not finished. A
	// report already carrying a rung was never asked again, and one whose
	// findings had been superseded -- which is exactly what a return to the
	// village pair does -- could climb the whole ladder on the strength of a
	// level column.
	//
	// The end of that road is the case this field exists to make impossible:
	// a report reaching adjudicated with no findings behind it, where the
	// top-rung write is `adjudicated_percent = COALESCE(NULL, ...)` and
	// silently changes nothing. The dossier, the rollups and the farmer's
	// copy then all read a claim that was decided and carries no number.
	PairComplete bool

	// Contested means the pair diverged past the threshold, or one of them
	// refused the claim. Such a report climbs the same ladder and is treated
	// differently on the way.
	Contested bool

	// Closed reports accept nothing.
	Closed bool
}

// Move is what an officer is trying to do.
type Move struct {
	Role rbac.Role

	// Changed reports whether the officer is revising the figure.
	Changed bool

	// Reason is what they wrote. Required on a change, and on any move of a
	// contested report.
	Reason string

	// Bulk means this is one item of a batch rather than a report the officer
	// opened and read.
	Bulk bool
}

// Check applies the ladder's rules to one attempted approval.
//
// Pure, and the only place the sequencing rule is expressed. The handler, the
// bulk path and the tests all ask this same function, so there is one answer
// to "may this officer approve this report" rather than three that can drift.
func Check(s Standing, m Move) error {
	if s.Closed {
		return ErrAlreadyClosed
	}

	rung, ok := LevelFor(m.Role)
	if !ok {
		return ErrNoRung
	}

	// The village pair record findings, not approvals. Reaching here as a
	// Talathi means something upstream routed a finding into the approval
	// path, which would let one village officer sign for both.
	if rung == LevelVillagePair {
		return fmt.Errorf(
			"%w: the village authorities record a finding, which is not an approval",
			ErrNoRung)
	}

	// Asked of the findings, not of the level column. A report can carry a
	// rung and have nothing behind it -- see Standing.PairComplete.
	if !s.PairComplete {
		return ErrPairNotComplete
	}
	if s.At == "" {
		return ErrPairNotComplete
	}
	if !s.At.Valid() {
		return fmt.Errorf("%w: %q is not a level", ErrNotYourRung, s.At)
	}

	// Whoever the claim was routed to is the one who may settle it.
	//
	// Not "at or above": a Tehsildar taking a claim routed to the Circle is
	// the same wrong as a Circle Officer taking one routed to the Tehsil. Only
	// one officer was asked, and the record has to say it was that officer who
	// answered.
	//
	// The exception is a claim left at 'district' by the build that had a
	// fourth rung. Nobody is posted to decide those any more, and the
	// Tehsildar picks them up rather than leaving them stranded for ever.
	if s.At != rung && !(s.At == LevelDistrict && rung == LevelTehsil) {
		return fmt.Errorf("%w: it is waiting at %s", ErrNotYourRung, s.At)
	}

	// A claim only reaches a settling officer because the pair disagreed, so
	// a batch action is never the right instrument here: the whole reason it
	// was routed is that somebody has to look at it.
	if m.Bulk {
		return ErrContestedBulk
	}

	// Settling a disagreement is a decision about a person's payment that
	// two officers could not agree on. It is owed a reason whether or not the
	// figure moved: "I took the Talathi's number" is an answer, and silence
	// is not.
	if blank(m.Reason) {
		if m.Changed {
			return ErrReasonRequired
		}
		return ErrContestedReason
	}

	return nil
}

func blank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}
