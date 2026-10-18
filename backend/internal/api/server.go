// Package api exposes the platform over HTTP.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/agronomy"
	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/assessjob"
	"github.com/agribid/esurvey-platform/backend/internal/assessment"
	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/calibration"
	"github.com/agribid/esurvey-platform/backend/internal/event"
	"github.com/agribid/esurvey-platform/backend/internal/evidence"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
	"github.com/agribid/esurvey-platform/backend/internal/idempotency"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/livespend"
	"github.com/agribid/esurvey-platform/backend/internal/mail"
	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
	"github.com/agribid/esurvey-platform/backend/internal/pdf"
	"github.com/agribid/esurvey-platform/backend/internal/platform/cache"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/platform/saferr"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/retention"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
	"github.com/agribid/esurvey-platform/backend/internal/store"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
	"github.com/agribid/esurvey-platform/backend/internal/weather"

	"github.com/agribid/esurvey-platform/backend/internal/platform/gate"
)

// Server wires the platform's dependencies.
type Server struct {
	Registry  agristack.Source
	Satellite *satellite.Client
	AI        *ai.Client

	// Agent answers an officer's questions about their own claims. Nil where the
	// AI service is not configured, which every other feature is indifferent to.
	Agent *officeragent.Agent

	// PreparedExports holds the documents the assistant produces inside a
	// conversation, for the few minutes between the sentence that asked for one
	// and the click that downloads it.
	//
	// Nil leaves the assistant answering in words: the tools that produce a file
	// are not offered when there is nowhere to put one, and the officer still
	// has the export route and its buttons. See officeragent/prepared.go for why
	// this is Redis and not a map in this process.
	PreparedExports *officeragent.Exports

	// PDF turns a print-styled page into a PDF, through the Chromium service the
	// assessment report is already converted by. Optional, and its absence costs
	// only the PDF: every other export format is produced in this process, and
	// the printed report was always meant to be printed from the HTML.
	PDF *pdf.Renderer

	// LiveRelayURL is where a handset opens a spoken session, and LiveModel names
	// what does the talking. Empty disables the feature, which costs the farmer
	// nothing: every instruction needed to file a claim is scripted and offline.
	LiveRelayURL string
	LiveModel    string

	// LiveSpend counts what live guidance costs and refuses it past a daily
	// ceiling. Nil leaves it uncapped, which is only right in a test.
	LiveSpend *livespend.Store

	// Districts turns an LGD district code into the district's own name, which
	// live guidance needs because a model cannot read a code as a place. Unset it
	// is Identity, which holds that reference data; it is a field of its own so
	// the one path that needs a name can be given one without being handed the
	// whole identity store. See liveDistrict.
	Districts districtNames

	// LiveTicketSecret signs the short-lived permission a handset carries to the
	// relay, which cannot authenticate a farmer itself. The same shared secret the
	// analysis services are called with, so there is one secret to rotate rather
	// than two.
	LiveTicketSecret string

	// AIServiceURL is where the relay lives on the internal network.
	//
	// The handset never reaches it directly: a live session is proxied through this
	// service, so the process holding the model credential stays unpublished. See
	// live_proxy.go.
	AIServiceURL string
	Store        *store.Store
	Log          *slog.Logger

	// Identity, Auth, Keys and Audit are the harness every route sits behind.
	Identity *identity.Store
	Events   *event.Store
	Reports  *report.Store
	Captures *survey.Store
	Auth     *auth.Service
	Keys     *auth.Keyring
	Audit    *audit.Recorder

	// Mail carries account invitations. Always present; whether it can
	// actually deliver depends on ESURVEY_SMTP_HOST, and Configured() is how
	// the console knows to show an administrator the invitation link instead
	// of promising an email that will sit in a queue.
	Mail *mail.Mailer

	// ConsoleBaseURL is where an officer's browser reaches this console.
	// Invitation links are built against it, so a wrong value produces mail
	// whose only content is a link that goes nowhere.
	ConsoleBaseURL string

	// Notify records what has arrived at an officer's rung, for the bell in
	// the console's topbar.
	Notifications *notify.Officers

	// Retention applies the data-retention policy. Optional: a deployment
	// without it simply never sweeps, and says so rather than pretending.
	Retention *retention.Sweeper

	// Evidence gathers what a damage figure can be grounded in -- prior
	// seasons, the surrounding fields, radar, the season's weather, the
	// crop's stage, an officer's counts and the claim history. Optional, and
	// its absence is the state the platform was in before it existed: the
	// panel still assesses, from the claim and the photographs alone, and
	// says that is what it did.
	Evidence *evidence.Assembler

	// Frames yields one still from a stored clip, for the report's plates.
	//
	// The walk records only video; the picture beside each point is taken out
	// of that clip rather than asked of the farmer a second time. Nil where no
	// vision module is configured, and the report then prints the map and the
	// table -- which is what it printed before there were pictures at all.
	Frames ClipFrames

	// Calibration compares the farmer's declaration, the panel's assessment
	// and the officer's finding. Optional, and worth nothing until a season
	// of findings has accumulated -- which is why it exists now.
	Calibration *calibration.Store

	// Fieldwork records what an officer counted in a quadrat. Optional: a
	// deployment without it keeps recording judgements, which is what every
	// deployment did until now.
	Fieldwork *fieldwork.Store

	// Jobs is the queue of assessments waiting to be produced. Optional: a
	// deployment without it refuses new assessment requests and serves
	// everything already stored.
	Jobs *assessjob.Store

	// Idempotency records what each client-chosen Idempotency-Key already
	// did, so a write retried after a lost reply replays its first answer.
	// Optional: without it the header is ignored and writes behave as they
	// always did.
	Idempotency *idempotency.Store

	// MinAppVersion is the oldest app build allowed to write. Empty allows
	// every build. See requireCurrentApp.
	MinAppVersion string

	// Metrics is the Prometheus surface. Optional; nil disables /metrics and
	// the instrumentation wrapper along with it.
	Metrics *Metrics

	// Notify queues messages to farmers. Optional, and its absence is
	// reported rather than hidden: a platform that tells nobody anything
	// should say so on its own status page.
	Notify *notify.Queue

	// DB and Redis are held for readiness reporting. Nothing else reaches
	// through them: the stores own their own queries.
	DB    *db.Pool
	Redis *cache.Client

	// Reads routes the reads that tolerate a few seconds' staleness to a
	// replica: the administrator's counts, the audit search, device health,
	// coverage. Nil reads from DB. See db.Router for which reads qualify.
	Reads *db.Router

	// Pools are every connection pool this instance holds, for the health
	// view. A nil pool (no replica) is shown as not configured.
	Pools []NamedPool

	// CORSOrigins are the browser origins allowed to call the API.
	CORSOrigins []string

	// TrustedProxyHops is how many reverse proxies sit in front, for
	// resolving the client address. Zero ignores X-Forwarded-For entirely.
	TrustedProxyHops int

	// MetricsToken guards /metrics.
	//
	// The endpoint carries no farmer data -- its labels are route patterns,
	// never paths, so no report id has ever reached it -- but it does publish
	// how many requests this district is handling, which routes fail, and how
	// slowly. That is reconnaissance, and it was readable by anybody who
	// could reach the port.
	//
	// Empty does not mean open: with no token the endpoint answers only to
	// loopback, so a sidecar scraper works and the internet does not. The
	// check uses the connection's own address rather than the forwarded one,
	// because a header a caller controls is not an address.
	MetricsToken string

	Env string

	// Probes are the internal services the platform health view polls: radar,
	// vision, the satellite engine. Set by main from configuration.
	Probes []ServiceProbe

	// Gates are the per-upstream concurrency limits, for the health view.
	Gates []*gate.Gate
}

