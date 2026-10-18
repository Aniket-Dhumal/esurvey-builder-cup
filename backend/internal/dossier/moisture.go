package dossier

import (
	"github.com/agribid/esurvey-platform/backend/internal/moisture"
)

// Moisture builds §6a: leaf water and surface soil moisture.
//
// # Why every row corroborates and none determines
//
// Nothing here measures a loss. An NDMI anomaly says the canopy held less
// water than this field and its neighbours usually do; how much yield that
// cost depends on the crop and its stage, which §3 says and this does not. The
// fusion gives it no weight until a season of officer findings shows whether
// it earns one, and the role marker has to say the same thing the arithmetic
// does -- otherwise a reader would take a strongly worded stress finding as
// part of how the figure was reached.
//
// # Why the relevance line comes first
//
// NDMI is strong evidence for a dry spell and weak evidence for a hailstorm. A
// farmer disputing a hailstorm assessment who reads "canopy water stress" in
// their report is entitled to know, on the same page, that the platform itself
// does not think that bears much on hail.
func Moisture(r *moisture.Reading, unavailable string) Section {
	s := Section{
		Number: "6a",
		Title:  T("पानांतील व जमिनीतील ओलावा", "Leaf water and soil moisture"),
	}

	if r == nil {
		why := T(
			"या क्षेत्रासाठी ओलाव्याची नोंद उपलब्ध नव्हती",
			"No moisture reading was available for this parcel",
		)
		if unavailable != "" {
			why = T(unavailable, unavailable)
		}
		s.Rows = append(s.Rows, AbsentRow(
			T("ओलावा नोंद", "Moisture record"), why, Corroborates))
		return s
	}

	// A generated record is not an observation. Same treatment as a
	// generated radar series: its own source line, no weight, and the
	// statement on the row rather than in a note a reader may not reach.
	role := Corroborates
	optical := T("Sentinel-2 · NDMI (B8A, B11), 20 मी",
		"Sentinel-2 · NDMI (B8A, B11), 20 m")
	radar := T("Sentinel-1 · VV backscatter", "Sentinel-1 · VV backscatter")
	if r.Synthetic {
		role = Context
		optical = T("तयार केलेली नोंद · उपग्रह निरीक्षण नाही",
			"GENERATED RECORD · not a satellite observation")
		radar = optical
	}

	s.Rows = append(s.Rows, Row{
		Parameter: T("या आपत्तीसाठी महत्त्व", "Bearing on this calamity"),
		Observed:  T(r.Relevance, r.Relevance),
		Source:    T("आपत्तीच्या प्रकारानुसार", "by calamity type"),
		Role:      Context,
	})

	for _, f := range r.Findings {
		row := Row{
			Parameter: T("ओलावा निष्कर्ष", "Moisture finding"),
			Observed:  moistureFinding(f.Code),
			Source:    optical,
			Reference: Tf("विश्वासार्हता: %s", "confidence: %s", f.Confidence),
			Effect:    moistureEffect(f.Code),
			Role:      role,
		}
		if f.Code == moisture.FindingSurfaceWet || f.Code == moisture.FindingSurfaceDry {
			row.Source = radar
		}
		if f.Detail != "" {
			row.Working = append(row.Working, T(f.Detail, f.Detail))
		}
		for _, c := range f.Caveats {
			row.Caveats = append(row.Caveats, T(c, c))
		}
		if r.Synthetic {
			row.Caveats = append(row.Caveats, T(
				"ही नोंद कोणत्याही उपग्रहाने पाहिलेली नाही. ती चाचणीसाठी तयार "+
					"केलेली आहे आणि पुरावा म्हणून वापरता येणार नाही.",
				"No satellite observed this parcel. This record was generated "+
					"for testing and carries no evidential weight.",
			))
		}
		s.Rows = append(s.Rows, row)
	}

	if c := r.Canopy; c != nil && c.Near != nil {
		row := Row{
			Parameter: T("घटनेच्या आसपास NDMI", "NDMI near the event"),
			Observed: Tf("%.2f · %d पास", "%.2f · %d pass(es)",
				*c.Near, c.NearPasses),
			Source: optical,
			Effect: T(
				"पानांतील पाण्याचे प्रमाण. पाण्याचा ताण आल्यास हिरवेपणा (NDVI) "+
					"कमी होण्याआधीच हे घटते. हे नुकसानाचे मोजमाप नाही.",
				"How much water the leaves held. It falls before greenness "+
					"(NDVI) does when a crop runs short of water. It is not a "+
					"measure of loss.",
			),
			Role: role,
		}
		switch {
		case c.PriorMean != nil && c.Anomaly != nil:
			// Both languages take the arguments in the same order, because
			// Tf formats both with one argument list.
			row.Reference = Tf(
				"याच दिवसांत मागील %d हंगामांत %.2f (फरक %+.2f)",
				"same days in %d prior season(s): %.2f (difference %+.2f)",
				c.PriorYears, *c.PriorMean, *c.Anomaly)
		default:
			row.Reference = T("मागील हंगामांची नोंद नाही",
				"no prior season observed over these days")
		}
		s.Rows = append(s.Rows, row)

		if c.NeighbourGap != nil {
			ref := T("घटनेपूर्वीचा फरक उपलब्ध नाही", "no comparison before the event")
			if c.NeighbourGapBefore != nil {
				ref = Tf("घटनेपूर्वी %+.2f", "before the event %+.2f", *c.NeighbourGapBefore)
			}
			s.Rows = append(s.Rows, Row{
				Parameter: T("आसपासच्या शेतांशी तुलना", "Against surrounding cropland"),
				Observed:  Tf("%+.2f NDMI", "%+.2f NDMI", *c.NeighbourGap),
				Source: T("१.५ किमी परिसरातील शेतजमीन · ESA WorldCover",
					"cropland within 1.5 km · ESA WorldCover"),
				Reference: ref,
				Effect: T(
					"त्याच दिवशी त्याच हवामानात आसपासची शेते. संपूर्ण परिसर "+
						"कोरडा असल्यास ते आपत्तीला पुष्टी देते, एका शेताला नव्हे.",
					"The fields around it, on the same day, under the same "+
						"weather. A whole locality drying corroborates the event, "+
						"not one field.",
				),
				Role: role,
			})
		}
	}

	if sl := r.Soil; sl != nil && sl.Index != nil {
		row := Row{
			Parameter: T("जमिनीतील ओलावा निर्देशांक", "Soil moisture index"),
			Observed: Tf("%.0f%% · %s", "%.0f%% · %s",
				*sl.Index*100, sl.IndexDate),
			Source: radar,
			Reference: Tf(
				"या शेताची कोरडी पातळी %.1f dB ते ओली %.1f dB (%d पास, कक्षा %d)",
				"this field's dry %.1f dB to wet %.1f dB (%d passes, orbit %d)",
				sl.DryDB, sl.WetDB, sl.HistoryPasses, sl.RelativeOrbit),
			Effect: T(
				"दोन वर्षांतील या शेताच्या स्वतःच्या श्रेणीत आजचे वाचन कुठे आहे. "+
					"ढगांतूनही पाहता येते. आकारमानात्मक पाण्याचे प्रमाण नाही.",
				"Where the day's reading sits within this field's own range "+
					"over two years. Sees through cloud. Not a volumetric water "+
					"content.",
			),
			Role: role,
		}
		if sl.CanopyObscured {
			row.Caveats = append(row.Caveats, T(
				"त्या वेळी पीक दाट होते, त्यामुळे रडारला बहुतांश पाने दिसली; "+
					"हा निर्देशांक जमिनीचा कमकुवत निर्देशक आहे.",
				"The crop was dense at the time, so radar mostly saw leaves; this "+
					"index is a weak guide to the soil.",
			))
		}
		s.Rows = append(s.Rows, row)
	}

	for _, c := range r.Caveats {
		s.Notes = append(s.Notes, T(c, c))
	}
	return s
}

