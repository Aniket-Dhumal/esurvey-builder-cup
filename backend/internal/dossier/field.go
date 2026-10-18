package dossier

import (
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
)

// FieldMeasurement builds §4, the plant counts.
//
// # Where these counts come from
//
// Not from a frame laid in a field: nobody visits. They are read off the
// farmer's own photographs -- by the vision model, or by an officer counting
// what the model reported.
//
// That makes them weaker than a quadrat and still the most objective figure
// in the document, for one reason: an appellate officer can open the same
// photograph and count again. Every other section is an instrument reading
// from orbit or a model of the weather, and none of those can be re-checked
// by looking.
//
// # Why the arithmetic is re-phrased and never re-computed
//
// Every number below comes from fieldwork.Derive. This function formats those
// values into a bilingual sentence and derives nothing of its own -- a second
// implementation of the same arithmetic is a second thing to keep correct,
// and the day the two disagree the report contradicts the assessment it is
// reporting on.
func FieldMeasurement(ms []fieldwork.Measurement, c Case) Section {
	s := Section{
		Number: "4",
		Title:  T("शेतातील प्रत्यक्ष मोजणी", "Field measurement"),
	}

	if len(ms) == 0 {
		// Said in the section rather than by omitting it. A missing §4 would
		// read as a formatting gap; this reads as what it is, and it changes
		// how the whole document should be weighed.
		// The ordinary state, not a lapse.
		//
		// Counting plants by hand was officer data entry, and it is no longer
		// asked for: the vision pipeline measures the clips (§7a) and the
		// panel records the findings (§11), and the officer confirms that or
		// overrides it with a reason. So this says what the figure does rest
		// on rather than dwelling on what it does not -- a sentence that read
		// as a deficiency would now appear on every report ever printed.
		s.Preamble = T(
			"रोपांची हातमोजणी या प्रणालीत मागितली जात नाही. नुकसानीचा आकडा "+
				"चित्रीकरणाच्या मोजमापावर (विभाग ७अ) आणि प्रणालीच्या "+
				"निष्कर्षांवर (विभाग ११) आधारित आहे; अधिकारी तो आकडा "+
				"कारणासह मान्य करतात किंवा बदलतात.",
			"A hand count of plants is not asked for in this system. The loss "+
				"figure rests on the measurement of the survey video (§7a) and "+
				"the panel's findings (§11), which an officer then confirms or "+
				"overrides with a reason.",
		)
		s.Rows = append(s.Rows, AbsentRow(
			T("रोपांची हातमोजणी", "Hand count of plants"),
			T("या प्रणालीत मागितली जात नाही", "Not asked for in this system"),
			Context,
		))
		return s
	}

	s.Preamble = T(
		"हे आकडे तुम्ही पाठवलेल्या छायाचित्रांवरून मोजलेले आहेत, शेतात जाऊन "+
			"नाही. तेच आकडे त्याच छायाचित्रांवरून पुन्हा मोजून तपासता येतात.",
		"These counts were read from the photographs the farmer sent, not from "+
			"a frame laid in a field. They can be checked by counting again "+
			"from the same photographs.",
	)

	for i, m := range ms {
		d := fieldwork.Derive(&m)
		s.Rows = append(s.Rows, measurementRows(i, len(ms), m, d, c)...)
	}

	return s
}