// Routes returns the HTTP handler.
//
// Every route names the permission it needs at the point it is registered.
// A route added without one does not compile into an unprotected endpoint by
// omission -- it fails to state a permission, which is visible in this table.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// --- public ----------------------------------------------------------
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /.well-known/esurvey-keys", s.publicKeys)

	mux.HandleFunc("POST /api/v1/auth/officer/login", s.officerLogin)
	mux.HandleFunc("POST /api/v1/auth/officer/verify", s.officerVerify)
	mux.HandleFunc("POST /api/v1/auth/farmer/otp", s.farmerRequestOTP)
	mux.HandleFunc("POST /api/v1/auth/farmer/verify", s.farmerVerifyOTP)
	mux.HandleFunc("GET /api/v1/auth/invite", s.checkInvite)
	mux.HandleFunc("POST /api/v1/auth/invite/accept", s.acceptInvite)
	mux.HandleFunc("POST /api/v1/auth/password/reset", s.requestReset)
	mux.HandleFunc("POST /api/v1/auth/password/reset/accept", s.acceptReset)
	mux.HandleFunc("POST /api/v1/auth/refresh", s.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)

	// --- authenticated, no permission required ---------------------------
	// Both are the way out of a restricted first sign-in, so neither can sit
	// behind a permission the restricted principal does not carry.
	mux.HandleFunc("GET /api/v1/auth/me", s.requireAuthDuringPasswordChange(s.me))
	mux.HandleFunc("GET /api/v1/notifications",
		s.authenticated(s.listNotifications))
	mux.HandleFunc("POST /api/v1/notifications/{id}/read",
		s.authenticated(s.markNotificationRead))
	mux.HandleFunc("POST /api/v1/notifications/read-all",
		s.authenticated(s.markAllNotificationsRead))
	mux.HandleFunc("POST /api/v1/auth/password",
		s.requireAuthDuringPasswordChange(s.changePassword))

	// An officer's own handset, which is their second factor. Their own and
	// nobody else's: the user id comes from the session, never the body.
	mux.HandleFunc("POST /api/v1/me/mobile", s.authenticated(s.startMobileChange))
	mux.HandleFunc("POST /api/v1/me/mobile/verify",
		s.authenticated(s.confirmMobileChange))
	mux.HandleFunc("DELETE /api/v1/me/mobile", s.authenticated(s.removeMobile))

	// --- registry --------------------------------------------------------
	mux.HandleFunc("GET /api/v1/farmers",
		s.require(rbac.PermFarmerRead, s.listFarmers))
	mux.HandleFunc("GET /api/v1/farmers/{farmerID}",
		s.require(rbac.PermFarmerRead, s.getFarmer))
	mux.HandleFunc("POST /api/v1/farmers/{farmerID}/enrol",
		s.require(rbac.PermFarmerEnrol, s.enrolFarmer))

	// A farmer reading their own registry record, to fill a report from it.
	// Authenticated but unpermissioned: the check that matters is that the
	// record's registered mobile is the one this session verified, and it
	// lives in the handler. See farmer_registry_handlers.go.
	mux.HandleFunc("GET /api/v1/me/registry/{farmerID}",
		s.requireAuth(s.myRegistryRecord))
	// Naming a farmer and answering for them. A POST because the answer is a
	// date of birth, which has no business in a URL, a proxy log or a browser
	// history. See farmer_lookup.go for what guards it.
	mux.HandleFunc("POST /api/v1/me/registry/lookup",
		s.requireAuth(s.myRegistryLookup))

	// --- handsets ---------------------------------------------------------
	// Any signed-in account: farmers and officers both carry queues.
	mux.HandleFunc("POST /api/v1/devices/sync-status",
		s.requireAuth(s.reportSyncStatus))

	// --- running the platform --------------------------------------------
	mux.HandleFunc("GET /api/v1/platform/devices",
		s.require(rbac.PermPlatformRead, s.deviceHealth))
	// Not officer tools. platform:read is held by whoever runs this and by
	// nobody who uses it.
	mux.HandleFunc("GET /api/v1/platform/integrations",
		s.require(rbac.PermPlatformRead, s.integrations))
	mux.HandleFunc("GET /api/v1/platform/overview",
		s.require(rbac.PermPlatformRead, s.platformOverview))
	mux.HandleFunc("GET /api/v1/platform/health",
		s.require(rbac.PermPlatformRead, s.platformHealth))
	mux.HandleFunc("GET /api/v1/platform/circle-coverage",
		s.require(rbac.PermPlatformRead, s.circleCoverage))

	// --- assessments -----------------------------------------------------
	// Queued, not produced here: see internal/api/assessment_jobs.go.
	mux.HandleFunc("POST /api/v1/assessments",
		s.require(rbac.PermAssessmentCreate, s.idempotent(s.requestAssessment)))
	// A path of their own, not /assessments/{id}/…: a job id and an assessment
	// id are different things, and Go's mux is right to refuse a pattern where
	// "jobs" could be either.
	mux.HandleFunc("GET /api/v1/assessment-jobs/{id}",
		s.require(rbac.PermAssessmentRead, s.assessmentJob))
	mux.HandleFunc("GET /api/v1/assessment-jobs",
		s.require(rbac.PermAssessmentRead, s.assessmentBacklog))
	mux.HandleFunc("GET /api/v1/assessments",
		s.require(rbac.PermAssessmentRead, s.listAssessments))
	mux.HandleFunc("GET /api/v1/assessments/{id}",
		s.require(rbac.PermAssessmentRead, s.getAssessment))
	// The assessment as a printable document, rendered server-side from the
	// evidence packet stored with it. Same permission as reading it: seeing
	// the figures and seeing them laid out are the same disclosure.
	mux.HandleFunc("GET /api/v1/assessments/{id}/dossier",
		s.require(rbac.PermAssessmentRead, s.assessmentDossier))

	// Oversight above a district: districts side by side, by crop, by event,
	// and the filing trend. Read from the rollups, because the caller holds
	// the whole state in scope and a live GROUP BY here is a full scan.
	mux.HandleFunc("GET /api/v1/statewide",
		s.require(rbac.PermReportRead, s.statewide))

	// --- survey events ---------------------------------------------------
	mux.HandleFunc("POST /api/v1/events",
		s.require(rbac.PermEventCreate, s.createEvent))
	mux.HandleFunc("GET /api/v1/events",
		s.require(rbac.PermEventRead, s.listEvents))
	mux.HandleFunc("GET /api/v1/events/{eventID}",
		s.require(rbac.PermEventRead, s.getEvent))
	mux.HandleFunc("POST /api/v1/events/{eventID}/state",
		s.require(rbac.PermEventRead, s.transitionEvent))
	// Coverage, editable while the event is running. {op} is add or remove.
	mux.HandleFunc("POST /api/v1/events/{eventID}/areas/{op}",
		s.require(rbac.PermEventCreate, s.editEventAreas))
	mux.HandleFunc("GET /api/v1/events/{eventID}/eligibility",
		s.require(rbac.PermEventRead, s.checkEventEligibility))

	// --- crop loss reports -----------------------------------------------
	mux.HandleFunc("POST /api/v1/reports",
		s.require(rbac.PermReportCreate, s.idempotent(s.createReport)))
	mux.HandleFunc("GET /api/v1/reports",
		s.require(rbac.PermReportRead, s.listReports))
	mux.HandleFunc("GET /api/v1/reports/{reportID}",
		s.require(rbac.PermReportRead, s.getReport))
	// The assessment of a decided claim, as a document the farmer keeps.
	// Report permission, not assessment permission: a farmer reads it as
	// their claim's outcome, gated on the decision. See claimAssessmentReport.
	mux.HandleFunc("GET /api/v1/reports/{reportID}/assessment-report",
		s.require(rbac.PermReportRead, s.claimAssessmentReport))
	mux.HandleFunc("GET /api/v1/reports/{reportID}/print",
		s.require(rbac.PermReportRead, s.printReport))
	mux.HandleFunc("POST /api/v1/reports/{reportID}/state",
		s.require(rbac.PermReportRead, s.transitionReport))

	// --- the officer's assistant ------------------------------------------
	//
	// report:read and nothing more, because reading is all it does: every tool is
	// a scoped store call, and there is none that writes. See askAgent.
	mux.HandleFunc("POST /api/v1/officer/agent/ask",
		s.require(rbac.PermReportRead, s.askAgent))
	// An officer's own report format, returned with their district's figures in
	// it. Only the column headings reach the model; the values are written here.
	mux.HandleFunc("POST /api/v1/officer/agent/fill-format",
		s.require(rbac.PermReportRead, s.fillReportFormat))
	// The same district's figures as a document, in the format the officer asked
	// for, where they had no format of their own to send. Read-only like the two
	// above: the rows come from the same scoped store call, and the model is not
	// consulted at all. See exportAgentData.
	mux.HandleFunc("POST /api/v1/officer/agent/export",
		s.require(rbac.PermReportRead, s.exportAgentData))
	// The document the assistant prepared while answering, handed to the officer
	// who asked for it. The store's key is composed from their own user id, so an
	// id in somebody else's hands reaches a key that was never written. See
	// officer_prepared_export.go.
	mux.HandleFunc("GET /api/v1/officer/agent/export/{id}",
		s.require(rbac.PermReportRead, s.downloadPreparedExport))

	// --- an officer asking the assistant out loud -------------------------
	//
	// Speech is a transport here and not a second assistant: the relay writes down
	// what was said and the console posts that text to the ask route above, so
	// every guardrail stays in officeragent. report:read, because that is what the
	// assistant itself asks for and this grants no more than a way of reaching it.
	// See officer_live_handlers.go, which explains why these are not the farmer's
	// live routes with a flag on them.
	mux.HandleFunc("POST /api/v1/officer/live/session",
		s.require(rbac.PermReportRead, s.startOfficerVoice))
	mux.HandleFunc("GET /api/v1/officer/live/relay",
		s.require(rbac.PermReportRead, s.proxyOfficerLiveSocket))

	// --- is this farm's connection good enough for live guidance ----------
	//
	// Measured against this service rather than asked of Android, which reports
	// the radio and not what is actually available. Authenticated but otherwise
	// open to any signed-in caller: it discloses nothing but the speed of the
	// road between them and here.
	mux.HandleFunc("GET /api/v1/net/probe",
		s.authenticated(s.netProbeDown))
	// Both verbs, and PUT is the one the app actually uses.
	//
	// It was registered under POST alone, and the handset measured its uplink with
	// a PUT. The 405 that came back was read as a failed measurement, which is read
	// as an uplink of zero, which fails the "can this farm carry a conversation"
	// test -- so live guidance was never offered to anybody and looked exactly like
	// a farm with a slow connection, which is the one thing it is supposed to
	// detect. Nothing anywhere said 405 to a person.
	//
	// PUT is the better verb for it: the request creates nothing and the bytes are
	// discarded, so sending it twice is sending it once. POST is kept because it
	// costs nothing and an older handset in a field may still be using it.
	mux.HandleFunc("PUT /api/v1/net/probe",
		s.authenticated(s.netProbeUp))
	mux.HandleFunc("POST /api/v1/net/probe",
		s.authenticated(s.netProbeUp))

	// --- live spoken guidance ---------------------------------------------
	//
	// A grant rather than a proxy: the handset opens the relay itself, and this
	// decides whether it may and for how long. Capture permission, because a
	// session exists to help gather evidence and is granted to whoever may gather
	// it.
	mux.HandleFunc("POST /api/v1/live/session",
		s.require(rbac.PermSurveyCapture, s.startLiveSession))
	mux.HandleFunc("POST /api/v1/live/session/end",
		s.require(rbac.PermSurveyCapture, s.endLiveSession))

	// The session itself, proxied to the relay so the process holding the model
	// credential is never published. Both carry the platform's signed ticket, which
	// the relay checks: two checks in series, neither relied on alone.
	mux.HandleFunc("POST /api/v1/live/webrtc/offer",
		s.require(rbac.PermSurveyCapture, s.proxyWebRTCOffer))
	mux.HandleFunc("GET /api/v1/live/relay",
		s.require(rbac.PermSurveyCapture, s.proxyLiveSocket))

	// --- the approval ladder ---------------------------------------------
	// Four rungs in order: the village pair, the Circle, the Tehsil, the
	// District. PermReportAdjudicate is already held by exactly the three
	// roles above the pair and by neither village role, so it is the right
	// gate and no new permission is needed. Everything past that -- the
	// order, the jurisdiction, the contested rule -- is enforced beneath
	// these handlers rather than in them.
	mux.HandleFunc("POST /api/v1/reports/{reportID}/approve",
		s.require(rbac.PermReportAdjudicate, s.approveReport))
	mux.HandleFunc("POST /api/v1/reports/{reportID}/return",
		s.require(rbac.PermReportAdjudicate, s.returnReport))
	mux.HandleFunc("GET /api/v1/reports/{reportID}/approvals",
		s.require(rbac.PermReportRead, s.reportApprovals))
	mux.HandleFunc("POST /api/v1/reports/approvals/bulk",
		s.require(rbac.PermReportAdjudicate, s.bulkApproveReports))

	// --- self-survey evidence --------------------------------------------
	// The four points a farmer is directed to walk to. Placed by the server
	// and frozen, so the report can say the evidence was sampled rather than
	// selected. See station_handlers.go.
	mux.HandleFunc("GET /api/v1/reports/{reportID}/stations",
		s.require(rbac.PermSurveyRead, s.reportStations))

	// Resumable upload, for video too large to send in one request. The GET
	// is the point of it: a handset that lost signal asks where it got to
	// rather than starting fifty megabytes again. See upload_handlers.go.
	// Not wrapped in idempotent(): beginning an upload is already keyed by the
	// handset's upload_key, and its answer is the upload's progress *now*.
	// Replaying the first answer would send a resumed upload back to chunk
	// zero.
	mux.HandleFunc("POST /api/v1/reports/{reportID}/uploads",
		s.require(rbac.PermSurveyCapture, s.beginUpload))
	mux.HandleFunc("GET /api/v1/reports/{reportID}/uploads/{uploadID}",
		s.require(rbac.PermSurveyCapture, s.uploadStatus))
	mux.HandleFunc("PUT /api/v1/reports/{reportID}/uploads/{uploadID}/chunks/{index}",
		s.require(rbac.PermSurveyCapture, s.putChunk))

	mux.HandleFunc("POST /api/v1/reports/{reportID}/captures",
		s.require(rbac.PermSurveyCapture, s.idempotent(s.submitCapture)))
	// Claims the pipeline cannot advance, grouped by the village that needs
	// staffing. Registered before the {reportID} routes so "stuck" is not
	// read as a report id.
	mux.HandleFunc("GET /api/v1/reports/stuck",
		s.require(rbac.PermReportRead, s.stuckClaims))
	// "I have just posted the officer those claims were waiting for." The
	// pipeline would get there on its own within half an hour; this is the
	// difference between staffing a village and seeing it work.
	mux.HandleFunc("POST /api/v1/reports/stuck/{villageCode}/retry",
		s.require(rbac.PermReportReview, s.retryVillage))

	// Where a claim has got to. Read by the officers who handle it and by the
	// farmer whose claim it is, which is why it is authenticated rather than
	// permissioned: the scope decides whose claims you can see, and a farmer
	// holds no permission at all.
	mux.HandleFunc("GET /api/v1/reports/{reportID}/progress",
		s.requireAuth(s.claimProgress))

	mux.HandleFunc("GET /api/v1/reports/{reportID}/captures",
		s.require(rbac.PermSurveyRead, s.listCaptures))
	mux.HandleFunc("GET /api/v1/reports/{reportID}/captures/{captureID}/media",
		s.require(rbac.PermSurveyRead, s.captureMedia))
	// The same four points, keyed on the parcel rather than a report, because
	// the handset is offline-first and has no report id in the field. See
	// station_handlers.go.
	mux.HandleFunc("GET /api/v1/parcels/{farmID}/stations",
		s.require(rbac.PermParcelRead, s.parcelStations))
	mux.HandleFunc("GET /api/v1/parcels/{farmID}/boundary",
		s.require(rbac.PermParcelRead, s.parcelBoundary))

	// --- verification by the village authorities -------------------------
	mux.HandleFunc("GET /api/v1/worklist",
		s.require(rbac.PermReportVerify, s.worklist))
	// A supervisor's view of what has run past its deadline, and with whom.
	mux.HandleFunc("GET /api/v1/overdue",
		s.require(rbac.PermReportReview, s.overdue))
	mux.HandleFunc("GET /api/v1/villages/{villageCode}/authorities",
		s.require(rbac.PermReportRead, s.villageAuthorities))
	mux.HandleFunc("GET /api/v1/parcels",
		s.require(rbac.PermParcelRead, s.listParcels))
	mux.HandleFunc("GET /api/v1/farmers/summary",
		s.require(rbac.PermFarmerRead, s.registrySummary))
	mux.HandleFunc("GET /api/v1/overview",
		s.require(rbac.PermReportRead, s.overview))
	// --- grievances -------------------------------------------------------
	//
	// The appeal path. A farmer files against their own report; an officer
	// reads and answers the ones in their villages.
	mux.HandleFunc("GET /api/v1/grievance-grounds",
		s.require(rbac.PermGrievanceFile, s.grievanceGrounds))
	mux.HandleFunc("POST /api/v1/grievances",
		s.require(rbac.PermGrievanceFile, s.idempotent(s.fileGrievance)))
	mux.HandleFunc("GET /api/v1/grievances",
		s.require(rbac.PermGrievanceRead, s.listGrievances))
	mux.HandleFunc("GET /api/v1/grievances/{grievanceID}",
		s.require(rbac.PermGrievanceRead, s.getGrievance))
	mux.HandleFunc("POST /api/v1/grievances/{grievanceID}/assign",
		s.require(rbac.PermGrievanceAssign, s.assignGrievance))
	mux.HandleFunc("POST /api/v1/grievances/{grievanceID}/decide",
		s.require(rbac.PermGrievanceDecide, s.decideGrievance))
	// Withdrawal sits behind file rather than decide: it is the farmer's own
	// to stop, and requiring an officer's permission to give up would be a
	// strange way to run an appeal.
	mux.HandleFunc("POST /api/v1/grievances/{grievanceID}/withdraw",
		s.require(rbac.PermGrievanceFile, s.withdrawGrievance))

	mux.HandleFunc("GET /api/v1/adjudications",
		s.require(rbac.PermReportAdjudicate, s.adjudicationQueue))
	mux.HandleFunc("POST /api/v1/reports/{reportID}/allocate",
		s.require(rbac.PermReportReview, s.allocateReport))
	mux.HandleFunc("GET /api/v1/reports/{reportID}/brief",
		s.require(rbac.PermReportRead, s.verificationBrief))
	mux.HandleFunc("POST /api/v1/reports/{reportID}/verify",
		s.require(rbac.PermReportVerify, s.idempotent(s.verifyReport)))
	// Revising your own finding after reading the other authority's. PUT,
	// because there is one standing finding per officer per tier and this
	// replaces it — the earlier one is kept and marked.
	mux.HandleFunc("PUT /api/v1/reports/{reportID}/verify",
		s.require(rbac.PermReportVerify, s.amendVerification))
	mux.HandleFunc("POST /api/v1/reports/{reportID}/measurements",
		s.require(rbac.PermReportVerify, s.recordMeasurement))
	mux.HandleFunc("GET /api/v1/reports/{reportID}/measurements",
		s.require(rbac.PermReportRead, s.listMeasurements))
	mux.HandleFunc("POST /api/v1/parcels/{farmID}/boundary",
		s.require(rbac.PermSurveyCapture, s.idempotent(s.traceBoundary)))
	mux.HandleFunc("POST /api/v1/parcels/{farmID}/boundary/confirm",
		s.require(rbac.PermReportVerify, s.confirmBoundary))
	mux.HandleFunc("GET /api/v1/admin/circle-coverage",
		s.require(rbac.PermReferenceRead, s.circleCoverage))
	mux.HandleFunc("GET /api/v1/calibration",
		s.require(rbac.PermReportAdjudicate, s.calibration))
	mux.HandleFunc("GET /api/v1/expected-stand",
		s.require(rbac.PermReportVerify, s.expectedStand))
	mux.HandleFunc("GET /api/v1/reports/{reportID}/findings",
		s.require(rbac.PermReportRead, s.reportFindings))
	mux.HandleFunc("POST /api/v1/reports/{reportID}/adjudicate",
		s.require(rbac.PermReportAdjudicate, s.adjudicateReport))

	// --- reference -------------------------------------------------------
	mux.HandleFunc("GET /api/v1/report-form",
		s.require(rbac.PermReferenceRead, s.reportForm))
	mux.HandleFunc("GET /api/v1/calamity-types",
		s.require(rbac.PermReferenceRead, s.calamityTypes))
	mux.HandleFunc("GET /api/v1/jurisdictions",
		s.require(rbac.PermReferenceRead, s.listJurisdictions))
	mux.HandleFunc("GET /api/v1/roles",
		s.require(rbac.PermReferenceRead, s.roles))

	// --- administration --------------------------------------------------
	mux.HandleFunc("POST /api/v1/admin/users",
		s.require(rbac.PermUserCreate, s.createOfficer))
	mux.HandleFunc("GET /api/v1/admin/users",
		s.require(rbac.PermUserRead, s.listUsers))
	mux.HandleFunc("POST /api/v1/admin/users/{userID}/roles",
		s.require(rbac.PermRoleGrant, s.grantRole))
	mux.HandleFunc("POST /api/v1/admin/users/{userID}/roles/revoke",
		s.require(rbac.PermRoleGrant, s.revokeRole))
	mux.HandleFunc("POST /api/v1/admin/users/{userID}/status",
		s.require(rbac.PermUserUpdate, s.setUserStatus))
	mux.HandleFunc("POST /api/v1/admin/users/{userID}/invite",
		s.require(rbac.PermUserUpdate, s.resendInvite))
	mux.HandleFunc("GET /api/v1/admin/audit",
		s.require(rbac.PermAuditRead, s.listAudit))

	// What the platform keeps, and the one route that removes it.
	mux.HandleFunc("GET /api/v1/admin/retention",
		s.require(rbac.PermRetentionRead, s.listRetentionPolicies))
	mux.HandleFunc("POST /api/v1/admin/retention/sweep",
		s.require(rbac.PermRetentionRun, s.runRetention))

	// Ordering is load-bearing. A correlation id must exist before anything
	// logs; the panic guard must wrap everything below it; authentication
	// must run before any handler asks who the caller is.
	// Unauthenticated on purpose, and bound to the loopback interface in
	// deployment: a scrape agent has no token, and every label here is a
	// method, a route template and a status. Nothing per-farmer, per-report
	// or per-village is exposed, because a metrics endpoint is usually the
	// least-guarded surface in a deployment.
	mux.HandleFunc("GET /metrics", s.metrics)

	var h http.Handler = mux
	// Inside authenticate, so the limiter can key on the signed-in officer
	// rather than on an office's shared address.
	h = s.rateLimit(h)
	// Before any handler, after nothing that needs a principal: an old build
	// is refused without spending a database round trip on it.
	h = s.requireCurrentApp(h)
	h = s.authenticate(h)
	h = cors(s.CORSOrigins, h)
	h = s.securityHeaders(h)
	// Outside logging so a request is timed including what logging costs,
	// and inside the panic guard so a handler that panics is still counted.
	h = s.instrument(h)
	h = logging(s.Log, s.TrustedProxyHops, h)
	h = recovering(s.Log)(h)
	h = requestID(h)
	return h
}

