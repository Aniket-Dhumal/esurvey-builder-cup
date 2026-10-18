package dossier

// Who chose the evidence.
//
// # The question this section answers
//
// Every photograph and clip in the next section was taken by the person whose
// loss is being assessed. That is not a criticism of farmers -- it is the
// design of a platform where nobody visits -- but it is the first thing an
// appellate officer should ask about, and until now the report had no answer.
//
// A farmer who records only the waterlogged corner of a field turns a thirty
// per cent loss into an eighty per cent one. Usually without intending to: the
// wet patch is the part that feels worth showing, and nothing in the app ever
// said otherwise.
//
// So the app now says otherwise. Four points are placed across the parcel by
// the server before the walk begins, and the app offers each in turn as a
// suggestion.
//
// # What the farmer is and is not required to do
//
// They are never required to stand on a suggested point, and this section must
// not imply that they were. A farmer cannot always reach a coordinate: there
// may be standing water on it, a bund, a neighbour's crop, or simply mud. The
// app asks two things instead, and refuses a recording for neither reason
// other than these: be inside your own boundary, and be a set distance from
// everywhere this walk has already recorded (survey.SpacingFor).
//
// That is what makes the evidence a sample of the field rather than a
// selection from it, and it is all this section may claim. An earlier version
// of this text said the farmer was guided to each point and reported any
// distance over fifteen metres as "could not reach the assigned point and
// stopped short" -- a failure the farmer never committed, printed in a
// document that decides their payment.
//
// # Why it is not marked as determining
//
// Coverage does not measure damage. It measures whether the things that do
// measure damage were looking at a representative part of the field. Marked
// as determining, it would read as evidence of loss, which it is not -- but
// absent from the report entirely, the photographs would carry an authority
// nobody had checked.

