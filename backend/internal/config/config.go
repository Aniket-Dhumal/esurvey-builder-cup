// Package config loads and validates the service's configuration.
//
// Two rules shape this package.
//
// First, every problem is reported at once. An operator bringing the stack up
// for the first time should see the complete list of what is missing, not
// discover it one restart at a time.
//
// Second, the safe default differs by environment. A missing signing key in
// development is an inconvenience worth working around with an ephemeral key;
// in production it is a refusal to start. Anything that would silently weaken
// security in a real deployment is a hard error there.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment names the deployment this process believes it is running in.
type Environment string

const (
	Development Environment = "development"
	Staging     Environment = "staging"
	Production  Environment = "production"
)

// Hardened reports whether production security rules apply. Staging is
// included deliberately: a staging environment that is weaker than production
// tests something other than what will be shipped.
func (e Environment) Hardened() bool {
	return e == Production || e == Staging
}

// Config is the fully resolved configuration.
type Config struct {
	Env  Environment
	Addr string

	// CORSOrigins is an explicit allowlist. Credentials are attached to
	// browser calls, so a wildcard is not an option.
	CORSOrigins []string

	// TLSCertFile and TLSKeyFile terminate TLS in this process.
	//
	// Optional, and the alternative is not "no TLS" -- it is a proxy that
	// terminates instead, declared by setting TrustedProxyHops. A hardened
	// environment must have one or the other, because everything this service
	// carries is a bearer token or a named farmer's land.
	TLSCertFile string
	TLSKeyFile  string

	// TrustedProxyHops is how many reverse proxies sit in front of this
	// service. The client IP is read that many entries from the right of
	// X-Forwarded-For; anything else is attacker-controlled. Zero means the
	// header is ignored entirely.
	TrustedProxyHops int

	// MetricsToken guards /metrics. Empty restricts it to loopback rather
	// than opening it: see api.Server.MetricsToken.
	MetricsToken string

	Database Database
	Redis    Redis
	Auth     Auth

	// Upstreams.
	MockDir string
	// EvidenceDir holds farmer-captured media when no bucket is configured.
	// The development default, and the fallback for a single-node install.
	EvidenceDir string

	// EvidenceBucket, when set, sends evidence to Cloud Storage instead.
	//
	// Photographs of identified people's land are the most sensitive thing
	// this platform holds, so the bucket is expected to already enforce
	// uniform access, public-access prevention, versioning and a region the
	// data is allowed to be in. The application does not configure any of
	// that: a bucket built once and audited is safer than one every
	// deployment could quietly weaken. See docs/EVIDENCE_STORAGE.md.
	EvidenceBucket string
	// EvidencePrefix separates a trial's evidence from production's inside
	// one bucket, without depending on anyone remembering to.
	EvidencePrefix   string
	SatelliteURL     string
	SARServiceURL    string
	VisionServiceURL string
	SatelliteAPIKey  string
	AIServiceURL     string

	// PDFServiceURL is the Chromium-behind-HTTP converter that turns a
	// print-styled page into a PDF.
	//
	// Defaulted to the compose service name rather than left empty, because a
	// deployment that has a converter has it there, and one that does not loses
	// only the PDF: the assessment report was always meant to be printed from
	// its HTML, and every other export format is produced in process. See
	// internal/pdf, which returns an error the caller falls back from rather
	// than failing.
	PDFServiceURL string

	// LiveRelayURL is the WebSocket a handset opens for spoken guidance, and
	// LiveModel names what does the talking.
	//
	// Empty disables the feature, which is the default: it needs a network a farm
	// may not have, and everything required to file a claim works without it. A
	// deployment turns it on deliberately.
	//
	// It is this service's own public address, not the AI service's: a session is
	// proxied through here so the process holding the model credential is never
	// published. Ordinarily wss://<the console's host>/api/v1/live/relay.
	LiveRelayURL string
	LiveModel    string

	// InternalToken authenticates this API to the analysis services it calls.
	//
	// They hold the Gemini credential, see farmer photographs, and run billable
	// Earth Engine work. Neither checked its caller until now, on the reasoning
	// that both are bound to loopback -- true of one deployment, and not a
	// property of the services. Empty leaves the check off, which is what a
	// laptop runs with.
	InternalToken string

	// MinAppVersion is the oldest mobile build allowed to send claims, as
	// "1.4.0". Empty accepts every build. Raised when the sync protocol
	// changes in a way an old build cannot follow; the old build is told to
	// update and keeps its queue until it does.
	MinAppVersion string

	// AssessmentWorkers is how many assessments this instance produces at
	// once. The ceiling is the Earth Engine project's concurrency and the
	// model quota, not this process -- see api.AssessmentWorkers.
	AssessmentWorkers int

	// Gates are how many calls every instance together may have running on
	// each upstream at once: set each to that upstream's own quota, not to
	// anything about this instance. See internal/platform/gate.
	Gates Gates

	// PipelineWorkers is how many loops advance claims (acknowledge, queue
	// the assessment, allocate) in this instance. See api.PipelineWorkers.
	PipelineWorkers int

	// NotificationRetention is how long a delivered notice is kept. Thirteen
	// months by default: longer than any appeal against the season it belongs
	// to. What was never delivered is kept until somebody deals with it.
	NotificationRetention time.Duration

	// Bootstrap creates the first administrator when the user table is
	// empty. Without it a fresh deployment has no way in.
	BootstrapAdminUsername string
	BootstrapAdminPassword string
	SMS                    SMS
	SMTP                   SMTP

	// ConsoleBaseURL is where an officer's browser reaches the console, e.g.
	// https://esurvey.example. Invitation links are built against it, so an
	// unset or wrong value produces emails whose only useful content is a
	// link that goes nowhere.
	ConsoleBaseURL string
}

