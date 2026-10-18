package api

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Every route states the permission it needs, and two of them nearly did not.
//
// # What these tests are for
//
// The route table is the whole access-control surface of this platform. A
// route registered behind the wrong guard is not a bug anybody trips over in
// testing -- it works perfectly, for the wrong people, until somebody notices.
// Both findings below were exactly that shape.

// routeGuards reads the registration table and pairs each route with its
// guard.
//
// Parsed from source rather than exercised through the mux, because the
// question is what the table *says*. A handler that happens to check the same
// thing internally is a different guarantee -- it can be changed by somebody
// editing the handler, without the route table showing that anything moved.
func routeGuards(t *testing.T) map[string]string {
	t.Helper()

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading the route table: %v", err)
	}

	// The registration spans two lines more often than not.
	pattern := regexp.MustCompile(
		`mux\.HandleFunc\(\s*"((?:GET|POST|PUT|PATCH|DELETE) [^"]*)",\s*` +
			`(s\.require\(rbac\.[A-Za-z]+|s\.requireAuthDuringPasswordChange|s\.requireAuth|s\.[a-z][A-Za-z]*)`)

	out := map[string]string{}
	for _, m := range pattern.FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = strings.TrimPrefix(m[2], "s.")
	}
	if len(out) == 0 {
		t.Fatal("no routes were parsed; this check would pass vacuously")
	}
	return out
}

func TestUploadingEvidenceNeedsTheSamePermissionAsSubmittingIt(t *testing.T) {
	// The finding.
	//
	// The resumable upload routes were registered behind requireAuth while
	// POST /captures required survey:capture. Both end in a row in
	// survey_captures, so the upload path was a way to create evidence
	// without the permission that exists to control exactly that.
	//
	// It mattered because of who holds what: a Tehsildar has survey:read and
	// report:read but deliberately not survey:capture -- they adjudicate, and
	// an adjudicator manufacturing evidence on a claim they then decide is
	// the specific thing that separation was built to prevent. The same was
	// true of the District Magistrate and the State Officer.
	guards := routeGuards(t)

	const capture = "require(rbac.PermSurveyCapture"
	for _, route := range []string{
		"POST /api/v1/reports/{reportID}/uploads",
		"GET /api/v1/reports/{reportID}/uploads/{uploadID}",
		"PUT /api/v1/reports/{reportID}/uploads/{uploadID}/chunks/{index}",
		"POST /api/v1/reports/{reportID}/captures",
	} {
		got, ok := guards[route]
		if !ok {
			t.Errorf("%s is not registered", route)
			continue
		}
		if got != capture {
			t.Errorf("%s is guarded by %q; evidence creation needs %q",
				route, got, capture)
		}
	}
}

func TestTheAdjudicatingRolesCannotCreateEvidence(t *testing.T) {
	// The other half of the finding, stated as the property rather than the
	// route. If somebody ever grants survey:capture to an adjudicating role,
	// the guard above would still pass and the separation would be gone.
	for _, role := range []rbac.Role{
		rbac.RoleTehsildar,
		rbac.RoleDistrictMagistrate,
		rbac.RoleStateOfficer,
	} {
		if rbac.RoleCan(role, rbac.PermSurveyCapture) {
			t.Errorf("%s can capture evidence and also adjudicate; an "+
				"adjudicator who can manufacture the evidence they weigh is "+
				"not a separation of duties", role)
		}
		if !rbac.RoleCan(role, rbac.PermSurveyRead) {
			t.Errorf("%s cannot read evidence, so it cannot review a claim", role)
		}
	}
}

func TestAFarmerCanStillSendTheirOwnEvidence(t *testing.T) {
	// The tightening must not have locked out the people the routes are for.
	if !rbac.RoleCan(rbac.RoleFarmer, rbac.PermSurveyCapture) {
		t.Fatal("a farmer cannot capture; the self-survey is the platform")
	}
	if !rbac.RoleCan(rbac.RoleFarmer, rbac.PermSurveyRead) {
		t.Error("a farmer cannot read their own sampling stations")
	}
}