// health is the liveness probe: is this process running.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"env":    s.Env,
	})
}

// ready is the readiness probe: can this process serve traffic.
//
// Kept apart from health on purpose. A container that cannot reach Postgres
// should be taken out of the load balancer, not restarted -- restarting it
// does not bring the database back and does lose the requests in flight.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	checks := map[string]any{}
	ready := true

	if err := s.DB.Ping(ctx); err != nil {
		checks["postgres"] = map[string]any{"status": "down", "error": err.Error()}
		ready = false
	} else {
		stat := s.DB.Stat()
		checks["postgres"] = map[string]any{
			"status":            "up",
			"connections_total": stat.TotalConns(),
			"connections_idle":  stat.IdleConns(),
		}
	}

	if err := s.Redis.Ping(ctx).Err(); err != nil {
		checks["redis"] = map[string]any{"status": "down", "error": err.Error()}
		ready = false
	} else {
		checks["redis"] = map[string]any{"status": "up"}
	}

	// The agent is reported but does not gate readiness: the console, the
	// registry and every past assessment stay usable while it is down, and
	// only new assessments fail.
	if h, err := s.AI.Health(ctx); err == nil {
		checks["ai"] = h
	} else {
		checks["ai"] = map[string]any{"status": "unreachable", "error": err.Error()}
	}

	// The registry reports its own problems. A record dropped in during a
	// trial that failed to parse is otherwise invisible until someone tries
	// to assess that farmer and gets "no record" -- by which point the file
	// is plainly sitting in the directory and nothing explains why.
	reg := map[string]any{"status": "up"}
	var upstream any = s.Registry
	if stored, ok := s.Registry.(*agristack.Registry); ok {
		st := stored.Stats(ctx)
		reg["farmers"] = st.Farmers
		reg["upstream"] = st.Upstream
		reg["served_stale"] = st.ServedStale
		reg["refresh_failures"] = st.Failures
		if st.CircuitOpen {
			// Degraded, not down: stored records are still served.
			reg["status"] = "degraded"
			reg["note"] = "the registry is failing; stored records are being served"
		}
		upstream = stored.Upstream()
	}
	if r, ok := upstream.(interface{ Status() agristack.Status }); ok {
		st := r.Status()
		reg["dir"] = st.Dir
		reg["files"] = st.Files
		reg["loaded_at"] = st.LoadedAt
		if len(st.Problems) > 0 {
			reg["status"] = "degraded"
			reg["problems"] = st.Problems
		}
		if st.Farmers == 0 {
			reg["status"] = "empty"
			reg["note"] = "no registry records are loaded; place one in " + st.Dir
		}
	}
	// The analytics engine. Reported, and deliberately not gating readiness:
	// the console, the registry and every past assessment stay usable while
	// it is down, and only new assessments fail. But an operator looking at
	// a run of failed assessments should find the reason here rather than in
	// a log.
	sat := map[string]any{"status": "up"}
	if open, failures := s.Satellite.Health(); open {
		sat["status"] = "unreachable"
		sat["consecutive_failures"] = failures
		sat["note"] = "new assessments will fail until it answers again"
	} else if failures > 0 {
		sat["status"] = "degraded"
		sat["consecutive_failures"] = failures
	}
	checks["satellite"] = sat

	// Radar and ground vision, as the assessment path sees them.
	//
	// Neither is required and both were absent for a long time, so "is this
	// deployment wired to one at all" is a real question with a real answer,
	// and it is the question the console's sources page exists to put in
	// front of an officer. No network call: this is about configuration, and
	// the health view polls the services themselves.
	checks["radar"] = moduleCheck(s.Evidence != nil && s.Evidence.Radar != nil,
		"Sentinel-1 backscatter and soil moisture",
		"assessments report radar as an absent evidence source")
	checks["ground_vision"] = moduleCheck(s.Evidence != nil && s.Evidence.Vision != nil,
		"the survey video, measured",
		"assessments report the ground score as an absent evidence source, "+
			"which is the heaviest signal in the specification")

	// What the platform has failed to tell people. Reported and not gating:
	// a backlog is a problem for whoever runs this, not a reason to take the
	// console away from officers who are working.
	if s.Notify != nil {
		if b, err := s.Notify.Backlog(ctx); err == nil {
			n := map[string]any{
				"status":   "up",
				"provider": b.Provider,
				"queued":   b.Queued,
				"failed":   b.Failed,
				"sent":     b.Sent,
			}
			if b.Provider == "none" && b.Queued > 0 {
				n["status"] = "undelivered"
				n["note"] = "messages are being queued and nothing is " +
					"configured to send them"
				n["oldest_queued"] = b.Oldest
			}
			if b.Abandoned > 0 {
				n["abandoned"] = b.Abandoned
			}
			// Why, not just how many. An operator reading "failed: 812" can
			// tell nothing from it; the same line with "Insufficient
			// Balance!" beside it is a job somebody can finish.
			if b.LastError != "" && (b.Failed > 0 || b.Abandoned > 0) {
				n["last_error"] = b.LastError
				if b.Provider != "none" {
					n["status"] = "failing"
				}
			}
			checks["notifications"] = n
		}
	}

	checks["registry"] = reg

	// Where the evidence is, and whether it is reachable. Gates readiness:
	// a platform that cannot store a photograph cannot accept a survey, and
	// finding that out when a farmer is standing in a field is too late.
	ev := map[string]any{"where": s.Captures.Evidence().Describe()}
	if err := s.Captures.Evidence().Check(ctx); err != nil {
		ev["status"] = "unreachable"
		ev["error"] = err.Error()
		ready = false
	} else {
		ev["status"] = "up"
	}
	checks["evidence"] = ev

	status := http.StatusOK
	label := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		label = "not ready"
	}
	writeJSON(w, status, map[string]any{"status": label, "checks": checks})
}

