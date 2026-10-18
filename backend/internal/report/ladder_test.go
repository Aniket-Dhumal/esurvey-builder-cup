package report

import (
	"errors"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The rule that decides who may settle a disagreement.
//
// The village pair decide a claim; where they disagree, exactly one officer is
// asked to settle it. This function is the whole of what keeps a second officer
// from answering a question that was put to somebody else, and what keeps the
// answer from being given without a reason. It is pure so it can be tested
// exhaustively, and it is tested exhaustively because "a Tehsildar settled a
// claim routed to the Circle" is not a defect anyone would notice from a
// screenshot.

// Waiting at a rung, which by definition means the village pair finished.
// PairComplete is stated rather than implied -- inferring it from At is the
// bug TestARungWithNoFindingsBehindItIsRefused covers.
func at(l Level) Standing { return Standing{At: l, PairComplete: true} }

// by is a settlement by one role. Every settlement carries a reason -- see
// TestSettlingAlwaysNeedsAReason -- so the helper supplies one and the tests
// that are about the reason build their own Move.
func by(r rbac.Role) Move { return Move{Role: r, Reason: "settled"} }

// The officer who acts at a rung -- the inverse of LevelFor, for tests that
// want to walk the ladder by rung rather than by role.
func roleFor(l Level) rbac.Role {
	switch l {
	case LevelCircle:
		return rbac.RoleMandalOfficer
	case LevelTehsil:
		return rbac.RoleTehsildar
	case LevelDistrict:
		return rbac.RoleDistrictMagistrate
	}
	return rbac.RoleTalathi
}

// --- the order --------------------------------------------------------------

func TestTheDistrictIsNotARung(t *testing.T) {
	// The District Magistrate declares the calamity, staffs the district and
	// hears appeals. They approve no claim, so there is no rung for them to
	// occupy and no role mapped to one.
	for _, l := range Ladder() {
		if l == LevelDistrict {
			t.Error("the district is still in the ladder")
		}
	}
	if _, ok := LevelFor(rbac.RoleDistrictMagistrate); ok {
		t.Error("the District Magistrate still maps to a rung, so they would " +
			"be offered an approval they are not meant to make")
	}
}

func TestTheSettlingRungsAreTheCircleAndTheTehsil(t *testing.T) {
	// A disagreement goes to one officer, not up a chain: the Circle Officer
	// where one is posted over the village, the Tehsildar where none is.
	want := []Level{LevelVillagePair, LevelCircle, LevelTehsil}
	got := Ladder()
	if len(got) != len(want) {
		t.Fatalf("the ladder is %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the ladder is %v, want %v", got, want)
		}
	}
}

func TestRolesMapToExactlyOneRung(t *testing.T) {
	cases := []struct {
		role rbac.Role
		want Level
		ok   bool
	}{
		{rbac.RoleTalathi, LevelVillagePair, true},
		{rbac.RoleKrushiSahayak, LevelVillagePair, true},
		{rbac.RoleMandalOfficer, LevelCircle, true},
		{rbac.RoleTehsildar, LevelTehsil, true},
		// The District Magistrate declares the calamity and hears appeals.
		// They approve no claim, so they map to no rung -- see
		// TestTheDistrictIsNotARung.
		{rbac.RoleDistrictMagistrate, "", false},

		// The state ministry reads aggregates and approves nothing, and a
		// system administrator administers rather than decides. Neither has a
		// rung, and giving either one would put a payment decision in the
		// hands of somebody with no accountability for the field.
		{rbac.RoleStateOfficer, "", false},
		{rbac.RoleSystemAdmin, "", false},
		{rbac.RoleFarmer, "", false},
	}
	for _, c := range cases {
		got, ok := LevelFor(c.role)
		if ok != c.ok || got != c.want {
			t.Errorf("LevelFor(%s) = (%q,%v), want (%q,%v)", c.role, got, ok, c.want, c.ok)
		}
	}
}

// --- the sequencing rule ----------------------------------------------------

