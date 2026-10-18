package reference

import (
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The LGD file is the one input a person places by hand, and everything in the
// platform hangs off it: a village that is absent cannot file, and a village
// attached to the wrong taluka is filed under the wrong Tehsildar. The parser
// refuses rather than guesses, and these tests are about what it refuses and
// what it says when it does.

const header = "level,code,name,parent_level,parent_code\n"

func parseString(t *testing.T, body string) map[rbac.Level][]Node {
	t.Helper()
	got, err := parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	return got
}

func mustFail(t *testing.T, body string, wantIn string) {
	t.Helper()
	_, err := parse(strings.NewReader(body))
	if err == nil {
		t.Fatalf("accepted a file it should have refused")
	}
	if !strings.Contains(err.Error(), wantIn) {
		t.Errorf("error = %v, want it to mention %q", err, wantIn)
	}
}

func TestAWellFormedFileParsesIntoItsLevels(t *testing.T) {
	got := parseString(t, header+
		"state,27,Maharashtra,,\n"+
		"district,521,Pune,state,27\n"+
		"sub_district,4021,Haveli,district,521\n"+
		"circle,C-11,Wadgaon,sub_district,4021\n"+
		"village,562100,Wadgaon,circle,C-11\n")

	for level, want := range map[rbac.Level]int{
		rbac.LevelState:       1,
		rbac.LevelDistrict:    1,
		rbac.LevelSubDistrict: 1,
		rbac.LevelCircle:      1,
		rbac.LevelVillage:     1,
	} {
		if n := len(got[level]); n != want {
			t.Errorf("%s: %d rows, want %d", level, n, want)
		}
	}

	v := got[rbac.LevelVillage][0]
	if v.Code != "562100" || v.Name != "Wadgaon" {
		t.Errorf("village parsed as %+v", v)
	}
	if v.ParentLevel != rbac.LevelCircle || v.ParentCode != "C-11" {
		t.Errorf("village parent parsed as %s %s", v.ParentLevel, v.ParentCode)
	}
}

func TestAByteOrderMarkIsNotAColumnName(t *testing.T) {
	// Anything saved as CSV from Excel begins with one, and this is the file
	// somebody places by hand. Without stripping it the message compares two
	// strings that are identical on screen.
	got := parseString(t, "\ufeff"+header+"state,27,Maharashtra,,\n")
	if len(got[rbac.LevelState]) != 1 {
		t.Error("a file with a byte-order mark did not parse")
	}
}

func TestTheHeaderMustBeTheHeader(t *testing.T) {
	// A file whose columns are in a different order would load silently and
	// wrongly -- names as codes, parents as names -- and the first sign would
	// be farmers unable to find their village.
	mustFail(t,
		"code,level,name,parent_level,parent_code\n"+
			"27,state,Maharashtra,,\n",
		"expected")
}

func TestTheHeaderIsMatchedWithoutRegardToCase(t *testing.T) {
	got := parseString(t,
		"Level,Code,Name,Parent_Level,Parent_Code\n"+
			"state,27,Maharashtra,,\n")
	if len(got[rbac.LevelState]) != 1 {
		t.Error("a capitalised header was refused")
	}
}

func TestAnUnknownLevelIsRefused(t *testing.T) {
	// Not skipped. A file with a level this platform does not model is a file
	// somebody built against a different hierarchy, and loading the rows that
	// happen to fit produces a partial state nobody asked for.
	mustFail(t, header+"tehsil,4021,Haveli,district,521\n", "unknown level")
}

func TestARowWithNoCodeIsRefused(t *testing.T) {
	mustFail(t, header+"village,,Wadgaon,circle,C-11\n", "empty code")
}

func TestARowWithNoNameIsRefused(t *testing.T) {
	// A village with a code and no name is a row in a dropdown that a farmer
	// cannot identify as theirs.
	mustFail(t, header+"village,562100,,circle,C-11\n", "empty name")
}

func TestAStateWithAParentIsRefused(t *testing.T) {
	mustFail(t, header+"state,27,Maharashtra,country,IN\n", "no parent")
}

func TestAnythingBelowAStateNeedsAParent(t *testing.T) {
	for _, row := range []string{
		"district,521,Pune,,\n",
		"district,521,Pune,state,\n",
		"district,521,Pune,,27\n",
	} {
		mustFail(t, header+row, "has no parent")
	}
}

func TestADuplicateNamesBothLines(t *testing.T) {
	// Two rows for one village is how a hand-merged file fails, and the second
	// would otherwise overwrite the first with whatever parent it names. The
	// message carries both line numbers because finding the other one in
	// forty-five thousand rows is the actual work.
	_, err := parse(strings.NewReader(header +
		"village,562100,Wadgaon,circle,C-11\n" +
		"village,562101,Nimgaon,circle,C-11\n" +
		"village,562100,Wadgaon Bk,circle,C-12\n"))
	if err == nil {
		t.Fatal("a duplicated code was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"line 4", "line 2", "562100"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not carry %q: %s", want, msg)
		}
	}
}