// moduleCheck describes an optional evidence module in the readiness answer.
//
// Configured or not, and what its absence costs. An officer reading this page
// is asking what the assessment they are about to sign rests on.
func moduleCheck(configured bool, does, absence string) map[string]any {
	if configured {
		return map[string]any{"status": "up", "role": does}
	}
	return map[string]any{
		"status": "not_configured",
		"role":   does,
		"effect": absence,
	}
}

// listFarmers returns one page of farmer rows.
//
// # Three things this fixes
//
// It used to return every farmer id, unfiltered, and the console then fetched
// each record individually to render a name -- one request per farmer, and
// the whole registry in the browser.
//
// It had no jurisdiction filter at all, and handed a Talathi posted to one
// village the identifiers of every farmer in the registry. A filter a client
// applies is not a filter.
//
// And it walked every record in memory on every request -- scope, search,
// sort, page in Go -- which is a registry that fits in one process. The rows
// now come from the stored registry (agristack.Registry), with the caller's
// reach and the page in the query.
func (s *Server) listFarmers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}
	reg, ok := s.registryListings(w)
	if !ok {
		return
	}
	reach, err := registryReach(rbac.FromContext(ctx))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		return
	}

	// The one filter this listing needed: a parcel with no boundary cannot
	// be assessed, so "which of my farmers cannot be assessed" is the
	// question a Circle Officer opens this page to answer.
	rows, total, err := reg.Farmers(ctx, agristack.FarmerQuery{
		Reach: reach, Search: pg.Query, Boundary: r.URL.Query().Get("boundary"),
		Offset: pg.Offset, Limit: pg.Limit,
	})
	if err != nil {
		s.Log.Error("listing farmers", "error", err)
		writeError(w, http.StatusInternalServerError, "REGISTRY_FAILED",
			"the request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, page.Wrap(rows, total, pg))
}

// listParcels returns one page of parcels, scoped and searchable.
func (s *Server) listParcels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}
	reg, ok := s.registryListings(w)
	if !ok {
		return
	}
	reach, err := registryReach(rbac.FromContext(ctx))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		return
	}

	// The parcels that cannot be assessed, which is the list somebody
	// actually works through when chasing AgriStack for missing geometry.
	rows, total, err := reg.Parcels(ctx, agristack.ParcelQuery{
		Reach: reach, Search: pg.Query,
		Boundary: r.URL.Query().Get("boundary"), Crop: r.URL.Query().Get("crop"),
		Offset: pg.Offset, Limit: pg.Limit,
	})
	if err != nil {
		s.Log.Error("listing parcels", "error", err)
		writeError(w, http.StatusInternalServerError, "REGISTRY_FAILED",
			"the request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, page.Wrap(rows, total, pg))
}

// registrySummary totals the registry inside the caller's reach.
func (s *Server) registrySummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	reg, ok := s.registryListings(w)
	if !ok {
		return
	}
	reach, err := registryReach(rbac.FromContext(ctx))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		return
	}
	totals, err := reg.Totals(ctx, reach)
	if err != nil {
		s.Log.Error("summarising registry", "error", err)
		writeError(w, http.StatusInternalServerError, "REGISTRY_FAILED",
			"the request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, totals)
}

// registryListings is the stored registry, which is what can list.
type registryListings interface {
	Farmers(context.Context, agristack.FarmerQuery) ([]agristack.FarmerSummary, int, error)
	Parcels(context.Context, agristack.ParcelQuery) ([]agristack.ParcelRow, int, error)
	Totals(context.Context, agristack.Reach) (agristack.Totals, error)
}

func (s *Server) registryListings(w http.ResponseWriter) (registryListings, bool) {
	reg, ok := s.Registry.(registryListings)
	if !ok {
		writeError(w, http.StatusInternalServerError, "REGISTRY_UNSUPPORTED",
			"this registry cannot list")
	}
	return reg, ok
}

// registryReach is whose registry rows a caller may list.
//
// A farmer sees themselves; an officer the villages beneath their postings.
// An officer with no posting reaches nothing -- the empty reach is not
// "everywhere", which is the mistake the September scope review found in
// three other places.
func registryReach(p *rbac.Principal) (agristack.Reach, error) {
	if p == nil {
		return agristack.Reach{}, rbac.ErrUnauthenticated
	}
	if p.IsFarmer() {
		return agristack.Reach{FarmerID: p.FarmerID}, nil
	}
	return agristack.Reach{JurisdictionIDs: p.Scope().JurisdictionIDs}, nil
}

func (s *Server) getFarmer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	farmerID := r.PathValue("farmerID")

	rec, err := s.Registry.FarmerRecord(ctx, farmerID)
	if err != nil {
		s.writeFarmerRecordError(w, err)
		return
	}

	if err := s.assertFarmerInScope(r, rbac.FromContext(ctx), rec); err != nil {
		s.writeScopeError(w, err)
		return
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionFarmerRead, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "farmer", ResourceID: farmerID,
		JurisdictionCode: rec.Farmer.VillageLGDCode,
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, rec)
}

// createAssessmentRequest asks for an assessment of one parcel.
//
// SowingDate may be omitted when the parcel carries a crop declaration, in
// which case it is taken from there.
type createAssessmentRequest struct {
	FarmerID     string `json:"farmer_id"`
	FarmID       string `json:"farm_id"`
	IncidentDate string `json:"incident_date"`
	SowingDate   string `json:"sowing_date,omitempty"`

	// CropLossReportID attaches the farmer's own photographs to the
	// assessment. It is the difference between an assessment that must refuse
	// a damage figure and one that can state one: satellite evidence
	// establishes that a crop grew, and only ground imagery can measure how
	// badly it was hurt.
	CropLossReportID string `json:"crop_loss_report_id,omitempty"`

	// SurveyEventID is the declared calamity this assessment is for.
	//
	// The console asks an officer to choose it before anything else, because
	// it is what dates the work. It then had nowhere to go: the request
	// carried only a date, and assessments.survey_event_id -- a column with
	// an index, built when survey events were -- was null on every row in the
	// database. Empty where the pipeline queued this for a claim, which names
	// its own event.
	SurveyEventID string `json:"survey_event_id,omitempty"`
}

