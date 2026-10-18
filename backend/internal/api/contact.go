package api

import (
	"errors"
	"regexp"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/identity"
)

// Normalising the two things an officer is reached by.
//
// Both are stored in one canonical form, because both are unique keys and a
// value that can be written two ways is a uniqueness constraint that does not
// hold. An officer invited as R.Deshmukh@nic.in who later types
// r.deshmukh@nic.in is one person; two rows for them is two sets of postings,
// and only one of them gets revoked when they transfer.

// normaliseEmail lowercases and trims. Not the local part only: the standard
// says the local part may be case-sensitive, and in twenty years of mail
// nobody has run a server where it is, whereas officers typing their own
// address with a capital first letter is every day.
func normaliseEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// looksLikeEmail is identity.NormalizeEmail's verdict: the one definition of
// an officer's login, shared with the store and the database constraint.
func looksLikeEmail(s string) bool {
	_, err := identity.NormalizeEmail(s)
	return err == nil
}

var digitsOnly = regexp.MustCompile(`\D`)

// normaliseMobile turns what an officer types into E.164.
//
// Indian numbers arrive as "9876543210", "09876543210", "+91 98765 43210" and
// "91-9876543210", and the column is UNIQUE. Stored as typed, the same handset
// registers four times and none of the four can be matched to the others.
//
// Empty is allowed and returns empty: an officer's mobile number is contact
// detail, not a credential, and an account is perfectly usable without one.
func normaliseMobile(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	d := digitsOnly.ReplaceAllString(raw, "")

	switch {
	case len(d) == 10:
		// A bare national number. Only assumed to be Indian when it starts
		// with a digit the numbering plan actually issues mobiles from --
		// otherwise a ten-digit landline or a foreign number would be
		// silently relabelled as an Indian handset.
		if d[0] < '6' {
			return "", errors.New(
				"an Indian mobile number starts with 6, 7, 8 or 9")
		}
		return "+91" + d, nil
	case len(d) == 11 && d[0] == '0':
		return normaliseMobile(d[1:])
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		return normaliseMobile(d[2:])
	case strings.HasPrefix(raw, "+") && len(d) >= 8 && len(d) <= 15:
		// Already international, and not Indian. Kept as given: an officer on
		// deputation with a foreign number is rare and not an error.
		return "+" + d, nil
	}
	return "", errors.New("that does not look like a mobile number")
}
