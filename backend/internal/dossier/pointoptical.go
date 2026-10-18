package dossier

import (
	"github.com/agribid/esurvey-platform/backend/internal/pointndvi"
)

// The optical record at the spots the farmer filmed.
//
// # Why this sits beside §6 and not inside it
//
// §6 is the parcel's own series, and it is unchanged: the same passes, the same
// peak, the same reduction, and it is still what the loss figure is derived
// from. This section is an addition, and keeping it separate is what makes that
// visible. A reader must be able to see that the field figure was not quietly
// replaced by a figure from four small patches.
//
// # What it answers that §6 cannot
//
// Whether the ground each clip came from looks, from orbit, like what the clip
// shows. A field mean cannot: it averages the filmed ground together with all
// the ground nobody filmed, and the two cases that matter most both disappear
// into it -- footage of a ruined crop taken where the satellite sees vigour,
// and four clips taken from the one bad corner of an adequate field.
//
// # Why every figure here carries its ground area
//
// The patch is 20 m around the point, clipped to the boundary, so a point near
// a corner keeps a fraction of it. A reading over 400 m² is four Sentinel-2
// pixels and a reading over 1,250 is a dozen; neither is a confident number,
// and the reader is told which they are looking at rather than left to assume
// they are the same.

// PointOptical builds the section for the per-point optical readings.
//
// Returns an empty section where the analysis did not run, which Add drops: a
// heading with nothing under it reads as a missing analysis rather than as an
// analysis that was never applicable.
func PointOptical(c *pointndvi.Comparison) Section {
	s := Section{
		Number: "6b",
		Title: T("चित्रीकरणाच्या ठिकाणांवरील उपग्रह नोंद",
			"Optical record at the filmed points"),
	}
	if c == nil {
		return s
	}

	s.Preamble = Tf(
		"विभाग ६ मधील आकडे संपूर्ण शेताचे आहेत आणि ते जसे आहेत तसेच आहेत. "+
			"खालील नोंदी त्याच उपग्रह विश्लेषणाच्या, त्याच कालावधीतल्या, "+
			"परंतु शेतकऱ्याने ज्या ठिकाणी चित्रीकरण केले त्या प्रत्येक "+
			"ठिकाणाभोवती %.0f मीटरच्या भागापुरत्या आहेत. हे आकडे नुकसानीचा "+
			"आकडा ठरवत नाहीत; ते चित्रीकरण ज्या जमिनीवरचे आहे तीच जमीन "+
			"उपग्रहातून कशी दिसते हे सांगतात.",
		"The figures in §6 are for the whole parcel and are unchanged. The "+
			"readings below come from the same optical analysis over the same "+
			"window, taken instead over the ground within %.0f m of each point "+
			"the farmer filmed. They do not set the loss figure. They answer a "+
			"question the field mean cannot: whether the ground the footage "+
			"came from looks, from orbit, like what the footage shows.",
		c.RadiusM,
	)

	for _, r := range c.Readings {
		row := Row{
			Parameter: Tf("ठिकाण %d", "Point %d", r.StationNo),
			Observed: Tf("घट %.0f%%", "%.0f%% reduction",
				r.RRVLPercent),
			Source: Tf("उपग्रह, %.0f मी. परिसर (%.0f चौ.मी., %d वेळा)",
				"satellite, %.0f m patch (%.0f m², %d passes)",
				c.RadiusM, r.AreaM2, r.Observations),
			Reference: Tf("संपूर्ण शेत %.0f%%", "parcel %.0f%%",
				c.FieldRRVLPercent),
			Role: Corroborates,
		}

		// The two instruments side by side, which is the comparison this
		// section exists to make possible.
		if r.FootageSCV != nil {
			row.Evidence = Tf("चित्रीकरण %.0f", "footage %.0f", *r.FootageSCV)
		} else {
			row.Evidence = T("—", "—")
		}

		switch {
		case r.Weak:
			row.Effect = weakness(r, c.RadiusM)
			row.Caveats = Texts{T(
				"या नोंदीवरून कोणताही निष्कर्ष काढलेला नाही.",
				"No finding rests on this reading.",
			)}
		case r.AgainstFieldPoints >= pointndvi.SelectiveMarginPoints:
			row.Effect = Tf(
				"ही जागा संपूर्ण शेताच्या सरासरीपेक्षा %.0f अंकांनी जास्त "+
					"बाधित आहे. म्हणजे या ठिकाणचे चित्रीकरण शेताच्या "+
					"सरासरीचे प्रतिनिधित्व करत नाही.",
				"This ground is %.0f points worse than the parcel average, so "+
					"the footage from here is not representative of the field "+
					"as a whole.",
				r.AgainstFieldPoints)
		case r.AgainstFieldPoints <= -pointndvi.SelectiveMarginPoints:
			row.Effect = Tf(
				"ही जागा संपूर्ण शेताच्या सरासरीपेक्षा %.0f अंकांनी कमी "+
					"बाधित आहे.",
				"This ground is %.0f points less affected than the parcel "+
					"average.",
				-r.AgainstFieldPoints)
		default:
			row.Effect = T(
				"ही जागा संपूर्ण शेताच्या सरासरीशी जुळते.",
				"This ground is in line with the parcel as a whole.",
			)
		}

		s.Rows = append(s.Rows, row)
	}

	// A patch nobody could read. Said out loud: silence here would let the
	// absence read as corroboration of whatever the footage claimed there.
	for _, no := range c.CloudedPoints {
		s.Notes = append(s.Notes, Tf(
			"ठिकाण %d: या भागावर ढगांमुळे एकही स्वच्छ उपग्रह नोंद मिळाली "+
				"नाही, त्यामुळे या ठिकाणच्या चित्रीकरणाची उपग्रहाशी तुलना "+
				"झालेली नाही.",
			"Point %d: no cloud-free pass covered this patch, so the footage "+
				"from here has not been compared with the satellite at all.",
			no))
	}

	// The finding this analysis was built for.
	//
	// Printed as its own note per point rather than folded into a total,
	// because it is an allegation about one clip and an officer overruling it
	// needs to know which.
	for _, x := range c.Contradictions {
		s.Notes = append(s.Notes, Tf(
			"ठिकाण %d: चित्रीकरणात %.0f इतके नुकसान मोजले गेले, पण त्याच "+
				"%.0f चौ.मी. जमिनीवर उपग्रहाची घट फक्त %.0f%% आहे. दोन्ही "+
				"नोंदी एकाच जमिनीच्या आहेत, त्यामुळे \"उपग्रह संपूर्ण शेताची "+
				"सरासरी घेतो\" हे स्पष्टीकरण येथे लागू होत नाही. या "+
				"विसंगतीचे कारण नोंदवल्याशिवाय हा दावा निकाली काढू नये.",
			"Point %d: the footage measured %.0f for damage, while the "+
				"satellite over that same %.0f m² shows a reduction of only "+
				"%.0f%%. Both readings are of the same ground, so the usual "+
				"explanation -- that the satellite averages the whole field -- "+
				"does not apply here. This claim should not be decided without "+
				"a recorded reason for the discrepancy.",
			x.StationNo, x.FootageSCV, x.AreaM2, x.PointRRVL))
	}

	// The pattern across the points, phrased here and not in the analysis:
	// this document is printed in two languages and the wording belongs where
	// the rest of the wording is.
	if c.Skewed && c.SampledWorseThanFieldPoints != nil {
		s.Notes = append(s.Notes, Tf(
			"वाचता आलेली %d ठिकाणे संपूर्ण शेताच्या सरासरीपेक्षा सरासरी %.0f "+
				"अंकांनी जास्त बाधित आहेत. याचा अर्थ फसवणूक नाही -- शेतकरी "+
				"जिथे उभा आहे तिथून चित्रीकरण करतो आणि नुकसान बहुतेक वेळा "+
				"सर्वत्र सारखे नसते -- पण संपूर्ण क्षेत्रावर टक्केवारी "+
				"लावताना हे लक्षात घ्यावे की चित्रीकरण शेताच्या सरासरीचे "+
				"प्रतिनिधित्व करत नाही.",
			"The %d points that could be read came out %.0f points worse than "+
				"the parcel average. This is not an allegation -- the farmer "+
				"records from where they are standing, and a real loss is "+
				"often patchy -- but an officer applying a percentage to the "+
				"whole area should know the footage is not representative of "+
				"the field average.",
			c.ComparablePoints, *c.SampledWorseThanFieldPoints))
	}

	// What the whole section cannot support, whatever it found.
	s.Notes = append(s.Notes, Tf(
		"मर्यादा: %.0f मीटरचा परिसर म्हणजे उपग्रहाच्या दहा-बारा पिक्सेल, "+
			"आणि त्यातले काही शेजारच्या जमिनीशी मिसळलेले. त्यामुळे लहान फरक "+
			"येथे अर्थपूर्ण नाही; वरील निष्कर्ष फक्त मोठ्या फरकांवरच "+
			"नोंदवले जातात.",
		"Limitation: a %.0f m patch is a dozen satellite pixels, some of them "+
			"mixed with the ground beyond it. Small differences here mean "+
			"nothing, and the findings above are recorded only where the gap "+
			"is far larger than that noise can produce.",
		c.RadiusM))

	return s
}

