package dossier

// The sections that close the document: the model's opinion, the officers who
// decided, the evidence anybody can check, what the report cannot say, and
// how to object to it.

// AIAssessment is what the model concluded, and on what.
type AIAssessment struct {
	LossPercent   *float64
	Reasoning     string
	Citations     []string
	Model         string
	PromptVersion string
	RanAt         string
}

// Assessment builds §11.
//
// # Why this determines rather than corroborates
//
// It used to be marked Corroborates, on the reasoning that officers decided
// and the model merely agreed or did not. That was true when an officer stood
// in the field: they had something the model did not.
//
// Nobody visits now. The officers read the same photographs the model read,
// so the model's analysis is not a second opinion on an independent
// measurement -- it is part of how the figure was arrived at, and the marker
// has to say so. Pretending otherwise would let the document claim a human
// judgement was reached without the machine's help.
//
// Printed in the farmer's copy in full, and that matters more under this
// model than it did before: withholding it invites the belief that a machine
// decided in secret, which is now closer to half-true and therefore the
// accusation this platform is most exposed to.
func Assessment(a *AIAssessment, decidedBy string) Section {
	s := Section{
		Number: "11",
		Title:  T("संगणकीय मूल्यांकन", "Computer assessment"),
	}

	if a == nil {
		s.Rows = append(s.Rows, AbsentRow(
			T("संगणकीय मूल्यांकन", "Computer assessment"),
			T("या दाव्यावर संगणकीय मूल्यांकन चालवले गेले नाही",
				"No computer assessment was run on this claim"),
			Corroborates,
		))
		return s
	}

	s.Preamble = T(
		"खालील निष्कर्ष संगणक प्रणालीने तुमच्या छायाचित्रांवरून काढलेला आहे. "+
			"अधिकाऱ्यांनीही तीच छायाचित्रे पाहिली आहेत, त्यामुळे हा निष्कर्ष "+
			"मूल्यांकनाचा भाग आहे. अंतिम निर्णय मात्र अधिकाऱ्यांचाच असतो. "+
			"प्रणालीने कोणत्या आधारावर हे म्हटले ते लपवले जात नाही, म्हणून ते "+
			"संपूर्ण येथे दिलेले आहे.",
		"What follows was produced by a computer system from the farmer's "+
			"photographs. The officers read the same photographs, so this is "+
			"part of how the assessment was reached rather than a separate "+
			"opinion about it -- though the decision remains theirs. Nothing "+
			"about how the system reached it is withheld, which is why it is "+
			"printed here in full.",
	)

	observed := T("आकडा दिलेला नाही", "No figure given")
	if a.LossPercent != nil {
		observed = Tf("%.0f%%", "%.0f%%", *a.LossPercent)
	}

	s.Rows = append(s.Rows, Row{
		Parameter: T("प्रणालीचा निष्कर्ष", "The system's conclusion"),
		Observed:  observed,
		Source: Tf("%s · प्रॉम्प्ट आवृत्ती %s · %s रोजी चालवले",
			"%s · prompt version %s · run on %s",
			orDash(a.Model, "—"), orDash(a.PromptVersion, "—"),
			orDash(a.RanAt, "—")),
		Reference: Tf("अंतिम निर्णय: %s", "decided by: %s",
			orDash(decidedBy, "—")),
		Effect: T(
			"हा आकडा प्रणालीने तुमच्याच छायाचित्रांवरून काढला आहे, आणि "+
				"अधिकाऱ्यांनीही तीच छायाचित्रे पाहिली आहेत. त्यामुळे हा आकडा "+
				"निर्णयाचा भाग आहे — मात्र अंतिम निर्णय अधिकाऱ्यांचाच आहे आणि "+
				"ते हा आकडा बदलू शकतात.",
			"This figure was produced by the system from the same photographs "+
				"the officers read, so it is part of how the assessment was "+
				"reached rather than a separate opinion about it. The decision "+
				"remains the officers' and they may set it aside.",
		),
		Role: Determines,
	})

	if a.Reasoning != "" {
		s.Notes = append(s.Notes, T(a.Reasoning, a.Reasoning))
	}
	for _, c := range a.Citations {
		s.Notes = append(s.Notes, Tf("आधार: %s", "cited: %s", c))
	}

	return s
}