func TestOnlyTheWayOutOfAPasswordChangeSkipsThatGate(t *testing.T) {
	// The second finding.
	//
	// requireAuth did not apply the must-change-password restriction that
	// require() applies, so an officer still holding the password an
	// administrator issued them -- a credential somebody else chose, and may
	// still have written down -- could reach every unpermissioned route while
	// being refused every permissioned one.
	//
	// Two routes genuinely need the exemption, because the gate would
	// otherwise deadlock: setting the new password, and reading who you are,
	// which is how a client learns it must ask for one. Any third is a hole.
	guards := routeGuards(t)

	exempt := map[string]bool{
		"GET /api/v1/auth/me":        true,
		"POST /api/v1/auth/password": true,
	}

	for route, guard := range guards {
		if guard != "requireAuthDuringPasswordChange" {
			continue
		}
		if !exempt[route] {
			t.Errorf("%s skips the must-change-password gate. Only the two "+
				"routes that lead out of a restricted first sign-in may.",
				route)
		}
	}

	for route := range exempt {
		if guards[route] != "requireAuthDuringPasswordChange" {
			t.Errorf("%s is guarded by %q; it must stay reachable while a "+
				"password change is pending or a restricted officer is "+
				"locked out of the only thing they are allowed to do",
				route, guards[route])
		}
	}
}

func TestNoApiRouteIsRegisteredWithoutAGuard(t *testing.T) {
	// The property the route table's comment claims. Worth asserting rather
	// than trusting: a route added in a hurry, during an event, is exactly
	// when the guard gets left off.
	guards := routeGuards(t)

	allowed := map[string]bool{
		"require":                         true,
		"requireAuth":                     true,
		"requireAuthDuringPasswordChange": true,
		// Signed in, and nothing more. For resources whose scope is the
		// caller themselves -- their own notifications -- where there is no
		// role that should be able to read another officer's, and so nothing
		// to name in a permission.
		"authenticated": true,
	}

	// The routes that are public by necessity: you cannot require a token to
	// obtain one. Listed by name rather than matched by prefix, so adding a
	// sixth public auth route is a decision somebody writes down here rather
	// than one that slips in under a pattern.
	//
	// Each carries its own protection in the handler instead -- the OTP
	// request is rate-limited and answers 202 whether or not the account
	// exists, and refresh detects token reuse and kills the family.
	publicByDesign := map[string]bool{
		"POST /api/v1/auth/officer/login": true,
		// The second half of the same sign-in: the challenge it takes is
		// itself the proof, and no session exists yet to guard it with.
		"POST /api/v1/auth/officer/verify": true,
		"POST /api/v1/auth/farmer/otp":     true,
		"POST /api/v1/auth/farmer/verify":  true,
		"POST /api/v1/auth/refresh":        true,
		"POST /api/v1/auth/logout":         true,

		// Claiming an invited account. Public because the whole point is that
		// the holder has no credential yet -- the invitation token is the
		// credential, and it is 32 bytes from crypto/rand, single-use,
		// expiring, and stored only as a SHA-256 hash. See
		// identity/invite.go.
		"GET /api/v1/auth/invite":         true,
		"POST /api/v1/auth/invite/accept": true,

		// Getting back in after forgetting a password. Public because the
		// person asking cannot sign in -- that is the whole condition. The
		// request answers identically whether or not the account exists, is
		// capped at one link per address per five minutes, and the token it
		// issues lives an hour and is stored only as a hash.
		"POST /api/v1/auth/password/reset":        true,
		"POST /api/v1/auth/password/reset/accept": true,
	}

	for route, guard := range guards {
		if !strings.HasPrefix(route, "GET /api/v1") &&
			!strings.HasPrefix(route, "POST /api/v1") &&
			!strings.HasPrefix(route, "PUT /api/v1") &&
			!strings.HasPrefix(route, "PATCH /api/v1") &&
			!strings.HasPrefix(route, "DELETE /api/v1") {
			continue
		}

		if publicByDesign[route] {
			continue
		}

		name := guard
		if i := strings.IndexByte(name, '('); i >= 0 {
			name = name[:i]
		}
		if !allowed[name] {
			t.Errorf("%s is registered with %q, which is a bare handler and "+
				"not a guard; the route is open to anyone who can reach it",
				route, guard)
		}
	}
}