func TestALevelMayActOnlyAtItsOwnRung(t *testing.T) {
	if err := Check(at(LevelCircle), by(rbac.RoleMandalOfficer)); err != nil {
		t.Errorf("the Circle could not settle a claim routed to it: %v", err)
	}
	if err := Check(at(LevelTehsil), by(rbac.RoleTehsildar)); err != nil {
		t.Errorf("the Tehsil could not settle a claim routed to it: %v", err)
	}
}

func TestTheVillagePairRecordFindingsNotApprovals(t *testing.T) {
	// If a Talathi could approve, one village officer could sign for a rung
	// that exists precisely to require two people.
	for _, r := range []rbac.Role{rbac.RoleTalathi, rbac.RoleKrushiSahayak} {
		err := Check(at(LevelVillagePair), by(r))
		if !errors.Is(err, ErrNoRung) {
			t.Errorf("%s was allowed into the approval path: %v", r, err)
		}
	}
}

func TestNothingMovesUntilBothVillageOfficersHaveRecorded(t *testing.T) {
	// An empty rung means the pair is not complete. The Circle has nothing to
	// approve yet, however senior the person asking.
	for _, r := range []rbac.Role{rbac.RoleMandalOfficer, rbac.RoleTehsildar} {
		if err := Check(Standing{}, by(r)); !errors.Is(err, ErrPairNotComplete) {
			t.Errorf("%s acted before the village pair finished: %v", r, err)
		}
	}
}

func TestRolesWithNoRungAreRefused(t *testing.T) {
	for _, r := range []rbac.Role{
		rbac.RoleStateOfficer, rbac.RoleSystemAdmin, rbac.RoleFarmer,
	} {
		if err := Check(at(LevelCircle), by(r)); !errors.Is(err, ErrNoRung) {
			t.Errorf("%s was let into the ladder: %v", r, err)
		}
	}
}

func TestAClosedReportAcceptsNothing(t *testing.T) {
	s := Standing{At: LevelDistrict, PairComplete: true, Closed: true}
	if err := Check(s, by(rbac.RoleDistrictMagistrate)); !errors.Is(err, ErrAlreadyClosed) {
		t.Fatalf("err = %v, want ErrAlreadyClosed", err)
	}
}

// --- changing the figure ----------------------------------------------------

func TestChangingTheFigureRequiresAReason(t *testing.T) {
	// The settling officer may write a different number from either of the two
	// the pair recorded, and theirs is the one the farmer is paid on. It has to
	// say why, or the record cannot answer who decided what a farmer got.
	m := Move{Role: rbac.RoleTehsildar, Changed: true}
	if err := Check(at(LevelTehsil), m); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("err = %v, want ErrReasonRequired", err)
	}

	m.Reason = "the pair measured one corner; the whole plot is flattened"
	if err := Check(at(LevelTehsil), m); err != nil {
		t.Errorf("a reasoned change was refused: %v", err)
	}
}

func TestWhitespaceIsNotAReason(t *testing.T) {
	m := Move{Role: rbac.RoleTehsildar, Changed: true, Reason: "  \t\n "}
	if err := Check(at(LevelTehsil), m); !errors.Is(err, ErrReasonRequired) {
		t.Errorf("blank space passed as a reason: %v", err)
	}
}

// A report can carry a rung and have nothing behind it.
//
// Completeness used to be inferred from the level column: an empty At meant
// the pair had not finished, and anything else meant they had. Returning a
// report to the village pair supersedes their findings *and* sets the level
// to village_pair, so the two facts come apart exactly where it matters.
//
// Left inferred, such a report climbs to the top and the District's write
// becomes `adjudicated_percent = COALESCE(NULL, adjudicated_percent)` -- a
// no-op. The claim is marked adjudicated and carries no number, which the
// dossier, the rollups and the farmer's copy all read.
func TestARungWithNoFindingsBehindItIsRefused(t *testing.T) {
	for _, rung := range []Level{LevelCircle, LevelTehsil} {
		s := Standing{At: rung} // a level, and no findings
		err := Check(s, by(roleFor(rung)))
		if !errors.Is(err, ErrPairNotComplete) {
			t.Errorf("at %s with no findings: got %v, want ErrPairNotComplete",
				rung, err)
		}
	}
}