// measurementRows renders one quadrat.
func measurementRows(i, total int, m fieldwork.Measurement, d fieldwork.Derived, c Case) []Row {
	// Numbered only when there is more than one, because "Quadrat 1 of 1" is
	// noise on the common case.
	name := func(mr, en string) Text {
		if total <= 1 {
			return T(mr, en)
		}
		return Tf(mr+" (चौकट %d)", en+" (quadrat %d)", i+1)
	}

	source := Tf(
		"%s यांनी %s रोजी छायाचित्रावरून नोंदवले · %.1f मी. चौकट (%.1f चौ.मी.)",
		"Read from the photograph by %s on %s · %.1f m frame (%.1f m²)",
		orDash(m.RecordedName, m.RecordedBy), m.RecordedAt,
		m.QuadratSideM, m.AreaM2(),
	)

	evidence := T("—", "—")
	if m.CaptureID != "" {
		evidence = Tf("छायाचित्र %s", "Photograph %s", m.CaptureID)
	}

	var rows []Row

	if d.StandLossPercent != nil && m.PlantsExpected != nil && m.PlantsPresent != nil {
		rows = append(rows, Row{
			Parameter: name("रोपांची घट", "Stand loss"),
			Observed:  Tf("%.0f%%", "%.0f%%", *d.StandLossPercent),
			Source:    source,
			Reference: Tf(
				"अपेक्षित %d रोपे (लागवडीच्या अंतरानुसार)",
				"%d plants expected at this spacing",
				*m.PlantsExpected,
			),
			Effect: T(
				"गहाळ झालेली रोपे हे उपलब्ध असलेले सर्वात वस्तुनिष्ठ माप आहे आणि "+
					"नुकसानीचा आकडा मुख्यतः यावर ठरतो. त्याच छायाचित्रावरून हा "+
					"आकडा पुन्हा मोजून तपासता येतो.",
				"Missing plants are the most objective measure of loss available "+
					"without a field visit, and the loss figure rests principally "+
					"on this. An appeal can check it by counting again from the "+
					"same photograph.",
			),
			Evidence: evidence,
			Role:     Determines,
			Working: Texts{Tf(
				"रोपांची घट: अपेक्षित %d वजा उपस्थित %d, भागिले अपेक्षित %d = %.0f%%",
				"stand loss: %d expected less %d present, over %d expected = %.0f%%",
				*m.PlantsExpected, *m.PlantsPresent, *m.PlantsExpected,
				*d.StandLossPercent,
			)},
		})
	}

	if d.LodgedPercent != nil && m.PlantsLodged != nil && m.PlantsPresent != nil {
		rows = append(rows, Row{
			Parameter: name("आडवी पडलेली रोपे", "Lodged plants"),
			Observed:  Tf("%.0f%%", "%.0f%%", *d.LodgedPercent),
			Source:    source,
			Reference: T("उपस्थित रोपांपैकी", "Of the plants present"),
			Effect:    lodgingEffect(*d.LodgedPercent, c),
			Evidence:  evidence,
			Role:      Determines,
			Working: Texts{Tf(
				"आडवी पडलेली: %d पैकी %d उपस्थित रोपे = %.0f%%",
				"lodged: %d of %d plants present = %.0f%%",
				*m.PlantsLodged, *m.PlantsPresent, *d.LodgedPercent,
			)},
		})
	}

	if d.OrganDamagePercent != nil && m.OrganSampled != nil && m.OrganDamaged != nil {
		rows = append(rows, Row{
			Parameter: name("दाणे / शेंगांचे नुकसान", "Harvested organ damage"),
			Observed:  Tf("%.0f%%", "%.0f%%", *d.OrganDamagePercent),
			Source:    source,
			Reference: Tf(
				"तपासलेल्या %d नमुन्यांपैकी",
				"of %d sampled",
				*m.OrganSampled,
			),
			Effect: T(
				"उभे राहिलेले पीकही उत्पादन देऊ शकत नाही. रोपे शाबूत असताना "+
					"शेंगा किंवा दाणे खराब झाले असतील, तर रोपांच्या घटीवरून दिसणारे "+
					"नुकसान प्रत्यक्षापेक्षा कमी भासते.",
				"A standing crop can still fail to yield. Where plants survive "+
					"but pods or grain are damaged, stand loss alone understates "+
					"what was actually lost.",
			),
			Evidence: evidence,
			Role:     Determines,
			Working: Texts{Tf(
				"नुकसान: %d पैकी %d नमुने = %.0f%%",
				"organ damage: %d of %d sampled = %.0f%%",
				*m.OrganDamaged, *m.OrganSampled, *d.OrganDamagePercent,
			)},
		})
	}

	if d.PlantsPerM2 != nil {
		rows = append(rows, Row{
			Parameter: name("रोपांची घनता", "Plant density"),
			Observed:  Tf("%.1f / चौ.मी.", "%.1f per m²", *d.PlantsPerM2),
			Source:    source,
			Reference: T("—", "—"),
			Effect: T(
				"वेगवेगळ्या आकाराच्या चौकटींतील मोजण्या एकमेकांशी तुलना करता "+
					"याव्यात म्हणून. तसेच अपेक्षित रोपसंख्या लागवडीच्या अंतराशी "+
					"जुळते का हे यावरून तपासता येते.",
				"Lets one quadrat be compared with another taken over a different "+
					"frame, and lets the expected plant count be checked against "+
					"the spacing it was derived from.",
			),
			Evidence: evidence,
			Role:     Context,
		})
	}

	if m.SoilType != "" {
		rows = append(rows, Row{
			Parameter: name("जमिनीचा प्रकार", "Soil type"),
			Observed:  T(m.SoilType, m.SoilType),
			Source:    source,
			// "Context, not a measure of loss" used to follow this, which the
			// Role column beside it already says. A reader told four times in
			// one page what a figure is not stops reading the column that also
			// tells them what it is.
			Effect: T(
				"भारी काळ्या जमिनीत पाणी अधिक काळ साचते आणि पीक सावरायला वेळ "+
					"लागतो; हलक्या जमिनीत पाणी लवकर निघून जाते. त्यामुळे तेवढाच "+
					"पाऊस झाला तरी जमिनीच्या प्रकारानुसार परिणाम वेगळा असतो.",
				"Heavy black soil holds water for longer and the crop takes "+
					"longer to recover; light soil drains. The same rainfall "+
					"therefore does different things on different soils, which is "+
					"what makes this worth recording beside the rain.",
			),
			Role: Context,
		})
	}

	// fieldwork's own caveats, which are what the counts cannot support.
	// Carried through rather than summarised: the one about spacing is the
	// difference between a real loss and a mis-set expectation.
	if len(rows) > 0 && len(d.Caveats) > 0 {
		for _, c := range d.Caveats {
			rows[0].Caveats = append(rows[0].Caveats, T(c, c))
		}
	}

	if m.Notes != "" {
		rows = append(rows, Row{
			Parameter: name("अधिकाऱ्याची टिप्पणी", "Officer's note"),
			Observed:  T(m.Notes, m.Notes),
			Source:    source,
			Role:      Context,
		})
	}

	return rows
}

