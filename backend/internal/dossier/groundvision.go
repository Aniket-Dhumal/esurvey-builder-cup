package dossier

import (
	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
)

// GroundVision builds the section for what the survey video measured.
//
// # Why this section exists even when it is empty
//
// The specification's fusion gives this signal half the weight in clear
// weather and nearly two thirds under cloud — more than the satellite and the
// terrain put together. Until a vision pipeline exists there is nothing to put
// here, and the honest thing is to print the absence rather than to leave the
// report looking complete.
//
// A dossier that silently omits its heaviest input reads far stronger than it
// is. An officer deciding a payment should be able to see that the one piece
// of evidence which actually looked at the crop was never taken.
func GroundVision(g *groundcv.Score, unavailable string) Section {
	s := Section{
		Number: "7a",
		Title:  T("शेतातील चित्रीकरण", "Ground survey video"),
	}

	if g == nil {
		why := T(
			"या अहवालासाठी चित्रीकरणाचे मोजमाप उपलब्ध नाही",
			"No measurement of the survey video is available for this report",
		)
		if unavailable != "" {
			why = T(unavailable, unavailable)
		}
		s.Rows = append(s.Rows, AbsentRow(
			T("चित्रीकरण मोजमाप", "Video measurement"), why, Determines))
		s.Notes = append(s.Notes, T(
			"तांत्रिक मानकानुसार हाच पुरावा सर्वाधिक महत्त्वाचा आहे — आकाश निरभ्र "+
				"असताना निम्मे आणि ढगाळ असताना जवळपास दोन तृतीयांश वजन याला आहे. "+
				"तो उपलब्ध नसल्याने खालील निष्कर्ष उपग्रह व भूभागावर अवलंबून आहे.",
			"This is the heaviest evidence in the specification — half the "+
				"weight in clear weather and nearly two thirds under cloud. "+
				"Without it the finding below rests on the satellite and the "+
				"terrain, neither of which looks at the crop itself.",
		))
		return s
	}

	src := Tf(
		"%d चित्रचौकटी · गुणवत्ता %.2f",
		"%d keyframes · quality %.2f",
		g.Keyframes, g.Quality,
	)

	s.Rows = append(s.Rows,
		Row{
			Parameter: T("एकूण दृश्य नुकसान", "Ground damage score"),
			Observed:  Tf("%.1f%%", "%.1f%%", g.SCV),
			Source:    src,
			Reference: T("S_CV, तांत्रिक मानकानुसार", "S_CV, per the specification"),
			Effect: T(
				"पिकाकडे प्रत्यक्ष पाहणारा हाच एकमेव पुरावा आहे. उपग्रह शेत पाहतो, "+
					"हे चित्रीकरण पीक पाहते.",
				"The only evidence here that looks at the crop. The satellite "+
					"sees the field; this sees what is growing on it.",
			),
			Working: []Text{Tf(
				"0.45 × आडवे पडणे %.1f + 0.35 × बुडालेले %.1f%% + 0.20 × करपलेले %.1f%%",
				"0.45 × lodging %.1f + 0.35 × submerged %.1f%% + 0.20 × necrotic %.1f%%",
				g.LodgingLoss, g.SubmergedRatio*100, g.NecroticRatio*100)},
			Role: Determines,
		},
		Row{
			Parameter: T("खोड आडवे पडण्याचा कोन", "Stem lodging angle"),
			Observed:  Tf("%.0f°", "%.0f°", g.StemAngleDegree),
			Source:    src,
			Reference: lodgingBand(g.LodgingClass),
			Effect: T(
				"उभ्या खोडांचा उभ्या दिशेशी कोन. पीक जितके आडवे, तितके यंत्राने "+
					"काढणे अशक्य — म्हणून तीनपैकी याला सर्वाधिक वजन आहे.",
				"The angle of the standing stems from vertical. The flatter the "+
					"crop the less of it a harvester can lift, which is why this "+
					"carries the most weight of the three.",
			),
			Role: Corroborates,
		},
		Row{
			Parameter: T("बुडालेली व करपलेली पाने", "Submerged and necrotic"),
			Observed: Tf("बुडालेली %.1f%%, करपलेली %.1f%%",
				"submerged %.1f%%, necrotic %.1f%%",
				g.SubmergedRatio*100, g.NecroticRatio*100),
			Source: src,
			Reference: Tf("चित्रचौकटीतील पिकाच्या क्षेत्राशी प्रमाण (%.0f%% पीक)",
				"as a share of crop area in frame (%.0f%% crop)",
				g.CropPixelShare*100),
			Effect: T(
				"आकाश व जमीन वगळून केवळ पिकाच्या क्षेत्रावरून काढलेले प्रमाण, "+
					"त्यामुळे कॅमेरा मागे नेल्याने नुकसान कमी दिसत नाही.",
				"Measured against crop area only, with sky and soil excluded, so "+
					"stepping back from the field does not make the damage look "+
					"smaller.",
			),
			Role: Corroborates,
		},
	)

	// A score over a frame that was mostly sky divides by a small number and
	// still returns something between 0 and 100. The share is what shows it.
	if g.CropPixelShare < 0.25 {
		s.Notes = append(s.Notes, Tf(
			"चित्रचौकटींपैकी केवळ %.0f%% भाग पीक होता. उर्वरित आकाश, जमीन किंवा "+
				"पाणी असल्याने वरील प्रमाण थोड्या क्षेत्रावरून काढलेले आहे.",
			"Only %.0f%% of the scored frames were crop at all; the rest was sky, "+
				"soil or water. The ratios above rest on that small a share.",
			g.CropPixelShare*100,
		))
	}

	if g.Quality < 0.60 {
		s.Notes = append(s.Notes, Tf(
			"चित्रीकरणाची गुणवत्ता कमी (%.2f) आहे. मानकानुसार अशा वेळी या "+
				"पुराव्याचे वजन घटवून उपग्रहावर अधिक भर दिला जातो.",
			"The footage scored low for quality (%.2f). The specification "+
				"responds by taking weight off this evidence and leaning on the "+
				"satellite instead.",
			g.Quality,
		))
	}

	return s
}

func lodgingBand(class string) Text {
	switch class {
	case groundcv.LodgingErect:
		return T("उभे · ≤15°", "erect · ≤15°")
	case groundcv.LodgingMild:
		return T("किंचित आडवे · 15–45°", "mild · 15–45°")
	case groundcv.LodgingSevere:
		return T("गंभीर आडवे · 45–75°", "severe · 45–75°")
	case groundcv.LodgingProstrate:
		return T("पूर्ण आडवे · >75°", "prostrate · >75°")
	default:
		return T("—", "—")
	}
}