// And the converse, so the guard cannot be satisfied by blocking everything:
// the same rungs with findings behind them are allowed through.
func TestARungWithFindingsBehindItIsAllowed(t *testing.T) {
	for _, rung := range []Level{LevelCircle, LevelTehsil} {
		if err := Check(at(rung), by(roleFor(rung))); err != nil {
			t.Errorf("at %s with findings: %v", rung, err)
		}
	}
}

// --- settling a disagreement --------------------------------------------

func TestOnlyTheOfficerTheClaimWasRoutedToMaySettleIt(t *testing.T) {
	// A Tehsildar taking a claim routed to the Circle is the same wrong as a
	// Circle Officer taking one routed to the Tehsil: one officer was asked,
	// and the record has to show it was that officer who answered.
	settle := Move{Role: rbac.RoleTehsildar, Reason: "took the Talathi's figure"}
	err := Check(at(LevelCircle), settle)
	if !errors.Is(err, ErrNotYourRung) {
		t.Fatalf("a Tehsildar settled a claim routed to the Circle: %v", err)
	}

	circle := Move{Role: rbac.RoleMandalOfficer, Reason: "stood in the field"}
	if err := Check(at(LevelTehsil), circle); !errors.Is(err, ErrNotYourRung) {
		t.Fatalf("a Circle Officer settled a claim routed to the Tehsil: %v", err)
	}
}

func TestTheDistrictMagistrateCannotSettleAnything(t *testing.T) {
	// They declare the calamity, staff the district and hear appeals. Offering
	// them an approval would be offering an action their role no longer has.
	m := Move{Role: rbac.RoleDistrictMagistrate, Reason: "looked at it"}
	for _, l := range []Level{LevelVillagePair, LevelCircle, LevelTehsil} {
		if err := Check(at(l), m); !errors.Is(err, ErrNoRung) {
			t.Errorf("the District settled a claim at %s: %v", l, err)
		}
	}
}

func TestAClaimStrandedAtTheOldDistrictRungIsPickedUpByTheTehsildar(t *testing.T) {
	// Claims filed before the district rung was removed may still be sitting
	// at it. Nobody is posted to decide those any more, and a claim waiting
	// for a rung that no longer exists waits for ever.
	m := Move{Role: rbac.RoleTehsildar, Reason: "the district rung was removed"}
	if err := Check(at(LevelDistrict), m); err != nil {
		t.Errorf("a stranded claim could not be settled: %v", err)
	}
	// And nobody else inherits them.
	circle := Move{Role: rbac.RoleMandalOfficer, Reason: "x"}
	if err := Check(at(LevelDistrict), circle); !errors.Is(err, ErrNotYourRung) {
		t.Errorf("a Circle Officer took a stranded claim: %v", err)
	}
}

func TestSettlingAlwaysNeedsAReason(t *testing.T) {
	// A claim only reaches a settling officer because two officers disagreed
	// about somebody's payment. "I took the Talathi's number" is an answer;
	// silence is not, even when the figure does not move.
	unchanged := Move{Role: rbac.RoleMandalOfficer}
	if err := Check(at(LevelCircle), unchanged); !errors.Is(err, ErrContestedReason) {
		t.Errorf("a disagreement was settled with no reason: %v", err)
	}

	changed := Move{Role: rbac.RoleMandalOfficer, Changed: true}
	if err := Check(at(LevelCircle), changed); !errors.Is(err, ErrReasonRequired) {
		t.Errorf("the figure was changed with no reason: %v", err)
	}

	with := Move{Role: rbac.RoleMandalOfficer, Changed: true, Reason: "re-counted"}
	if err := Check(at(LevelCircle), with); err != nil {
		t.Errorf("a reasoned settlement was refused: %v", err)
	}
}

func TestADisagreementIsNeverSettledInABatch(t *testing.T) {
	// Every claim that reaches this rung is one two officers could not agree
	// on. A batch action is precisely not looking at it.
	m := Move{Role: rbac.RoleMandalOfficer, Reason: "fine", Bulk: true}
	if err := Check(at(LevelCircle), m); !errors.Is(err, ErrContestedBulk) {
		t.Fatalf("a disagreement was swept into a batch: %v", err)
	}
}
