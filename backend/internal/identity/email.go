package identity

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// An officer signs in with their email address, and nothing else.
//
// # Why one rule, here
//
// The login used to be a free-text username -- "admin", "dm.solapur",
// "talathi.malegaon" -- beside an optional email. Two identifiers for one
// person meant two ways to type them, no way to reach the person behind a
// username, and accounts that could not receive the invitation or a password
// reset they depended on. The email is now the login, and this is the single
// definition of what counts as one: the API, the bootstrap, the store and the
// database constraint (migration 0046) all hold to it.
//
// # What it checks, and what it cannot
//
// Shape, not existence. Whether a mailbox exists is settled by delivering to
// it, which the invitation does. What this refuses is everything a person can
// get visibly wrong, and every address that can never receive mail: no dot in
// the domain, a reserved name such as .local or example.com, characters no
// mail system issues.

// ErrInvalidEmail is returned for an address that fails the rule. Its message
// says which part is wrong, because "invalid email" sends somebody to retype
// the whole thing looking for a mistake they cannot see.
var ErrInvalidEmail = errors.New("invalid email address")

var (
	emailLocal = regexp.MustCompile(`^[a-z0-9!#$%&'*+/=?^_{|}~-]+(\.[a-z0-9!#$%&'*+/=?^_{|}~-]+)*$`)
	emailLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	emailTLD   = regexp.MustCompile(`^[a-z]{2,63}$`)
)

// reservedTLDs can never deliver mail to an officer: RFC 2606 and RFC 6761
// names, and the private suffixes networks invent for themselves.
var reservedTLDs = map[string]bool{
	"local": true, "localhost": true, "test": true, "example": true,
	"invalid": true, "internal": true, "lan": true, "home": true,
	"corp": true, "intranet": true, "private": true, "localdomain": true,
}

// reservedDomains are documentation domains that resolve and take no mail.
var reservedDomains = map[string]bool{
	"example.com": true, "example.net": true, "example.org": true,
}

// NormalizeEmail trims and lowercases an address and checks it can be a real
// officer's login. The returned address is the form stored and compared.
func NormalizeEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))

	fail := func(why string) (string, error) {
		return "", fmt.Errorf("%w: %s", ErrInvalidEmail, why)
	}

	switch {
	case e == "":
		return fail("an email address is required")
	case len(e) > 254:
		return fail("it is longer than an email address can be")
	case strings.ContainsAny(e, " \t\r\n\"(),:;<>[\\]"):
		return fail("it contains a space or a character addresses cannot hold")
	case strings.Count(e, "@") != 1:
		return fail("it must contain exactly one @")
	}

	at := strings.LastIndex(e, "@")
	local, domain := e[:at], e[at+1:]

	switch {
	case local == "":
		return fail("the part before the @ is missing")
	case len(local) > 64:
		return fail("the part before the @ is too long")
	case !emailLocal.MatchString(local):
		return fail("the part before the @ is not valid (check for a leading, trailing or double dot)")
	case domain == "":
		return fail("the domain after the @ is missing")
	case len(domain) > 253:
		return fail("the domain is too long")
	}

	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return fail("the domain needs a dot, as in nic.in or gov.in")
	}
	for _, l := range labels {
		if !emailLabel.MatchString(l) {
			return fail("the domain is not valid (check for a double dot or a stray hyphen)")
		}
	}
	tld := labels[len(labels)-1]
	switch {
	case !emailTLD.MatchString(tld):
		return fail("the domain must end in a real suffix such as .in or .com")
	case reservedTLDs[tld]:
		return fail("." + tld + " addresses cannot receive mail")
	case reservedDomains[domain]:
		return fail(domain + " is reserved and cannot receive mail")
	}
	return e, nil
}
