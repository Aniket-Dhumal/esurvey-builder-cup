package event

import "testing"

// The crop check, which decides what a farmer is told and what an officer sees.
//
// The decision this encodes is that a mismatch never stops a survey. It is
// worth testing hard anyway, because the flag it raises is what the village
// pair are asked to settle, and a flag raised on the wrong farmer wastes a
// visit while one missed lets a claim through unexamined.

var soybeanAndCotton = []Crop{
	{Code: "0201001", Name: "Soybean"},
	{Code: "0301002", Name: "Cotton"},
}

func TestARegistryCropTheEventCoversMatches(t *testing.T) {
	got := MatchCrop(soybeanAndCotton, "0201001", "Soybean")

	if !got.Matched || got.Mismatch || got.NoRegistryCrop {
		t.Fatalf("a covered crop did not match cleanly: %+v", got)
	}
}

func TestTheCodeIsWhatIsCompared(t *testing.T) {
	// The two systems disagree about names far more than about codes. A
	// registry row carrying the right code and a differently spelt name is a
	// match.
	got := MatchCrop(soybeanAndCotton, "0201001", "Soya bean")
	if !got.Matched {
		t.Errorf("a matching code was refused because the name differed: %+v", got)
	}
}

func TestTheNameIsAFallbackAndIsCaseInsensitive(t *testing.T) {
	// A registry record with a name and no code still has to be usable.
	for _, name := range []string{"Cotton", "cotton", "COTTON", "  Cotton  "} {
		if got := MatchCrop(soybeanAndCotton, "", name); !got.Matched {
			t.Errorf("MatchCrop(_, \"\", %q) did not match: %+v", name, got)
		}
	}
}

func TestAGenuineContradictionIsAMismatch(t *testing.T) {
	got := MatchCrop(soybeanAndCotton, "0401003", "Tur")

	if !got.Mismatch {
		t.Fatalf("a crop the event does not cover was not flagged: %+v", got)
	}
	if got.Matched || got.NoRegistryCrop {
		t.Errorf("a mismatch reported something else as well: %+v", got)
	}

	// The officer has to see both sides of the disagreement, not a boolean.
	if got.RegistryCrop != "Tur" {
		t.Errorf("registry crop = %q, want Tur", got.RegistryCrop)
	}
	if len(got.EventCrops) != 2 {
		t.Errorf("event crops = %v, want both", got.EventCrops)
	}
}

func TestNoRegistryCropIsNotAMismatch(t *testing.T) {
	// The ordinary case on live data: AgriStack returns an empty
	// crop_sown_data for every farmer this platform has reached. Treating
	// silence as contradiction would flag every genuine claimant.
	got := MatchCrop(soybeanAndCotton, "", "")

	if !got.NoRegistryCrop {
		t.Fatalf("an empty registry crop was not reported as such: %+v", got)
	}
	if got.Mismatch {
		t.Error("silence was treated as a contradiction")
	}
	if got.Matched {
		t.Error("silence was treated as a match; it is neither")
	}
}

func TestAnEventNamingNoCropsCoversAll(t *testing.T) {
	// A Collector declaring a cyclone across four talukas has no reason to
	// enumerate what was planted in them.
	for _, c := range []struct{ code, name string }{
		{"0401003", "Tur"}, {"0201001", "Soybean"}, {"", "Sugarcane"},
	} {
		got := MatchCrop(nil, c.code, c.name)
		if got.Mismatch {
			t.Errorf("an unnarrowed event flagged %q as a mismatch: %+v", c.name, got)
		}
		if !got.Matched {
			t.Errorf("an unnarrowed event did not cover %q: %+v", c.name, got)
		}
	}
}

func TestAnEventNamingNoCropsStillReportsSilence(t *testing.T) {
	// Covering every crop does not conjure one onto the record.
	got := MatchCrop(nil, "", "")
	if !got.NoRegistryCrop {
		t.Errorf("an absent registry crop was reported as present: %+v", got)
	}
}

func TestNothingBlocksTheSurvey(t *testing.T) {
	// The decision, pinned. If this ever changes it should change here, in one
	// line, with somebody arguing for it — not drift in through a handler.
	cases := []CropCheck{
		MatchCrop(soybeanAndCotton, "0201001", "Soybean"),
		MatchCrop(soybeanAndCotton, "0401003", "Tur"),
		MatchCrop(soybeanAndCotton, "", ""),
		MatchCrop(nil, "", ""),
	}
	for _, c := range cases {
		if c.Blocks() {
			t.Errorf("a crop check stopped a survey: %+v", c)
		}
	}
}

func TestWhitespaceIsNotACrop(t *testing.T) {
	got := MatchCrop(soybeanAndCotton, "   ", "  ")
	if !got.NoRegistryCrop {
		t.Errorf("blank space was read as a crop: %+v", got)
	}
}
