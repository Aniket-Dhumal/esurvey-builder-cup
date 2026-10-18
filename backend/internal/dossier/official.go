package dossier

import (
	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// The state's own rainfall, at the top of §5.
//
// # Why it goes first, and marked
//
// Everything else in §5 comes from reanalysis, which over this region reports
// roughly twice the rain that fell and carries no normal to compare it against.
// For Barshi's 2026 kharif the two sources read 505 mm and 286 mm. Both numbers
// in one table, the model's above the gauges', with nothing saying which is
// which, is a document that invites an officer to cite the wrong one at an
// appeal -- and the wrong one is the one that makes the drought disappear.
//
// So the gauge figure is first, its role is Determines rather than Corroborates
// for the question of amount, and the reanalysis row below it is relabelled to
// say what it is.
//
// # Why the months are printed and not only the season
//
// A season at 55% of normal reads as a poor year. Inside Barshi's 55% sit August
// at 16% and September at 31%, consecutively, through grain fill. A crop does not
// experience a seasonal average, and the monthly rows are where an officer sees
// the difference between a bad year and a failed one.

// officialRows builds the gauge rows, or none where nothing was loaded.
func officialRows(o *weather.Official, c Case) []Row {
	if o == nil || o.Season == nil {
		return nil
	}

	// Built by hand rather than with Tf, because the unit word differs between
	// the halves ("तालुका" / "taluka") while Tf shares one argument list. Two
	// Tf calls, one per language, is how that shared list gets subverted; a
	// single Text assembled explicitly is honest about what is happening.
	uw := unitWord(o.UnitLevel)
	src := Text{
		MR: "राज्य शासनाची पर्जन्यमान नोंद, " + o.UnitName + " " + uw.MR,
		EN: "State rainfall record, " + o.UnitName + " " + uw.EN,
	}

	rows := []Row{
		{
			Parameter: T("हंगामातील पाऊस (सरासरीशी)",
				"Season rainfall against normal"),
			Observed: Tf(
				"%.1f मिमी (सरासरी %.1f मिमी) — सरासरीच्या %.1f%%",
				"%.1f mm of a normal %.1f mm — %.1f%% of normal",
				o.Season.ActualMM, o.Season.NormalMM, o.Season.PercentOfNormal,
			),
			Source: src,
			Reference: Tf(
				"%s; तूट %.1f टक्के अंक",
				"%s; deficit %.1f percentage points",
				o.Season.Label, o.Season.DeficitPoints(),
			),
			Effect: seasonEffect(o),
			// Corroborates, not Determines -- and the distinction is the rule
			// this whole platform rests on.
			//
			// This row settles something real: whether the rain failed, on the
			// state's own record. What it cannot settle is how badly this crop
			// was hurt, and Determines in this document means "determines the
			// damage figure". Barshi is the case in point: the season came to
			// 55% of normal and August to 16%, and the farmer's own footage
			// shows a dense green canopy. The rain failed and this field did
			// not. A rainfall deficit that determined a damage figure would pay
			// out on that.
			//
			// So it is the strongest corroboration in the section and still not
			// a measurement of loss.
			Role: Corroborates,
		},
	}

	for _, m := range o.Months {
		rows = append(rows, Row{
			Parameter: T(m.Label, m.Label),
			Observed: Tf(
				"%.1f मिमी (सरासरी %.1f मिमी) — %.1f%%",
				"%.1f mm of a normal %.1f mm — %.1f%%",
				m.ActualMM, m.NormalMM, m.PercentOfNormal,
			),
			Source:    src,
			Reference: monthReference(m),
			Effect:    monthEffect(m, c),
			Role:      Context,
		})
	}
	return rows
}

// seasonEffect says what the season's departure means, in the state's own terms.
func seasonEffect(o *weather.Official) Text {
	if o.SeasonDeficient() {
		return T(
			"राज्याच्या स्वतःच्या पर्जन्यमापकांनुसार हंगामातील पाऊस सरासरीच्या "+
				"निम्म्याहून कमी आहे. महाराष्ट्रात दुष्काळ जाहीर करण्याचा हा "+
				"प्राथमिक निकष आहे, म्हणजे शासनाच्या नोंदीवरूनच या भागात पाऊस "+
				"अपुरा पडला हे सिद्ध होते आणि दाव्याला पुष्टी मिळते. पुढचा प्रश्न "+
				"या शेतापुरता आहे: सिंचन असलेले शेत याच तुटीतून पीक वाचवू शकते, "+
				"म्हणून पुढील विभागांतील छायाचित्रे व उपग्रह नोंद पहावी.",
			"On the state's own gauges the season is at or below half its normal, "+
				"which is Maharashtra's primary drought indicator -- so the "+
				"government's own record establishes that the rain failed here, and "+
				"it supports the claim. What remains is whether it failed on this "+
				"field: an irrigated one carries a crop through the same deficit, "+
				"which is what the photographs and the satellite record below "+
				"answer.",
		)
	}
	return T(
		"पावसाची तूट सरासरीच्या निम्म्यापेक्षा कमी आहे, म्हणजे दुष्काळाचा "+
			"प्राथमिक निकष केवळ या आकड्यावरून पूर्ण होत नाही. महिन्यानिहाय "+
			"आकडे खाली पहावेत — हंगामाची बेरीज ठीक दिसत असतानाही एखादा "+
			"महिना कोरडा गेल्याने पीक जाऊ शकते.",
		"The shortfall is less than half the normal, so the primary drought "+
			"indicator is not met on this figure alone. Read the monthly rows "+
			"below: a crop can be lost in one dry month inside a season whose "+
			"total looks adequate.",
	)
}

func monthReference(m weather.OfficialPeriod) Text {
	switch {
	case m.PercentOfNormal <= weather.FailedMonthPercent:
		return T("सरासरीच्या एक चतुर्थांशाहून कमी",
			"under a quarter of normal")
	case m.PercentOfNormal <= 50:
		return T("सरासरीच्या निम्म्याहून कमी", "under half of normal")
	case m.PercentOfNormal >= 100:
		return T("सरासरीपेक्षा अधिक", "above normal")
	}
	return Tf("तूट %.1f टक्के अंक", "deficit %.1f percentage points",
		m.DeficitPoints())
}

// monthEffect grades the month rather than labelling most of them "monthly
// distribution".
//
// The first version flagged only months at or under a quarter of normal and gave
// everything else one filler sentence. The real Barshi report then printed
// September -- 51 mm of a normal 167, a 69-point shortfall, through pod
// development -- as "Monthly distribution. A season total does not show whether
// the rain arrived when the crop needed it." That is the least useful row in the
// section sitting on one of the most important figures in it.
//
// Where the stage is known it is named, because a dry month matters by when it
// fell: the same shortfall in June delays sowing and in September takes the
// grain.
func monthEffect(m weather.OfficialPeriod, c Case) Text {
	stage := func(mr, en string) Text {
		if !c.StageKnown {
			return T(mr, en)
		}
		return Tf(mr+" या महिन्यात पीक %s अवस्थेत होते.",
			en+" The crop was at %s through this month.", c.Stage.EN)
	}

	switch {
	case m.PercentOfNormal <= weather.FailedMonthPercent:
		return stage(
			"या महिन्यात पिकाला पाणी मिळालेले नाही — सरासरीच्या एक "+
				"चतुर्थांशाहून कमी पाऊस. हंगामाची बेरीज काहीही असली तरी असा "+
				"महिना उत्पादनावर परिणाम करतो.",
			"The crop had no water to speak of this month -- under a quarter of "+
				"its normal rainfall. Whatever the season totals to, a month like "+
				"this costs yield.")

	case m.PercentOfNormal <= 50:
		return stage(
			"या महिन्यात सरासरीच्या निम्म्याहून कमी पाऊस पडला, म्हणजे पिकाला "+
				"पाण्याचा ताण जाणवण्याइतकी तूट आहे.",
			"Under half its normal rainfall this month -- a shortfall large "+
				"enough for the crop to have felt water stress.")

	case m.PercentOfNormal >= 100:
		return stage(
			"या महिन्यात सरासरीइतका किंवा त्याहून अधिक पाऊस पडला. दुष्काळाच्या "+
				"दाव्यात हा महिना तुटीचा भाग नाही, आणि हंगामाची तूट इतर "+
				"महिन्यांतून आली आहे हे यावरून दिसते.",
			"This month had its normal rainfall or more. It is not part of any "+
				"shortfall, and it shows that a seasonal deficit was built in the "+
				"other months.")
	}

	return stage(
		"या महिन्यातील पाऊस सरासरीच्या जवळपास आहे, म्हणजे हंगामाची तूट या "+
			"महिन्यातून आलेली नाही.",
		"Close to its normal rainfall, so this month did not contribute much to "+
			"any seasonal shortfall.")
}

func unitWord(level string) Text {
	switch level {
	case "sub_district":
		return T("तालुका", "taluka")
	case "circle":
		return T("महसूल मंडळ", "revenue circle")
	default:
		return T(level, level)
	}
}