// OfficerFinding is one officer's recorded figure.
type OfficerFinding struct {
	Role        string
	Name        string
	LossPercent *float64
	RecordedAt  string

	// Blind says the officer recorded without seeing the others' figures.
	// A review that saw them first is a different piece of evidence, and the
	// two must never be presented as though they were the same.
	Blind bool
}

// Consensus is how the findings were reconciled.
type Consensus struct {
	DeltaPoints float64
	Route       string
	DecidedBy   string
	DecidedAt   string
}

// Verification builds §12.
func Verification(findings []OfficerFinding, c *Consensus) Section {
	s := Section{
		Number: "12",
		Title:  T("अधिकाऱ्यांची पडताळणी", "Officer verification"),
	}

	if len(findings) == 0 {
		s.Rows = append(s.Rows, AbsentRow(
			T("अधिकाऱ्यांची नोंद", "Officer findings"),
			T("अद्याप कोणत्याही अधिकाऱ्याने नोंद केलेली नाही",
				"No officer has recorded a finding yet"),
			Determines,
		))
		return s
	}

	for _, f := range findings {
		observed := T("आकडा नोंदवलेला नाही", "No figure recorded")
		if f.LossPercent != nil {
			observed = Tf("%.0f%%", "%.0f%%", *f.LossPercent)
		}

		blind := T("इतरांचे आकडे न पाहता नोंदवलेले",
			"Recorded without seeing the others' figures")
		if !f.Blind {
			blind = T("इतरांचे आकडे पाहिल्यानंतर नोंदवलेले",
				"Recorded after seeing the others' figures")
		}

		s.Rows = append(s.Rows, Row{
			Parameter: T(orDash(f.Role, "—"), orDash(f.Role, "—")),
			Observed:  observed,
			Source: Tf("%s · %s", "%s · %s",
				orDash(f.Name, "—"), orDash(f.RecordedAt, "—")),
			Reference: blind,
			Effect: T(
				"नुकसानीचा अंतिम आकडा या नोंदींवरून ठरतो. स्वतंत्रपणे नोंदवलेले "+
					"आकडे एकमेकांना स्वतंत्र पुष्टी देतात; एकमेकांचे पाहून "+
					"नोंदवलेले तसे देत नाहीत.",
				"The assessed figure comes from these findings. Figures recorded "+
					"independently corroborate each other; figures recorded after "+
					"seeing the others do not.",
			),
			Role: Determines,
		})
	}

	if c != nil {
		s.Rows = append(s.Rows, Row{
			Parameter: T("नोंदींमधील फरक", "Difference between findings"),
			Observed:  Tf("%.1f टक्के गुण", "%.1f percentage points", c.DeltaPoints),
			Source:    T(orDash(c.Route, "—"), orDash(c.Route, "—")),
			Reference: Tf("निर्णय: %s, %s रोजी", "decided by %s on %s",
				orDash(c.DecidedBy, "—"), orDash(c.DecidedAt, "—")),
			Effect: T(
				"अधिकाऱ्यांच्या नोंदींतील फरकावरून पुढील मार्ग ठरतो: थोडा फरक "+
					"असल्यास मंजुरी, मध्यम फरकासाठी लवाद, आणि मोठ्या फरकासाठी वरच्या "+
					"पातळीवर पाठवणी.",
				"The spread between the officers decides the route: a small "+
					"difference approves, a middling one goes to arbitration, and a "+
					"large one escalates to a higher tier.",
			),
			Role: Determines,
		})
	}

	return s
}

// EvidenceItem is one photograph or trace, and what can be checked about it.
type EvidenceItem struct {
	Kind       string
	CapturedAt string
	AccuracyM  float64

	// Geofence is the result of testing the capture against the parcel
	// boundary at the moment it was received.
	Geofence string
	SHA256   string

	// AskedFor is what the live assistant asked the farmer to show in this clip,
	// in the words they heard. Empty for most clips, and that is the ordinary
	// case: every clip recorded before the assistant existed, every one from a
	// build without it, and every one on a farm whose connection could not carry
	// a session.
	//
	// Printed because it changes what the clip is. A close-up of split pods that
	// the farmer chose to take is the farmer's selection of evidence; the same
	// clip taken because the assistant asked for it is a directed observation,
	// and that distinction is what answers a selective-evidence challenge. A
	// reader cannot tell them apart unless the document says which.
	AskedFor string
}

