package dossier

import "github.com/agribid/esurvey-platform/backend/internal/baseline"

// DenominatorOf answers "a percentage of what?".
//
// # Why this runs before any other section is built
//
// docs/LOSS_PARAMETERS.md opens on the problem it solves: damage_percent is a
// percentage of nothing defined. Sixty per cent off a crop already failing in
// a dry August is a different fact from sixty per cent off a crop heading for
// its best year, and no quantity of NDVI tables separates the two.
//
// An issued threshold yield is the only figure here in yield units, so it is
// the only true denominator. The parcel's own history and its neighbourhood
// are in NDVI, which is a proxy for canopy and not for yield -- they say
// whether the loss is real and unusual, not what it is a percentage of. The
// distinction is kept because collapsing it would let a satellite proxy be
// read as a shortfall against threshold yield, which is what
// baseline/threshold.go warns against in its own header.
func DenominatorOf(d *baseline.Denominators) Denominator {
	if d == nil || d.Yield == nil {
		return Denominator{
			Known: false,
			AbsentWhy: T(
				"या पिकासाठी व हंगामासाठी शासनाने संदर्भ उत्पादन (threshold yield) "+
					"जाहीर केलेले आढळले नाही. त्यामुळे नुकसानीची टक्केवारी कोणत्या "+
					"उत्पादनाशी तुलना करून काढली, हे या अहवालात नमूद करता येत नाही; "+
					"खालील आकडा हा अधिकाऱ्यांनी नोंदवलेला निर्णय आहे, मोजमाप नाही.",
				"No threshold yield was found as issued for this crop, season and "+
					"unit. The loss percentage therefore has no stated yield to be "+
					"a percentage of, and the figure below is a judgement recorded "+
					"by named officers rather than a measurement.",
			),
		}
	}

	y := d.Yield
	return Denominator{
		Known:          true,
		ThresholdYield: y.YieldKgPerHa,
		Unit:           T("किलो / हेक्टर", "kg per hectare"),
		Basis: Tf(
			"%s — %s यांनी %s साठी जाहीर केलेले (%s)",
			"%s — issued by %s for %s (%s)",
			orDash(y.Basis, "आधार नोंदवलेला नाही"), orDash(y.Source, "—"),
			orDash(y.UnitName, "—"), orDash(y.IssuedAt, "—"),
		),
		Working: Texts{Tf(
			"संदर्भ उत्पादन %.0f किलो/हेक्टर, %s पातळीवरील %s या घटकासाठी, "+
				"%s हंगाम %s",
			"threshold yield %.0f kg/ha, for %s at %s level, %s season %s",
			y.YieldKgPerHa, orDash(y.UnitName, "—"), orDash(y.UnitLevel, "—"),
			orDash(y.Season, "—"), orDash(y.CropYear, "—"),
		)},
	}
}

// Baseline builds §9: what this parcel normally does, and what the ground
// around it did.
func Baseline(d *baseline.Denominators) Section {
	s := Section{
		Number: "9",
		Title:  T("संदर्भ व इतिहास", "Baseline and history"),
	}

	if d == nil || !d.Grounded() {
		s.Preamble = T(
			"या मूल्यांकनाला कोणताही संदर्भ आधार नाही: ही टक्केवारी या जमिनीच्या "+
				"नेहमीच्या उत्पादनाशी, आजूबाजूच्या शेतांशी, किंवा जाहीर संदर्भ "+
				"उत्पादनाशी तुलना करून काढलेली नाही. हा अहवाल तसे स्पष्टपणे "+
				"नमूद करतो, कारण त्याशिवाय आकडा मोजमाप वाटतो.",
			"This assessment has no baseline: the percentage is not measured "+
				"against what this land normally does, what the surrounding "+
				"fields did, or an issued threshold yield. The report says so "+
				"plainly, because without that the figure reads as a measurement.",
		)
	}

	if d == nil {
		return s
	}

	s.Rows = append(s.Rows, thresholdRow(d.Yield))
	s.Rows = append(s.Rows, historyRow(d.History))
	s.Rows = append(s.Rows, peerRow(d.Peer))

	// baseline names each denominator it could not obtain. Carried through
	// verbatim: an assessment with all three absent is a judgement and should
	// be read as one, and that is exactly what these lines say.
	for _, a := range d.Absent {
		s.Notes = append(s.Notes, T(a, a))
	}

	return s
}