// SMTP is the mail server that carries account invitations.
//
// # Why plain SMTP rather than a provider SDK
//
// Everywhere this is likely to run -- a NIC relay, Google Workspace, SES, a
// departmental Exchange -- speaks SMTP, and a deployment chooses between them
// with these five variables instead of a code change.
//
// # What an empty Host means
//
// Not an error. Invitations queue in outbound_email and the console shows the
// administrator a one-time link to pass on by hand, which is what a district
// setting the platform up for the first time needs. See internal/mail.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string

	// From is the header and envelope sender, e.g.
	// "eSurvey <no-reply@esurvey.example>".
	From string

	// StartTLS upgrades a plaintext connection, which is what port 587
	// expects. Port 465 is TLS from the first byte and needs this off.
	StartTLS bool

	Timeout time.Duration
}

type Database struct {
	// URL is where writes and ordinary request reads go: the primary, or a
	// transaction pooler in front of it (see Pooled).
	URL string

	// DirectURL reaches the primary without a pooler. Migrations take a
	// session advisory lock and the audit recorder carries its chain seed as
	// a session setting; neither survives transaction pooling. Empty uses URL.
	DirectURL string

	// ReadURL is a read replica, for the reads that tolerate a few seconds'
	// staleness: dashboards, the state-wide view, audit search, registry
	// listings. Empty sends them to the primary.
	ReadURL string
	// ReadMaxConns bounds the replica pool.
	ReadMaxConns int32
	// ReplicaMaxLag is how far behind the replica may be before reads go back
	// to the primary.
	ReplicaMaxLag time.Duration

	// WorkerMaxConns bounds the pool the background workers use -- the claim
	// pipeline, assessments, the outbox, housekeeping -- so a backlog cannot
	// take the connections a farmer's request needs.
	WorkerMaxConns int32

	// Pooled says URL (and ReadURL) go through a transaction pooler such as
	// PgBouncer. Session settings cannot be sent on those connections, so the
	// statement and idle timeouts are set as defaults on the database role
	// instead, and the audit chain seed only ever travels on DirectURL.
	Pooled bool

	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	// StatementTimeout bounds any single query. An unbounded query on a
	// shared pool is how one slow report takes down an API.
	StatementTimeout time.Duration
	MigrateOnStart   bool

	// AuditChainSeed keys the audit log's hash chain.
	//
	// Without it the chain still links, and still catches accidental
	// corruption and anyone who does not know it is there. With it, somebody
	// who can write rows directly cannot also recompute a consistent chain
	// over rows they invented -- which is the case the chain exists for, since
	// the people holding database credentials are the people the log is meant
	// to hold accountable.
	//
	// Kept wherever ESURVEY_JWT_SEED is kept, and needed to verify the log.
	AuditChainSeed string
}