// produceAssessment runs the pipeline and stores what it produced.
//
// Called by a worker, not by a request: see internal/assessjob. Everything a
// caller could have got wrong -- the farmer, the parcel, the officer's scope,
// a missing sowing date -- was settled when the job was queued, so what can
// fail here is the work itself, and each failure says whether trying again
// could succeed.
func (s *Server) produceAssessment(
	ctx context.Context, req createAssessmentRequest, principal *rbac.Principal,
) (id string, failure *assessFailure) {
	rec, err := s.Registry.FarmerRecord(ctx, req.FarmerID)
	if errors.Is(err, agristack.ErrUnavailable) {
		return "", &assessFailure{Code: "REGISTRY_UNAVAILABLE", Retryable: true,
			Message: "the farmer registry is not answering"}
	}
	if err != nil {
		return "", refuse("FARMER_NOT_FOUND", err.Error())
	}
	parcel, ok := rec.ParcelByFarmID(req.FarmID)
	if !ok {
		return "", refuse("PARCEL_NOT_FOUND",
			fmt.Sprintf("farmer %s has no parcel %s", req.FarmerID, req.FarmID))
	}

	// Geometry is the one thing AgriStack does not return today, which until
	// now meant no live farmer could be assessed at all.
	//
	// A boundary the farmer walked stands in for it. That is not a lesser
	// answer than the registry's -- for most of Maharashtra it is the only
	// answer there is -- but it is a different one, so the provenance travels
	// with every figure derived from it and an officer sees which they are
	// reading.
	boundarySource := "agristack"
	if parcel.PlotGeometry == nil {
		geo, trace, err := s.Captures.TracedBoundary(ctx, req.FarmID)
		if err != nil {
			return "", refuse("NO_GEOMETRY",
				"this parcel has no boundary. AgriStack returns none for it, and "+
					"nobody has walked its edge in the app. Without one the field "+
					"cannot be found from satellite and no assessment is possible.")
		}
		var g agristack.Geometry
		if err := json.Unmarshal(geo, &g); err != nil {
			s.Log.Error("decoding a traced boundary", "farm_id", req.FarmID, "error", err)
			return "", refuse("BAD_BOUNDARY",
				"the recorded boundary for this parcel could not be read")
		}
		parcel.PlotGeometry = &g
		boundarySource = trace.Source
		if trace.ConfirmedAt != "" {
			boundarySource = trace.Source + "_confirmed"
		}
	}

	crops := rec.CropsForParcel(req.FarmID)
	sowingDate := req.SowingDate
	cropName := ""
	if len(crops) > 0 {
		cropName = crops[0].CropName
		if sowingDate == "" {
			if d, err := crops[0].SowingDateISO(); err == nil {
				sowingDate = d
			}
		}
	}
	if sowingDate == "" {
		return "", refuse("MISSING_SOWING_DATE",
			"no sowing_date supplied and the parcel carries no crop declaration")
	}

	extent, err := parcel.ExtentHectares()
	if err != nil {
		return "", refuse("BAD_EXTENT", err.Error())
	}

	geom := satellite.Geometry{
		Type:        parcel.PlotGeometry.Type,
		Coordinates: parcel.PlotGeometry.Coordinates,
	}

	// No ceiling of its own: the worker's (assessmentCeiling) bounds the job,
	// waiting for upstream slots included, and each upstream call is bounded
	// by its client once it has a slot. A five-minute ceiling here counted
	// the wait for a slot, so under load every job timed out in the queue
	// and spent an attempt doing so.

	ndvi, err := s.Satellite.NDVI(ctx, satellite.NDVIRequest{
		Geometry:     geom,
		SowingDate:   sowingDate,
		IncidentDate: req.IncidentDate,
		FarmerID:     req.FarmerID,
		FieldID:      req.FarmID,
	})
	if err != nil {
		return "", upstreamFailure("ndvi", err)
	}

	terrain, err := s.Satellite.Terrain(ctx, satellite.TerrainRequest{
		Geometry: geom,
		FarmerID: req.FarmerID,
		FieldID:  req.FarmID,
	})
	if err != nil {
		return "", upstreamFailure("terrain", err)
	}

	sat, err := assessment.ScoreSatellite(ndvi.TimeSeries)
	if err != nil {
		// A wholly clouded window is a successful analysis with nothing to
		// say. Refusing to score it is correct: zero observations is not zero
		// damage -- and a retry next minute sees the same clouds.
		return "", refuse("NO_OBSERVATIONS", err.Error())
	}
	ter := assessment.ScoreTerrain(terrain)

	// The agent judges; this service verifies. Satellite establishes whether a
	// crop grew and whether the claim matches the record — it cannot measure
	// damage, so a figure only survives verification when ground imagery
	// backed it.
	agentReq := s.buildAgentRequest(
		req, parcel, crops, sowingDate, extent, sat, ter, ndvi, terrain,
		boundarySource)

	// Ground evidence, where the farmer has captured any.
	//
	// A photograph taken outside the plot, or before the loss, is still shown
	// to the officer with the reason -- and must not reach the model, which
	// has no way to know it is not what it claims. Everything else goes, with
	// its caveats in the caption: a model told an image is ten days old can
	// reason about that; one shown it as though it were fresh cannot.
	var groundUsed, groundHeld int
	if req.CropLossReportID != "" {
		g, err := s.groundImagery(ctx, req.CropLossReportID, principal)
		if err != nil {
			return "", refuse("GROUND_EVIDENCE",
				"the photographs on the report this assessment names could not "+
					"be read: "+err.Error())
		}
		agentReq.Images = append(agentReq.Images, g.Images...)
		groundUsed, groundHeld = g.Used, g.Held

		// What the farmer says happened. Without it the weather specialist
		// cannot ask whether the record bears the claim out, and the
		// phenology specialist cannot ask whether this damage mode fits the
		// growth stage -- two of the four have nothing to run on.
		agentReq.CalamityType = g.Report.CalamityType
		agentReq.LossCategory = string(g.Report.LossCategory)
		agentReq.CalamityDeclaredBy = "farmer"
	}

	// The survey event, which decides two things a claim cannot.
	//
	// A screening has no claim at all, so the calamity comes from the event the
	// officer chose. Without it the weather specialist can describe a fortnight
	// and nothing more -- "could not evaluate consistency because the reported
	// calamity is not stated" -- and susceptibility cannot be looked up.
	//
	// And where there *is* a claim, the event still decides who declared the
	// calamity and over what period. This was gated on the calamity type being
	// absent, so it never ran for a claim, and every claim reported as
	// "declared by: farmer" however official the event it was filed under.
	// Barshi, 2026: a Collector's declared drought reached the panel as one
	// farmer's assertion, was checked against a reanalysis grid that had been
	// asked about the wrong fortnight, and came back as "contradicts".
	//
	// A farmer who files under a district declaration is not asserting the
	// drought. The district asserted it; they are claiming their share of it.
	var declaredWindow weather.Span
	if req.SurveyEventID != "" && s.Events != nil {
		if ev, err := s.Events.Get(ctx, req.SurveyEventID); err == nil && ev != nil {
			if agentReq.CalamityType == "" {
				agentReq.CalamityType = ev.CalamityType
			}
			if ev.DeclaredAt != nil {
				agentReq.CalamityDeclaredBy = "district"
			}
			declaredWindow = weather.Span{
				From: ev.IncidentStart,
				To:   ev.IncidentEnd,
			}
		} else if err != nil {
			s.Log.Warn("reading the event for an assessment",
				"event", req.SurveyEventID, "error", err)
		}
	}

	// Everything the figure can be grounded in.
	//
	// Assembled after the ground imagery because the calamity type comes from
	// the report, and susceptibility cannot be looked up without it. None of
	// it can fail the assessment: the assembler returns a packet whose Absent
	// list explains what it could not get, and an assessment made from the
	// claim and the photographs alone is what this platform did before any of
	// these sources existed.
	pack := s.assembleEvidence(ctx, req, parcel, sowingDate, extent, sat, ndvi,
		agentReq, declaredWindow)
	if pack != nil {
		agentReq.Evidence = pack
	}

	agentResp, err := s.AI.Assess(ctx, agentReq)
	if err != nil {
		return "", agentFailure(err)
	}

	verified := assessment.Verify(agentResp.Result, agentReq)

	// The field does not hold the crop it was claimed for.
	//
	// Raised here rather than left to the panel: the survey walk is the only
	// evidence that can answer it, and a check that depends on a model
	// choosing to mention it in prose is not a check. The panel may say it
	// too; a duplicate flag on a claim of this kind is not a problem.
	if ind := pack.CropMismatch(); ind != nil {
		verified.FraudIndicators = append(verified.FraudIndicators, *ind)
		verified.BackendGuardrails = append(verified.BackendGuardrails,
			"crop_not_as_declared")
		s.Log.Warn("the survey video does not show the declared crop",
			"report", req.CropLossReportID, "farm_id", req.FarmID,
			"declared", pack.GroundCrop.Declared, "seen", pack.GroundCrop.Seen,
			"confidence", pack.GroundCrop.Confidence, "severity", ind.Severity)
	}

	verified.Specialists = agentResp.Specialists
	verified.Degraded = agentResp.Degraded
	verified.DegradedReason = agentResp.DegradedReason
	s.withholdProviderErrors(ctx, &verified)

	// The specification's composite, computed beside the panel's figure rather
	// than instead of it. Nil until a vision pipeline exists; where it is
	// present and lands far from the panel, TriageFor sends the claim to a
	// person. See internal/assessment/fusion.go for why it does not publish.
	verified.Fusion = assessment.Reconcile(assessment.FusionInput{
		Ground:     groundScoreOf(pack),
		SSat:       sat.RRVLPercent,
		SDEM:       waterloggingScore(ter),
		CloudCover: opticalLoss(sat, sowingDate, req.IncidentDate),
	})

	triage := assessment.TriageFor(verified)

	a := &store.Assessment{
		FarmerID:     req.FarmerID,
		FarmID:       req.FarmID,
		SurveyNumber: parcel.SurveyNumber,
		// Copied from the parcel, because this is what every later
		// authorization decision about this record will be made against.
		VillageLGDCode:      parcel.VillageLGDCode,
		SubDistrictLGDCode:  parcel.SubDistrictLGDCode,
		DistrictLGDCode:     parcel.DistrictLGDCode,
		StateLGDCode:        rec.StateLGDCode,
		SowingDate:          sowingDate,
		IncidentDate:        req.IncidentDate,
		CropName:            cropName,
		OwnerExtentHectares: extent,
		Assessment:          verified,
		Satellite:           sat,
		Terrain:             ter,
		Triage:              triage,
		NDVISeries:          ndvi.TimeSeries,
		CreatedBy:           principal.UserID,
		CropLossReportID:    req.CropLossReportID,
		SurveyEventID:       req.SurveyEventID,
		// What this figure was measured against, and what could not be
		// obtained. Kept with the assessment so it can be reproduced at an
		// appeal: the same percentage derived against three prior seasons and
		// a corroborated neighbourhood is a different claim from one derived
		// against nothing.
		Baseline: marshalPacket(pack, s.Log),
		Images: &store.Images{
			NDVIMidDate:   ndvi.MidDateImage,
			NDVILastVisit: ndvi.LastVisitImage,
			TerrainSlope:  terrain.Slope.Base64Map,
			TerrainAspect: terrain.Aspect.Base64Map,
		},
	}

	id, err = s.Store.Put(ctx, a)
	if err != nil {
		// A store failure is a database error, and a database error names our
		// tables and constraints. Logged here; the officer is told to wait.
		s.Log.Error("storing an assessment",
			"request_id", RequestID(ctx), "error", err)
		return "", &assessFailure{Code: "STORE_FAILED",
			Message: "the assessment could not be stored", Retryable: true}
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: audit.ActionAssessmentCreated, Outcome: audit.OutcomeAllow,
		ActorUserID: principal.UserID, RequestID: RequestID(ctx),
		ResourceType: "assessment", ResourceID: id,
		JurisdictionCode: parcel.VillageLGDCode,
		Detail: map[string]any{
			"farm_id":        req.FarmID,
			"farmer_id":      req.FarmerID,
			"incident_date":  req.IncidentDate,
			"triage":         triage,
			"crop_presence":  verified.CropPresence,
			"model":          verified.Model,
			"prompt_version": verified.PromptVersion,
			"ground_images":  groundUsed,
			"ground_held":    groundHeld,
		},
	}.FromContext(ctx))

	damage := "-"
	if verified.DamagePercent != nil {
		damage = fmt.Sprintf("%.2f", *verified.DamagePercent)
	}
	s.Log.Info("assessment created",
		"id", id,
		"farmer_id", req.FarmerID,
		"farm_id", req.FarmID,
		"crop_presence", verified.CropPresence,
		"damage", damage,
		"confidence", verified.Confidence,
		"triage", triage,
		"fraud", len(verified.FraudIndicators),
		"ground_images", groundUsed,
		"panel_ran", ranSpecialists(agentResp.Specialists),
		"panel_degraded", agentResp.Degraded,
		"panel_ran", ranSpecialists(agentResp.Specialists),
		"panel_degraded", agentResp.Degraded,
		"observations", sat.ObservationCount,
		"model", verified.Model,
		"agent_ms", agentResp.LatencyMS,
		"backend_guardrails", verified.BackendGuardrails)

	return id, nil
}