// Evidence builds §13.
//
// # Why the photographs determine
//
// With no field visit they are not supporting material, they are the
// observation. Every figure above is a reading of them: the plant counts, the
// model's assessment, and both officers' findings. A report that marked them
// as merely corroborating would be describing a measurement that was never
// taken.
//
// Hashes print in full. This is the section that makes the report checkable
// rather than merely readable: everything above can be re-derived from what
// is listed here, and a digest that does not match is a document somebody has
// altered.
func Evidence(items []EvidenceItem) Section {
	s := Section{
		Number: "14",
		Title:  T("पुरावा यादी", "Evidence"),
	}

	if len(items) == 0 {
		// The gravest absence in the document. With no field visit, a claim
		// with no photographs has nothing anybody looked at.
		s.Preamble = T(
			"या दाव्यासोबत एकही छायाचित्र मिळालेले नाही. प्रत्यक्ष पाहणी होत "+
				"नसल्याने, तपासण्यासारखे काहीही उपलब्ध नव्हते.",
			"No photograph was received with this claim. Since no visit is "+
				"made, there was nothing for anybody to examine.",
		)
		s.Rows = append(s.Rows, AbsentRow(
			T("छायाचित्रे", "Photographs"),
			T("या दाव्यासोबत कोणतेही छायाचित्र प्राप्त झालेले नाही",
				"No photograph was received with this claim"),
			Determines,
		))
		return s
	}

	directed := 0
	for _, it := range items {
		effect := T(
			"छायाचित्र मिळाल्याच्या क्षणी ते क्षेत्राच्या हद्दीत घेतले होते "+
				"का, हे तपासले जाते आणि तोच निकाल कायमचा नोंदवला जातो. "+
				"नंतर पुन्हा तपासणी होत नाही.",
			"Whether the photograph was taken inside the parcel boundary is "+
				"tested at the moment it is received, and that result is "+
				"recorded permanently. It is not re-evaluated later.",
		)

		// A clip the assistant asked for is a directed observation rather than
		// the farmer's own selection of what to show, and the row says which.
		if it.AskedFor != "" {
			directed++
			effect = Tf(
				"हे चित्रीकरण सहाय्यकाने मागितल्यावर घेतले आहे: %q. म्हणजे काय "+
					"दाखवायचे हे शेतकऱ्याने निवडलेले नाही, ते सांगितले गेले होते. "+
					"हद्दीची तपासणी मिळाल्याच्या क्षणीच होते आणि तोच निकाल "+
					"कायमचा राहतो.",
				"This clip was recorded because the assistant asked for it: %q. "+
					"What it shows was therefore directed rather than chosen by "+
					"the farmer. The boundary test is still made at the moment "+
					"it is received, and that result stands.",
				it.AskedFor)
		}

		r := Row{
			Parameter: T(orDash(it.Kind, "—"), orDash(it.Kind, "—")),
			Observed:  T(orDash(it.CapturedAt, "—"), orDash(it.CapturedAt, "—")),
			Source: Tf("स्थाननिश्चिती अचूकता %.0f मी.",
				"GNSS accuracy %.0f m", it.AccuracyM),
			Reference: T(orDash(it.Geofence, "—"), orDash(it.Geofence, "—")),
			Effect:    effect,
			Evidence:  T(orDash(it.SHA256, "—"), orDash(it.SHA256, "—")),
			Role:      Determines,
		}
		s.Rows = append(s.Rows, r)
	}

	// Said once, above the table, where a reader forms their impression of what
	// this evidence is. Only where some of it was directed: a claim walked with
	// the assistant switched off is not weaker for it, and a preamble mentioning
	// an absent assistant would imply it was.
	if directed > 0 {
		s.Preamble = Tf(
			"यांपैकी %d चित्रीकरणे सहाय्यकाने विशिष्ट गोष्ट दाखवण्यास सांगितल्यावर "+
				"घेतली आहेत. काय दाखवायचे ते शेतकऱ्याने निवडलेले नाही. "+
				"किती चित्रीकरणे आवश्यक आहेत हे सहाय्यक ठरवत नाही — ते ॲप ठरवते "+
				"आणि नेटवर्कशिवायही तेच राहते.",
			"%d of these clips were recorded because the assistant asked for "+
				"something specific to be shown, so what they show was not the "+
				"farmer's own selection. The assistant does not decide how many "+
				"clips are required: the app does, and that is unchanged with no "+
				"network.",
			directed)
	}

	return s
}

