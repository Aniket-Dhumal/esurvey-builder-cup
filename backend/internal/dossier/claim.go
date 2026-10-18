package dossier

import (
	"github.com/agribid/esurvey-platform/backend/internal/agronomy"
	"github.com/agribid/esurvey-platform/backend/internal/claims"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Claim builds §1: what the farmer stated.
//
// Kept strictly separate from what was found. The two are compared throughout
// the rest of the document, and a form that merged them would leave nobody
// able to see where the claim and the assessment parted company -- which is
// the only thing an appeal is really about.
func Claim(r *report.Report, item *report.Item) Section {
	s := Section{
		Number: "1",
		Title:  T("दाखल केलेला दावा", "The claim as filed"),
		Preamble: T(
			"या विभागात शेतकऱ्याने स्वतः दिलेली माहिती आहे. पुढील विभागांत "+
				"आढळलेल्या गोष्टी यापासून वेगळ्या मांडल्या आहेत, म्हणजे दोन्ही "+
				"कुठे जुळतात आणि कुठे नाही हे स्पष्ट दिसते.",
			"This section is what the farmer stated. What was found is kept "+
				"separate in the sections that follow, so where the two agree and "+
				"where they part company can both be seen.",
		),
	}

	if r == nil {
		return s
	}

	s.Rows = append(s.Rows, Row{
		Parameter: T("कालामत व दिनांक", "Calamity and date"),
		Observed: Tf("%s · %s", "%s · %s",
			orDash(r.CalamityType, "—"),
			day(r.OccurredAt)),
		Source: T("शेतकऱ्याने नोंदवलेले", "As stated by the farmer"),
		Effect: T(
			"हवामान, उपग्रह आणि रडार या सर्वांची तपासणी याच तारखेभोवती केली "+
				"जाते. तारीख चुकीची असल्यास सर्व पुरावे चुकीच्या काळातले ठरतात.",
			"Every weather, satellite and radar check is made around this date. "+
				"If the date is wrong, all the evidence is drawn from the wrong "+
				"window.",
		),
		Role: Context,
	})

	if item != nil {
		s.Rows = append(s.Rows,
			Row{
				Parameter: T("पीक", "Crop"),
				Observed:  T(orDash(item.CropName, "—"), orDash(item.CropName, "—")),
				Source:    T("शेतकऱ्याने नोंदवलेले", "As stated by the farmer"),
				Effect: T(
					"पिकानुसार वाढीच्या अवस्था, उष्णता-बेरीज आणि कोणत्या अवस्थेत "+
						"कोणत्या कालामतीचा किती परिणाम होतो हे ठरते.",
					"The crop sets the growth stages, the thermal time, and which "+
						"calamity harms which stage and by how much.",
				),
				Role: Context,
			},
			Row{
				Parameter: T("पेरणी केलेले क्षेत्र", "Area sown"),
				Observed:  Tf("%.2f हेक्टर", "%.2f ha", item.AreaSownHa),
				Source:    T("शेतकऱ्याने नोंदवलेले", "As stated by the farmer"),
				Reference: Tf("सर्व्हे नं. %s", "Survey no. %s",
					orDash(item.SurveyNumber, "—")),
				Effect: T(
					"नुकसानभरपाईची गणना क्षेत्रावर होते, त्यामुळे हा आकडा थेट "+
						"रकमेवर परिणाम करतो. सातबारा उताऱ्यावरील क्षेत्राशी याची "+
						"तुलना केली जाते.",
					"Compensation is computed over area, so this figure bears "+
						"directly on the amount. It is checked against the extent "+
						"in the land record.",
				),
				Role: Context,
			},
			Row{
				Parameter: T("नुकसानग्रस्त क्षेत्र", "Area affected"),
				Observed:  Tf("%.2f हेक्टर", "%.2f ha", item.AreaAffectedHa),
				Source:    T("शेतकऱ्याने नोंदवलेले", "As stated by the farmer"),
				Reference: Tf("पेरणी केलेल्या %.2f हेक्टरपैकी",
					"of %.2f ha sown", item.AreaSownHa),
				Effect: T(
					"संपूर्ण शेतापैकी किती भाग बाधित झाला. संपूर्ण शेतात एकसारखे "+
						"नुकसान होत नाही, त्यामुळे हा आकडा टक्केवारीपेक्षा वेगळा आहे.",
					"How much of the parcel was affected. Damage is rarely uniform "+
						"across a field, which is why this is a separate figure from "+
						"the percentage.",
				),
				Role: Context,
			},
			Row{
				Parameter: T("शेतकऱ्याचा नुकसानीचा अंदाज", "Farmer's estimate of loss"),
				Observed:  Tf("%.0f%%", "%.0f%%", item.DamagePercent),
				Source:    T("शेतकऱ्याने नोंदवलेले", "As stated by the farmer"),
				Effect: T(
					"हा शेतकऱ्याचा अंदाज आहे आणि तो अंतिम आकडा नाही. शेतात उभे "+
						"राहून पाहणाऱ्या व्यक्तीचा अंदाज म्हणून त्याला महत्त्व आहे, "+
						"परंतु अंतिम टक्केवारी अधिकाऱ्यांच्या मोजणीवरून ठरते.",
					"This is the farmer's estimate and not the final figure. It "+
						"carries weight as the judgement of somebody standing in the "+
						"field, but the assessed percentage comes from the officers' "+
						"measurement.",
				),
				Role: Context,
			},
		)
	}

	if r.FiledLate {
		s.Rows = append(s.Rows, Row{
			Parameter: T("उशिरा दाखल", "Filed late"),
			Observed:  T("होय", "Yes"),
			Source:    T(orDash(r.LateReason, "—"), orDash(r.LateReason, "—")),
			Effect: T(
				"उशिरा दाखल केल्याने दावा आपोआप फेटाळला जात नाही. मात्र घटनेनंतर "+
					"जितका उशीर, तितके शेतातील पुरावे कमी होतात — पीक सावरते, "+
					"पाणी ओसरते, किंवा शेत साफ केले जाते.",
				"Filing late does not by itself defeat a claim. But the longer "+
					"after the event, the less the field can show: a crop recovers, "+
					"water drains, or the land is cleared.",
			),
			Role: Context,
		})
	}

	return s
}

// Parcel builds §2.
//
// The boundary provenance is the point of the section. AgriStack returns no
// geometry for any live farmer, so every area-derived figure in this document
// rests on a boundary somebody walked -- and that is stated once, here,
// plainly, rather than implied by its absence everywhere else.
func Parcel(item *report.Item, place report.Place, ownerExtentHa float64,
	boundary *BoundaryProvenance) Section {

	s := Section{
		Number: "2",
		Title:  T("क्षेत्र", "The parcel"),
	}

	if item != nil {
		s.Rows = append(s.Rows, Row{
			Parameter: T("सर्व्हे क्रमांक", "Survey number"),
			Observed:  T(orDash(item.SurveyNumber, "—"), orDash(item.SurveyNumber, "—")),
			Source: Tf("%s, %s, %s", "%s, %s, %s",
				orDash(place.VillageName, "—"),
				orDash(place.SubDistrictName, "—"),
				orDash(place.DistrictName, "—")),
			Role: Context,
		})
	}

	if ownerExtentHa > 0 && item != nil {
		s.Rows = append(s.Rows, Row{
			Parameter: T("मालकीचे क्षेत्र", "Owner's extent"),
			Observed:  Tf("%.2f हेक्टर", "%.2f ha", ownerExtentHa),
			Source:    T("भूमी अभिलेख", "Land record"),
			Reference: Tf("दाव्यात नोंदवलेले %.2f हेक्टर",
				"%.2f ha claimed", item.AreaSownHa),
			Effect: T(
				"सातबाऱ्यावरील क्षेत्र हे या मालकाचा वाटा आहे; संपूर्ण सर्व्हे "+
					"क्रमांकाचे क्षेत्र त्याहून मोठे असू शकते. या दोन आकड्यांची "+
					"गल्लत केल्यास भरपाई चुकीची निघते.",
				"The extent in the record is this owner's share; the whole survey "+
					"number may be larger. Conflating the two produces the wrong "+
					"compensation.",
			),
			Role: Context,
		})
	}

	s.Rows = append(s.Rows, boundaryRow(boundary))
	return s
}

// BoundaryProvenance is where the parcel's geometry came from.
type BoundaryProvenance struct {
	WalkedBy       string
	WalkedAt       string
	Corners        int
	WorstAccuracyM float64
	AreaHa         float64
}

func boundaryRow(b *BoundaryProvenance) Row {
	if b == nil {
		return AbsentRow(
			T("क्षेत्राची हद्द", "Parcel boundary"),
			T(
				"या क्षेत्राची हद्द नोंदवलेली नाही. AgriStack कडून कोणत्याही "+
					"शेतकऱ्यासाठी हद्दीची भूमिती मिळत नाही, आणि ती शेतात चालून "+
					"नोंदवलेलीही नाही — त्यामुळे उपग्रह व रडार विश्लेषण नेमक्या "+
					"क्षेत्रावर करता आलेले नाही.",
				"No boundary has been recorded for this parcel. AgriStack returns "+
					"no geometry for any farmer, and none was walked in the field, "+
					"so the satellite and radar analysis could not be confined to "+
					"the parcel itself.",
			),
			Context,
		)
	}

	r := Row{
		Parameter: T("क्षेत्राची हद्द", "Parcel boundary"),
		Observed: Tf("%d कोपरे, %.2f हेक्टर", "%d corners, %.2f ha",
			b.Corners, b.AreaHa),
		Source: Tf("%s यांनी %s रोजी शेतात चालून नोंदवली",
			"Walked in the field by %s on %s",
			orDash(b.WalkedBy, "—"), orDash(b.WalkedAt, "—")),
		Reference: Tf("सर्वात कमी अचूकता %.0f मी.",
			"worst GNSS accuracy %.0f m", b.WorstAccuracyM),
		Effect: T(
			"या हद्दीवरच उपग्रह व रडार विश्लेषण केले गेले आहे, आणि छायाचित्रे "+
				"याच हद्दीत घेतली गेली का हे तपासले गेले आहे. AgriStack कडून "+
				"भूमिती मिळत नसल्याने ही हद्द शेतात चालून नोंदवलेली आहे.",
			"The satellite and radar analysis is confined to this boundary, and "+
				"the photographs were checked against it. AgriStack supplies no "+
				"geometry, so this boundary was walked in the field.",
		),
		Role: Context,
	}

	// A GNSS fix good to thirty metres traces a boundary whose area can be
	// out by a great deal on a small parcel, and area is what compensation is
	// computed over.
	if b.WorstAccuracyM > 15 {
		r.Caveats = append(r.Caveats, Tf(
			"हद्द नोंदवताना उपग्रह स्थाननिश्चितीची अचूकता %.0f मीटरपर्यंत खाली "+
				"गेली होती. लहान क्षेत्रावर यामुळे क्षेत्रफळात लक्षणीय फरक पडू "+
				"शकतो, आणि भरपाई क्षेत्रावरच मोजली जाते.",
			"GNSS accuracy fell to %.0f m while the boundary was walked. On a "+
				"small parcel that can move the area appreciably, and compensation "+
				"is computed over area.",
			b.WorstAccuracyM,
		))
	}

	return r
}

// ContextNotes builds §10.
//
// Nothing here determines anything, and every note prints beside the ordinary
// explanation for it. claims.Signal makes Innocent a required field for
// exactly this reason: a flag on its own reads as an accusation, and this is
// the document where being accused by implication would be hardest to answer.
func ContextNotes(signals []claims.Signal) Section {
	s := Section{
		Number: "10",
		Title:  T("तपासणी नोंदी", "Context notes"),
	}

	if len(signals) == 0 {
		return s
	}

	s.Preamble = T(
		"खालील नोंदी केवळ लक्ष वेधण्यासाठी आहेत. यांपैकी कोणतीही नोंद नुकसानीचा "+
			"आकडा ठरवत नाही आणि कोणताही आरोप नाही. प्रत्येक नोंदीशेजारी तिचे "+
			"नेहमीचे, निर्दोष स्पष्टीकरण मुद्दाम दिलेले आहे.",
		"The notes below are for attention only. None of them determines the "+
			"loss figure and none is an allegation. Each is printed beside its "+
			"ordinary, innocent explanation, deliberately.",
	)

	for _, sig := range signals {
		s.Rows = append(s.Rows, Row{
			Parameter: T(sig.Code, sig.Code),
			Observed:  T(sig.Detail, sig.Detail),
			Source:    Tf("गांभीर्य: %s", "severity: %s", sig.Severity),
			// The innocent explanation is in the Effect column, not a
			// footnote. A reader who stops at the detail must already have
			// read the ordinary explanation for it.
			Effect: T(sig.Innocent, sig.Innocent),
			Role:   Context,
		})
	}

	return s
}

// CropAndStage builds §3.
//
// The section carrying the most explanatory weight in the document. Hail at
// flowering and hail at grain-fill are different events with different
// outcomes, and this is where the report says which one happened and why that
// matters.
func CropAndStage(th *agronomy.Thermal, sus *agronomy.Susceptibility,
	sow *agronomy.SowingCheck) Section {

	s := Section{
		Number: "3",
		Title:  T("पीक, पेरणी व अवस्था", "Crop, sowing and growth stage"),
	}

	s.Rows = append(s.Rows, sowingRow(sow))
	s.Rows = append(s.Rows, stageRow(th))
	s.Rows = append(s.Rows, susceptibilityRow(sus))
	return s
}

func sowingRow(c *agronomy.SowingCheck) Row {
	if c == nil {
		return AbsentRow(
			T("पेरणीची तारीख तपासणी", "Sowing date check"),
			T(
				"उपग्रह नोंदींतून पिकाची उगवण दिसली नाही, त्यामुळे नोंदवलेली "+
					"पेरणीची तारीख स्वतंत्रपणे तपासता आली नाही",
				"Green-up was not detectable in the satellite record, so the "+
					"declared sowing date could not be checked independently",
			),
			Corroborates,
		)
	}

	r := Row{
		Parameter: T("पेरणीची तारीख", "Sowing date"),
		Observed: Tf("नोंदवलेली %s", "declared %s",
			orDash(c.DeclaredDate, "—")),
		Source: Tf("उपग्रह नोंदींतून उगवण %s रोजी दिसली",
			"green-up observed in the satellite record on %s",
			orDash(c.ObservedDate, "—")),
		Reference: Tf("फरक %d दिवस", "%d days apart", c.DifferenceD),
		Role:      Corroborates,
	}

	if c.Agrees {
		r.Effect = T(
			"नोंदवलेली पेरणीची तारीख उपग्रह नोंदींशी जुळते. त्यामुळे पिकाच्या "+
				"अवस्थेची गणना विश्वासार्ह ठरते, आणि तीच अवस्था नुकसानीचा अर्थ "+
				"ठरवते.",
			"The declared sowing date agrees with the satellite record, which "+
				"makes the growth stage below reliable -- and the stage is what "+
				"gives the damage its meaning.",
		)
	} else {
		r.Effect = T(
			"नोंदवलेली तारीख आणि उपग्रहात दिसलेली उगवण यांत फरक आहे. उगवणीला "+
				"पाच दिवसांपासून तीन आठवड्यांपर्यंत वेळ लागू शकतो — कोरडवाहू पीक "+
				"पावसाची वाट पाहते — त्यामुळे हा फरक स्वतःहून चूक दर्शवत नाही. "+
				"मात्र यामुळे पिकाच्या अवस्थेची गणना कमी निश्चित होते.",
			"The declared date and the observed green-up differ. Emergence takes "+
				"anywhere from five days to three weeks -- a dryland crop waits "+
				"for rain -- so the difference is not by itself an error. It does "+
				"make the growth stage below less certain.",
		)
	}

	if c.Note != "" {
		r.Caveats = append(r.Caveats, T(c.Note, c.Note))
	}
	if c.Confidence != "" {
		r.Working = append(r.Working, Tf("विश्वासार्हता: %s", "confidence: %s",
			c.Confidence))
	}

	return r
}

func stageRow(th *agronomy.Thermal) Row {
	if th == nil {
		return AbsentRow(
			T("घटनेच्या वेळची पिकाची अवस्था", "Growth stage at the event"),
			T(
				"पेरणीची तारीख किंवा हवामान नोंद नसल्याने अवस्था काढता आली नाही",
				"The stage could not be derived without a sowing date or a "+
					"weather record",
			),
			Corroborates,
		)
	}

	r := Row{
		Parameter: T("घटनेच्या वेळची पिकाची अवस्था", "Growth stage at the event"),
		Observed:  T(orDash(th.StageMr, th.Stage), orDash(th.StageEn, th.Stage)),
		Source: T(
			"उष्णता-बेरजेवरून (GDD), दिवस मोजून नाही",
			"From accumulated thermal time (GDD), not calendar days",
		),
		Reference: Tf("जीवनचक्राच्या %.0f%% पूर्ण", "%.0f%% through its cycle",
			th.PercentOfCycle),
		Effect: T(
			"नुकसानीचा अर्थ अवस्थेवरून ठरतो. फुलोऱ्यातील गारपीट आणि दाणे "+
				"भरतानाची गारपीट या वेगळ्या घटना आहेत — पहिलीत पीक पुन्हा "+
				"फुलू शकते, दुसरीत गमावलेले दाणे परत येत नाहीत.",
			"The stage is what gives the damage its meaning. Hail at flowering "+
				"and hail at grain-fill are different events: the first can be "+
				"flowered again, the second cannot be recovered.",
		),
		Role: Corroborates,
		Working: Texts{Tf(
			"जमा उष्णता-बेरीज %.0f, पक्वतेसाठी %.0f = %.0f%%",
			"accumulated GDD %.0f of %.0f to maturity = %.0f%%",
			th.AccumulatedGDD, th.MaturityGDD, th.PercentOfCycle,
		)},
	}

	// The assumed-duration flag travels with the figure so nobody reads an
	// assumed class as a stated one.
	if th.DurationAssumed {
		r.Caveats = append(r.Caveats, Tf(
			"वाणाचा कालावधी नोंदवलेला नव्हता, त्यामुळे %s या मधल्या वर्गाचा "+
				"आधार घेतला आहे. लवकर किंवा उशिरा येणाऱ्या वाणासाठी वरील अवस्था "+
				"एका पायरीने बदलू शकते.",
			"The variety's duration was not declared, so the middle class (%s) "+
				"was assumed. For an early or late variety the stage above could "+
				"move by one step.",
			orDash(th.Duration, "—"),
		))
	}

	return r
}

func susceptibilityRow(s *agronomy.Susceptibility) Row {
	if s == nil {
		return AbsentRow(
			T("या अवस्थेतील संवेदनशीलता", "Susceptibility at this stage"),
			T(
				"या पीक-अवस्था-कालामत जोडणीसाठी नोंद उपलब्ध नाही",
				"No entry exists for this crop, stage and calamity together",
			),
			Corroborates,
		)
	}

	r := Row{
		Parameter: T("या अवस्थेतील संवेदनशीलता", "Susceptibility at this stage"),
		Observed:  T(s.Level, s.Level),
		Source:    T(orDash(s.Provenance, "—"), orDash(s.Provenance, "—")),
		Reference: Tf("%s, %s अवस्थेत", "%s, at the %s stage",
			orDash(s.Calamity, "—"), orDash(s.Stage, "—")),
		Effect: T(s.Reason, s.Reason),
		Role:   Corroborates,
	}

	// Whether the crop can regrow is a different assessment from how badly it
	// was hit, and the two are routinely conflated.
	if s.Recoverable {
		r.Working = append(r.Working, T(
			"या अवस्थेत पीक पुन्हा वाढू शकते. त्यामुळे घटनेनंतर लगेच दिसणारे "+
				"नुकसान आणि हंगामाअखेर प्रत्यक्ष उत्पादनातील घट या वेगळ्या "+
				"असू शकतात.",
			"The crop can regrow from this stage, so the damage visible just "+
				"after the event and the shortfall at harvest may differ.",
		))
	} else {
		r.Working = append(r.Working, T(
			"या अवस्थेतून पीक पुन्हा वाढू शकत नाही. या टप्प्यावरील नुकसान "+
				"कायमचे आहे.",
			"The crop cannot regrow from this stage. Damage here is permanent.",
		))
	}

	return r
}
