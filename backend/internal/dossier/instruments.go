package dossier

import (
	"github.com/agribid/esurvey-platform/backend/internal/assessment"
	"github.com/agribid/esurvey-platform/backend/internal/sar"
	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// The instrument sections: weather, optical satellite, radar and terrain.
//
// None of these measures damage, and every one of them is routinely read as
// though it does. A fall in NDVI is a fall in canopy greenness, which a
// harvest produces as readily as a hailstorm; a collapse in backscatter is a
// smooth surface, which a flooded field and a freshly rolled one share. The
// Effect column on each row is where that distinction is made, in words,
// rather than left for a reader to know already.

// Weather builds §5.
//
// official is the state's own rainfall record for the unit this parcel sits in,
// where one has been loaded. It goes first and it is marked as the amount of
// record: a reanalysis row saying 505 mm above a gauge row saying 286 mm, in
// that order, is a document inviting an officer to read the wrong number.
func Weather(w *weather.Window, official *weather.Official, c Case) Section {
	s := Section{
		Number: "5",
		Title:  T("हवामान", "Weather"),
	}

	s.Rows = append(s.Rows, officialRows(official, c)...)

	if w == nil && len(s.Rows) > 0 {
		// The gauge figure stands on its own. Saying the reanalysis is missing
		// beside a real measurement would read as a gap where there is none.
		return s
	}
	if w == nil {
		s.Rows = append(s.Rows, AbsentRow(
			T("हवामान नोंद", "Weather record"),
			T(
				"या कालावधीसाठी हवामानाची नोंद मिळाली नाही",
				"No weather record was obtained for this window",
			),
			Corroborates,
		))
		return s
	}

	src := Tf(
		"%s ते %s (%d दिवस)",
		"%s to %s (%d days)",
		w.From, w.To, w.Days,
	)

	rows := []Row{
		{
			// Relabelled where the state's gauges are above it. Two totals in one
			// table that differ by a factor of two, both called "total rainfall",
			// is the row an officer cites by mistake at an appeal.
			Parameter: reanalysisTotalName(official),
			Observed:  Tf("%.0f मिमी, %d दिवस", "%.0f mm over %d days", w.RainMM, w.RainDays),
			Source:    src,
			Reference: Tf("त्यापैकी %d दिवस अतिवृष्टीचे", "%d of them heavy", w.HeavyDays),
			Effect:    reanalysisTotalEffect(official),
			Role:      Context,
		},
		{
			Parameter: T("एका दिवसातील सर्वाधिक पाऊस", "Heaviest single day"),
			Observed:  Tf("%.0f मिमी", "%.0f mm", w.MaxDailyMM),
			Source:    src,
			Reference: Tf("दिनांक %s", "on %s", orDash(w.MaxDailyOn, "—")),
			Effect:    heaviestDayEffect(w, c),
			Evidence:  Tf("दिनांक %s", "%s", orDash(w.MaxDailyOn, "—")),
			Role:      Corroborates,
		},
		{
			Parameter: T("सर्वाधिक वाऱ्याचा वेग", "Maximum gust"),
			Observed:  Tf("%.0f किमी/तास", "%.0f km/h", w.MaxGustKmh),
			Source:    src,
			Reference: Tf("दिनांक %s", "on %s", orDash(w.MaxGustOn, "—")),
			Effect:    gustEffect(w, c),
			Evidence:  Tf("दिनांक %s", "%s", orDash(w.MaxGustOn, "—")),
			Role:      Corroborates,
		},
		{
			Parameter: T("गारपिटीचे नोंदवलेले दिवस", "Hail-coded days"),
			Observed:  Tf("%d", "%d", w.HailDays),
			Source:    src,
			Reference: Tf("वादळाचे %d दिवस", "%d thunderstorm days", w.ThunderDays),
			Effect:    hailEffect(w, c),
			Role:      Corroborates,
		},
		{
			Parameter: T("सर्वात मोठा कोरडा कालावधी", "Longest dry spell"),
			Observed:  Tf("%d दिवस", "%d days", w.LongestDrySpell),
			Source:    src,
			Reference: Tf("प्रारंभ %s", "from %s", orDash(w.LongestDrySpellFrom, "—")),
			Effect:    drySpellEffect(w, c),
			Role:      Corroborates,
		},
		{
			Parameter: T("तापमान", "Temperature"),
			Observed: Tf("सर्वाधिक %.1f°C, सर्वात कमी %.1f°C",
				"highest %.1f°C, lowest %.1f°C", w.HottestC, w.ColdestC),
			Source: src,
			Effect: T(
				"उष्णता किंवा थंडीचा ताण, आणि पिकाच्या अवस्थेची गणना करण्यासाठी "+
					"वापरलेली उष्णता-बेरीज याच नोंदीतून येते.",
				"Heat and cold stress, and the thermal time used to place the "+
					"crop's growth stage comes from this same record.",
			),
			Role: Context,
		},
	}

	s.Rows = append(s.Rows, rows...)

	// What the total is worth.
	//
	// The reanalysis resolves convection by averaging it over an eleven-
	// kilometre cell, so a downpour on one village is returned as a thin film
	// over all of them. A window can then show a respectable total while most
	// of its wet days held a millimetre or two -- days that were dry on the
	// ground. Barshi's kharif read 468 mm across 49 rain days with a further
	// 52 days holding only a trace, through a drought the district declared.
	//
	// An officer weighing a drought claim against "468 mm" has to be told
	// this, or the strongest-looking number in the section is the least
	// reliable one in it.
	if w.RainDays > 0 && w.TraceDays >= w.RainDays {
		s.Notes = append(s.Notes, Tf(
			"या कालावधीत %d दिवस पाऊस पडला, तर अन्य %d दिवस केवळ हलक्या सरी "+
				"(२.५ मिमीपेक्षा कमी) नोंदल्या गेल्या. हवामान नोंद ११ किमीच्या "+
				"क्षेत्रावर सरासरी काढते, त्यामुळे वरील एकूण पाऊस या शेतापर्यंत "+
				"पोहोचलेल्या पावसाची कमाल मर्यादा आहे — प्रत्यक्ष आकडा नाही. "+
				"पावसाचे दिवस व कोरडा कालावधी हे आकडे अधिक विश्वासार्ह आहेत.",
			"It rained on %d days of this window; a further %d days held only a "+
				"trace, under 2.5 mm. The record averages over an eleven-kilometre "+
				"cell, so the total above is an upper bound on what reached this "+
				"field rather than a measurement of it. The rain-day count and the "+
				"dry spell are the more reliable figures here.",
			w.RainDays, w.TraceDays,
		))
	}

	// Missing days are stated rather than silently averaged over. A window
	// with a third of its days absent is a different piece of evidence from a
	// complete one, and only one of them is worth arguing with.
	if w.Missing > 0 {
		s.Notes = append(s.Notes, Tf(
			"या कालावधीतील %d दिवसांची नोंद उपलब्ध नाही. वरील आकडे उर्वरित "+
				"दिवसांवरून काढलेले आहेत.",
			"%d days in this window have no record. The figures above are drawn "+
				"from the remaining days.",
			w.Missing,
		))
	}

	return s
}

// drySpellEffect says what this run of dry days did to this crop.
//
// The old text said "the figure a drought claim turns on", which is true of the
// parameter and says nothing about the fifteen days actually in front of the
// officer. A dry spell is read by its length, when it fell, and what the crop
// was doing then: a fortnight without rain during vegetative growth sets a crop
// back, and the same fortnight through grain fill takes the yield.
func drySpellEffect(w *weather.Window, c Case) Text {
	from := orDash(w.LongestDrySpellFrom, "—")

	// Short enough that nothing follows from it.
	if w.LongestDrySpell < 10 {
		if c.Drought() {
			return Tf(
				"या कालावधीतील सर्वात मोठा कोरडा खंड %d दिवसांचा आहे, जो "+
					"नोंदवलेल्या दुष्काळाच्या दृष्टीने अपेक्षेपेक्षा लहान आहे. "+
					"वरील राज्य शासनाच्या मासिक नोंदी पहाव्यात: हंगामातील तूट "+
					"एका लांब खंडाऐवजी अनेक लहान खंडांतून आलेली असू शकते.",
				"The longest run without useful rain here is %d days, which is "+
					"shorter than a drought claim would ordinarily rest on. Read "+
					"the state's monthly figures above: a seasonal shortfall can "+
					"arrive as several short breaks rather than one long one.",
				w.LongestDrySpell)
		}
		return Tf(
			"सर्वात मोठा कोरडा खंड %d दिवसांचा — पिकावर ताण येण्यास पुरेसा "+
				"नाही. नोंदवलेल्या घटनेपूर्वी पीक कोरड्या ताणात नव्हते, म्हणजे "+
				"नुकसानीचे कारण ठरवणे तुलनेने सोपे आहे.",
			"The longest run without useful rain was %d days, too short to "+
				"stress the crop. The crop was therefore not already under dry "+
				"stress when the claimed event arrived, which makes attributing "+
				"the loss to it simpler.",
			w.LongestDrySpell)
	}

	// Long enough to matter. What it means depends on what the crop was doing.
	switch {
	case c.Drought() && c.StageKnown:
		return Tf(
			"%s पासून %d दिवस पिकाला उपयुक्त पाऊस मिळालेला नाही, आणि त्या "+
				"काळात पीक %s अवस्थेत होते. नोंदवलेल्या दुष्काळाचा हा प्रत्यक्ष "+
				"पुरावा आहे: या अवस्थेत इतका काळ पाणी न मिळाल्यास उत्पादनावर "+
				"परिणाम होतो.",
			"%d days from %s with no useful rain, and the crop was at %s "+
				"through them. This is the drought claimed, in the record: a run "+
				"this long at this stage costs yield.",
			w.LongestDrySpell, from, c.Stage.EN)

	case c.Drought():
		return Tf(
			"%s पासून %d दिवस पिकाला उपयुक्त पाऊस मिळालेला नाही. नोंदवलेल्या "+
				"दुष्काळाशी हे सुसंगत आहे. पीक कोणत्या अवस्थेत होते हे ठरवता "+
				"आलेले नाही, म्हणून या खंडाचा उत्पादनावर किती परिणाम झाला हे "+
				"येथून सांगता येत नाही.",
			"%d days from %s with no useful rain, which is consistent with the "+
				"drought claimed. No growth stage could be established, so how "+
				"much of the yield this run cost cannot be read from here.",
			w.LongestDrySpell, from)

	case c.Water():
		return Tf(
			"%s पासून %d दिवसांचा कोरडा खंड. नोंदवलेला दावा अतिवृष्टीचा आहे, "+
				"म्हणून हा खंड त्या घटनेचा भाग नाही — पण तो महत्त्वाचा आहे: "+
				"घटनेपूर्वीच पीक कोरड्या ताणात असल्यास नुकसान केवळ पाण्यामुळे "+
				"झाले असे म्हणता येत नाही.",
			"A dry run of %d days from %s. The claim here is about too much "+
				"water, so this run is not part of that event -- but it bears on "+
				"it: a crop already under dry stress when the water arrived did "+
				"not lose everything to the water.",
			w.LongestDrySpell, from)

	default:
		return Tf(
			"%s पासून %d दिवस पिकाला उपयुक्त पाऊस मिळालेला नाही. नोंदवलेल्या "+
				"घटनेपूर्वी पीक कोरड्या ताणात होते, म्हणजे नुकसान कशामुळे झाले "+
				"हे ठरवताना दोन्ही कारणे लक्षात घ्यावी लागतात.",
			"%d days from %s with no useful rain. The crop was already under dry "+
				"stress before the event claimed, so attributing the loss means "+
				"weighing both causes rather than one.",
			w.LongestDrySpell, from)
	}
}

func hailEffect(w *weather.Window, c Case) Text {
	if !c.Wind() {
		return T(
			"गारपिटीच्या नोंदी संदर्भासाठी. या दाव्यात गारपीट नोंदवलेली नाही.",
			"Hail coding for reference. Hail is not the calamity claimed here.",
		)
	}
	if w.HailDays > 0 {
		return T(
			"दाव्यात नोंदवलेल्या गारपिटीला हवामान नोंदीतून पुष्टी मिळते.",
			"The hail claimed is corroborated by the weather record.",
		)
	}
	return T(
		"गारपीट नोंदवली गेलेली नाही. यावरून दावा खोटा ठरत नाही — गारपीट अतिशय "+
			"स्थानिक असते आणि हवामान नोंदींची जाळी विरळ असल्याने ती अनेकदा नोंदली "+
			"जात नाही. वादळाचे दिवस आणि वाऱ्याचा वेग हे अधिक उपयुक्त संकेत आहेत.",
		"No hail was coded. That does not refute the claim: hail is intensely "+
			"local and the observation grid is coarse, so it routinely goes "+
			"unrecorded. Thunderstorm days and gust speed are the better signals.",
	)
}

// Optical builds §6.
func Optical(sat *assessment.Satellite, c Case) Section {
	s := Section{
		Number: "6",
		Title:  T("ऑप्टिकल उपग्रह निरीक्षण", "Optical satellite"),
	}

	if sat == nil {
		s.Rows = append(s.Rows, AbsentRow(
			T("NDVI मालिका", "NDVI series"),
			T(
				"या कालावधीत ढगांमुळे एकही स्पष्ट प्रतिमा मिळाली नाही. हा निष्कर्ष "+
					"आहे, त्रुटी नाही — मान्सूनच्या काळात हे नेहमीचे आहे.",
				"No cloud-free pass was obtained in this window. This is a "+
					"finding, not a failure: during the monsoon it is ordinary.",
			),
			Corroborates,
		))
		return s
	}

	src := Tf(
		"Sentinel-2 · %d ढगमुक्त निरीक्षणे",
		"Sentinel-2 · %d cloud-free observations",
		sat.ObservationCount,
	)

	s.Rows = append(s.Rows,
		Row{
			Parameter: T("शिखर हरितता (NDVI)", "Peak greenness (NDVI)"),
			Observed:  Tf("%.2f", "%.2f", sat.BaselineNDVI),
			Source:    src,
			Reference: Tf("दिनांक %s", "on %s", orDash(sat.BaselineDate, "—")),
			Effect: T(
				"घटनेपूर्वी पीक किती वाढले होते. नुकसानाची घट याच पातळीवरून "+
					"मोजली जाते, त्यामुळे शिखर कमी असल्यास घटही कमी दिसते.",
				"How far the crop had grown before the event. The fall is measured "+
					"from here, so a low peak makes the fall look smaller.",
			),
			Role: Corroborates,
		},
		Row{
			Parameter: T("घटनेनंतरची हरितता", "Greenness after the event"),
			Observed:  Tf("%.2f", "%.2f", sat.IncidentNDVI),
			Source:    src,
			Reference: Tf("दिनांक %s", "on %s", orDash(sat.IncidentDate, "—")),
			Effect: T(
				"घटनेनंतर शेतात किती हिरवे राहिले.",
				"How much green remained on the field after the event.",
			),
			Role: Corroborates,
		},
		Row{
			Parameter: T("सापेक्ष हरितता घट (RRVL)", "Relative vegetation loss (RRVL)"),
			Observed:  Tf("%.0f%%", "%.0f%%", sat.RRVLPercent),
			Source:    src,
			Reference: Tf("NDVI घट %.2f", "ΔNDVI %.2f", sat.DeltaNDVI),
			Effect:    rrvlEffect(sat, c),
			Evidence: Tf("%s → %s", "%s → %s",
				orDash(sat.BaselineDate, "—"), orDash(sat.IncidentDate, "—")),
			Role: Corroborates,
			Working: Texts{Tf(
				"घट %.2f, उघड्या जमिनीच्या पातळीवरील शिखरावर भागिले = %.0f%%",
				"fall of %.2f over the peak above bare soil = %.0f%%",
				sat.DeltaNDVI, sat.RRVLPercent,
			)},
		},
		gapRow(sat),
	)

	// Nothing grew that could be lost is a strong finding and is stated
	// separately rather than left to be inferred from a low peak.
	if sat.VegetationDetected != nil && !*sat.VegetationDetected {
		s.Rows = append(s.Rows, Row{
			Parameter: T("पीक आढळले का", "Was a crop detected"),
			Observed:  T("नाही", "No"),
			Source:    src,
			Effect: T(
				"या हंगामात या क्षेत्रावर उपग्रह नोंदीत लक्षणीय हिरवाई कधीही "+
					"आढळली नाही. म्हणजे गमावण्यासारखे पीक वाढल्याचे दिसत नाही. हा "+
					"गंभीर निष्कर्ष आहे. ढगाळ हंगामात असे चुकीचेही दिसू शकते, "+
					"त्यामुळे अधिकाऱ्यांनी तो छायाचित्रांशी ताडून पाहावा आणि "+
					"शंका राहिल्यास प्रत्यक्ष पाहणीची मागणी करावी.",
				"The satellite record never showed meaningful green on this "+
					"parcel this season, so no crop appears to have grown that "+
					"could be lost. This is a serious finding: officers must check "+
					"it against the photographs, and ask for a visit if doubt "+
					"remains, because a heavily clouded season can produce it "+
					"wrongly.",
			),
			Role: Corroborates,
		})
	}

	return s
}

func gapRow(sat *assessment.Satellite) Row {
	r := Row{
		Parameter: T("निरीक्षणांमधील सर्वात मोठे अंतर", "Largest gap between passes"),
		Observed:  Tf("%d दिवस", "%d days", sat.LargestGapDays),
		Source: Tf("एकूण %d निरीक्षणे", "%d observations in all",
			sat.ObservationCount),
		Role: Context,
	}

	// Printed at the same weight as the result it qualifies. A reader who is
	// not shown the gap will over-trust the delta, and this is the row that
	// stops the whole section being read as more certain than it is.
	if sat.LargestGapDays >= 15 {
		r.Effect = T(
			"हे अंतर मोठे आहे. या काळात पिकाचे शिखर आणि प्रत्यक्ष घटना या दोन्ही "+
				"नोंदींतून निसटलेल्या असू शकतात, त्यामुळे वरील घट प्रत्यक्षापेक्षा "+
				"कमी किंवा जास्त दिसू शकते.",
			"This is a long gap. Both the canopy peak and the event itself may "+
				"fall inside it, so the fall above may understate or overstate "+
				"what happened.",
		)
	} else {
		r.Effect = T(
			"निरीक्षणे पुरेशी सलग आहेत, त्यामुळे वरील घट विश्वासार्ह मानता येते.",
			"The series is close enough together for the fall above to be "+
				"relied on.",
		)
	}
	return r
}

// Radar builds §7.
func Radar(f *sar.Finding, r *sar.Reading, unavailable string, c Case) Section {
	s := Section{
		Number: "7",
		Title:  T("रडार निरीक्षण", "Radar"),
	}

	if f == nil {
		why := T(
			"या क्षेत्रासाठी रडार नोंद उपलब्ध नव्हती",
			"No radar reading was available for this parcel",
		)
		if unavailable != "" {
			why = T(unavailable, unavailable)
		}
		s.Rows = append(s.Rows, AbsentRow(
			T("रडार नोंद", "Radar record"), why, Corroborates))
		return s
	}

	// A generated series is not an observation, and must not be dressed as
	// one. It keeps its own source line, drops to Context so it carries no
	// weight, and says what it is on the row itself rather than in a footnote
	// an officer may not reach.
	generated := r != nil && r.Synthetic

	src := T("Sentinel-1 · VV आणि VH backscatter",
		"Sentinel-1 · VV and VH backscatter")
	role := Corroborates
	if generated {
		src = T("तयार केलेली मालिका · उपग्रह निरीक्षण नाही",
			"GENERATED SERIES · not a satellite observation")
		role = Context
	}

	main := Row{
		Parameter: T("रडार निष्कर्ष", "Radar finding"),
		Observed:  radarFinding(f.Code),
		Source:    src,
		Reference: Tf("विश्वासार्हता: %s", "confidence: %s", f.Confidence),
		Effect:    radarEffect(f.Code, c),
		Role:      role,
	}
	if generated {
		main.Caveats = append(main.Caveats, T(
			"ही मालिका कोणत्याही उपग्रहाने पाहिलेली नाही. ती चाचणीसाठी तयार "+
				"केलेली आहे आणि पुरावा म्हणून वापरता येणार नाही.",
			"No satellite observed this parcel. This series was generated for "+
				"testing and carries no evidential weight.",
		))
	}
	if f.Detail != "" {
		main.Working = append(main.Working, T(f.Detail, f.Detail))
	}
	for _, c := range f.Caveats {
		main.Caveats = append(main.Caveats, T(c, c))
	}
	s.Rows = append(s.Rows, main)

	if r != nil {
		s.Rows = append(s.Rows, Row{
			Role:      role,
			Parameter: T("Backscatter बदल", "Backscatter change"),
			Observed: Tf("VV %.1f dB, VH %.1f dB, गुणोत्तर %.1f dB",
				"VV %.1f dB, VH %.1f dB, ratio %.1f dB",
				r.DeltaVV, r.DeltaVH, r.DeltaRatio),
			Source: src,
			Reference: Tf(
				"घटनेपूर्वी VV %.1f, VH %.1f",
				"before the event VV %.1f, VH %.1f",
				r.BaselineVV, r.BaselineVH),
			Effect: T(
				"वरील निष्कर्ष याच आकड्यांवरून काढला आहे. उभ्या पिकामुळे VH "+
					"वाढतो; पीक आडवे पडल्यास तो घटतो. पाणी साचल्यास पृष्ठभाग "+
					"गुळगुळीत होऊन दोन्ही कोसळतात.",
				"The finding above is derived from these. A standing canopy lifts "+
					"VH; flatten it and VH falls. Standing water makes the surface "+
					"smooth and both collapse.",
			),
		})

		s.Rows = append(s.Rows, radarTimingRow(r))
	}

	return s
}

func radarTimingRow(r *sar.Reading) Row {
	row := Row{
		Parameter: T("घटनेनंतरचा पहिला रडार पास", "First radar pass after the event"),
		Observed: Tf("%s, %d दिवसांनी", "%s, %d days after",
			orDash(r.FirstAfter, "—"), r.DaysToFirst),
		Source: T("Sentinel-1 revisit", "Sentinel-1 revisit"),
		Role:   Context,
	}

	if r.DaysToFirst >= 6 {
		row.Effect = T(
			"रडार दर सहा ते बारा दिवसांनी जातो. या काळात पाणी ओसरले असल्यास "+
				"रडारला त्याची कोणतीही खूण दिसणार नाही — म्हणजे पाणी साचले "+
				"नव्हते असे नाही.",
			"Radar revisits every six to twelve days. Water that drained inside "+
				"that gap leaves no trace, so an absence of inundation here does "+
				"not mean the field was never flooded.",
		)
	} else {
		row.Effect = T(
			"घटनेनंतर लवकरच पास झाला, त्यामुळे पाणी साचले असल्यास ते दिसण्याची "+
				"शक्यता जास्त होती.",
			"The pass came soon after the event, so standing water would most "+
				"likely have been seen.",
		)
	}

	// Backscatter differs by several decibels between ascending and
	// descending passes for the same unchanged field. Comparing across them
	// produces changes that did not happen, and a reader is entitled to know
	// which comparison was made.
	if !r.OrbitsMatched {
		row.Caveats = append(row.Caveats, T(
			"तुलना वेगवेगळ्या कक्षांतील पासांमध्ये केली आहे. एकाच न बदललेल्या "+
				"शेतासाठीही अशा दोन पासांत काही डेसिबलचा फरक येतो, त्यामुळे वरील "+
				"बदल सावधपणे वाचावा.",
			"The comparison is across different orbits. For the same unchanged "+
				"field two such passes differ by several decibels, so the change "+
				"above should be read with that in mind.",
		))
	}

	return row
}

func radarFinding(code string) Text {
	switch code {
	case sar.FindingInundation:
		return T("पाणी साचलेले आढळले", "Standing water detected")
	case sar.FindingLodging:
		return T("पीक आडवे पडलेले आढळले", "Flattened canopy detected")
	case sar.FindingCanopyLoss:
		return T("पीक नष्ट झाल्याचे संकेत", "Canopy loss detected")
	case sar.FindingNoChange:
		return T("लक्षणीय बदल आढळला नाही", "No material change")
	}
	return T(code, code)
}

// radarEffect reads a radar finding against the claim it is evidence about.
//
// # The bug this replaces
//
// It used to say, of standing water, "Standing water strengthens the claim" --
// whatever had been claimed. On a drought claim that is not a weaker statement,
// it is the opposite of the truth: radar finding water on a field said to have
// gone dry is one of the strongest contradictions this platform can produce, and
// the report called it corroboration.
//
// Lodging had the same shape ("two independent sources agree", where only one of
// them was about the claimed event), and no-change said it "does not agree with
// the claim" even for a drought, where an unchanged surface is what radar
// ordinarily sees: drought does not roughen a field the way hail does.
//
// A finding is a fact about the surface. What it means is a question about the
// claim, and cannot be answered without it.
func radarEffect(code string, c Case) Text {
	switch code {
	case sar.FindingInundation:
		switch {
		case c.Water():
			return T(
				"रडार ढगांतूनही पाहू शकतो, त्यामुळे पावसाळी ढगांखाली झालेल्या "+
					"नुकसानाचा हा स्वतंत्र पुरावा आहे. नोंदवलेला दावा पाण्याचाच "+
					"आहे आणि रडारला पाणी साचलेले दिसले — दोन्ही जुळतात.",
				"Radar sees through cloud, so this is independent evidence of "+
					"damage that happened under monsoon cover. The claim is about "+
					"water and radar found water standing: the two agree.")
		case c.Drought():
			return T(
				"रडारला या शेतावर पाणी साचलेले आढळले, तर दावा दुष्काळाचा आहे. "+
					"हे परस्परविरोधी आहे आणि या अहवालातील सर्वात गंभीर विसंगती "+
					"आहे. मात्र दोन शक्यता तपासाव्यात: रडारची तारीख नोंदवलेल्या "+
					"घटनेनंतरच्या पावसाची असू शकते, आणि गुळगुळीत ओली जमीन व "+
					"पाणी साचणे रडारला सारखेच दिसू शकते. अधिकाऱ्यांनी रडारची "+
					"तारीख आणि छायाचित्रे दोन्ही तपासावीत.",
				"Radar found water standing on this parcel while the claim is of "+
					"drought. These contradict each other, and it is the most "+
					"serious inconsistency in this report. Two innocent readings "+
					"remain: the radar pass may post-date the claimed event and "+
					"catch later rain, and wet smooth ground can look like "+
					"standing water to radar. Check the pass date and the "+
					"photographs before relying on it.")
		default:
			return Tf(
				"रडारला पाणी साचलेले आढळले, तर नोंदवलेली घटना %s आहे. रडार "+
					"ढगांतूनही पाहतो, म्हणून ही नोंद विश्वासार्ह आहे — पण ती "+
					"दाव्यातील घटनेची नाही. शेतावर पाणी साचल्याचे कारण "+
					"तपासण्याजोगे आहे.",
				"Radar found standing water where the event claimed is %s. Radar "+
					"sees through cloud so the observation is sound, but it is not "+
					"of the event claimed. Why water was standing on this parcel "+
					"is worth establishing.",
				c.CalamityText().EN)
		}

	case sar.FindingLodging:
		if c.Wind() {
			return T(
				"आडवे पडलेले पीक रडारला दिसते, आणि नोंदवलेली घटना पीक आडवे "+
					"पाडणारीच आहे. शेतातील मोजणीतही आडवी रोपे आढळली असल्यास दोन "+
					"स्वतंत्र स्रोत एकच गोष्ट सांगतात — त्यांपैकी एक ढगांतून "+
					"पाहणारा आहे.",
				"A flattened canopy is visible to radar, and the event claimed is "+
					"one that flattens crops. Where the count from the photographs "+
					"also found lodged plants, two independent sources agree -- and "+
					"one of them saw through the cloud.")
		}
		return Tf(
			"रडारला आडवे पडलेले पीक आढळले, तर नोंदवलेली घटना %s आहे. पीक आडवे "+
				"पडण्याचे कारण वारा, गारपीट, पाणी साचणे किंवा जास्त वाढ असू "+
				"शकते, म्हणून हा निष्कर्ष दाव्याला थेट पुष्टी देत नाही; नुकसान "+
				"झाले आहे एवढे तो सांगतो.",
			"Radar found a flattened canopy where the event claimed is %s. "+
				"Lodging follows from wind, hail, standing water or rank growth, "+
				"so this does not corroborate the claim directly -- what it "+
				"establishes is that the canopy went down.",
			c.CalamityText().EN)

	case sar.FindingCanopyLoss:
		if c.HarvestPlausible() {
			return T(
				"पीक निघून गेल्याचे किंवा नष्ट झाल्याचे संकेत, परंतु पाणी साचणे "+
					"किंवा आडवे पडणे यांपैकी कोणतीही विशिष्ट खूण नाही. घटनेच्या "+
					"वेळी पीक काढणीयोग्य होते, म्हणून काढणी हे तितकेच संभाव्य "+
					"स्पष्टीकरण आहे; रडारच्या तारखेची आणि छायाचित्रांची तुलना "+
					"करावी.",
				"Consistent with the crop being removed or destroyed, with the "+
					"signature of neither flooding nor lodging. The crop was "+
					"harvestable at the incident, so a harvest is an equally "+
					"available explanation: compare the pass date with the "+
					"photographs.")
		}
		return T(
			"पीक निघून गेल्याचे किंवा नष्ट झाल्याचे संकेत, परंतु पाणी साचणे "+
				"किंवा आडवे पडणे यांपैकी कोणतीही खूण नाही. घटनेच्या वेळी पीक "+
				"काढणीयोग्य नव्हते, म्हणून काढणीचे स्पष्टीकरण लागू होत नाही — "+
				"आच्छादन गेले याचे कारण अन्यत्र शोधावे लागेल.",
			"Consistent with the crop being removed or destroyed, with the "+
				"signature of neither flooding nor lodging. The crop was not "+
				"harvestable at the incident, so a harvest does not explain it "+
				"and the reason the canopy went must be found elsewhere.")

	case sar.FindingNoChange:
		if c.Drought() {
			return T(
				"रडारने ढगांतूनही हे शेत पाहिले आणि पृष्ठभागात लक्षणीय बदल "+
					"आढळला नाही. दुष्काळाच्या दाव्यात याचा अर्थ मर्यादित आहे: "+
					"रडार पृष्ठभागाचा खरबरीतपणा मोजतो, आणि पाण्याच्या ताणाने "+
					"वाळणारे पीक गारपिटीसारखा पृष्ठभाग बदलत नाही. म्हणून ही नोंद "+
					"दाव्याला विरोध करत नाही — दुष्काळाचा पुरावा उपग्रहाच्या "+
					"हरितता नोंदीत व पावसाच्या आकड्यांत शोधावा.",
				"Radar saw this field through the cloud and the surface did not "+
					"materially change. For a drought claim that means less than it "+
					"appears: radar measures surface roughness, and a crop drying "+
					"under water stress does not alter roughness the way hail does. "+
					"This does not contradict the claim -- the evidence for drought "+
					"is in the optical record and the rainfall figures.")
		}
		return Tf(
			"रडारने ढगांतूनही हे शेत पाहिले आणि त्यात लक्षणीय बदल आढळला नाही. "+
				"नोंदवलेली घटना (%s) पृष्ठभाग बदलणारी आहे, म्हणून ही नोंद "+
				"दाव्याशी जुळत नाही आणि इतर निष्कर्षांइतकीच महत्त्वाची आहे. "+
				"अधिकाऱ्यांनी ती छायाचित्रांतील मोजणीशी ताडून पाहावी.",
			"Radar saw this field through the cloud and it did not materially "+
				"change. The event claimed (%s) is one that alters the surface, so "+
				"this finding does not agree with the claim and carries the same "+
				"weight as the others. Officers must weigh it against the count "+
				"from the photographs.",
			c.CalamityText().EN)
	}
	return T("", "")
}

// TerrainSection builds §8.
func TerrainSection(t *assessment.Terrain, c Case) Section {
	s := Section{
		Number: "8",
		Title:  T("भूभाग", "Terrain"),
	}

	if t == nil {
		s.Rows = append(s.Rows, AbsentRow(
			T("उताराची माहिती", "Slope data"),
			T("या क्षेत्राची उताराची नोंद मिळाली नाही",
				"No slope data was obtained for this parcel"),
			Context,
		))
		return s
	}

	s.Rows = append(s.Rows, Row{
		Parameter: T("सरासरी उतार", "Mean slope"),
		Observed:  Tf("%.1f अंश", "%.1f degrees", t.MeanSlopeDegrees),
		Source:    T("भूभाग विश्लेषण (DEM)", "Terrain model (DEM)"),
		Reference: T(orDash(t.Classification, "—"), orDash(t.Classification, "—")),
		Effect:    slopeEffect(t, c),
		Role:      Context,
	})

	if t.WaterloggingRisk != "" {
		s.Rows = append(s.Rows, Row{
			Parameter: T("पाणी साचण्याची शक्यता", "Waterlogging susceptibility"),
			Observed:  T(t.WaterloggingRisk, t.WaterloggingRisk),
			Source:    T("उतारावरून काढलेले", "Derived from slope"),
			Effect:    waterloggingEffect(t, c),
			Role:      Context,
		})
	}

	return s
}

// slopeEffect says what this parcel's slope bears on for the claim in hand.
//
// It used to explain waterlogging whatever had been claimed, which on a drought
// report is a paragraph about an event nobody alleged. Slope decides how water
// leaves a field. Whether that matters here depends entirely on what is said to
// have happened.
func slopeEffect(t *assessment.Terrain, c Case) Text {
	flat := t.MeanSlopeDegrees < 3

	switch {
	case c.Water() && flat:
		return Tf(
			"%.1f अंश उतार म्हणजे जमीन जवळपास सपाट आहे, त्यामुळे पडलेले पाणी "+
				"वाहून जाण्यास जागा नाही. नोंदवलेल्या पाणी साचण्याच्या घटनेशी "+
				"जमिनीची ठेवण सुसंगत आहे. जमीन सपाट असल्याने नुकसान झाले असे "+
				"नाही — पण या दाव्यातील भौगोलिक अडचण दूर होते.",
			"At %.1f degrees this parcel is close to level, so water that "+
				"arrives has nowhere to run. The ground is consistent with the "+
				"water damage claimed. It does not show the damage happened; it "+
				"removes the geographical objection to the claim.",
			t.MeanSlopeDegrees)

	case c.Water() && !flat:
		return Tf(
			"%.1f अंश उतारावरून पाणी वाहून जाते, त्यामुळे या शेतात पाणी साचून "+
				"राहिल्याचा दावा तपासून पाहण्याजोगा आहे. उताराच्या खालच्या "+
				"बाजूस किंवा बांधाजवळ पाणी साचू शकते, म्हणून हा दावा यावरून "+
				"खोटा ठरत नाही — पण छायाचित्रांतून तो पडताळावा.",
			"Water runs off %.1f degrees, so a claim of standing water on this "+
				"parcel is worth checking. It can still collect at the foot of a "+
				"slope or behind a bund, so this does not refute the claim -- but "+
				"the photographs should be read with it in mind.",
			t.MeanSlopeDegrees)

	case c.Drought():
		return Tf(
			"%.1f अंश उतार. नोंदवलेल्या दुष्काळाच्या दृष्टीने उतार जवळपास "+
				"निरर्थक आहे: उतार पाणी कोठे जाते हे ठरवतो, पाऊस पडला की नाही "+
				"हे नाही. उतार असलेल्या जमिनीत पाणी लवकर निघून जाते एवढाच "+
				"मर्यादित संबंध आहे; पाणी साचल्याचा दावा असता तर हा आकडा "+
				"निर्णायक ठरला असता.",
			"At %.1f degrees. For the drought claimed here slope bears almost "+
				"nothing: it decides where water goes, not whether rain arrived. "+
				"Its only bearing is that sloping ground drains sooner and so "+
				"holds less of what did fall. Had waterlogging been claimed this "+
				"figure would have been decisive.",
			t.MeanSlopeDegrees)

	case c.Wind():
		return Tf(
			"%.1f अंश उतार. गारपीट व वादळाचे नुकसान उतारावर अवलंबून नसते, "+
				"त्यामुळे नोंदवलेल्या घटनेच्या दृष्टीने या आकड्याचा उपयोग नाही. "+
				"तो येथे नोंदवला आहे कारण पुढे पाणी साचल्याचा दावा आल्यास ही "+
				"नोंद उपयोगी ठरते.",
			"At %.1f degrees. Hail and storm damage does not depend on slope, so "+
				"this bears nothing on the event claimed. It is recorded because "+
				"it would matter to a later claim of standing water on the same "+
				"parcel.",
			t.MeanSlopeDegrees)
	}

	// An unfamiliar calamity. Describe the ground and say what it governs,
	// without asserting anything about an event this code cannot classify.
	return Tf(
		"%.1f अंश उतार (%s). उतार हा पाणी शेतातून किती लवकर निघून जाते हे "+
			"ठरवतो. पाण्याशी संबंधित दाव्यांसाठी हा आकडा महत्त्वाचा आहे; "+
			"इतर आपत्तींसाठी तो केवळ संदर्भ आहे.",
		"At %.1f degrees (%s). Slope governs how quickly water leaves a field. "+
			"That matters to a claim about water and is background to any other.",
		t.MeanSlopeDegrees, orDash(t.Classification, "unclassified"))
}

// waterloggingEffect reads the derived susceptibility against the claim.
func waterloggingEffect(t *assessment.Terrain, c Case) Text {
	risk := T(t.WaterloggingRisk, t.WaterloggingRisk)

	if c.Drought() {
		return Tf(
			"पाणी साचण्याची शक्यता: %s. हा आकडा उतारावरून काढलेला आहे आणि "+
				"दुष्काळाच्या दाव्याशी त्याचा थेट संबंध नाही. मात्र एक अप्रत्यक्ष "+
				"अर्थ आहे: जिथे पाणी साचून राहते तिथे जमीन ओलावा जास्त काळ "+
				"धरून ठेवते, म्हणून अशा शेतात कोरड्या कालावधीचा परिणाम काहीसा "+
				"उशिरा जाणवतो.",
			"Waterlogging susceptibility: %s. Derived from slope, and it bears "+
				"no direct relation to a drought claim. Its one indirect bearing "+
				"is that ground which holds water also holds soil moisture "+
				"longer, so a dry spell reaches the crop later on a parcel like "+
				"this than on a well-drained one.",
			risk.EN)
	}
	if c.Water() {
		return Tf(
			"पाणी साचण्याची शक्यता: %s. नोंदवलेला दावा पाण्याशी संबंधित आहे, "+
				"म्हणून हा आकडा थेट लागू होतो: शक्यता जास्त असल्यास दावा "+
				"भौगोलिकदृष्ट्या सुसंगत ठरतो, कमी असल्यास तो तपासून "+
				"पाहण्याजोगा ठरतो — खोटा ठरत नाही.",
			"Waterlogging susceptibility: %s. The claim is about water, so this "+
				"applies directly: a high susceptibility makes the claim "+
				"geographically consistent, and a low one makes it worth "+
				"checking rather than refuting it.",
			risk.EN)
	}
	return Tf(
		"पाणी साचण्याची शक्यता: %s. उतारावरून काढलेली. नोंदवलेल्या घटनेशी "+
			"संबंध नाही; पाण्याशी संबंधित दाव्यासाठी ती लागू होईल.",
		"Waterlogging susceptibility: %s. Derived from slope. It bears on a "+
			"claim about water rather than on the event claimed here.",
		risk.EN)
}

// reanalysisTotalName distinguishes the model's total from the state's.
func reanalysisTotalName(official *weather.Official) Text {
	if official == nil || official.Season == nil {
		return T("एकूण पाऊस", "Total rainfall")
	}
	return T("एकूण पाऊस (हवामान प्रतिमान — तुलनेसाठी)",
		"Total rainfall (reanalysis model — for comparison)")
}

// reanalysisTotalEffect says what the model's total is worth once a gauge
// reading is present.
//
// Over this region the reanalysis reports roughly twice the rain that fell, and
// an officer who sees "505 mm" under a heading that does not say it is modelled
// has been handed the figure that makes a declared drought disappear.
func reanalysisTotalEffect(official *weather.Official) Text {
	if official == nil || official.Season == nil {
		return T(
			"पाणी साचणे व लोळणे यांच्याशी संबंधित. एकूण पाऊस स्वतःहून नुकसान "+
				"दर्शवत नाही — तो नुकसान शक्य होते का, एवढेच सांगतो. या "+
				"आकड्याची राज्याच्या पर्जन्यमापकांशी तुलना करता आलेली नाही.",
			"Bears on waterlogging and lodging. A total on its own shows no "+
				"damage; it says only whether damage was possible. This figure "+
				"has not been checked against the state's rain gauges.",
		)
	}
	return T(
		"पाऊस कोणत्या दिवशी पडला हे या नोंदीतून कळते — खालील कोरडा कालावधी, "+
			"तापमान व वाऱ्याचा वेग हे सर्व आकडे येथूनच आले आहेत, आणि चालू "+
			"हंगामाची दैनिक नोंद अन्यत्र उपलब्ध नाही. पावसाचे एकूण प्रमाण मात्र "+
			"वरील राज्य शासनाच्या नोंदीतून घ्यावे: हे प्रतिमान या भागात प्रत्यक्ष "+
			"पडलेल्या पावसापेक्षा बराच अधिक पाऊस दाखवते.",
		"Which days the rain fell on, and the source of every figure below it "+
			"-- the dry spell, the temperature range and the gusts. It is the "+
			"only daily record available for the current season. For the amount "+
			"of rain, read the state's figure above: this model reports "+
			"substantially more than fell over this region.",
	)
}

// rrvlEffect reads this parcel's relative loss against the claim and the
// calendar.
//
// # What changed and why
//
// It used to warn, on every report, that a harvest produces the same fall in
// greenness. That is the single most available misreading of this figure and the
// warning earns its place -- but only where a harvest was actually possible. A
// crop a third of the way through its cycle that lost its canopy did not lose it
// to a combine, and sending an officer after that explanation wastes the one
// sentence they are given and invites them to doubt a sound claim.
//
// So the alternative explanation is offered where the calendar allows it and
// ruled out where it does not, which is the difference between documenting a
// parameter and analysing a case.
func rrvlEffect(sat *assessment.Satellite, c Case) Text {
	// The fall itself, always. Whatever follows, the officer is told what the
	// number is a measure of.
	head := Tf(
		"या शेताने हंगामात गाठलेल्या सर्वोच्च हरिततेपैकी %.0f%% घट झाली — "+
			"तुलना शेजारच्या शेतांशी नाही, या शेताच्या स्वतःच्या शिखराशी आहे.",
		"A fall of %.0f%% from the greenness this field reached at its own peak "+
			"-- measured against itself, not against its neighbours.",
		sat.RRVLPercent)

	var tail Text
	switch {
	case !c.StageKnown:
		tail = T(
			"पीक कोणत्या अवस्थेत होते हे ठरवता आलेले नाही, म्हणून लवकर काढणी "+
				"केल्याने हीच घट दिसू शकते ही शक्यता नाकारता येत नाही. "+
				"छायाचित्रांतील मोजणीशी जुळल्यावरच हा आकडा दाव्याला पुष्टी देतो.",
			"No growth stage could be established, so an early harvest -- which "+
				"produces exactly this fall -- cannot be ruled out. The figure "+
				"corroborates the claim only where it agrees with the count from "+
				"the photographs.")

	case c.HarvestPlausible():
		tail = Tf(
			"घटनेच्या वेळी पीक %s अवस्थेत, आयुष्याच्या %.0f%% टप्प्यावर होते — "+
				"म्हणजे काढणी शक्य होती, आणि लवकर काढणी केल्यासही अगदी अशीच घट "+
				"दिसते. या दोन्हींत फरक उपग्रहावरून करता येत नाही; "+
				"छायाचित्रांतील मोजणी तो करते.",
			"At the incident the crop was at %s, %.0f%% through its cycle -- so "+
				"it was harvestable, and an early harvest produces exactly this "+
				"fall. Orbit cannot separate the two; the count from the "+
				"photographs can.",
			c.Stage.EN, c.PercentOfCycle)

	default:
		tail = Tf(
			"घटनेच्या वेळी पीक %s अवस्थेत, आयुष्याच्या केवळ %.0f%% टप्प्यावर "+
				"होते — काढणीसाठी काहीच तयार नव्हते. म्हणून या घटीचे सर्वात "+
				"सामान्य दुसरे स्पष्टीकरण, लवकर काढणी, येथे लागू होत नाही. "+
				"ही घट पिकाची हानी, पाण्याचा ताण किंवा पुन्हा पेरणी यांतून "+
				"आलेली असावी.",
			"At the incident the crop was at %s, only %.0f%% through its cycle "+
				"-- there was nothing to harvest. The commonest alternative "+
				"explanation for a fall like this is therefore ruled out by the "+
				"calendar. What remains is crop loss, water stress, or a "+
				"re-sowing.",
			c.Stage.EN, c.PercentOfCycle)
	}

	// Where the state's gauges show the season short, the two readings are
	// about the same event and the officer should be told they agree.
	if c.Drought() && c.RainFailed() {
		tail = Text{
			MR: tail.MR + " राज्य शासनाच्या नोंदीनुसार हंगामातील पाऊस सरासरीपेक्षा " +
				"बराच कमी आहे, त्यामुळे हरिततेतील घट आणि पावसाची तूट एकाच " +
				"घटनेकडे निर्देश करतात.",
			EN: tail.EN + " The state's gauges show the season well short of " +
				"normal, so this fall and the rainfall deficit point at the same " +
				"event.",
		}
	}

	return Text{MR: head.MR + " " + tail.MR, EN: head.EN + " " + tail.EN}
}

// heaviestDayEffect reads the wettest single day against the claim.
//
// The old text opened "For a heavy-rain claim this matters more than the total",
// which is sound and was printed on a drought report, where the wettest day of
// the season bears on the claim hardly at all. What it does bear on there is
// whether the dry spell was ever broken.
func heaviestDayEffect(w *weather.Window, c Case) Text {
	on := orDash(w.MaxDailyOn, "—")

	switch {
	case c.Water():
		return Tf(
			"अतिवृष्टीच्या दाव्यासाठी एकूण पावसापेक्षा हा आकडा अधिक महत्त्वाचा "+
				"आहे: दोन आठवड्यांत पडलेले २०० मिमी आणि एका दिवसात पडलेले २०० "+
				"मिमी या वेगळ्या घटना आहेत. %s रोजी पडलेले %.0f मिमी हेच "+
				"नोंदवलेल्या घटनेचे दिवस असू शकतात — तारीख दाव्यातील तारखेशी "+
				"ताडून पहावी.",
			"For a claim about too much water this matters more than the total: "+
				"200 mm over a fortnight and 200 mm in a day are different events. "+
				"The %.0f mm on %s may be the event itself -- check the date "+
				"against the one claimed.",
			w.MaxDailyMM, on)

	case c.Drought():
		return Tf(
			"हंगामातील सर्वाधिक पावसाचा दिवस: %s रोजी %.0f मिमी. दुष्काळाच्या "+
				"दाव्यात या आकड्याचा उपयोग एकाच गोष्टीसाठी आहे — कोरडा कालावधी "+
				"कधी खंडित झाला हे पाहण्यासाठी. एकाही दिवशी मोठा पाऊस नसणे "+
				"तुटीला बळकटी देते.",
			"The season's wettest day: %.0f mm on %s. On a drought claim this "+
				"serves one purpose -- showing when, if ever, the dry spell was "+
				"broken. No single substantial fall anywhere in the season "+
				"reinforces the shortfall.",
			w.MaxDailyMM, on)

	case c.Wind():
		return Tf(
			"%s रोजी %.0f मिमी. गारपीट व वादळासोबत जोरदार पाऊस येतो, म्हणून "+
				"नोंदवलेल्या घटनेच्या तारखेला मोठा पाऊस आढळल्यास दोन्ही एकाच "+
				"घटनेकडे निर्देश करतात. तारखांची तुलना करावी.",
			"%.0f mm on %s. Hail and storms arrive with heavy rain, so a large "+
				"fall on the date claimed points at the same event. Compare the "+
				"two dates.",
			w.MaxDailyMM, on)
	}
	return Tf(
		"हंगामातील सर्वाधिक पावसाचा दिवस: %s रोजी %.0f मिमी. एका दिवसात "+
			"पडलेला पाऊस आणि हंगामाची बेरीज या वेगळ्या गोष्टी आहेत.",
		"The season's wettest day: %.0f mm on %s. What falls in one day and what "+
			"falls across a season are different facts.",
		w.MaxDailyMM, on)
}

// gustEffect reads the strongest gust against the claim.
//
// Lodging is what wind does to a crop, so this row is evidence on a hail or
// storm claim and background on a drought. Printing "the main cause of lodging"
// on a drought report invites an officer to look for lodging nobody alleged.
func gustEffect(w *weather.Window, c Case) Text {
	on := orDash(w.MaxGustOn, "—")
	strong := w.MaxGustKmh >= 50

	switch {
	case c.Wind() && strong:
		return Tf(
			"%s रोजी %.0f किमी/तास — पीक आडवे पाडण्याइतका वारा. नोंदवलेली घटना "+
				"वाऱ्याचीच आहे, म्हणून हा आकडा तिचा थेट पुरावा आहे. "+
				"छायाचित्रांत आडवी पडलेली रोपे आढळल्यास दोन स्वतंत्र स्रोत "+
				"एकच गोष्ट सांगतात.",
			"%.0f km/h on %s -- enough to flatten a crop. The event claimed is "+
				"one of wind, so this is direct evidence of it. Where the "+
				"photographs also show lodged plants, two independent sources "+
				"agree.",
			w.MaxGustKmh, on)

	case c.Wind():
		return Tf(
			"%s रोजी सर्वाधिक %.0f किमी/तास. नोंदवलेली घटना वाऱ्याची आहे, पण "+
				"हा वेग पीक आडवे पाडण्यास सामान्यतः पुरेसा नाही. वाऱ्याची नोंद "+
				"११ किमीच्या क्षेत्रावरील आहे आणि स्थानिक झोत त्यात दिसत नाहीत, "+
				"म्हणून यावरून दावा खोटा ठरत नाही — पण छायाचित्रांतील पुरावा "+
				"अधिक महत्त्वाचा ठरतो.",
			"A strongest gust of %.0f km/h on %s. The event claimed is one of "+
				"wind, but this speed is not ordinarily enough to flatten a crop. "+
				"The wind record is an eleven-kilometre average and does not see "+
				"a local downburst, so it does not refute the claim -- it puts "+
				"the weight on the photographs.",
			w.MaxGustKmh, on)

	case c.Drought():
		return Tf(
			"%s रोजी सर्वाधिक %.0f किमी/तास. दुष्काळाच्या दाव्याशी वाऱ्याचा "+
				"संबंध नाही; ही नोंद येथे आहे कारण छायाचित्रांत आडवी पडलेली "+
				"रोपे आढळली तर ती कशामुळे, याचे उत्तर या ओळीत मिळू शकते.",
			"A strongest gust of %.0f km/h on %s. Wind bears nothing on a "+
				"drought claim. It is recorded because if the photographs show "+
				"lodged plants, this row is where the explanation would be.",
			w.MaxGustKmh, on)
	}
	return Tf(
		"%s रोजी सर्वाधिक %.0f किमी/तास. जोरदार वारा हे पीक आडवे पडण्याचे "+
			"मुख्य कारण आहे.",
		"A strongest gust of %.0f km/h on %s. Strong wind is the main cause of "+
			"lodging.",
		w.MaxGustKmh, on)
}