type Redis struct {
	Addr     string
	Password string
	DB       int
	PoolSize int
}

type Auth struct {
	// JWTSeed is the base64 (std, padded) encoding of a 32-byte Ed25519 seed.
	// Empty in development means an ephemeral key: tokens survive only until
	// restart, which is the correct trade for a laptop and unacceptable
	// anywhere else.
	JWTSeed string
	// PreviousSeeds are keys still trusted for verification but no longer
	// used to sign. Rotation without signing everyone out: promote a new
	// seed, keep the old one here for one access-token lifetime (a farmer's,
	// ESURVEY_FARMER_ACCESS_TTL, is the longest), drop it.
	PreviousSeeds []string
	Issuer        string
	Audience      string

	AccessTTL time.Duration
	// FarmerAccessTTL is longer: see auth.Service.FarmerAccessTTL. Key
	// rotation must keep a previous seed for at least this long.
	FarmerAccessTTL time.Duration
	// Officers work from shared desks and their refresh tokens are short.
	// Farmers use a personal handset where re-authenticating by OTP on every
	// visit would be the reason they stop filing claims at all.
	OfficerRefreshTTL time.Duration
	FarmerRefreshTTL  time.Duration

	// Lockout is per-identifier and per-IP, held in Redis.
	MaxFailedLogins int
	LockoutWindow   time.Duration
	LockoutDuration time.Duration

	OTPLength         int
	OTPTTL            time.Duration
	OTPMaxAttempts    int
	OTPResendCooldown time.Duration
	OTPDailyCap       int

	// OTPSender selects the delivery channel: "log" prints the code to the
	// service log and is for development only, and "sms" uses the gateway
	// configured below.
	OTPSender string
}