func thresholdRow(y *baseline.ThresholdYield) Row {
	if y == nil {
		return AbsentRow(
			T("जाहीर संदर्भ उत्पादन", "Issued threshold yield"),
			T(
				"या पीक, हंगाम व घटकासाठी जाहीर आकडा आढळला नाही",
				"No figure found as issued for this crop, season and unit",
			),
			Determines,
		)
	}

	return Row{
		Parameter: T("जाहीर संदर्भ उत्पादन", "Issued threshold yield"),
		Observed:  Tf("%.0f किलो/हेक्टर", "%.0f kg/ha", y.YieldKgPerHa),
		Source: Tf("%s · %s", "%s · %s",
			orDash(y.Source, "—"), orDash(y.Basis, "—")),
		Reference: Tf("%s (%s)", "%s (%s)",
			orDash(y.UnitName, "—"), orDash(y.UnitLevel, "—")),
		Effect: T(
			"नुकसानीची टक्केवारी याच आकड्याशी तुलना करून काढली जाते. हा "+
				"अहवालातील एकमेव आकडा आहे जो उत्पादनाच्या एककात आहे.",
			"The loss percentage is expressed against this figure. It is the "+
				"only number in this report in yield units.",
		),
		Evidence: Tf("जाहीर दिनांक %s", "Issued %s", orDash(y.IssuedAt, "—")),
		Role:     Determines,
	}
}

func historyRow(h *baseline.History) Row {
	if h == nil {
		return AbsentRow(
			T("या क्षेत्राचा स्वतःचा इतिहास", "This parcel's own history"),
			T(
				"मागील हंगामांची उपग्रह नोंद मिळाली नाही",
				"No prior-season satellite history was obtained",
			),
			Corroborates,
		)
	}

	observed := Tf(
		"यंदाचे शिखर NDVI %.2f",
		"peak NDVI this season %.2f",
		h.ThisSeasonPeak,
	)

	reference := T("तुलनेसाठी पुरेसे हंगाम नाहीत", "Too few prior seasons to compare")
	if len(h.Seasons) > 0 {
		reference = Tf(
			"मागील %d हंगामांचे सरासरी शिखर %.2f (मानक विचलन %.2f)",
			"mean peak over %d prior seasons %.2f (sd %.2f)",
			len(h.Seasons), h.MeanPeak, h.StdDev,
		)
	}

	r := Row{
		Parameter: T("या क्षेत्राचा स्वतःचा इतिहास", "This parcel's own history"),
		Observed:  observed,
		Source: Tf(
			"Sentinel-2 NDVI · %s विश्वासार्हता",
			"Sentinel-2 NDVI · %s confidence",
			orDash(h.Confidence, "—"),
		),
		Reference: reference,
		Effect:    historyEffect(h),
		Evidence:  Tf("%d मागील हंगाम", "%d prior seasons", len(h.Seasons)),
		Role:      Corroborates,
	}

	// The z-score is nil when too few seasons came back to mean anything,
	// which baseline calls the honest answer rather than zero. Printed only
	// when it exists, for the same reason.
	if h.ZScore != nil {
		r.Working = append(r.Working, Tf(
			"z-मूल्य %.2f: यंदाचे शिखर सरासरीपासून किती मानक विचलन दूर आहे",
			"z-score %.2f: how many standard deviations this season's peak sits "+
				"from the mean",
			*h.ZScore,
		))
	}
	for _, c := range h.Caveats {
		r.Caveats = append(r.Caveats, T(c, c))
	}

	return r
}

func historyEffect(h *baseline.History) Text {
	switch h.Verdict {
	case baseline.VerdictBelowNormal:
		return T(
			"या हंगामात या शेतातील पीक नेहमीपेक्षा कमी वाढले. हे दाव्याला पुष्टी "+
				"देते, परंतु नुकसान कशामुळे झाले हे सांगत नाही — कमी वाढ "+
				"कालामतीमुळे असू शकते, तशीच उशिरा पेरणी किंवा पाण्याच्या कमतरतेमुळेही.",
			"The canopy on this parcel stayed below what it normally reaches. "+
				"That corroborates the claim without explaining it: a poor canopy "+
				"can come from the calamity, and equally from late sowing or a "+
				"shortage of water.",
		)
	case baseline.VerdictAboveNormal:
		return T(
			"या हंगामात या शेतातील पीक नेहमीपेक्षा चांगले वाढले होते. त्यामुळे "+
				"घटनेपूर्वी पीक चांगल्या स्थितीत होते असे दिसते; नुकसान त्या "+
				"स्थितीवरून झालेले आहे.",
			"The canopy this season was better than this parcel normally "+
				"reaches, so the crop was in good condition before the event and "+
				"the loss is measured from there.",
		)
	case baseline.VerdictNormal:
		return T(
			"या हंगामातील वाढ या शेताच्या नेहमीच्या पातळीवर होती.",
			"Growth this season was within what this parcel normally does.",
		)
	default:
		return T(
			"पुरेशी मागील नोंद नसल्याने या शेताच्या नेहमीच्या पातळीशी तुलना "+
				"करता आलेली नाही.",
			"Too little prior record to compare against what this parcel "+
				"normally does.",
		)
	}
}

// minPeerObservations is the floor below which the neighbourhood comparison is
// suppressed rather than printed.
//
// The neighbourhood is a grid cell, not a list of parcels, so nothing here
// identifies a neighbour -- there are no survey numbers in the analysis to
// disclose. The floor is about confidence, not privacy: a fall computed from
// two cloud-free passes is a number with the shape of evidence and none of the
// substance, and printing it beside a farmer's compensation invites it to be
// argued over.
const minPeerObservations = 4

