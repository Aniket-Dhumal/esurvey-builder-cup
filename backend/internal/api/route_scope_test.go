package api

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// Every route has a written answer to "whose data does this reach".
//
// The scope gaps found in September 2026 -- an empty jurisdiction list read as
// "everywhere", a shared check that waved farmers through, routes that never
// asked -- had one thing in common: nobody had been made to write the answer
// down. Each was a route added with a permission guard and the assumption
// that the guard was the whole question.
//
// So the answer is written here for every route, and this test fails when a
// route is added or removed without one. The entry is not proof -- the scope
// tests are -- but a new route cannot reach review without somebody deciding,
// in words, what limits it.
//
//	public    -- no account, no records
//	self      -- only the caller's own account or session
//	farmer    -- a farmer's own record, by mobile or grant
//	platform  -- operator-only configuration or health, no farmer records
//	reference -- enumerations and arithmetic, no records
//	scoped    -- records, limited to the caller's area or own claims
var routeScope = map[string]string{
	"GET /healthz":                                                     "public: liveness, no data",
	"GET /readyz":                                                      "public: readiness, no data",
	"GET /.well-known/esurvey-keys":                                    "public: token verification keys",
	"POST /api/v1/auth/officer/login":                                  "public: sign-in",
	"POST /api/v1/auth/officer/verify":                                 "public: sign-in, second factor",
	"POST /api/v1/me/mobile":                                           "own account: the caller's own handset",
	"POST /api/v1/me/mobile/verify":                                    "own account: the caller's own handset",
	"DELETE /api/v1/me/mobile":                                         "own account: the caller's own handset",
	"POST /api/v1/auth/farmer/otp":                                     "public: sign-in, same answer whether or not registered",
	"POST /api/v1/auth/farmer/verify":                                  "public: sign-in",
	"GET /api/v1/auth/invite":                                          "public: by single-use token",
	"POST /api/v1/auth/invite/accept":                                  "public: by single-use token",
	"POST /api/v1/auth/password/reset":                                 "public: same answer whether or not the account exists",
	"POST /api/v1/auth/password/reset/accept":                          "public: by single-use token",
	"POST /api/v1/auth/refresh":                                        "self: the caller own refresh token",
	"POST /api/v1/auth/logout":                                         "self: the caller own session",
	"GET /api/v1/auth/me":                                              "self",
	"GET /api/v1/notifications":                                        "self: by caller user id",
	"POST /api/v1/notifications/{id}/read":                             "self: by caller user id",
	"POST /api/v1/notifications/read-all":                              "self: by caller user id",
	"POST /api/v1/auth/password":                                       "self",
	"POST /api/v1/officer/agent/fill-format":                           "scoped: officeragent.RowsFor lists through report.Store with the caller's own rbac.Scope; only column headings reach the model",
	"POST /api/v1/officer/agent/export":                                "scoped: officeragent.BuildExport reads through RowsFor with the caller's own rbac.Scope; the request carries a format and a choice of columns from the closed vocabulary and has no field for a claim or a scope; farmers refused",
	"GET /api/v1/officer/agent/export/{id}":                            "scoped: a document prepared earlier in the caller's own conversation, whose rows were read through RowsFor with the caller's own rbac.Scope; the store key is composed from the caller's user id, so another officer holding the id reads a key that was never written and gets 404; farmers refused",
	"POST /api/v1/officer/live/session":                                "self: a signed relay ticket for the caller's own microphone; no claim, no farmer, and not counted against any farmer's allowance",
	"GET /api/v1/officer/live/relay":                                   "self: the caller's own speech, proxied to the relay in transcribe-only mode with everything but the ticket stripped off",
	"POST /api/v1/live/webrtc/offer":                                   "farmer: survey:capture, then the platform ticket the relay checks",
	"GET /api/v1/live/relay":                                           "farmer: survey:capture, then the platform ticket the relay checks",
	"POST /api/v1/live/session":                                        "farmer: actingScope, and a daily spend ceiling per farmer and per handset",
	"POST /api/v1/live/session/end":                                    "self: usage recorded against the calling account",
	"GET /api/v1/net/probe":                                            "authenticated: random bytes, discloses only the speed of the link",
	"PUT /api/v1/net/probe":                                            "authenticated: body read and discarded, nothing stored",
	"POST /api/v1/net/probe":                                           "authenticated: body read and discarded, nothing stored",
	"POST /api/v1/officer/agent/ask":                                   "scoped: every tool is a report.Store read taking the caller's own rbac.Scope; no tool writes. An attached file is the caller's own upload, audited per file and never a database field",
	"GET /api/v1/farmers":                                              "scoped: agristack.Registry, reach in the query",
	"GET /api/v1/farmers/{farmerID}":                                   "scoped: assertFarmerInScope",
	"POST /api/v1/farmers/{farmerID}/enrol":                            "scoped: assertFarmerInScope",
	"GET /api/v1/me/registry/{farmerID}":                               "farmer: own mobile or a grant for that farmer",
	"POST /api/v1/me/registry/lookup":                                  "farmer: id and date of birth, with lockout",
	"POST /api/v1/devices/sync-status":                                 "self: written against the caller",
	"GET /api/v1/platform/devices":                                     "platform: platform:read",
	"GET /api/v1/platform/integrations":                                "platform: platform:read",
	"GET /api/v1/platform/overview":                                    "platform: counts by state and district, no records",
	"GET /api/v1/platform/health":                                      "platform: service health and load, no records",
	"GET /api/v1/platform/circle-coverage":                             "scoped: all for platform:read, else caller jurisdictions",
	"POST /api/v1/assessments":                                         "scoped: assertInScope on the parcel village",
	"GET /api/v1/assessment-jobs/{id}":                                 "scoped: requester, or assertInScope on the village",
	"GET /api/v1/assessment-jobs":                                      "scoped: WaitingIn caller jurisdictions",
	"GET /api/v1/assessments":                                          "scoped: store scopePredicate",
	"GET /api/v1/assessments/{id}":                                     "scoped: store scopePredicate",
	"GET /api/v1/assessments/{id}/dossier":                             "scoped: store scopePredicate",
	"GET /api/v1/statewide":                                            "scoped: rollups by caller jurisdictions; farmers refused",
	"POST /api/v1/events":                                              "scoped: declaringDistrict, the caller district posting",
	"GET /api/v1/events":                                               "scoped: eventVisible; an officer with no posting sees none",
	"GET /api/v1/events/{eventID}":                                     "scoped: GetVisible for officers",
	"POST /api/v1/events/{eventID}/state":                              "scoped: districtInScope",
	"POST /api/v1/events/{eventID}/areas/{op}":                         "scoped: districtInScope",
	"GET /api/v1/events/{eventID}/eligibility":                         "scoped: acting farmer, or parcel village in scope",
	"POST /api/v1/reports":                                             "scoped: acting farmer, or assertInScope on the village",
	"GET /api/v1/reports":                                              "scoped: store scopePredicate, acting farmer",
	"GET /api/v1/reports/{reportID}":                                   "scoped: actingScope",
	"GET /api/v1/reports/{reportID}/assessment-report":                 "scoped: actingScope; a farmer only once decided",
	"GET /api/v1/reports/{reportID}/print":                             "scoped: actingScope",
	"POST /api/v1/reports/{reportID}/state":                            "scoped: store Transition with caller scope",
	"POST /api/v1/reports/{reportID}/approve":                          "scoped: Decide with caller scope",
	"POST /api/v1/reports/{reportID}/return":                           "scoped: Decide with caller scope",
	"GET /api/v1/reports/{reportID}/approvals":                         "scoped: Reports.Get with caller scope",
	"POST /api/v1/reports/approvals/bulk":                              "scoped: BulkApprove with caller scope",
	"GET /api/v1/reports/{reportID}/stations":                          "scoped: Reports.Get with caller scope",
	"POST /api/v1/reports/{reportID}/uploads":                          "scoped: actingScope",
	"GET /api/v1/reports/{reportID}/uploads/{uploadID}":                "scoped: actingScope",
	"PUT /api/v1/reports/{reportID}/uploads/{uploadID}/chunks/{index}": "scoped: actingScope",
	"POST /api/v1/reports/{reportID}/captures":                         "scoped: own report, or assertInScope",
	"GET /api/v1/reports/stuck":                                        "scoped: store scopePredicate",
	"POST /api/v1/reports/stuck/{villageCode}/retry":                   "scoped: store scopePredicate",
	"GET /api/v1/reports/{reportID}/progress":                          "scoped: acting farmer, store scope",
	"GET /api/v1/reports/{reportID}/captures":                          "scoped: actingScope",
	"GET /api/v1/reports/{reportID}/captures/{captureID}/media":        "scoped: actingScope, capture on that report",
	"GET /api/v1/parcels/{farmID}/stations":                            "scoped: mayReadParcel",
	"GET /api/v1/parcels/{farmID}/boundary":                            "scoped: mayReadParcel",
	"GET /api/v1/worklist":                                             "scoped: the caller own allocations",
	"GET /api/v1/overdue":                                              "scoped: store scope",
	"GET /api/v1/villages/{villageCode}/authorities":                   "scoped: VillagesInScope; farmers refused",
	"GET /api/v1/parcels":                                              "scoped: agristack.Registry, reach in the query",
	"GET /api/v1/farmers/summary":                                      "scoped: agristack.Registry, reach in the query",
	"GET /api/v1/overview":                                             "scoped: store scopePredicate; open events by the eventVisible rule",
	"GET /api/v1/grievance-grounds":                                    "reference: enumerations only",
	"POST /api/v1/grievances":                                          "scoped: Reports.Get with caller scope",
	"GET /api/v1/grievances":                                           "scoped: grievanceScope; an empty scope reaches none",
	"GET /api/v1/grievances/{grievanceID}":                             "scoped: grievanceScope; an empty scope reaches none",
	"POST /api/v1/grievances/{grievanceID}/assign":                     "scoped: grievanceScope; assignee posted over the village",
	"POST /api/v1/grievances/{grievanceID}/decide":                     "scoped: grievanceScope",
	"POST /api/v1/grievances/{grievanceID}/withdraw":                   "scoped: grievanceScope",
	"GET /api/v1/adjudications":                                        "scoped: store scope",
	"POST /api/v1/reports/{reportID}/allocate":                         "scoped: Reports.Get with caller scope",
	"GET /api/v1/reports/{reportID}/brief":                             "scoped: Reports.Get with caller scope; farmers refused",
	"POST /api/v1/reports/{reportID}/verify":                           "scoped: Reports.Get and allocation",
	"PUT /api/v1/reports/{reportID}/verify":                            "scoped: Reports.Get and allocation",
	"POST /api/v1/reports/{reportID}/measurements":                     "scoped: Reports.Get and allocation",
	"GET /api/v1/reports/{reportID}/measurements":                      "scoped: Reports.Get with caller scope; farmers refused",
	"POST /api/v1/parcels/{farmID}/boundary":                           "scoped: parcelOf (farmer grant, officer village)",
	"POST /api/v1/parcels/{farmID}/boundary/confirm":                   "scoped: VillageOfParcel then assertInScope",
	"GET /api/v1/admin/circle-coverage":                                "scoped: caller jurisdictions",
	"GET /api/v1/calibration":                                          "scoped: store scope",
	"GET /api/v1/expected-stand":                                       "reference: arithmetic on query parameters",
	"GET /api/v1/reports/{reportID}/findings":                          "scoped: Reports.Get with caller scope; farmers refused",
	"POST /api/v1/reports/{reportID}/adjudicate":                       "scoped: Reports.Get with caller scope",
	"GET /api/v1/report-form":                                          "reference: enumerations only",
	"GET /api/v1/calamity-types":                                       "reference: enumerations only",
	"GET /api/v1/jurisdictions":                                        "scoped: search and tree walk inside caller jurisdictions",
	"GET /api/v1/roles":                                                "reference: enumerations only",
	"POST /api/v1/admin/users":                                         "scoped: mayPost, rank and place",
	"GET /api/v1/admin/users":                                          "scoped: caller jurisdictions; the administrator all",
	"POST /api/v1/admin/users/{userID}/roles":                          "scoped: mayManage and mayPost",
	"POST /api/v1/admin/users/{userID}/roles/revoke":                   "scoped: mayManage",
	"POST /api/v1/admin/users/{userID}/status":                         "scoped: mayManage",
	"POST /api/v1/admin/users/{userID}/invite":                         "scoped: mayManage",
	"GET /api/v1/admin/audit":                                          "scoped: jurisdiction_id in caller area, or the caller own actions",
	"GET /api/v1/admin/retention":                                      "platform: retention policy, no records",
	"POST /api/v1/admin/retention/sweep":                               "platform: retention:run",
	"GET /metrics":                                                     "platform: token or loopback only (mayScrape)",
}

func TestEveryRouteStatesWhoseDataItReaches(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading the route table: %v", err)
	}
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`mux\.HandleFunc\(\s*"([A-Z]+ [^"]+)"`).
		FindAllStringSubmatch(string(src), -1) {
		found[m[1]] = true
	}
	if len(found) < 50 {
		t.Fatalf("parsed only %d routes; this check would pass vacuously", len(found))
	}

	var unstated, stale []string
	for r := range found {
		if routeScope[r] == "" {
			unstated = append(unstated, r)
		}
	}
	for r := range routeScope {
		if !found[r] {
			stale = append(stale, r)
		}
	}
	sort.Strings(unstated)
	sort.Strings(stale)
	for _, r := range unstated {
		t.Errorf("%s has no scope decision: add it to routeScope, naming what "+
			"limits the data it reaches, with a test that proves it", r)
	}
	for _, r := range stale {
		t.Errorf("%s is in routeScope but no longer registered", r)
	}
}
