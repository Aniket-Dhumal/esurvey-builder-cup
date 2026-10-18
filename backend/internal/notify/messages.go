package notify

import (
	"fmt"
	"strings"
)

// What the farmer actually reads.
//
// # Why Marathi, and why it is not a translation layer
//
// The recipient is a farmer in a Maharashtra village. English here would be a
// message that was technically delivered. There is no i18n framework because
// there is one audience and one language; adding a framework for a second
// language nobody has asked for would be structure without content, and the
// day a second is needed this file is the place it goes.
//
// # Why every message carries the reference
//
// A farmer who receives an SMS and rings the taluka office has to be able to
// say which claim it is about. The reference is the one thing that turns a
// message into something actionable rather than a notification they cannot
// follow up.
//
// # Why none of them promises money
//
// This platform assesses loss. It does not disburse, and it does not know
// what any figure is worth in rupees. A message that reads as a payment
// notice would be the single most damaging thing it could send.

// Render builds the body for an event.
//
// Unknown codes return an error rather than a blank or a generic string: a
// message nobody wrote is a message nobody checked, and sending one to a
// farmer is worse than sending nothing.
func Render(eventCode string, v Vars) (string, error) {
	switch eventCode {
	case EventReportAcknowledged:
		return join(
			fmt.Sprintf("तुमची पीक नुकसान सूचना %s मिळाली आहे.", v.Reference),
			"पोहोच पावती नोंदवली गेली आहे.",
			fmt.Sprintf("गाव: %s", v.Village),
			// Not "the officers will come". Nobody visits: the photographs
			// and the boundary the farmer sent are what gets examined, and a
			// message promising a visit leaves somebody waiting at home for
			// one that was never going to happen.
			"तुम्ही पाठवलेले फोटो व माहिती दोन अधिकारी स्वतंत्रपणे तपासतील.",
		), nil

	case EventReportVerified:
		return join(
			fmt.Sprintf("सूचना %s ची तपासणी पूर्ण झाली.", v.Reference),
			fmt.Sprintf("नोंदवलेले नुकसान: %s", v.Figure),
			"तुम्ही सहमत नसल्यास हरकत नोंदवू शकता.",
		), nil

	case EventReportAdjudicated:
		return join(
			fmt.Sprintf("सूचना %s वर निर्णय झाला.", v.Reference),
			fmt.Sprintf("अंतिम नोंद: %s", v.Figure),
			// The one thing this message must not imply.
			"ही नुकसानीची नोंद आहे, रकमेची नाही.",
			"हरकत असल्यास १५ दिवसांत नोंदवा.",
		), nil

	case EventSurveyOpened:
		return join(
			fmt.Sprintf("%s साठी पीक नुकसान सर्वेक्षण सुरू झाले आहे.", v.Village),
			fmt.Sprintf("कारण: %s", v.Calamity),
			fmt.Sprintf("अंतिम तारीख: %s", v.Deadline),
			"तुमच्या शेताचे नुकसान झाले असल्यास अ‍ॅपवर नोंद करा.",
		), nil

	case EventSurveyClosing:
		return join(
			fmt.Sprintf("%s मधील सर्वेक्षण %s रोजी बंद होईल.", v.Village, v.Deadline),
			"नोंद अद्याप केली नसल्यास लवकर करा.",
		), nil

	case EventGrievanceFiled:
		return join(
			fmt.Sprintf("तुमची हरकत %s नोंदवली आहे.", v.Reference),
			"१५ दिवसांत अधिकारी उत्तर देतील.",
		), nil

	case EventGrievanceDecided:
		return join(
			fmt.Sprintf("हरकत %s वर निर्णय झाला.", v.Reference),
			v.Outcome,
			"तपशीलासाठी अ‍ॅप पहा किंवा तलाठी कार्यालयात संपर्क साधा.",
		), nil
	}

	return "", fmt.Errorf("no message is written for %q", eventCode)
}

// Vars are what a message needs filled in.
type Vars struct {
	Reference string
	Village   string
	Calamity  string
	Deadline  string
	// Figure is pre-formatted, and is a percentage or "नोंद नाही" — never a
	// rupee amount, because this platform does not know one.
	Figure  string
	Outcome string
}

// join assembles the lines, dropping any that came out empty.
//
// An SMS with a blank line reads as a broken message, and a farmer who
// receives one has no way to tell whether something was left out.
func join(lines ...string) string {
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}