func peerRow(p *baseline.Peer) Row {
	if p == nil {
		return AbsentRow(
			T("आजूबाजूच्या शेतांशी तुलना", "Comparison with surrounding fields"),
			T(
				"शेजारच्या क्षेत्राची उपग्रह नोंद मिळाली नाही",
				"No neighbourhood satellite reading was obtained",
			),
			Corroborates,
		)
	}

	if p.Observations < minPeerObservations {
		return AbsentRow(
			T("आजूबाजूच्या शेतांशी तुलना", "Comparison with surrounding fields"),
			Tf(
				"केवळ %d ढगमुक्त निरीक्षणे मिळाली; इतक्या कमी नोंदींवरून तुलना "+
					"करणे दिशाभूल करणारे ठरेल",
				"only %d cloud-free observations; a comparison drawn from so few "+
					"would mislead",
				p.Observations,
			),
			Corroborates,
		)
	}

	return Row{
		Parameter: T("आजूबाजूच्या शेतांशी तुलना", "Comparison with surrounding fields"),
		Observed: Tf(
			"शेजारच्या भागातील घट %.0f%% · या शेतातील घट %.0f%%",
			"neighbourhood fell %.0f%% · this parcel fell %.0f%%",
			p.NeighbourhoodRRVL, p.ParcelRRVL,
		),
		Source: Tf(
			"Sentinel-2 NDVI, %d मी. परिसर · %d ढगमुक्त निरीक्षणे",
			"Sentinel-2 NDVI over a %d m neighbourhood · %d cloud-free observations",
			p.RadiusM, p.Observations,
		),
		Reference: Tf(
			"शेजारच्या भागाचे शिखर %.2f, घटनेच्या वेळी %.2f",
			"neighbourhood peak %.2f, at incident %.2f",
			p.NeighbourhoodPeak, p.NeighbourhoodIncident,
		),
		Effect:   peerEffect(p),
		Evidence: Tf("विश्लेषण घटक %s", "Analysis cell %s", orDash(p.GridCell, "—")),
		Role:     Corroborates,
		Working: Texts{Tf(
			"सापेक्ष फरक %.1f टक्के गुण: या शेताची घट वजा शेजारच्या भागाची घट",
			"relative anomaly %.1f points: this parcel's fall less the "+
				"neighbourhood's",
			p.RelativeAnomaly,
		)},
		Caveats: peerCaveats(p),
	}
}

func peerEffect(p *baseline.Peer) Text {
	switch p.Verdict {
	case baseline.PeerRegional:
		return T(
			"हे शेत आणि आजूबाजूचा संपूर्ण भाग दोन्ही घटले. ज्या जमिनीवर "+
				"दावेदाराचे नियंत्रण नाही, तिथूनही तीच घट दिसते — त्यामुळे "+
				"जाहीर केलेल्या कालामतीला स्वतंत्र पुष्टी मिळते.",
			"This parcel and the ground around it both fell. The same fall "+
				"appears on land the claimant does not control, which "+
				"independently corroborates the calamity as declared.",
		)
	case baseline.PeerLocal:
		return T(
			"या शेतात घट दिसते, शेजारी दिसत नाही. हे खऱ्या स्थानिक नुकसानाशी "+
				"सुसंगत आहे, आणि लवकर काढणी केल्यासही तेच दिसते. हा तपासण्याचा "+
				"मुद्दा आहे, निष्कर्ष नाही.",
			"This parcel fell and its neighbours did not. That is consistent "+
				"with a genuine localised loss, and equally with an early "+
				"harvest. It is a reason to look, not a finding.",
		)
	case baseline.PeerContradicted:
		return T(
			"आजूबाजूच्या भागात घट दिसते, या शेतात दिसत नाही. उपग्रह नोंदीत या "+
				"शेतावर नुकसान आढळलेले नाही; शेतातील मोजणी आणि अधिकाऱ्यांचे "+
				"निरीक्षण याबाबत निर्णायक आहेत.",
			"The surrounding ground fell and this parcel did not. The satellite "+
				"record shows no loss here; the quadrat count and the officers' "+
				"findings are what decide the matter.",
		)
	case baseline.PeerNoLoss:
		return T(
			"या शेतात किंवा आजूबाजूला उपग्रह नोंदीत कोणतीही घट आढळली नाही.",
			"No fall is visible in the satellite record, on this parcel or "+
				"around it.",
		)
	default:
		return T(
			"तुलना निर्णायक ठरली नाही.",
			"The comparison was inconclusive.",
		)
	}
}

func peerCaveats(p *baseline.Peer) Texts {
	var out Texts
	for _, c := range p.Caveats {
		out = append(out, T(c, c))
	}
	if p.Reason != "" {
		out = append(out, T(p.Reason, p.Reason))
	}
	return out
}