// SMS is the gateway that reaches farmers.
//
// # Why the templates are configuration
//
// India regulates transactional SMS through DLT: every sender header is
// registered, every message template is registered, and an operator silently
// rejects a message that does not match a template registered against the
// header that sent it. Registering a new template is a form somebody fills
// in, not a deployment, so the mapping from event to template id lives here.
//
// A message whose event has no template is refused before it reaches the
// wire. That is deliberate: an aggregator answers 200 either way, and a
// platform that believes it told four thousand farmers is worse off than one
// that knows it did not.
type SMS struct {
	// Provider is "msg91", "generic", or empty to leave SMS off.
	Provider string
	BaseURL  string

	// Username is the account name, for a gateway that authenticates with a
	// username and password rather than a single key; AuthKey is then the
	// password.
	Username string
	AuthKey  string

	// PEID is the DLT Principal Entity ID, where the gateway wants one.
	PEID string

	// OTPText is the one-time code message, with {code} where the code goes.
	// It has to match the registered DLT template exactly; empty uses the
	// platform's own Marathi wording.
	OTPText string

	// SenderID is the registered six-character header, e.g. ESURVY.
	SenderID string

	// Workers bounds how many messages the dispatcher sends at once. The
	// limit that matters is the aggregator's contracted throughput.
	Workers int

	// Templates maps a notification event code to its DLT template id, from
	// ESURVEY_SMS_TEMPLATES as "event=id,event=id".
	Templates map[string]string

	// DryRun logs what would be sent and reports success without sending.
	// For a rehearsal before a district-wide event, where the failure
	// everybody fears is a test that actually messages every farmer.
	DryRun bool
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	env := Environment(strings.ToLower(str("ESURVEY_ENV", string(Development))))
	switch env {
	case Development, Staging, Production:
	default:
		fail("ESURVEY_ENV is %q; expected development, staging or production", env)
		env = Production // fail closed while we collect the rest
	}

	cfg := &Config{
		Env:              env,
		Addr:             str("ESURVEY_ADDR", ":8090"),
		CORSOrigins:      splitList(str("ESURVEY_CORS_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000")),
		TrustedProxyHops: intVal("ESURVEY_TRUSTED_PROXY_HOPS", 0, fail),
		MetricsToken:     strings.TrimSpace(os.Getenv("ESURVEY_METRICS_TOKEN")),

		Database: Database{
			URL:              os.Getenv("ESURVEY_DATABASE_URL"),
			DirectURL:        os.Getenv("ESURVEY_DATABASE_DIRECT_URL"),
			ReadURL:          os.Getenv("ESURVEY_DATABASE_READ_URL"),
			ReadMaxConns:     int32(intVal("ESURVEY_DB_READ_MAX_CONNS", 20, fail)),
			ReplicaMaxLag:    dur("ESURVEY_DB_REPLICA_MAX_LAG", 10*time.Second, fail),
			WorkerMaxConns:   int32(intVal("ESURVEY_DB_WORKER_MAX_CONNS", 10, fail)),
			Pooled:           boolVal("ESURVEY_DATABASE_POOLED", false, fail),
			MaxConns:         int32(intVal("ESURVEY_DB_MAX_CONNS", 20, fail)),
			MinConns:         int32(intVal("ESURVEY_DB_MIN_CONNS", 2, fail)),
			MaxConnLifetime:  dur("ESURVEY_DB_CONN_LIFETIME", time.Hour, fail),
			MaxConnIdleTime:  dur("ESURVEY_DB_CONN_IDLE", 30*time.Minute, fail),
			StatementTimeout: dur("ESURVEY_DB_STATEMENT_TIMEOUT", 30*time.Second, fail),
			MigrateOnStart:   boolVal("ESURVEY_DB_MIGRATE_ON_START", true, fail),
			AuditChainSeed:   os.Getenv("ESURVEY_AUDIT_CHAIN_SEED"),
		},

		Redis: Redis{
			Addr:     str("ESURVEY_REDIS_ADDR", "127.0.0.1:6379"),
			Password: os.Getenv("ESURVEY_REDIS_PASSWORD"),
			DB:       intVal("ESURVEY_REDIS_DB", 0, fail),
			PoolSize: intVal("ESURVEY_REDIS_POOL_SIZE", 20, fail),
		},

		Auth: Auth{
			JWTSeed:           os.Getenv("ESURVEY_JWT_SEED"),
			PreviousSeeds:     splitList(os.Getenv("ESURVEY_JWT_PREVIOUS_SEEDS")),
			Issuer:            str("ESURVEY_JWT_ISSUER", "esurvey"),
			Audience:          str("ESURVEY_JWT_AUDIENCE", "esurvey-api"),
			AccessTTL:         dur("ESURVEY_ACCESS_TTL", 15*time.Minute, fail),
			FarmerAccessTTL:   dur("ESURVEY_FARMER_ACCESS_TTL", 6*time.Hour, fail),
			OfficerRefreshTTL: dur("ESURVEY_OFFICER_REFRESH_TTL", 12*time.Hour, fail),
			FarmerRefreshTTL:  dur("ESURVEY_FARMER_REFRESH_TTL", 30*24*time.Hour, fail),
			MaxFailedLogins:   intVal("ESURVEY_MAX_FAILED_LOGINS", 5, fail),
			LockoutWindow:     dur("ESURVEY_LOCKOUT_WINDOW", 15*time.Minute, fail),
			LockoutDuration:   dur("ESURVEY_LOCKOUT_DURATION", 15*time.Minute, fail),
			OTPLength:         intVal("ESURVEY_OTP_LENGTH", 6, fail),
			OTPTTL:            dur("ESURVEY_OTP_TTL", 5*time.Minute, fail),
			OTPMaxAttempts:    intVal("ESURVEY_OTP_MAX_ATTEMPTS", 5, fail),
			OTPResendCooldown: dur("ESURVEY_OTP_RESEND_COOLDOWN", 60*time.Second, fail),
			OTPDailyCap:       intVal("ESURVEY_OTP_DAILY_CAP", 10, fail),
			OTPSender:         str("ESURVEY_OTP_SENDER", "log"),
		},

		TLSCertFile:    os.Getenv("ESURVEY_TLS_CERT_FILE"),
		TLSKeyFile:     os.Getenv("ESURVEY_TLS_KEY_FILE"),
		MockDir:        str("ESURVEY_MOCK_DIR", "../mock_files"),
		EvidenceDir:    str("ESURVEY_EVIDENCE_DIR", "../evidence"),
		EvidenceBucket: str("ESURVEY_EVIDENCE_BUCKET", ""),
		EvidencePrefix: str("ESURVEY_EVIDENCE_PREFIX", ""),
		SatelliteURL:   str("SATELLITE_URL", "http://34.93.99.56:8080"),
		// The radar module. Empty means the platform never calls it and
		// reports radar as an absent evidence source, which is the state
		// every deployment was in before satellite-intelligence existed.
		SARServiceURL:     str("SAR_SERVICE_URL", ""),
		VisionServiceURL:  str("VISION_SERVICE_URL", ""),
		SatelliteAPIKey:   os.Getenv("SATELLITE_API_KEY"),
		InternalToken:     os.Getenv("ESURVEY_INTERNAL_TOKEN"),
		MinAppVersion:     str("ESURVEY_MIN_APP_VERSION", ""),
		AssessmentWorkers: intVal("ESURVEY_ASSESSMENT_WORKERS", 24, fail),
		Gates: Gates{
			Satellite: intVal("ESURVEY_GATE_SATELLITE", 4, fail),
			Radar:     intVal("ESURVEY_GATE_RADAR", 4, fail),
			AI:        intVal("ESURVEY_GATE_AI", 8, fail),
			Vision:    intVal("ESURVEY_GATE_VISION", 4, fail),
		},
		PipelineWorkers: intVal("ESURVEY_PIPELINE_WORKERS", 4, fail),
		NotificationRetention: dur("ESURVEY_NOTIFICATION_RETENTION",
			400*24*time.Hour, fail),
		AIServiceURL:  str("AI_SERVICE_URL", "http://127.0.0.1:8100"),
		PDFServiceURL: str("PDF_SERVICE_URL", "http://pdf:3000"),

		// Off unless a deployment sets it.
		//
		// Stated rather than derived, because this service does not know the name it
		// is reached by: it sits behind a reverse proxy under a public hostname, and
		// guessing one from a bind address would produce a URL no handset can open.
		LiveRelayURL: str("ESURVEY_LIVE_RELAY_URL", ""),
		LiveModel:    str("ESURVEY_LIVE_MODEL", "gemini-3.8-live"),

		BootstrapAdminUsername: os.Getenv("ESURVEY_BOOTSTRAP_ADMIN_USERNAME"),
		BootstrapAdminPassword: os.Getenv("ESURVEY_BOOTSTRAP_ADMIN_PASSWORD"),

		SMS: SMS{
			Provider: str("ESURVEY_SMS_PROVIDER", ""),
			// How many messages may be in flight at once. The ceiling is the
			// aggregator's throughput, which is contractual; see
			// notify.DefaultWorkers.
			Workers:   intVal("ESURVEY_SMS_WORKERS", 16, fail),
			BaseURL:   str("ESURVEY_SMS_URL", ""),
			Username:  str("ESURVEY_SMS_USERNAME", ""),
			AuthKey:   os.Getenv("ESURVEY_SMS_AUTH_KEY"),
			PEID:      str("ESURVEY_SMS_PEID", ""),
			OTPText:   str("ESURVEY_SMS_OTP_TEXT", ""),
			SenderID:  str("ESURVEY_SMS_SENDER_ID", ""),
			Templates: templateMap(os.Getenv("ESURVEY_SMS_TEMPLATES")),
			DryRun:    boolVal("ESURVEY_SMS_DRY_RUN", false, fail),
		},

		SMTP: SMTP{
			Host:     str("ESURVEY_SMTP_HOST", ""),
			Port:     intVal("ESURVEY_SMTP_PORT", 587, fail),
			Username: str("ESURVEY_SMTP_USERNAME", ""),
			Password: os.Getenv("ESURVEY_SMTP_PASSWORD"),
			From:     str("ESURVEY_SMTP_FROM", "eSurvey <no-reply@esurvey.local>"),
			StartTLS: boolVal("ESURVEY_SMTP_STARTTLS", true, fail),
			Timeout:  dur("ESURVEY_SMTP_TIMEOUT", 20*time.Second, fail),
		},

		ConsoleBaseURL: str("ESURVEY_CONSOLE_URL", "http://localhost:3100"),
	}

	// A mail server named without a sender is a configuration that queues
	// every message and then fails to send all of them at once, an hour
	// later, in a log nobody is reading.
	if cfg.SMTP.Host != "" && strings.TrimSpace(cfg.SMTP.From) == "" {
		fail("ESURVEY_SMTP_FROM is required when ESURVEY_SMTP_HOST is set")
	}

	// --- always required -------------------------------------------------
	if cfg.Database.URL == "" {
		fail("ESURVEY_DATABASE_URL is required")
	}
	if cfg.SatelliteAPIKey == "" {
		fail("SATELLITE_API_KEY is required; every assessment would fail at the first upstream call")
	}
	if cfg.Database.Pooled && cfg.Database.DirectURL == "" {
		fail("ESURVEY_DATABASE_POOLED is set but ESURVEY_DATABASE_DIRECT_URL is not: " +
			"migrations and the audit chain need a connection that is not pooled")
	}
	if cfg.Database.WorkerMaxConns < 2 {
		fail("ESURVEY_DB_WORKER_MAX_CONNS (%d) must be at least 2", cfg.Database.WorkerMaxConns)
	}
	if cfg.Database.MinConns > cfg.Database.MaxConns {
		fail("ESURVEY_DB_MIN_CONNS (%d) exceeds ESURVEY_DB_MAX_CONNS (%d)",
			cfg.Database.MinConns, cfg.Database.MaxConns)
	}
	if cfg.Auth.OTPLength < 4 || cfg.Auth.OTPLength > 10 {
		fail("ESURVEY_OTP_LENGTH must be between 4 and 10")
	}

	// A gateway that is configured has to be configured completely. Half a
	// gateway fails at the first farmer rather than at startup, and the
	// failure looks like a delivery problem rather than a missing variable.
	if cfg.SMS.Provider != "" {
		if cfg.SMS.SenderID == "" {
			fail("ESURVEY_SMS_SENDER_ID is required with a provider; an " +
				"unregistered header is rejected by every Indian operator")
		}
		if len(cfg.SMS.Templates) == 0 {
			fail("ESURVEY_SMS_TEMPLATES is required with a provider: India " +
				"rejects any message with no registered DLT template, and the " +
				"aggregator answers 200 either way")
		}
		if cfg.SMS.Provider != "msg91" && cfg.SMS.BaseURL == "" {
			fail("ESURVEY_SMS_URL is required for the generic provider")
		}
		// The enterprise gateway authenticates with a pair. Caught here
		// rather than at the first farmer, where a missing username looks
		// like the aggregator refusing the account.
		if cfg.SMS.Provider == "enterprise" || cfg.SMS.Provider == "fe" {
			if cfg.SMS.Username == "" {
				fail("ESURVEY_SMS_USERNAME is required for the enterprise provider")
			}
			if cfg.SMS.AuthKey == "" {
				fail("ESURVEY_SMS_AUTH_KEY is the account password for the " +
					"enterprise provider and is required")
			}
		}
	}
	// A code message with nowhere to put the code reaches the farmer as a
	// template with a hole in it, and no code.
	if cfg.SMS.OTPText != "" && !strings.Contains(cfg.SMS.OTPText, "{code}") {
		fail("ESURVEY_SMS_OTP_TEXT must contain {code}, which is where the " +
			"one-time code goes")
	}
	if cfg.Auth.OTPSender == "sms" && cfg.SMS.Provider == "" {
		fail("ESURVEY_OTP_SENDER=sms but no SMS gateway is configured; " +
			"nobody would be able to sign in")
	}

	// --- hardened environments ------------------------------------------
	if env.Hardened() {
		if cfg.Auth.JWTSeed == "" {
			fail("ESURVEY_JWT_SEED is required outside development; an ephemeral " +
				"key would invalidate every session on restart and cannot be " +
				"shared with a second replica")
		}
		if cfg.Redis.Password == "" {
			fail("ESURVEY_REDIS_PASSWORD is required outside development; Redis " +
				"holds refresh tokens and OTP material")
		}
		if cfg.Auth.OTPSender == "log" {
			fail("ESURVEY_OTP_SENDER is \"log\", which prints one-time codes to " +
				"the service log; configure a real delivery channel")
		}
		// Where a season's evidence goes.
		//
		// Four clips and four stills per claim is about fifty megabytes, so a
		// district-wide event is terabytes (docs/PLATFORM_SCALE_REVIEW.md, A3).
		// The container's volume cannot hold that, and when it fills the
		// symptom is a farmer's video refused at the last chunk. A bucket is
		// not an optimisation here; it is the only place the evidence fits.
		if cfg.EvidenceBucket == "" {
			fail("ESURVEY_EVIDENCE_BUCKET is not set: a season of photographs " +
				"and station clips is terabytes, which a container volume " +
				"cannot hold. See deploy/evidence-lifecycle.json")
		}

		for _, o := range cfg.CORSOrigins {
			if strings.HasPrefix(o, "http://") && !strings.Contains(o, "localhost") {
				fail("CORS origin %q is plaintext HTTP; session credentials would "+
					"cross the network in the clear", o)
			}
		}
		// Transport encryption, one way or the other.
		//
		// This service hands out bearer tokens and returns identified farmers'
		// land, losses and photographs. In the clear on a shared network, a
		// session belongs to whoever is listening -- and the networks this runs
		// on are village Wi-Fi and taluka offices, not a private datacentre
		// fabric.
		//
		// Two configurations are acceptable and there is no third: terminate
		// here with a certificate, or sit behind a proxy that terminates and
		// say how many hops away it is. The second is the common one; the
		// requirement is that somebody decided, rather than that plaintext
		// happened to be the default nobody changed.
		switch {
		case cfg.TLSCertFile != "" && cfg.TLSKeyFile == "",
			cfg.TLSKeyFile != "" && cfg.TLSCertFile == "":
			fail("ESURVEY_TLS_CERT_FILE and ESURVEY_TLS_KEY_FILE must be set together")
		case cfg.TLSCertFile == "" && cfg.TrustedProxyHops == 0:
			fail("no transport encryption: set ESURVEY_TLS_CERT_FILE and " +
				"ESURVEY_TLS_KEY_FILE to terminate TLS here, or " +
				"ESURVEY_TRUSTED_PROXY_HOPS if a proxy terminates in front. " +
				"Bearer tokens and farmer records must not cross a network in " +
				"the clear")
		}

		// The connection to the database, which is the other half of §
		// transport encryption and was for a while the forgotten half.
		//
		// Requiring TLS on the API while every farmer record, Aadhaar hash and
		// assessment crossed to Postgres in the clear would have been securing
		// the door and leaving the delivery entrance open. A managed database
		// is reached over a network somebody else operates.
		//
		// 'prefer' and 'allow' are refused alongside 'disable' because they
		// fall back to plaintext silently: the connection succeeds, nothing is
		// logged, and the only way to discover it is to look at packets.
		//
		// 'require' encrypts but does not verify the server, so it stops
		// passive capture and not an active impostor. 'verify-full' is the
		// right target and needs a CA to be configured, so it is urged rather
		// than demanded -- a rule nobody can satisfy today gets worked around
		// rather than met.
		switch sslMode(cfg.Database.URL) {
		case "disable", "allow", "prefer":
			fail("ESURVEY_DATABASE_URL has sslmode=%s, which sends every farmer "+
				"record and Aadhaar hash to the database in the clear or "+
				"silently falls back to doing so; use verify-full, or require "+
				"at the least", sslMode(cfg.Database.URL))
		case "require":
			problems = append(problems, "note: the database connection uses "+
				"sslmode=require, which encrypts but does not verify the "+
				"server. verify-full is what stops an impostor")
		}

		// The analysis services are only as private as the network they sit on,
		// and on an orchestrated one that is every workload in the cluster.
		if cfg.InternalToken == "" && (cfg.AIServiceURL != "" ||
			cfg.SARServiceURL != "" || cfg.VisionServiceURL != "") {
			fail("ESURVEY_INTERNAL_TOKEN is required outside development; " +
				"without it the analysis services accept work from anything " +
				"that can reach them, and they hold the model credential and " +
				"see farmer photographs")
		}

		// An unkeyed audit chain still links, and still catches accidental
		// corruption -- but anybody who can write rows directly can also
		// recompute a consistent chain over rows they invented, which is the
		// case it exists for.
		if cfg.Database.AuditChainSeed == "" {
			fail("ESURVEY_AUDIT_CHAIN_SEED is required outside development; " +
				"without it the audit log's hash chain can be recomputed by " +
				"anyone who can write to the database")
		}

		// A bootstrap credential left in the environment after first boot.
		//
		// It is not a second way in -- the account is only created when the
		// user table is empty -- but it is the *live* administrator's password
		// sitting in a file, an orchestrator's secret store and very likely a
		// shell history, long after the person who set it stopped thinking
		// about it. Startup already warned about this and nobody reads warnings
		// from a service that started successfully.
		if cfg.BootstrapAdminPassword != "" {
			fail("ESURVEY_BOOTSTRAP_ADMIN_PASSWORD is still set. It created the " +
				"first administrator and is now that account's live password " +
				"sitting in the environment; remove it")
		}

		if strings.HasPrefix(cfg.SatelliteURL, "http://") {
			// Not fatal: the analytics engine is reachable only over the
			// private network today. Worth saying out loud all the same.
			problems = append(problems, "note: SATELLITE_URL is plaintext HTTP")
		}
	}

	if len(problems) > 0 {
		// Notes are advisory; only real failures block startup.
		var hard []string
		for _, p := range problems {
			if !strings.HasPrefix(p, "note: ") {
				hard = append(hard, p)
			}
		}
		if len(hard) > 0 {
			return nil, fmt.Errorf("configuration is not usable:\n  - %s",
				strings.Join(hard, "\n  - "))
		}
	}

	return cfg, nil
}