// StationCoverage is one suggested point and what came back from it.
type StationCoverage struct {
	StationNo int    `json:"station_no"`
	Role      string `json:"role"`
	Placement string `json:"placement"`

	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`

	// Captured is whether evidence arrived from this station at all.
	Captured bool `json:"captured"`

	// OffsetM is how far from the suggested point the capture was actually
	// taken. Nil when nothing was captured.
	//
	// A few metres is ordinary GNSS scatter. Tens of metres means the station
	// could not be reached -- water too deep to wade, a bund, a neighbour's
	// standing crop -- and the farmer moved it. They are not asked to justify
	// that, so the number is the only account of it anybody gets, and it is
	// the reason this column exists.
	OffsetM *float64 `json:"offset_m,omitempty"`

	CapturedAt string `json:"captured_at,omitempty"`
	CaptureID  string `json:"capture_id,omitempty"`
}

// stationName is what a station is called in the report.
//
// Under geometric placement neither end is higher than the other in any way a
// reader could check, so calling one "the low part" would be an unfounded
// claim about the land. They become the ends of the parcel, which is all the
// placement actually established.
func stationName(c StationCoverage) Text {
	terrain := c.Placement == "terrain"
	switch c.Role {
	case "low":
		if terrain {
			return T("१. शेताचा खालचा भाग (उतार जिथे पाणी जाते)",
				"1. Low end of the parcel (where water drains to)")
		}
		return T("१. शेताचे पहिले टोक", "1. First end of the parcel")
	case "mid_left":
		return T("२. मध्यभाग, एक बाजू", "2. Mid-parcel, one flank")
	case "mid_right":
		return T("३. मध्यभाग, दुसरी बाजू", "3. Mid-parcel, other flank")
	case "high":
		if terrain {
			return T("४. शेताचा उंच भाग", "4. High end of the parcel")
		}
		return T("४. शेताचे दुसरे टोक", "4. Far end of the parcel")
	default:
		return Tf("%d. सुचवलेले ठिकाण", "%d. Suggested point", c.StationNo)
	}
}

// SamplingCoverage is the record of where the farmer was sent and what came
// back.
// SamplingCoverage is the section, with the map and the plates when the
// caller could build them.
func SamplingCoverage(
	stations []StationCoverage, fig *Figure, spacingM float64,
) Section {
	s := Section{
		Number: "13",
		Title:  T("नमुना घेण्याची व्याप्ती", "Sampling coverage"),
		Figure: fig,
	}

	if len(stations) == 0 {
		// Older reports, and any parcel with no boundary to place points on.
		// Stated rather than omitted: a reader comparing two reports must be
		// able to tell a claim where the spread of the evidence was planned and
		// enforced from one where nothing constrained it, and a silent absence
		// reads as the stronger of the two.
		s.Preamble = T(
			"या दाव्यासाठी नमुना ठिकाणांची योजना नोंदवलेली नाही. पुढील "+
				"विभागातील पुरावा शेतकऱ्याने स्वतः निवडलेल्या ठिकाणी घेतलेला "+
				"आहे, आणि ती ठिकाणे एकमेकांपासून किती दूर असावीत याची अटही "+
				"लावलेली नाही; त्यामुळे तो संपूर्ण शेताचे प्रातिनिधिक चित्र "+
				"असेलच असे नाही.",
			"No sampling plan was recorded for this claim. The evidence in the "+
				"next section was recorded wherever the farmer chose, with no "+
				"requirement that those places be spread apart, so it is not "+
				"necessarily representative of the whole parcel.",
		)
		s.Rows = append(s.Rows, AbsentRow(
			T("नमुना ठिकाणांची योजना", "Sampling plan"),
			T("या दाव्यासाठी नमुना ठिकाणांची योजना नोंदवलेली नाही",
				"No sampling plan was recorded for this claim"),
			Context,
		))
		return s
	}

	terrain := stations[0].Placement == "terrain"

	s.Preamble = preamble(spacingM)

	var covered int
	for _, c := range stations {
		if c.Captured {
			covered++
		}

		if !c.Captured {
			s.Rows = append(s.Rows, AbsentRow(
				stationName(c),
				T("या ठिकाणाहून कोणताही पुरावा मिळालेला नाही",
					"No evidence was received from this point"),
				Corroborates,
			))
			continue
		}

		row := Row{
			Parameter: stationName(c),
			Observed:  T(orDash(c.CapturedAt, "—"), orDash(c.CapturedAt, "—")),
			Source: Tf("सुचवलेले ठिकाण %.5f, %.5f",
				"Suggested point %.5f, %.5f", c.Lat, c.Lon),
			Role:     Corroborates,
			Evidence: T(orDash(c.CaptureID, "—"), orDash(c.CaptureID, "—")),
		}

		switch {
		case c.OffsetM == nil:
			row.Reference = T("—", "—")
			row.Effect = T(
				"सुचवलेल्या ठिकाणीच पुरावा घेतला गेला.",
				"The recording was made at the suggested point itself.",
			)
		case *c.OffsetM <= 15:
			row.Reference = Tf("%.0f मी. अंतरावर", "%.0f m from the point", *c.OffsetM)
			row.Effect = T(
				"हे अंतर सामान्य स्थाननिश्चिती चुकीच्या मर्यादेत आहे; "+
					"पुरावा सुचवलेल्या ठिकाणचाच मानावा.",
				"That distance is within ordinary GNSS scatter; the recording "+
					"should be read as coming from the suggested point.",
			)
		default:
			// Recorded well away from the suggestion, which is permitted and
			// ordinary. What it costs is coverage of one part of the parcel,
			// and that is the only thing said here: the farmer is not asked
			// to justify where they stood, and is not reported as having
			// failed to do something nothing asked of them.
			row.Reference = Tf("%.0f मी. अंतरावर", "%.0f m from the point", *c.OffsetM)
			row.Effect = Tf(
				"शेतकऱ्याने सुचवलेल्या ठिकाणापासून %.0f मीटर अंतरावरून पुरावा "+
					"घेतला. हे नियमानुसार चालते -- शेतकरी शेतात जिथे उभा आहे "+
					"तिथून चित्रीकरण करतो, आणि अट एवढीच की प्रत्येक चित्रीकरण "+
					"आधीच्या ठिकाणांपासून पुरेसे दूर असावे. त्यामुळे शेताचा "+
					"जो भाग या ठिकाणाने तपासायचा होता तो कमी प्रमाणात "+
					"तपासला गेला असू शकतो; वरील नकाशा प्रत्यक्ष किती भाग "+
					"तपासला गेला ते दाखवतो.",
				"The recording was made %.0f m from the suggested point. That "+
					"is permitted: the farmer records from where they are "+
					"standing inside the field, and the only requirement is "+
					"that each recording be far enough from the others. The "+
					"part of the parcel this point would have sampled may "+
					"therefore be under-represented; the map above shows what "+
					"was actually covered.",
				*c.OffsetM)
		}

		s.Rows = append(s.Rows, row)
	}

	// How the ends were decided.
	if terrain {
		s.Notes = append(s.Notes, T(
			"ठिकाणांचा क्रम जमिनीच्या उताराप्रमाणे ठरवला आहे: पहिले ठिकाण "+
				"पाणी जिथे साचते तो भाग, आणि शेवटचे ठिकाण उंचावरचा भाग. "+
				"त्यामुळे ओला भाग आणि कोरडा भाग दोन्ही पुराव्यात येतात.",
			"The order follows the slope: the first point is where water "+
				"collects and the last is the high ground. That is what puts "+
				"both the wet part and the dry part into the evidence.",
		))
	} else {
		s.Notes = append(s.Notes, T(
			"या शेतात उंचसखलपणा नगण्य असल्याने ठिकाणे लांबीनुसार समान "+
				"अंतरावर घेतली आहेत. कोणतेही टोक दुसऱ्यापेक्षा सखल आहे असा "+
				"दावा येथे केलेला नाही.",
			"This parcel has too little relief for a slope to mean anything, "+
				"so the points are spaced along its length instead. No claim "+
				"is made that either end is lower than the other.",
		))
	}

	// What the coverage supports, said plainly and without overclaiming.
	switch {
	case covered == len(stations):
		s.Notes = append(s.Notes, Tf(
			"सर्व %d ठिकाणांसाठी पुरावा मिळाला. पुढील विभागातील पुरावा "+
				"शेताच्या वेगवेगळ्या भागांतून घेतलेला आहे, आणि कोणता भाग "+
				"तपासायचा हे शेतकऱ्याने ठरवलेले नाही.",
			"A recording was received for each of the %d points. The evidence "+
				"in the next section comes from separate parts of the parcel, "+
				"and which parts were sampled was not the farmer's choice.",
			len(stations)))
	case covered == 0:
		s.Notes = append(s.Notes, T(
			"एकाही ठिकाणासाठी पुरावा मिळालेला नाही.",
			"No recording was received for any of the points.",
		))
	default:
		s.Notes = append(s.Notes, Tf(
			"%d पैकी %d ठिकाणांहून पुरावा मिळाला. न मिळालेल्या ठिकाणचा भाग "+
				"तपासला गेलेला नाही, आणि तो भाग इतर भागांपेक्षा वेगळा असू शकतो.",
			"A recording was received for %d of %d points. The parts of the "+
				"parcel the missing points would have sampled have not been "+
				"examined, and may differ from those that were.",
			covered, len(stations)))
	}

	return s
}

// preamble governs how the whole table beneath it is read.
//
// It has one job: to stop a reader concluding, from a column headed "m from
// the point", that a farmer was supposed to be standing on the point. They
// were not. Saying what the walk actually required is both the honest
// statement and the stronger one, because it is the part a reader can check
// against the rows.
func preamble(spacingM float64) Text {
	if spacingM <= 0 {
		return T(
			"खालील ठिकाणे शेतकऱ्याने नव्हे तर प्रणालीने ठरवली, आणि पाहणी "+
				"सुरू होण्यापूर्वीच ठरवली. शेतकऱ्याने त्या ठिकाणी उभे "+
				"राहणे बंधनकारक नाही -- तो शेतात जिथे उभा आहे तिथून "+
				"चित्रीकरण करतो. अट एवढीच की प्रत्येक चित्रीकरण स्वतःच्या "+
				"शेताच्या हद्दीत असावे आणि आधीच्या ठिकाणांपासून पुरेसे "+
				"दूर असावे.",
			"The points below were decided by the system and not by the "+
				"farmer, and were decided before the walk began. The farmer "+
				"is not required to stand on them: they record from where "+
				"they are standing in the field. The only requirements are "+
				"that each recording be inside their own boundary and far "+
				"enough from the points already recorded.",
		)
	}
	return Tf(
		"खालील ठिकाणे शेतकऱ्याने नव्हे तर प्रणालीने ठरवली, आणि पाहणी सुरू "+
			"होण्यापूर्वीच ठरवली. शेतकऱ्याने त्या ठिकाणी उभे राहणे "+
			"बंधनकारक नाही -- तो शेतात जिथे उभा आहे तिथून चित्रीकरण करतो. "+
			"अट एवढीच की प्रत्येक चित्रीकरण स्वतःच्या शेताच्या हद्दीत असावे "+
			"आणि आधीच्या प्रत्येक ठिकाणापासून किमान %.0f मीटर दूर असावे. "+
			"त्यामुळे खालील अंतरे ही चूक नसून शेतकरी प्रत्यक्ष कुठे उभा "+
			"होता याची नोंद आहेत.",
		"The points below were decided by the system and not by the farmer, "+
			"and were decided before the walk began. The farmer is not "+
			"required to stand on them: they record from where they are "+
			"standing in the field. The only requirements are that each "+
			"recording be inside their own boundary and at least %.0f m from "+
			"every point already recorded. The distances below are therefore "+
			"a record of where the farmer actually stood, not a shortfall.",
		spacingM,
	)
}