// assembleEvidence gathers the denominators, the weather, the crop's stage,
// radar, the claim history and any counts an officer took.
//
// Returns nil when no assembler is configured, which is the state this
// platform was in before these sources existed and is still a valid
// deployment. Never returns an error: every source is evidence that may be
// absent, and the packet says which were.
func (s *Server) assembleEvidence(
	ctx context.Context,
	req createAssessmentRequest,
	parcel *agristack.Parcel,
	sowingDate string,
	extent float64,
	sat assessment.Satellite,
	ndvi *satellite.NDVIResult,
	agentReq ai.AssessRequest,
	declaredWindow weather.Span,
) *evidence.Packet {
	if s.Evidence == nil {
		return nil
	}

	in := evidence.Input{
		ReportID:       req.CropLossReportID,
		FarmerID:       req.FarmerID,
		FarmID:         req.FarmID,
		SurveyNumber:   parcel.SurveyNumber,
		VillageCode:    parcel.VillageLGDCode,
		CropName:       cropNameOf(agentReq),
		CalamityType:   agentReq.CalamityType,
		SowingDate:     sowingDate,
		IncidentDate:   req.IncidentDate,
		DeclaredWindow: declaredWindow,
		Geometry: satellite.Geometry{
			Type:        parcel.PlotGeometry.Type,
			Coordinates: parcel.PlotGeometry.Coordinates,
		},
		PeakNDVI:    sat.BaselineNDVI,
		RRVL:        sat.RRVLPercent,
		FieldAreaHa: extent,
	}
	if agentReq.Crop != nil {
		in.Season, in.CropYear = agentReq.Crop.Season, agentReq.Crop.Year
	}
	if agentReq.Parcel.CentroidLat != nil && agentReq.Parcel.CentroidLon != nil {
		in.Lat, in.Lon = *agentReq.Parcel.CentroidLat, *agentReq.Parcel.CentroidLon
		in.HasPoint = true
	}
	if from, to, ok := longestGap(ndvi.TimeSeries); ok {
		in.OpticalGapFrom, in.OpticalGapTo = from, to
	}

	// The series the sowing-date check reads, in the agronomy package's shape
	// so that package needs no dependency on the analytics client.
	series := make([]agronomy.Observation, 0, len(ndvi.TimeSeries))
	for _, pt := range ndvi.TimeSeries {
		series = append(series, agronomy.Observation{Date: pt.Date, NDVI: pt.NDVI})
	}

	return s.Evidence.Assemble(ctx, in, series)
}

// cropNameOf is the crop the assessment is about, where one was named.
func cropNameOf(r ai.AssessRequest) string {
	if r.Crop != nil {
		return r.Crop.Name
	}
	return ""
}

// groundScoreOf is the survey video's measurement, where one was taken.
func groundScoreOf(p *evidence.Packet) *groundcv.Score {
	if p == nil {
		return nil
	}
	return p.GroundCV
}

// waterloggingScore turns slope-derived susceptibility into the
// specification's S_DEM, in [0,100].
//
// Susceptibility, not damage, and the fusion weights it lowest of the three
// for exactly that reason: flat ground under a thriving crop is not a loss.
// It carries 0.15 to 0.25 of the composite and never decides one.
func waterloggingScore(t assessment.Terrain) float64 {
	switch t.WaterloggingRisk {
	case "Very High":
		return 90
	case "High":
		return 70
	case "Moderate":
		return 45
	case "Low":
		return 20
	default:
		return 5
	}
}

// opticalLoss estimates the share of the optical record cloud took away.
//
// # Why this is a proxy and is labelled one
//
// The specification's fusion selects its weights on cloud_cover, which in a
// full implementation is the per-scene cloud fraction the optical provider
// reports. This platform does not carry that: it keeps the passes that came
// back usable and discards the rest, so what it can honestly say is how many
// of the passes it should have had are missing.
//
// Sentinel-2 revisits every five days with both satellites in orbit. Expected
// passes across the window, against the ones that survived cloud screening,
// gives a figure that moves the way cloud cover moves and is derived from
// evidence the platform actually holds. It decides which weight branch is
// taken, not any percentage, so a proxy is proportionate here in a way it
// would not be if it fed the number directly.
func opticalLoss(sat assessment.Satellite, from, to string) float64 {
	days, err := daysBetweenDates(from, to)
	if err != nil || days <= 0 {
		return 0
	}
	const revisitDays = 5.0
	expected := float64(days) / revisitDays
	if expected < 1 {
		return 0
	}
	lost := 1 - float64(sat.ObservationCount)/expected
	return math.Max(0, math.Min(1, lost))
}

// daysBetweenDates is the span between two yyyy-mm-dd dates.
func daysBetweenDates(from, to string) (int, error) {
	a, err := time.Parse("2006-01-02", from)
	if err != nil {
		return 0, err
	}
	b, err := time.Parse("2006-01-02", to)
	if err != nil {
		return 0, err
	}
	return int(b.Sub(a).Hours() / 24), nil
}

// longestGap finds the widest stretch the optical series could not see.
//
// Radar is asked whether anything watched it. A two-month hole in kharif is
// ordinary, and "nothing observed this field for nine weeks" is a materially
// different assessment from "the field was observed and did not change".
func longestGap(ts []satellite.NDVIPoint) (from, to string, ok bool) {
	if len(ts) < 2 {
		return "", "", false
	}
	sorted := append([]satellite.NDVIPoint(nil), ts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Date < sorted[j].Date })

	var best int
	for i := 1; i < len(sorted); i++ {
		a, err1 := time.Parse("2006-01-02", sorted[i-1].Date)
		b, err2 := time.Parse("2006-01-02", sorted[i].Date)
		if err1 != nil || err2 != nil {
			continue
		}
		if d := int(b.Sub(a).Hours() / 24); d > best {
			best, from, to = d, sorted[i-1].Date, sorted[i].Date
		}
	}
	return from, to, best > 0
}

// marshalPacket encodes the evidence for storage.
//
// A packet that will not encode is logged and dropped rather than failing the
// assessment: the assessment itself is sound, and losing the record of what
// it stood on is a smaller harm than losing the assessment.
func marshalPacket(p *evidence.Packet, log *slog.Logger) json.RawMessage {
	if p == nil {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		log.Error("encoding the evidence packet for storage", "error", err)
		return nil
	}
	return raw
}

// parcelOf resolves a parcel and checks the caller may act on it.
//
// The registry is the only thing that can say whose parcel a survey number
// is, so a farmer tracing a boundary is checked against it rather than
// against anything they sent.
func (s *Server) parcelOf(
	r *http.Request, p *rbac.Principal, farmerID, farmID string,
) (*agristack.FarmerRecord, *agristack.Parcel, error) {
	// A farmer is one of the farmers they may act for -- their own, or one
	// they have confirmed on this handset and hold a grant for. Naming
	// anybody else is refused rather than quietly rewritten, for the same
	// reason filing is: one mobile number may now act for several people, and
	// silently substituting one for another puts a boundary under the wrong
	// person's land.
	//
	// An officer names the farmer, because the registry has no index from
	// parcel back to owner and scanning every record to build one would not
	// survive a real AgriStack.
	if p.IsFarmer() {
		acting, err := s.actingFarmer(r, p, farmerID)
		if err != nil {
			return nil, nil, errNotYours
		}
		farmerID = acting
	}
	if farmerID == "" {
		return nil, nil, fmt.Errorf(
			"%w: name the farmer whose parcel this is", errNotFound)
	}

	rec, err := s.Registry.FarmerRecord(r.Context(), farmerID)
	if errors.Is(err, agristack.ErrUnavailable) {
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s", errNotFound, farmerID)
	}
	parcel, ok := rec.ParcelByFarmID(farmID)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s is not a parcel of %s",
			errNotFound, farmID, farmerID)
	}
	if p.IsFarmer() && farmerID != rec.Farmer.FarmerID {
		return nil, nil, errNotYours
	}
	return rec, parcel, nil
}

var (
	errNotFound = errors.New("not found")
	errNotYours = errors.New("that parcel is not yours")
)

// writeRegistryError turns a parcel lookup failure into a response.
func (s *Server) writeRegistryError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	switch {
	case errors.Is(err, agristack.ErrUnavailable):
		writeRegistryUnavailable(w)
	case errors.Is(err, errNotYours):
		writeError(w, http.StatusForbidden, "NOT_YOUR_PARCEL",
			"that parcel belongs to another farmer")
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "PARCEL_NOT_FOUND", err.Error())
	default:
		writeError(w, http.StatusNotFound, "PARCEL_NOT_FOUND", err.Error())
	}
}