func orDash(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	if fallback != "" {
		return fallback
	}
	return "—"
}

// lodgingEffect reads the lodged fraction against what was claimed.
//
// Lodging is the signature of wind and hail. On a drought claim it is evidence
// of something the farmer did not allege, and an officer needs to be told that
// rather than handed the same sentence about double-counting.
func lodgingEffect(pct float64, c Case) Text {
	// The arithmetic rule holds whatever was claimed, and is the second half of
	// every branch below rather than the first.
	rule := Text{
		MR: " ही टक्केवारी रोपांच्या घटीत मिळवली जात नाही: तसे केल्यास एकाच " +
			"रोपाचे नुकसान दोनदा मोजले जाईल.",
		EN: " This percentage is kept separate from stand loss rather than added " +
			"to it, which would count the same plant's loss twice.",
	}

	var head Text
	switch {
	case c.Wind():
		head = Tf(
			"उपस्थित रोपांपैकी %.0f%% आडवी पडली आहेत. नोंदवलेली घटना पीक आडवे "+
				"पाडणारीच आहे, म्हणून हा आकडा दाव्याचा थेट पुरावा आहे. आडवी "+
				"पडलेली रोपे उत्पादन कमी करतात — दाणे भरत नाहीत, काढणी अवघड "+
				"होते — पण ती जागेवर उभी असतात.",
			"%.0f%% of the plants present are lodged. The event claimed is one "+
				"that flattens crops, so this is direct evidence of it. Lodged "+
				"plants yield less -- grain fills poorly and harvesting is harder "+
				"-- but they are still standing.",
			pct)

	case c.Drought():
		head = Tf(
			"उपस्थित रोपांपैकी %.0f%% आडवी पडली आहेत. नोंदवलेला दावा "+
				"दुष्काळाचा आहे आणि पाण्याच्या ताणाने पीक आडवे पडत नाही — ते "+
				"वाळते. म्हणजे या शेतावर वारा, पाऊस किंवा जास्त वाढ यांपैकी "+
				"काहीतरी घडले आहे जे दाव्यात नोंदवलेले नाही. हे शेतकऱ्याविरुद्ध "+
				"नाही: नुकसान दाव्यापेक्षा वेगळ्या कारणाने झाले असू शकते, आणि "+
				"तेही भरपाईस पात्र असू शकते.",
			"%.0f%% of the plants present are lodged. The claim is of drought, "+
				"and water stress does not flatten a crop -- it withers it. So "+
				"something happened on this parcel that the claim does not "+
				"describe: wind, rain, or rank growth. This is not against the "+
				"farmer: the loss may be real and caused by something other than "+
				"what was filed, which may itself be compensable.",
			pct)

	case c.Water():
		head = Tf(
			"उपस्थित रोपांपैकी %.0f%% आडवी पडली आहेत. साचलेले पाणी व त्यासोबतचा "+
				"पाऊस पीक आडवे पाडतात, म्हणून नोंदवलेल्या घटनेशी हे सुसंगत आहे. "+
				"आडवी पडलेली रोपे उत्पादन कमी करतात, पण ती जागेवर उभी असतात.",
			"%.0f%% of the plants present are lodged. Standing water and the rain "+
				"that brought it both flatten crops, so this is consistent with "+
				"the event claimed. Lodged plants yield less but are still "+
				"standing.",
			pct)

	default:
		head = Tf(
			"उपस्थित रोपांपैकी %.0f%% आडवी पडली आहेत. आडवी पडलेली रोपे उत्पादन "+
				"कमी करतात — दाणे भरत नाहीत, काढणी अवघड होते — पण ती जागेवर "+
				"उभी असतात.",
			"%.0f%% of the plants present are lodged. Lodged plants yield less "+
				"-- grain fills poorly and harvesting is harder -- but they are "+
				"still standing.",
			pct)
	}

	return Text{MR: head.MR + rule.MR, EN: head.EN + rule.EN}
}