// Limits builds §14 from what the document has already admitted.
//
// # Why it is assembled rather than written
//
// Every absence in this report was stated where it happened, in its own
// section. A reader working through fifteen sections will not hold all of
// them at once, and the combined effect is the thing that matters: a claim
// with no quadrat count, no cloud-free pass and no threshold yield has three
// separate caveats and one conclusion.
//
// Collecting them here means the list cannot fall out of step with the
// document, and a section that quietly stops reporting an absence stops
// appearing in the limits too -- which is a bug either way, and this is where
// it shows.
func Limits(d *Dossier) Section {
	s := Section{
		Number: "15",
		Title:  T("या अहवालाच्या मर्यादा", "What this report cannot say"),
	}

	// The two standing limits, true of every report this system produces
	// until they stop being true.
	s.Notes = append(s.Notes,
		T(
			"या प्रणालीचे आकडे अद्याप प्रत्यक्ष निकालांशी ताडून पाहिलेले नाहीत. "+
				"छायाचित्रित, अधिकाऱ्यांनी तपासलेल्या आणि प्रत्यक्ष उत्पादन "+
				"नोंदवलेल्या शेतांचा संच अजून तयार झालेला नाही. त्यामुळे हे आकडे "+
				"तर्कसंगत आहेत, सिद्ध झालेले नाहीत.",
			"The figures this system produces have not been validated against "+
				"known outcomes. No dataset of photographed, officer-assessed "+
				"fields with recorded yields yet exists, so these figures are "+
				"defensible rather than proven.",
		),
		T(
			"AgriStack कडून कोणत्याही शेतकऱ्यासाठी क्षेत्राची भूमिती मिळत नाही. "+
				"त्यामुळे क्षेत्रावर आधारित प्रत्येक आकडा शेतात चालून नोंदवलेल्या "+
				"हद्दीवर अवलंबून आहे, आणि त्या हद्दीची अचूकता §2 मध्ये दिली आहे.",
			"AgriStack returns no parcel geometry for any farmer. Every "+
				"area-derived figure therefore rests on a boundary walked in the "+
				"field, whose accuracy is stated in §2.",
		),
	)

	// And what was specifically absent for this claim.
	for _, sec := range d.Sections {
		for _, r := range sec.Rows {
			if !r.Absent {
				continue
			}
			s.Notes = append(s.Notes, Tf(
				"§%s %s — %s",
				"§%s %s — %s",
				sec.Number, r.Parameter.In(Marathi), r.AbsentWhy.In(Marathi),
			))
		}
	}

	return s
}

// Objecting builds §16.
func Objecting(window string) Section {
	s := Section{
		Number: "16",
		Title:  T("आक्षेप कसा नोंदवावा", "How to object"),
	}

	s.Preamble = T(
		"या अहवालातील कोणत्याही आकड्याशी तुम्ही सहमत नसाल, तर तुम्हाला आक्षेप "+
			"नोंदवण्याचा हक्क आहे. त्यासाठी वकिलाची गरज नाही आणि कोणतेही शुल्क "+
			"नाही.",
		"If you do not agree with any figure in this report you have the right "+
			"to object. You do not need a lawyer and there is no fee.",
	)

	s.Notes = append(s.Notes,
		T(
			"तुमच्या गावच्या तलाठी कार्यालयात किंवा तालुका कृषी कार्यालयात जाऊन "+
				"आक्षेप नोंदवा. हा अहवाल सोबत न्या — त्यावरील संदर्भ क्रमांकावरून "+
				"तुमचा दावा लगेच शोधता येतो.",
			"Take your objection to the village Talathi office or the taluka "+
				"agriculture office. Carry this report: the reference number on it "+
				"finds your claim at once.",
		),
		T(
			"§4 मधील शेतातील मोजणी हा आकडा ठरवणारा मुख्य आधार आहे. ती मोजणी "+
				"पुन्हा करून तपासता येते — तुम्हाला ती चुकीची वाटत असल्यास तेच "+
				"सांगा.",
			"The quadrat count in §4 is the main basis of the figure. It can be "+
				"repeated and checked -- if you believe it is wrong, say so.",
		),
	)

	if window != "" {
		s.Notes = append(s.Notes, Tf(
			"आक्षेप नोंदवण्याची मुदत: %s",
			"The window for objecting: %s",
			window,
		))
	}

	return s
}