// writeFarmerRecordError answers a registry read that did not return a record.
//
// Unavailable is its own answer. A registry that is down used to read as "no
// such farmer" -- to an officer, a farmer who does not exist; to the lookup
// lockout, a wrong guess counted against the person asking.
func (s *Server) writeFarmerRecordError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agristack.ErrUnavailable):
		writeRegistryUnavailable(w)
	case errors.Is(err, agristack.ErrNotFound):
		writeError(w, http.StatusNotFound, "FARMER_NOT_FOUND",
			"no registry record for that farmer id")
	default:
		s.Log.Error("reading a registry record", "error", err)
		writeError(w, http.StatusInternalServerError, "REGISTRY_FAILED",
			"the farmer record could not be read")
	}
}

func writeRegistryUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "30")
	writeError(w, http.StatusServiceUnavailable, "REGISTRY_UNAVAILABLE",
		"the farmer registry is not answering; try again shortly")
}

// buildAgentRequest marshals the evidence packet for the assessment agent.
//
// Provenance is passed explicitly — a mocked boundary or a synthetic crop
// declaration is labelled as such, because the agent cannot infer it and both
// change what the evidence can support.
func (s *Server) buildAgentRequest(
	req createAssessmentRequest,
	parcel *agristack.Parcel,
	crops []agristack.CropSown,
	sowingDate string,
	extent float64,
	sat assessment.Satellite,
	ter assessment.Terrain,
	ndvi *satellite.NDVIResult,
	terrain *satellite.TerrainResult,
	boundarySource string,
) ai.AssessRequest {
	out := ai.AssessRequest{
		AssessmentID: req.FarmID + "@" + req.IncidentDate,
		IncidentDate: req.IncidentDate,
		SowingDate:   sowingDate,
		// A claim brings the farmer's clips; a console screening brings
		// none, and was never going to.
		GroundExpected: req.CropLossReportID != "",
		Parcel: ai.Parcel{
			SurveyNumber:        parcel.SurveyNumber,
			FarmID:              parcel.FarmID,
			VillageLGDCode:      parcel.VillageLGDCode,
			DistrictLGDCode:     parcel.DistrictLGDCode,
			OwnerExtentHectares: extent,
			// The recorded provenance, not an assumption. A boundary the
			// farmer walked, one an officer confirmed, and one the registry
			// supplied support different claims, and the agent is told which
			// this is so it can say that derived figures inherit it.
			BoundarySource: boundarySource,
		},
		Satellite: ai.SatelliteEvidence{
			TimeSeries:       toAINDVI(ndvi.TimeSeries),
			BaselineNDVI:     &sat.BaselineNDVI,
			BaselineDate:     sat.BaselineDate,
			IncidentNDVI:     &sat.IncidentNDVI,
			DeltaNDVI:        &sat.DeltaNDVI,
			RRVLPercent:      &sat.RRVLPercent,
			ObservationCount: sat.ObservationCount,
			LargestGapDays:   sat.LargestGapDays,
		},
		Terrain: &ai.TerrainEvidence{
			MeanSlopeDegrees: ter.MeanSlopeDegrees,
			Classification:   ter.Classification,
			WaterloggingRisk: ter.WaterloggingRisk,
			ElevationMeanM:   &terrain.Elevation.MeanMeters,
			ElevationMinM:    &terrain.Elevation.MinMeters,
			ElevationMaxM:    &terrain.Elevation.MaxMeters,
			AspectCardinal:   terrain.Aspect.CardinalDirection,
		},
	}

	// Where this land actually is, so the weather specialist can look up what
	// the sky did over it rather than over the district headquarters.
	//
	// PlotGeometry is nil for every farmer live AgriStack can reach, so this
	// is the ordinary case, not the exceptional one. It costs the weather
	// specialist, which records why it was skipped, and nothing else.
	if parcel.PlotGeometry != nil {
		if lat, lon, ok := centroidOf(parcel.PlotGeometry.Coordinates); ok {
			out.Parcel.CentroidLat = &lat
			out.Parcel.CentroidLon = &lon
		}
	}

	if days, err := daysBetween(sowingDate, req.IncidentDate); err == nil {
		out.WindowDays = days
	}

	if len(crops) > 0 {
		c := crops[0]
		out.Crop = &ai.Crop{
			Name:           c.CropName,
			SowingDate:     c.SowingDate,
			Season:         c.Season,
			Year:           c.Year,
			IrrigationType: c.IrrigationType,
			DeclaredBy:     "mock",
		}
	}

	// Satellite overlays only. These are not ground truth and must not unlock
	// a damage figure; the agent is told so explicitly.
	if ndvi.MidDateImage != nil {
		out.Images = append(out.Images, ai.Image{
			Kind:    ai.ImageNDVIMid,
			Date:    ndvi.MidDateImage.Date,
			DataURI: ndvi.MidDateImage.Base64,
		})
	}
	if ndvi.LastVisitImage != nil {
		out.Images = append(out.Images, ai.Image{
			Kind:    ai.ImageNDVILast,
			Date:    ndvi.LastVisitImage.Date,
			DataURI: ndvi.LastVisitImage.Base64,
		})
	}
	if terrain.Slope.Base64Map != "" {
		out.Images = append(out.Images, ai.Image{
			Kind: ai.ImageTerrainSlope, DataURI: terrain.Slope.Base64Map,
		})
	}
	if terrain.Aspect.Base64Map != "" {
		out.Images = append(out.Images, ai.Image{
			Kind: ai.ImageTerrainAspect, DataURI: terrain.Aspect.Base64Map,
		})
	}

	return out
}

// groundImagery turns a report's captures into images the agent can look at.
//
// Returns how many were used and how many were held back, so both numbers
// reach the audit entry: "the model saw two photographs and was not shown a
// third" is a materially different assessment from "the model saw two".
// groundEvidence is what a filed report contributes to an assessment: the
// photographs that may be shown to the panel, and the claim they were filed
// against. The claim travels with them because the weather and phenology
// specialists are both asking a question about a named calamity.
type groundEvidence struct {
	Report *report.Report
	Images []ai.Image
	Used   int
	Held   int
}

func (s *Server) groundImagery(
	ctx context.Context, reportID string, p *rbac.Principal,
) (groundEvidence, error) {
	rep, err := s.Reports.Get(ctx, reportID, p.Scope())
	if err != nil {
		return groundEvidence{}, err
	}

	caps, err := s.Captures.ForReport(ctx, rep.ID)
	if err != nil {
		return groundEvidence{}, err
	}

	var out []ai.Image
	held := 0
	seen := map[string]bool{}

	for _, c := range caps {
		// Clips are not photographs, and were being sent as though they were.
		//
		// Three things followed from that: a twelve-megabyte video went into
		// the prompt as a base64 data URI, its caption called it a
		// photograph, and HasGroundImagery counted it -- so a damage figure
		// could be unlocked by footage the panel was never built to read. The
		// clips are measured by the vision pipeline, which is where the walk's
		// weight belongs (docs/EVIDENCE_REVALIDATION.md, V3).
		if survey.IsVideo(c.ContentType) {
			// One frame out of it, which is a photograph the panel can read.
			//
			// Not the clip. Sending the footage itself is the mistake this
			// guard was written for. But the walk records only video now --
			// the still that used to be taken beside each clip is gone -- so
			// skipping video outright left the assessment with no ground
			// imagery at all, and every damage figure was discarded by
			// damage_requires_ground_imagery however good the evidence was.
			//
			// ImageSurveyKeyfr exists for precisely this and already counts
			// as ground imagery; it simply had nothing producing it after the
			// still was removed.
			if img, ok := s.keyframeOf(ctx, c); ok {
				if seen[c.SHA256] {
					held++
					continue
				}
				seen[c.SHA256] = true
				out = append(out, img)
			} else {
				held++
			}
			continue
		}

		// The same bytes twice is one photograph, however many times it was
		// submitted. Sending both would let a duplicate weigh double.
		if !c.UsableAsEvidence() || seen[c.SHA256] {
			held++
			continue
		}
		uri, err := s.Captures.DataURI(ctx, c.ID)
		if err != nil {
			s.Log.Warn("reading capture media", "id", c.ID, "error", err)
			held++
			continue
		}
		seen[c.SHA256] = true
		out = append(out, ai.Image{
			Kind:    ai.ImageFieldPhoto,
			Date:    c.CapturedAt.UTC().Format("2006-01-02"),
			DataURI: uri,
			Caption: captureCaption(c),
		})
	}
	return groundEvidence{Report: rep, Images: out, Used: len(out), Held: held}, nil
}

// captureCaption tells the model what it is looking at and where it was taken.
func captureCaption(c survey.Capture) string {
	label := map[survey.Kind]string{
		survey.KindWideShot:      "wide view of the plot",
		survey.KindDamageCloseup: "close view of the damage",
		survey.KindCropCloseup:   "close view of the crop",
		survey.KindBoundaryPoint: "a point on the plot boundary",
	}[c.Kind]

	out := "Farmer-captured field photograph: " + label +
		", taken inside the parcel boundary on " +
		c.CapturedAt.UTC().Format("2 January 2006")
	if c.Caption != "" {
		out += ". The farmer describes it as: " + c.Caption
	}
	// The caveats travel with the image rather than being resolved away.
	if caveats := c.Caveats(); len(caveats) > 0 {
		out += ". Note: " + strings.Join(caveats, "; ") + "."
	}
	return out
}