func moistureFinding(code string) Text {
	switch code {
	case moisture.FindingCanopyWaterStress:
		return T("पानांमध्ये पाण्याचा ताण", "Canopy water stress")
	case moisture.FindingLocalityDry:
		return T("संपूर्ण परिसर कोरडा", "Whole locality dry")
	case moisture.FindingWetterThanNeighbours:
		return T("आसपासच्या शेतांपेक्षा ओले", "Wetter than surrounding fields")
	case moisture.FindingNoCanopyWaterChange:
		return T("पानांतील पाणी नेहमीप्रमाणे", "Leaf water at its usual level")
	case moisture.FindingSurfaceWet:
		return T("जमीन जवळपास सर्वाधिक ओली", "Soil near its wettest")
	case moisture.FindingSurfaceDry:
		return T("जमीन जवळपास सर्वाधिक कोरडी", "Soil near its driest")
	default:
		return T(code, code)
	}
}

func moistureEffect(code string) Text {
	switch code {
	case moisture.FindingCanopyWaterStress:
		return T(
			"हे शेत स्वतःच्या नेहमीच्या पातळीपेक्षा आणि आसपासच्या शेतांपेक्षाही "+
				"कोरडे होते — पाण्याअभावी पिकाचे हेच लक्षण असते. नुकसानाचे "+
				"प्रमाण पिकाच्या अवस्थेवर अवलंबून असते.",
			"The field was drier than both its own normal and the fields around "+
				"it — the pattern of a crop short of water. How much yield that "+
				"cost depends on the crop's stage.",
		)
	case moisture.FindingLocalityDry:
		return T(
			"आसपासची शेतेही तितकीच कोरडी होती. हे आपत्तीला पुष्टी देते, पण हे "+
				"शेत वेगळे ठरवत नाही.",
			"The fields around it dried as much. This corroborates the event; it "+
				"does not set this field apart.",
		)
	case moisture.FindingWetterThanNeighbours:
		return T(
			"त्याच हवामानात आसपासच्या शेतांपेक्षा बरेच ओले. सहसा याचे कारण "+
				"सिंचन असते. पाण्याशी संबंधित नसलेल्या दाव्यावर याचा परिणाम नाही.",
			"Much wetter than the fields around it under the same weather. The "+
				"usual reason is irrigation. It has no bearing on a claim that is "+
				"not about water.",
		)
	case moisture.FindingNoCanopyWaterChange:
		return T(
			"उपग्रहाला पानांमध्ये नेहमीइतके पाणी दिसले.",
			"The satellite saw the leaves holding their usual water.",
		)
	case moisture.FindingSurfaceWet:
		return T(
			"अतिवृष्टी किंवा साचलेल्या पाण्याशी सुसंगत.",
			"Consistent with heavy rain or standing water.",
		)
	case moisture.FindingSurfaceDry:
		return T(
			"कोरड्या कालावधीशी सुसंगत.",
			"Consistent with a dry spell.",
		)
	default:
		return T("", "")
	}
}