// weakness is why a reading cannot carry a comparison, in words.
//
// The analysis returns a reason and this turns it into both languages. A
// package that returned English prose would force one half of a bilingual
// report to print English, in a document a farmer is meant to be able to read.
func weakness(r pointndvi.Reading, radiusM float64) Text {
	switch r.WeakBecause {
	case pointndvi.FewPasses:
		return Tf(
			"या भागावर फक्त %d स्वच्छ उपग्रह नोंद मिळाली, त्यामुळे ती एक "+
				"मूल्य दाखवते, बदल नाही.",
			"Only %d cloud-free pass covered this patch, so it shows a value "+
				"and not a change.",
			r.Observations)
	case pointndvi.SmallPatch:
		// Both halves take the arguments in the same order, because Tf shares
		// one list across them. Written the other way round the Marathi read
		// "within 1250 m only 20 m2", which is the kind of error that survives
		// every review conducted in one language.
		return Tf(
			"शेताची फक्त %.0f चौ.मी. जमीन या ठिकाणापासून %.0f मीटरच्या आत "+
				"येते -- संपूर्ण शेताशी तुलना करण्यासाठी हे फारच कमी आहे.",
			"Only %.0f m² of the parcel lies within %.0f m of this point, "+
				"which is too little ground to compare with the field.",
			r.AreaM2, radiusM)
	case pointndvi.RecordedOutside:
		return T(
			"चित्रीकरण शेताच्या हद्दीबाहेरून झाले आहे, त्यामुळे ही नोंद "+
				"शेतकरी जिथे उभा होता त्या जमिनीची नसून हद्दीच्या आतल्या "+
				"जवळच्या जमिनीची आहे.",
			"The recording was made outside the boundary, so this reading is "+
				"of the nearest ground inside the field rather than of where "+
				"the farmer stood.",
		)
	}
	return T("", "")
}