// overview is the rollup behind every role's landing page.
//
// One endpoint rather than one per role. What a State Officer and a Talathi
// see differs because their jurisdiction differs, not because the server
// decided what their rank should be shown -- which keeps the scoping in the
// one predicate that is already tested rather than in a switch on role.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	principal := rbac.FromContext(r.Context())

	ov, err := s.Reports.Overview(r.Context(), principal.Scope(), r.URL.Query().Get("event_id"))
	if err != nil {
		s.Log.Error("building overview", "error", err)
		writeError(w, http.StatusInternalServerError, "OVERVIEW_FAILED",
			"the summary could not be built")
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

// adjudicationQueue answers "which reports are waiting on me".
//
// The platform could already adjudicate a report and show one by reference.
// What it could not do was find them, so reports sat in verified and the
// farmer heard nothing.
func (s *Server) adjudicationQueue(w http.ResponseWriter, r *http.Request) {
	principal := rbac.FromContext(r.Context())

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	result, err := s.Reports.AdjudicationQueue(r.Context(), principal.Scope(), pg)
	if err != nil {
		s.Log.Error("reading adjudication queue", "error", err)
		writeError(w, http.StatusInternalServerError, "QUEUE_FAILED",
			"the adjudication queue could not be read")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ranSpecialists counts the panel members that produced a finding.
func ranSpecialists(s []ai.Specialist) int {
	n := 0
	for _, x := range s {
		if x.Ran {
			n++
		}
	}
	return n
}

func toAINDVI(pts []satellite.NDVIPoint) []ai.NDVIPoint {
	out := make([]ai.NDVIPoint, 0, len(pts))
	for _, p := range pts {
		out = append(out, ai.NDVIPoint{Date: p.Date, NDVI: p.NDVI})
	}
	return out
}

func daysBetween(a, b string) (int, error) {
	ta, err := time.Parse("2006-01-02", a)
	if err != nil {
		return 0, err
	}
	tb, err := time.Parse("2006-01-02", b)
	if err != nil {
		return 0, err
	}
	return int(tb.Sub(ta).Hours() / 24), nil
}

// writeAgentError distinguishes an unconfigured agent, which is an operator
// action, from a genuine failure.
func (s *Server) writeAgentError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	var ae *ai.Error
	if errors.As(err, &ae) {
		if ae.Unavailable() {
			writeError(w, http.StatusServiceUnavailable, "AI_UNAVAILABLE", ae.Detail)
			return
		}
		writeError(w, http.StatusBadGateway, "AI_FAILED", ae.Detail)
		return
	}
	writeError(w, http.StatusBadGateway, "AI_UNREACHABLE", err.Error())
}

func (s *Server) listAssessments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	result, err := s.Store.List(r.Context(), store.Filter{
		// The scope comes from the token, never from the query string. A
		// farmer_id parameter narrows a listing; it cannot widen one.
		Scope:    rbac.FromContext(r.Context()).Scope(),
		FarmerID: q.Get("farmer_id"),
		FarmID:   q.Get("farm_id"),
		Triage:   q.Get("triage"),
		Page:     pg,
	})
	if err != nil {
		s.Log.Error("listing assessments", "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED",
			"the request could not be completed")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getAssessment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	a, err := s.Store.Get(ctx, id, rbac.FromContext(ctx).Scope())
	if errors.Is(err, store.ErrNotFound) {
		// Out of scope and non-existent answer identically; see store.ErrNotFound.
		writeError(w, http.StatusNotFound, "ASSESSMENT_NOT_FOUND",
			"no assessment with that id")
		return
	}
	if err != nil {
		s.Log.Error("reading assessment", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED",
			"the request could not be completed")
		return
	}

	// Reads of a named farmer record are logged. Who looked at what is the
	// question a grievance arrives with, and it cannot be reconstructed
	// afterwards from a request log that kept only paths.
	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionAssessmentRead, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "assessment", ResourceID: id,
		JurisdictionCode: a.VillageLGDCode,
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, a)
}

// writeUpstreamError surfaces the satellite engine's own error code so a
// client can tell a bad polygon from an outage.
func (s *Server) writeUpstreamError(w http.ResponseWriter, module string, err error) {
	// The circuit is open: the engine has failed repeatedly and this request
	// was not sent. Said plainly, because a connection error surfaced to an
	// officer reads as though they did something wrong, and because "try
	// shortly" is genuinely the right action here -- unlike a rejected
	// geometry, which will be rejected again.
	if errors.Is(err, satellite.ErrUpstreamDown) {
		writeError(w, http.StatusServiceUnavailable, "SATELLITE_DOWN",
			"The satellite analysis service is not responding. "+
				"Existing assessments and reports are unaffected; "+
				"try this one again shortly.")
		return
	}

	var se *satellite.Error
	if errors.As(err, &se) {
		status := http.StatusBadGateway
		if se.HTTPStatus == http.StatusBadRequest || se.HTTPStatus == http.StatusRequestEntityTooLarge {
			status = se.HTTPStatus
		}
		writeError(w, status, "SATELLITE_"+se.Code,
			fmt.Sprintf("%s module: %s", module, se.Message))
		return
	}
	// The module name is ours to say; the error under it is the satellite
	// client's and reads like a stack trace.
	writeError(w, http.StatusBadGateway, "SATELLITE_UNAVAILABLE",
		fmt.Sprintf("The %s service is not responding. Try again shortly.", module))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// looksInternal reports whether a message was written for a programmer.
//
// The list itself lives in internal/platform/saferr, because the evidence
// packet needs exactly the same judgement and a second copy would drift. See
// that package for why a denylist is the right trade here.
// looksInternal covers both families: a Go library describing itself, and a
// remote API's error body relayed through one of ours.
func looksInternal(message string) bool {
	return saferr.LooksInternal(message) || saferr.LooksLikeProviderError(message)
}

// fallbackFor is what a caller is told when the real reason is not theirs to
// read. One clear sentence per status, in the second person, saying what they
// can do about it — which is the part a bare status code never says.
func fallbackFor(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "Some of the details sent were not valid. Check them and try again."
	case http.StatusUnauthorized:
		return "Your session has ended. Sign in again to continue."
	case http.StatusForbidden:
		return "You do not have permission to do that."
	case http.StatusNotFound:
		return "That record could not be found."
	case http.StatusConflict:
		return "That has already been changed by someone else. Reload and try again."
	case http.StatusRequestEntityTooLarge:
		return "That file is too large to upload."
	case http.StatusTooManyRequests:
		return "Too many attempts. Wait a moment and try again."
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "A service this depends on is not responding. Try again shortly."
	default:
		if status >= 500 {
			return "Something went wrong at our end. Try again in a moment."
		}
		return "That request could not be completed."
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	if message == "" || looksInternal(message) {
		message = fallbackFor(status)
	}
	noteError(w, code, message)
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// withholdProviderErrors keeps a remote API's error body off the record.
//
// The second of two layers: the reasoning service describes its own failures
// rather than repeating them. This runs on the way into the database, because
// everything below is printed on a compensation record, shown to farmers and
// read at an appeal -- and it once carried Google's raw 403 response, with this
// server's public IP and the project number, in every assessment's reasoning.
//
// Logged when it fires, because firing means the first layer let something
// through, and that is a defect to find rather than a condition to live with.
func (s *Server) withholdProviderErrors(ctx context.Context, v *assessment.Verified) {
	const errorSentence = "the model service could not be used; the details are in the service log"
	const reasoningSentence = "This assessment was produced without the reasoning panel, " +
		"because the model service could not be used. No damage figure is " +
		"stated. The details are in the service log rather than on this record."

	var fired []string
	if out, ok := saferr.Withheld(v.Reasoning, reasoningSentence); ok {
		v.Reasoning = out
		fired = append(fired, "reasoning")
	}
	if out, ok := saferr.Withheld(v.CropPresenceReasoning, errorSentence); ok {
		v.CropPresenceReasoning = out
		fired = append(fired, "crop_presence_reasoning")
	}
	if out, ok := saferr.Withheld(v.DegradedReason, errorSentence); ok {
		v.DegradedReason = out
		fired = append(fired, "degraded_reason")
	}
	for i := range v.Specialists {
		if out, ok := saferr.Withheld(v.Specialists[i].Error, errorSentence); ok {
			v.Specialists[i].Error = out
			fired = append(fired, "specialist:"+v.Specialists[i].Agent)
		}
	}
	if len(fired) > 0 && s.Log != nil {
		s.Log.Warn("withheld provider error text from an assessment record",
			"request_id", RequestID(ctx), "fields", fired)
	}
}

// mayReadParcel decides whether the caller may be told where a parcel is.
//
// # Why a parcel's position is worth guarding
//
// The boundary and the sampling points are the shape and location of an
// identified person's land. parcel:read is held by every farmer and carries no
// jurisdiction, so the two read routes returned either for any farm id that
// was asked for -- and a farm id is an AgriStack identifier printed on
// documents and known around a village.
//
// # The rule
//
// The same one the write path has always used. A farmer reaches the parcels of
// the farmers they may act for; an officer reaches the villages they are
// posted to. Anything else is answered as not found rather than as refused: a
// caller who supplied the id learns nothing from being told it exists.
//
// Writes the response and returns an error when the answer is no, so a handler
// can return on the spot.
func (s *Server) mayReadParcel(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	farmID := r.PathValue("farmID")

	if p.IsFarmer() {
		if _, _, err := s.parcelOf(r, p, r.URL.Query().Get("farmer_id"), farmID); err != nil {
			writeError(w, http.StatusNotFound, "PARCEL_NOT_FOUND",
				"no parcel with that id for you")
			return errNotYours
		}
		return nil
	}

	// An officer is posted somewhere, and the parcel's own village is what
	// says whether this is theirs to look at. Read from the boundary rather
	// than the registry: the registry has no index from parcel back to owner,
	// and a parcel with no boundary recorded has no position to leak.
	village, err := s.Captures.VillageOfParcel(ctx, farmID)
	if err != nil || village == "" {
		// Nothing recorded for it. Nothing to protect, and nothing to say.
		return nil
	}
	if err := s.assertInScope(r, p, village, "parcel", farmID); err != nil {
		writeError(w, http.StatusNotFound, "PARCEL_NOT_FOUND",
			"no parcel with that id in your area")
		return err
	}
	return nil
}

// readDB is the pool for a staleness-tolerant read.
func (s *Server) readDB() *db.Pool {
	if p := s.Reads.Read(); p != nil {
		return p
	}
	return s.DB
}

// NamedPool is one of the instance's connection pools.
type NamedPool struct {
	Name string
	Pool *db.Pool
}

// ClipFrames yields one still frame from a stored clip.
//
// An interface rather than the vision client, for the usual reason: a
// deployment without a ground vision module still prints reports, and a test
// should not need an HTTP service to render one.
type ClipFrames interface {
	// Frame returns a data URI for one frame of the clip. sha256 is the clip's
	// own content hash, used to cache the result; an empty one skips the cache
	// rather than sharing a key with another clip.
	Frame(ctx context.Context, captureID, sha256 string) (string, error)
}

// keyframeOf pulls one still out of a station clip, for the panel to read.
//
// Returns false where no frame could be had -- no vision service configured,
// a clip the decoder cannot open, a store that will not answer. The caller
// counts it as held back rather than failing the assessment: a walk whose
// frames could not be extracted is a walk with no ground imagery, which the
// verification rules already handle by refusing a damage figure and saying so.
func (s *Server) keyframeOf(
	ctx context.Context, c survey.Capture,
) (ai.Image, bool) {
	if s.Frames == nil || !c.UsableAsEvidence() {
		return ai.Image{}, false
	}
	uri, err := s.Frames.Frame(ctx, c.ID, c.SHA256)
	if err != nil || uri == "" {
		s.Log.Warn("extracting a keyframe for the panel",
			"capture", c.ID, "error", err)
		return ai.Image{}, false
	}
	return ai.Image{
		Kind:    ai.ImageSurveyKeyfr,
		Date:    c.CapturedAt.UTC().Format("2006-01-02"),
		DataURI: uri,
		Caption: keyframeCaption(c),
	}, true
}

// keyframeCaption says where the frame came from and that it is a frame.
//
// The panel weighs a still from a directed point differently from a
// photograph the farmer chose to take, and it can only do that if the caption
// says which this is.
func keyframeCaption(c survey.Capture) string {
	where := "the survey walk"
	if c.StationNo != nil {
		where = fmt.Sprintf("point %d of the survey walk", *c.StationNo)
	}
	return fmt.Sprintf("a frame from the clip recorded at %s", where)
}