// ErrNoBootstrap reports that a deployment has no users and no way to create
// the first one.
var ErrNoBootstrap = errors.New(
	"no users exist and ESURVEY_BOOTSTRAP_ADMIN_USERNAME/PASSWORD are unset; " +
		"the deployment would be unreachable")

func str(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func splitList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intVal(key string, fallback int, fail func(string, ...any)) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		fail("%s is %q, which is not a number", key, raw)
		return fallback
	}
	return v
}

func boolVal(key string, fallback bool, fail func(string, ...any)) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		fail("%s is %q, which is not a boolean", key, raw)
		return fallback
	}
	return v
}

func dur(key string, fallback time.Duration, fail func(string, ...any)) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		fail("%s is %q, which is not a duration (try 15m, 12h)", key, raw)
		return fallback
	}
	if v <= 0 {
		fail("%s must be positive", key)
		return fallback
	}
	return v
}

// templateMap reads "event=id,event=id" into a lookup.
//
// A flat variable rather than one per event, because registering a template
// is a form somebody fills in with an operator and the set grows without any
// code changing. Whitespace is tolerated: this is pasted from an email.
func templateMap(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// sslMode reads the sslmode parameter from a Postgres URL.
//
// Returns "" when the URL has none, which libpq treats as prefer -- and prefer
// falls back to plaintext without saying so. An absent setting is therefore
// not a neutral one, and the caller treats it as the failure it is.
func sslMode(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		// An unparseable URL fails elsewhere with a better message than this
		// function could give.
		return ""
	}
	mode := strings.ToLower(strings.TrimSpace(u.Query().Get("sslmode")))
	if mode == "" {
		return "prefer"
	}
	return mode
}

// Gates are the deployment-wide concurrency limits per upstream.
type Gates struct {
	// Satellite is analyses running on the Farm Analytics Engine: its Earth
	// Engine project's concurrency.
	Satellite int
	// Radar is analyses running on the radar and moisture service: replicas
	// times ESURVEY_SAR_MAX_CONCURRENT on that service.
	Radar int
	// AI is assessments running on the agent: the model provider's quota,
	// divided by how many model calls one assessment makes at once.
	AI int
	// Vision is clips being measured at once.
	Vision int
}