func TestTheSameCodeAtTwoLevelsIsNotADuplicate(t *testing.T) {
	// LGD numbers a district and a village independently, and they collide.
	// Treating that as a duplicate would refuse a correct file.
	got := parseString(t, header+
		"state,27,Maharashtra,,\n"+
		"district,521,Pune,state,27\n"+
		"village,521,Somewhere,district,521\n")

	if len(got[rbac.LevelDistrict]) != 1 || len(got[rbac.LevelVillage]) != 1 {
		t.Errorf("a code reused at another level was collapsed: %+v", got)
	}
}

func TestSurroundingSpaceIsTrimmed(t *testing.T) {
	// Hand-edited files have it, and a code with a trailing space matches
	// nothing at lookup time while looking correct in every listing.
	got := parseString(t, header+
		"  district , 521 , Pune , state , 27 \n")

	d := got[rbac.LevelDistrict]
	if len(d) != 1 {
		t.Fatalf("rows = %d", len(d))
	}
	if d[0].Code != "521" || d[0].Name != "Pune" || d[0].ParentCode != "27" {
		t.Errorf("space survived trimming: %+v", d[0])
	}
}

func TestARowWithTheWrongNumberOfColumnsNamesItsLine(t *testing.T) {
	mustFail(t, header+
		"state,27,Maharashtra,,\n"+
		"district,521,Pune,state\n",
		"line 3")
}

func TestAnEmptyFileIsRefusedRatherThanLoadedAsNothing(t *testing.T) {
	// A truncated download is the ordinary way this file goes wrong, and
	// "loaded 0 jurisdictions" reported as success is how it goes unnoticed.
	mustFail(t, "", "reading header")
}

func TestAHeaderWithNoRowsParsesToNothing(t *testing.T) {
	// Distinct from the case above: this file is well formed and says there is
	// nothing, which is a caller's problem rather than a parser's.
	got := parseString(t, header)
	if len(got) != 0 {
		t.Errorf("a header-only file produced %d levels", len(got))
	}
}

// ---------------------------------------------------------------------------
// Circle coverage
// ---------------------------------------------------------------------------

func TestCommasGroupTheIndianWay(t *testing.T) {
	// 44,911 villages is read by people who write forty-four thousand as
	// 44,911 and four hundred thousand as 4,00,000.
	cases := map[int]string{
		0: "0", 42: "42", 911: "911",
		1000: "1,000", 44911: "44,911",
		100000: "1,00,000", 4491100: "44,91,100",
	}
	for n, want := range cases {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTheConsequenceOfAnUnmappedVillageIsStatedNotCounted(t *testing.T) {
	// A percentage nobody can act on is worse than a sentence naming who
	// cannot be posted where.
	rep := &CoverageReport{
		State: Coverage{
			SubDistricts: 358, SubDistrictsWithCircles: 1,
			Villages: 44911, VillagesInCircles: 1,
		},
	}
	got := consequencesOf(rep)
	if len(got) == 0 {
		t.Fatal("no consequences reported for a state with one circle")
	}

	joined := strings.Join(got, " ")
	for _, want := range []string{"Circle Officer", "Tehsildar", "44,910", "357 of 358"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the report does not mention %q: %s", want, joined)
		}
	}
}

func TestAFullyMappedStateSaysSoPlainly(t *testing.T) {
	rep := &CoverageReport{
		State: Coverage{
			SubDistricts: 358, SubDistrictsWithCircles: 358,
			Villages: 44911, VillagesInCircles: 44911,
		},
	}
	got := consequencesOf(rep)
	if len(got) != 1 || !strings.Contains(got[0], "Every village") {
		t.Errorf("a complete mapping reported %v", got)
	}
}

func TestPartlyMappedDistrictsAreDistinguishedFromCompleteOnes(t *testing.T) {
	rep := &CoverageReport{
		State: Coverage{SubDistricts: 10, SubDistrictsWithCircles: 3,
			Villages: 1000, VillagesInCircles: 400},
		Districts: []Coverage{
			{Name: "Done", Villages: 100, VillagesInCircles: 100, Percent: 100},
			{Name: "Started", Villages: 400, VillagesInCircles: 300, Percent: 75},
			{Name: "Untouched", Villages: 500, VillagesInCircles: 0, Percent: 0},
		},
	}
	joined := strings.Join(consequencesOf(rep), " ")
	if !strings.Contains(joined, "1 district(s) are fully mapped and 1 are part-way") {
		t.Errorf("the progress line is wrong: %s", joined)
	}
	if !strings.Contains(joined, "does not undo it") {
		t.Errorf("nothing says re-loading the LGD file is safe: %s", joined)
	}
}
