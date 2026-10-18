// Command server runs the eSurvey platform API.
//
// The registry source is the mock directory rather than the live AgriStack
// bridge: AgriStack returns plot_geometry: null and an empty crop_sown_data
// for every farmer we can reach, so a live source would supply parcels the
// satellite engine cannot analyse. See mock_files/README.md.
//
// Everything else -- accounts, roles, jurisdictions, sessions, audit -- is
// real. Startup is ordered so that a deployment which cannot serve safely
// does not serve at all: configuration is validated, then the database and
// Redis are reached, then the schema is migrated, then the signing key is
// resolved, and only then does the listener open.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/api"
	"github.com/agribid/esurvey-platform/backend/internal/assessjob"
	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/baseline"
	"github.com/agribid/esurvey-platform/backend/internal/blob"
	"github.com/agribid/esurvey-platform/backend/internal/calibration"
	"github.com/agribid/esurvey-platform/backend/internal/claims"
	"github.com/agribid/esurvey-platform/backend/internal/config"
	"github.com/agribid/esurvey-platform/backend/internal/event"
	"github.com/agribid/esurvey-platform/backend/internal/evidence"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
	"github.com/agribid/esurvey-platform/backend/internal/idempotency"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/livespend"
	"github.com/agribid/esurvey-platform/backend/internal/maharain"
	"github.com/agribid/esurvey-platform/backend/internal/mail"
	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
	"github.com/agribid/esurvey-platform/backend/internal/pdf"
	"github.com/agribid/esurvey-platform/backend/internal/platform/cache"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/retention"
	"github.com/agribid/esurvey-platform/backend/internal/rscache"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
	"github.com/agribid/esurvey-platform/backend/internal/sms"
	"github.com/agribid/esurvey-platform/backend/internal/store"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
	"github.com/agribid/esurvey-platform/backend/internal/vision"
	"github.com/agribid/esurvey-platform/backend/internal/weather"

	"github.com/agribid/esurvey-platform/backend/internal/platform/gate"
)

func main() {
	// Subcommands. The container probe re-invokes this binary rather than the
	// image carrying a shell for it; see healthcheck.go.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			addr := os.Getenv("ESURVEY_ADDR")
			if addr == "" {
				addr = ":8090"
			}
			os.Exit(healthcheck(addr))
		case "evidence-preflight":
			// Checked before switching, because every way this fails is
			// silent until a farmer cannot upload a photograph.
			os.Exit(evidencePreflight())
		case "lgd-load":
			if len(os.Args) < 3 {
				usage(newLogger(config.Development))
				os.Exit(1)
			}
			os.Exit(lgdLoad(os.Args[2]))
		case "threshold-load":
			if len(os.Args) < 3 {
				usage(newLogger(config.Development))
				os.Exit(1)
			}
			os.Exit(thresholdLoad(os.Args[2]))
		case "render-report":
			if len(os.Args) < 3 {
				usage(newLogger(config.Development))
				os.Exit(1)
			}
			os.Exit(renderReport(os.Args[2]))
		case "rainfall-load":
			// The district is optional: with none, the whole state.
			os.Exit(rainfallLoad(os.Args[2:]))
		default:
			usage(newLogger(config.Development))
			os.Exit(1)
		}
	}

	if err := run(); err != nil {
		// slog rather than log.Fatal so the failure is one structured line
		// like every other, and so deferred cleanup above still runs.
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.Env)
	log.Info("starting", "env", string(cfg.Env), "addr", cfg.Addr)

	// A generous ceiling on the whole of startup. A container that hangs
	// reaching a database is worse than one that exits and is restarted.
	startCtx, cancelStart := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelStart()

	// --- data stores ------------------------------------------------------
	//
	// Four pools, each for one kind of work: see internal/platform/db/pools.go.
	// The direct one first, because migrations and -- behind a pooler -- the
	// role's timeouts must be in place before anything else connects.
	direct, err := db.OpenFor(startCtx, cfg.Database, db.Direct)
	if err != nil {
		return err
	}
	defer direct.Close()

	if cfg.Database.MigrateOnStart {
		if err := db.Migrate(startCtx, direct, log); err != nil {
			return err
		}
	} else {
		log.Warn("migrations are disabled; the schema must already be current")
	}
	if cfg.Database.Pooled {
		if err := db.EnsureRoleDefaults(startCtx, direct, cfg.Database); err != nil {
			return err
		}
	}

	pool, err := db.OpenFor(startCtx, cfg.Database, db.Requests)
	if err != nil {
		return err
	}
	defer pool.Close()
	workPool, err := db.OpenFor(startCtx, cfg.Database, db.Workers)
	if err != nil {
		return err
	}
	defer workPool.Close()
	readPool, err := db.OpenFor(startCtx, cfg.Database, db.Reads)
	if err != nil {
		// A replica that cannot be reached at startup is not a reason to stay
		// down: the primary serves every read, and the router says so.
		log.Warn("read replica unreachable at startup; reading from the primary", "error", err)
		readPool = nil
	}
	if readPool != nil {
		defer readPool.Close()
	}
	reads := db.NewRouter(pool, readPool, cfg.Database.ReplicaMaxLag, log)
	routerCtx, stopRouter := context.WithCancel(context.Background())
	defer stopRouter()
	go reads.Watch(routerCtx, 3*time.Second)

	log.Info("database connected",
		"pooled", cfg.Database.Pooled,
		"api_max_conns", cfg.Database.MaxConns,
		"worker_max_conns", cfg.Database.WorkerMaxConns,
		"read_replica", readPool != nil)

	rdb, err := cache.Open(startCtx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()
	log.Info("redis connected", "addr", cfg.Redis.Addr)

	// --- signing keys -----------------------------------------------------
	seed := cfg.Auth.JWTSeed
	if seed == "" {
		// Only reachable in development; config.Load refuses an empty seed
		// anywhere else.
		seed, err = auth.GenerateSeed()
		if err != nil {
			return err
		}
		log.Warn("no ESURVEY_JWT_SEED set; using an ephemeral signing key",
			"consequence", "every session ends when this process restarts, and a "+
				"second replica would reject tokens minted by this one")
	}
	keys, err := auth.NewKeyring(cfg.Auth.Issuer, cfg.Auth.Audience, seed, cfg.Auth.PreviousSeeds)
	if err != nil {
		return err
	}
	log.Info("token signing ready", "kid", keys.SigningKeyID(),
		"previous_keys", len(cfg.Auth.PreviousSeeds))

	// --- services ---------------------------------------------------------
	// On the direct pool: the chain seed is a session setting, which a
	// transaction pooler would not carry.
	recorder := audit.New(direct, log)
	defer func() {
		// Drain before exit: a buffered audit entry lost to a shutdown is an
		// entry that will be looked for later and not found.
		flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := recorder.Close(flushCtx); err != nil {
			log.Error("audit recorder did not drain", "error", err)
		}
	}()

	ident := identity.NewStore(pool)

	if err := bootstrapAdmin(startCtx, cfg, ident, log); err != nil {
		return err
	}

	// The gateway that reaches farmers.
	//
	// Optional, and its absence is the state this platform has been in since
	// the first report was filed: messages queue, the backlog is reported,
	// and nobody is told anything. Configuring it is a procurement decision
	// and a DLT registration, neither of which is a deployment.
	var gateway *sms.Gateway
	if cfg.SMS.Provider != "" {
		gateway, err = sms.New(sms.Config{
			Provider:  cfg.SMS.Provider,
			BaseURL:   cfg.SMS.BaseURL,
			Username:  cfg.SMS.Username,
			AuthKey:   cfg.SMS.AuthKey,
			PEID:      cfg.SMS.PEID,
			SenderID:  cfg.SMS.SenderID,
			Templates: cfg.SMS.Templates,
			DryRun:    cfg.SMS.DryRun,
		}, nil)
		if err != nil {
			return fmt.Errorf("sms gateway: %w", err)
		}
		log.Info("sms gateway ready",
			"provider", gateway.Name(),
			"sender_id", cfg.SMS.SenderID,
			"templates", len(cfg.SMS.Templates))
	} else {
		log.Warn("no sms gateway is configured: notifications will queue and " +
			"nobody will be told anything")
	}

	var sender auth.Sender = auth.LogSender{Log: log}
	switch cfg.Auth.OTPSender {
	case "log":
	case "sms":
		// config.Load has already refused this combination without a gateway.
		sender = sms.OTP{Gateway: gateway, Log: log, Text: cfg.SMS.OTPText}
	default:
		return errors.New("ESURVEY_OTP_SENDER=" + cfg.Auth.OTPSender +
			" is not a channel; use \"log\" or \"sms\"")
	}

	authSvc := &auth.Service{
		Users:    ident,
		Sessions: auth.NewSessions(rdb),
		Throttle: auth.NewThrottle(rdb, cfg.Auth.MaxFailedLogins,
			cfg.Auth.LockoutWindow, cfg.Auth.LockoutDuration),
		OTP: auth.NewOTP(rdb, sender, cfg.Auth.OTPLength, cfg.Auth.OTPTTL,
			cfg.Auth.OTPMaxAttempts, cfg.Auth.OTPResendCooldown, cfg.Auth.OTPDailyCap),
		Keys:              keys,
		Audit:             recorder,
		Log:               log,
		AccessTTL:         cfg.Auth.AccessTTL,
		FarmerAccessTTL:   cfg.Auth.FarmerAccessTTL,
		OfficerRefreshTTL: cfg.Auth.OfficerRefreshTTL,
		FarmerRefreshTTL:  cfg.Auth.FarmerRefreshTTL,

		// Outside a hardened environment the farmer sign-in may say that a
		// number is an officer's. In production it stays silent, because the
		// same sentence answers "is this number a revenue officer's" for
		// anybody who types one.
		NameOfficerHandsets: !cfg.Env.Hardened(),
	}

	// The farmer registry: records stored in Postgres, refreshed from the
	// upstream registry -- the mock directory until AgriStack is connected.
	// See agristack.Registry for why nothing calls the upstream directly.
	files, err := agristack.NewFileSource(cfg.MockDir)
	if err != nil {
		return err
	}
	registry := agristack.NewRegistry(pool, files, log)
	registry.Reads = reads
	// The directory is read into memory, so a refresh costs a map lookup and
	// an edited file should show within a minute. A live registry keeps the
	// day-long default.
	registry.FreshFor = time.Minute
	registry.OnRecord = func(ctx context.Context, rec *agristack.FarmerRecord) {
		if _, err := survey.SyncRecordBoundaries(ctx, pool, rec); err != nil {
			log.Warn("syncing a registry record's boundaries", "farmer_id",
				rec.Farmer.FarmerID, "error", err)
		}
	}
	// Everything in the directory is stored at startup, and again whenever
	// the directory changes, so listings -- which read the stored registry --
	// show a record dropped in by hand without waiting for someone to ask for
	// that farmer by id.
	imported, err := registry.ImportAll(startCtx, files, files.Name())
	if err != nil {
		return fmt.Errorf("storing the registry directory: %w", err)
	}
	log.Info("registry loaded", "dir", cfg.MockDir, "farmers", imported)
	go func() {
		last := files.Status().LoadedAt
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			st := files.Status()
			if st.LoadedAt.Equal(last) {
				continue
			}
			last = st.LoadedAt
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			n, err := registry.ImportAll(ctx, files, files.Name())
			cancel()
			if err != nil {
				log.Warn("re-storing the registry directory", "error", err)
				continue
			}
			log.Info("registry directory changed; records stored", "farmers", n)
		}
	}()

	// Where the evidence goes. A bucket when one is named, the volume
	// otherwise -- and whichever it is, say so at startup, because an
	// operator should never have to infer where a farmer's photographs are
	// being kept.
	evidenceStore, err := openEvidenceStore(startCtx, cfg)
	if err != nil {
		return err
	}
	if err := evidenceStore.Check(startCtx); err != nil {
		// Refused at startup rather than at the first capture. A farmer
		// standing in a field is the worst possible place to discover that
		// the evidence store was never reachable.
		return fmt.Errorf("evidence store is not usable: %w", err)
	}
	log.Info("evidence store ready", "where", evidenceStore.Describe())

	captures := survey.NewStore(pool, evidenceStore).WithLogger(log)
	// Parcel geometry into PostGIS, so a capture can be checked against the
	// plot it claims to show. Idempotent; the registry stays the source.
	if _, err := survey.SyncBoundaries(startCtx, pool, files, log); err != nil {
		return err
	}

	// The outbox. No provider is connected, so messages queue and /readyz
	// reports the backlog -- which is the honest state, and better than a
	// no-op sender that reports success while telling nobody anything.
	// Nil sender where no gateway is configured: the dispatcher then reports
	// itself idle rather than spinning, and the queue is the true measure of
	// how much this platform is failing to tell people.
	var messages notify.Sender
	if gateway != nil {
		messages = sms.Notifications{Gateway: gateway, Log: log}
	}
	outbox := notify.NewQueue(workPool, messages, log).WithWorkers(cfg.SMS.Workers)

	// Delivery runs alongside the server. Enqueueing in the caller's
	// transaction is half the outbox; this is the half that empties it.
	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	defer stopDispatch()
	go outbox.Run(dispatchCtx, 30*time.Second)

	// The same shape for email. An officer waiting on an invitation is
	// waiting on a person who has just left their desk, so a minute is soon
	// enough and keeps a misconfigured relay from being hammered.
	mailer := mail.New(workPool, mail.Config{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.Username,
		Password: cfg.SMTP.Password,
		From:     cfg.SMTP.From,
		StartTLS: cfg.SMTP.StartTLS,
		Timeout:  cfg.SMTP.Timeout,
	}, log)
	go mailer.Run(dispatchCtx, time.Minute)

	// Everything a damage figure can be grounded in.
	//
	// The cache is shared between the satellite baselines and the weather
	// archive: both ask about closed windows over archives that will not
	// change, and both are asked the same question by every farmer in a
	// locality after a district-wide event.
	// One gate per upstream, shared by every client of it and counted across
	// every instance through Redis. The radar and moisture clients are the
	// same service and the same Earth Engine project, so they share one.
	gates := struct{ satellite, radar, ai, vision *gate.Gate }{
		satellite: gate.New("satellite", cfg.Gates.Satellite, rdb, log),
		radar:     gate.New("radar", cfg.Gates.Radar, rdb, log),
		ai:        gate.New("ai", cfg.Gates.AI, rdb, log),
		vision:    gate.New("vision", cfg.Gates.Vision, rdb, log),
	}
	log.Info("upstream gates",
		"satellite", cfg.Gates.Satellite, "radar", cfg.Gates.Radar,
		"ai", cfg.Gates.AI, "vision", cfg.Gates.Vision)

	analytics := satellite.New(cfg.SatelliteURL, cfg.SatelliteAPIKey)
	analytics.Gate = gates.satellite
	// Assembled only by the assessment workers, so on the workers' pool.
	cache := rscache.New(workPool, log)
	// The capture store the workers use.
	//
	// On the workers' pool like every other store the assembler holds: a
	// backlog of assessments must not take the connections a farmer's request
	// is waiting for, which is the whole reason that pool exists.
	bgCaptures := survey.NewStore(workPool, evidenceStore).WithLogger(log)

	gather := &evidence.Assembler{
		Baselines: baseline.NewStore(workPool, analytics, log),
		Weather:   weather.New(cache),
		Claims:    claims.NewStore(workPool),
		Fieldwork: fieldwork.NewStore(workPool),
		// The state's own rainfall, loaded by rainfall-load. Always wired:
		// unlike a remote service there is nothing to configure and nothing to
		// call, and a district nobody has loaded simply reports the source
		// absent. Leaving it behind a flag would mean a deployment that forgot
		// the flag silently went back to reanalysis for the amount.
		Official: &maharain.Store{Pool: workPool},
		Log:      log,
	}

	// Radar is its own service, on the same contract.
	//
	// Separate from the analytics engine because Sentinel-1 is a different
	// collection with different quotas, and because a deployment may have one
	// and not the other. Where no URL is set nothing is called and the
	// assessment reports radar as an absent evidence source -- which is what
	// every deployment did until satellite-intelligence existed.
	if cfg.SARServiceURL != "" {
		radar := satellite.New(cfg.SARServiceURL, cfg.InternalToken)
		radar.Gate = gates.radar
		gather.Radar = radar

		// The radar service can generate a plausible backscatter series with
		// no satellite behind it, so this side of the contract can be built
		// and tested without an Earth Engine project. Believing one outside
		// development would put invented decibels on a compensation record, so
		// the permission is granted here and nowhere else.
		gather.AllowSynthetic = cfg.Env == config.Development
		log.Info("radar module configured",
			"url", cfg.SARServiceURL,
			"generated_series_accepted", gather.AllowSynthetic)

		// Canopy water (Sentinel-2 NDMI) and surface soil moisture (the
		// Sentinel-1 record), from the same service because it is the same
		// Earth Engine project and the same concurrency limit.
		//
		// A service that predates the module answers 404, which the client
		// reads as unavailable, so this is safe to wire against an older
		// deployment: the assessment reports moisture absent and proceeds.
		//
		// Behind the shared remote-sensing cache, which is Postgres and
		// shared by every API instance. The service's own cache is memory,
		// lost on restart and not shared, and a settled window should be
		// bought from Earth Engine once rather than once per re-assessment.
		moistureClient := satellite.New(cfg.SARServiceURL, cfg.InternalToken)
		moistureClient.Gate = gates.radar
		gather.Moisture = evidence.CachedMoisture{
			Source: moistureClient,
			Cache:  cache,
		}
		log.Info("moisture module configured", "url", cfg.SARServiceURL)
	} else {
		log.Info("no radar module configured; assessments will report it absent")
	}

	// The survey video, which the specification weights above every other
	// input: half a compensation decision in clear weather and nearly two
	// thirds under cloud. Where no URL is set nothing is called and the
	// assessment reports the ground score as an absent evidence source, which
	// is what every deployment does today.
	var srvFrames api.ClipFrames
	if cfg.VisionServiceURL != "" {
		// The only implementation is an unvalidated classical baseline. It
		// labels itself, and the client refuses it outside development, for
		// the same reason a generated radar series is refused: an absence is
		// visible on the dossier and a bad number is not.
		allowBaseline := cfg.Env == config.Development
		vc := vision.New(
			cfg.VisionServiceURL, cfg.InternalToken, captures, allowBaseline)
		vc.Gate = gates.vision
		vc.WithFrameCache(evidenceStore)
		gather.Vision = vc
		// The same client prints the report's plates: one frame out of each
		// clip, instead of the second file the phone used to upload.
		srvFrames = vc
		log.Info("ground vision module configured",
			"url", cfg.VisionServiceURL,
			"baseline_measurements_accepted", allowBaseline)
	} else {
		log.Info("no ground vision module configured; " +
			"assessments will report the heaviest evidence source absent")
	}

	// The optical record at the spots the farmer filmed, beside the parcel's
	// own.
	//
	// The same engine and the same window, over a 20 m patch around each point
	// of the walk instead of the whole field. It is an addition: the
	// parcel-wide series, its peak and its RRVL are unchanged, and the loss
	// figure is still derived from them. What it adds is the one check a field
	// mean cannot make -- whether the ground a clip came from looks, from
	// orbit, like what the clip shows.
	//
	// Costs four more optical analyses per assessment, submitted one at a time
	// so a single claim cannot push every other officer's behind it. Where no
	// analytics engine is configured this stays nil and the assessment reports
	// the check as an absent source rather than omitting it.
	if cfg.SatelliteURL != "" {
		gather.Points = api.PointOpticalFor(bgCaptures, analytics)
		log.Info("per-point optical sampling configured",
			"radius_m", survey.SamplingRadiusM)
	}

	// The console's bell. Shared between the ladder and the event store, which
	// write to it, and the API, which reads it back.
	officerNotices := notify.NewOfficers(pool)

	events := event.NewStore(pool).
		WithOfficerNotices(officerNotices).
		WithLogger(log)

	reports := report.NewStore(pool).
		WithNotifier(outbox).
		WithOfficerNotices(officerNotices).
		WithReads(reads).
		WithLogger(log)

	// The same store on the workers' pool, for the loops and sweeps that run
	// behind the requests: the pipeline, the rollups, the overdue sweep.
	bgReports := report.NewStore(workPool).
		WithNotifier(outbox).
		WithOfficerNotices(notify.NewOfficers(workPool)).
		WithLogger(log)

	// A filing deadline arrives on a date fixed when the event was declared,
	// so nothing happens to hang a warning on -- somebody has to look. Hourly
	// is often enough for a window measured in weeks, and the dedupe index on
	// officer_notifications means a repeat sweep re-warns nobody who has not
	// already read and dismissed the first one.
	// The aggregates a state-level view reads, kept current incrementally.
	//
	// A minute, not an hour: this is what an oversight dashboard shows, and a
	// Collector refreshing during an event should not be looking at figures
	// from before lunch. It costs one indexed query returning no rows when
	// nothing has been filed. See report/rollup.go.
	go bgReports.RunRollups(dispatchCtx, time.Minute)

	// And once at start, so a deployment that has just migrated is not
	// showing an empty state view for its first minute.
	go func() {
		if n, err := bgReports.RefreshRollups(dispatchCtx); err != nil {
			log.Warn("building the rollups at start", "error", err)
		} else if n > 0 {
			log.Info("built rollups at start", "keys", n)
		}
	}()

	go func() {
		// Once on start, then hourly. An API that was down overnight should
		// warn the field the moment it is back rather than at the top of the
		// next hour -- and it makes the sweep something an operator can
		// trigger by restarting, instead of a thing that only ever runs
		// unobserved.
		sweep := func() {
			if n, err := events.SweepClosing(dispatchCtx, 72*time.Hour); err != nil {
				log.Warn("warning the field about closing events", "error", err)
			} else if n > 0 {
				log.Info("warned the field about closing events", "events", n)
			}
			// And the deadlines that have already passed. An event left open
			// past its own closing date is a deadline the platform advertised
			// and did not keep.
			if n, err := events.SweepExpired(dispatchCtx); err != nil {
				log.Warn("closing events past their deadline", "error", err)
			} else if n > 0 {
				log.Info("closed events past their deadline", "events", n)
			}
			// Reports that have sat at a rung longer than that rung allows.
			// Flagged and announced; never moved. See report/overdue_sweep.go.
			if n, err := bgReports.SweepOverdue(dispatchCtx); err != nil {
				log.Warn("flagging overdue reports", "error", err)
			} else if n > 0 {
				log.Info("flagged overdue reports", "reports", n)
			}
		}
		sweep()

		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-dispatchCtx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()

	agent := ai.New(cfg.AIServiceURL).WithToken(cfg.InternalToken)
	agent.Gate = gates.ai

	// The officer's assistant.
	//
	// Built from the same AI service URL and internal token, and given tools that
	// are nothing but scoped reads of the stores this process already owns -- so
	// what it can reach is what the asking officer could reach by hand, decided by
	// the same code. Nil when no AI service is configured, and every other feature
	// is indifferent to that.
	var officerAgent *officeragent.Agent
	if cfg.AIServiceURL != "" {
		officerAgent = officeragent.New(
			officeragent.NewHTTPTurner(cfg.AIServiceURL, cfg.InternalToken),
			officeragent.ReadOnlyTools(reports),
		).WithLogger(log)
	}

	srv := &api.Server{
		Gates: []*gate.Gate{gates.satellite, gates.radar, gates.ai, gates.vision},
		// The HTML-to-PDF converter. Optional in the sense that matters: an
		// unconfigured or unreachable one costs the PDF format and nothing else,
		// because the printed report prints from its own HTML and every other
		// export is written in this process.
		PDF:       pdf.New(cfg.PDFServiceURL),
		Registry:  registry,
		Satellite: analytics,
		AI:        agent,
		Agent:     officerAgent,
		// Where a document the assistant prepared waits for the click that
		// downloads it. Redis, because the API is several stateless replicas
		// behind one load balancer and nothing routes a download back to the
		// replica that prepared the file -- see officeragent/prepared.go, which
		// also carries the caps that keep it inside the Redis budget.
		PreparedExports: officeragent.NewRedisExports(rdb),

		// Spoken guidance, and the ceiling on what it may cost. The relay being
		// unset disables the feature; the ceiling being set is what makes it safe
		// to enable, since a session is held open by a farmer rather than by a
		// request.
		LiveRelayURL: cfg.LiveRelayURL,
		LiveModel:    cfg.LiveModel,
		LiveSpend:    livespend.New(pool),
		// The same shared secret the analysis services are called with: one secret
		// to rotate rather than two, and the relay already has it.
		LiveTicketSecret: cfg.InternalToken,
		AIServiceURL:     cfg.AIServiceURL,
		Store:            store.New(pool),
		Identity:         ident,
		Events:           events,
		Reports:          reports,
		Captures:         captures,
		Frames:           srvFrames,
		Retention:        retention.New(pool, evidenceStore, log),
		Evidence:         gather,
		Fieldwork:        fieldwork.NewStore(pool),
		Calibration:      calibration.NewStore(pool),
		Metrics:          api.NewMetrics(),
		// No provider is connected yet, so messages queue and the backlog is
		// reported. A no-op sender that claimed success would hide exactly
		// how much this platform is failing to tell people.
		Notify: outbox,
		// Account invitations. Queued the same way and for the same reason:
		// with no SMTP server configured they wait, and the console tells the
		// administrator to pass the link on by hand rather than promising an
		// email that is sitting in a table.
		Mail:           mailer,
		ConsoleBaseURL: cfg.ConsoleBaseURL,
		Notifications:  officerNotices,
		Auth:           authSvc,
		Keys:           keys,
		Audit:          recorder,
		DB:             pool,
		Reads:          reads,
		Pools: []api.NamedPool{
			{Name: "requests", Pool: pool},
			{Name: "workers", Pool: workPool},
			{Name: "replica", Pool: readPool},
			{Name: "direct", Pool: direct},
		},
		Redis:            rdb,
		Log:              log,
		CORSOrigins:      cfg.CORSOrigins,
		TrustedProxyHops: cfg.TrustedProxyHops,
		MetricsToken:     cfg.MetricsToken,
		Env:              string(cfg.Env),
		// Polled by the product administrator's health view. Each service
		// answers /healthz; an empty URL is shown as not configured.
		Probes: []api.ServiceProbe{
			{Key: "radar", Name: "Radar and moisture module", URL: healthURL(cfg.SARServiceURL)},
			{Key: "vision", Name: "Ground vision module", URL: healthURL(cfg.VisionServiceURL)},
		},
		// Retrying a write without doing it twice. Durable, in Postgres,
		// because the retry after a lost reply can arrive days later.
		Idempotency:   idempotency.New(pool),
		MinAppVersion: cfg.MinAppVersion,
		// Assessments are queued and produced by the workers started below,
		// not inside the request that asks for one.
		Jobs: assessjob.New(pool),
	}

	// The workers and sweeps run on a copy of the server whose stores use the workers'
	// pool, so a backlog of assessments cannot take the connections a
	// farmer's request is waiting for.
	bg := *srv
	bg.DB = workPool
	bg.Reads = nil
	bg.Store = store.New(workPool)
	bg.Jobs = assessjob.New(workPool)
	bg.Reports = bgReports
	bg.Captures = bgCaptures
	bg.Fieldwork = fieldwork.NewStore(workPool)
	bg.Calibration = calibration.NewStore(workPool)
	bg.Identity = identity.NewStore(workPool)

	// Housekeeping for what a district-wide event leaves behind.
	//
	// None of these ran before. Abandoned upload parts are the largest objects
	// the platform stores and were kept for ever; idempotency keys, handset
	// status rows and expired remote-sensing results grew without bound. At the
	// scale of lakhs of surveys that is not tidiness, it is the disk filling up
	// in the middle of the next event.
	//
	// Hourly, each independently: one failing is logged and does not stop the
	// others, and each is bounded so a large backlog is worked through over
	// several runs rather than in one long transaction.
	go func() {
		sweep := func() {
			ctx, cancel := context.WithTimeout(dispatchCtx, 10*time.Minute)
			defer cancel()

			// Redis holds sessions, one-time codes and throttles, and runs
			// with noeviction: full, it refuses writes rather than silently
			// dropping a code a farmer is about to type. Said well before
			// that happens, on every replica, because it is per instance.
			warnIfRedisFilling(ctx, rdb, log)

			// One replica sweeps. Each would otherwise repeat the same deletes
			// against the same tables in the same minute, contending with the
			// write path and with each other.
			if ok, err := rdb.SetNX(ctx, "housekeeping:lock", hostnameOr("api"), 50*time.Minute).Result(); err == nil && !ok {
				return
			}

			// Next month's audit partition, and the one after. A log that
			// cannot take a write at midnight on the first is an outage, so
			// the partitions are made long before they are needed.
			for _, ahead := range []time.Duration{31 * 24 * time.Hour, 62 * 24 * time.Hour} {
				if _, err := workPool.Exec(ctx,
					`SELECT ensure_audit_partition($1)`, time.Now().Add(ahead)); err != nil {
					log.Error("creating the next audit partition", "error", err)
				}
			}

			if n, err := bg.Captures.SweepAbandonedUploads(ctx, time.Now()); err != nil {
				log.Warn("sweeping abandoned uploads", "error", err)
			} else if n > 0 {
				log.Info("swept abandoned uploads", "uploads", n)
			}
			if n, err := idempotency.New(workPool).Sweep(ctx); err != nil {
				log.Warn("sweeping idempotency keys", "error", err)
			} else if n > 0 {
				log.Info("swept idempotency keys", "keys", n)
			}
			if n, err := bg.SweepDeviceStatus(ctx); err != nil {
				log.Warn("sweeping device sync status", "error", err)
			} else if n > 0 {
				log.Info("swept silent devices", "devices", n)
			}
			if n, err := outbox.SweepSent(ctx, cfg.NotificationRetention); err != nil {
				log.Warn("sweeping delivered notifications", "error", err)
			} else if n > 0 {
				log.Info("swept delivered notifications", "rows", n)
			}
			if n, err := cache.Sweep(ctx); err != nil {
				log.Warn("sweeping the remote sensing cache", "error", err)
			} else if n > 0 {
				log.Info("swept expired remote sensing results", "rows", n)
			}
		}
		sweep()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-dispatchCtx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()

	// The assessment queue. Each worker produces one assessment at a time,
	// at the rate the Earth Engine project and the model quota allow; the
	// request that asked for it returned long ago.
	bg.RunAssessments(dispatchCtx, cfg.AssessmentWorkers)
	// And the claim's own progress: acknowledged on complete receipt, assessed
	// without being asked, then allocated to the village pair.
	bg.RunClaimPipeline(dispatchCtx, cfg.PipelineWorkers)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// Generous: a cold assessment waits on two Earth Engine tasks and a
		// multimodal model.
		WriteTimeout: 6 * time.Minute,
		IdleTimeout:  2 * time.Minute,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	// TLS in this process, when a certificate is configured.
	//
	// The alternative is a proxy that terminates instead, which is the common
	// deployment and is why this is optional rather than required. What is not
	// optional is that one of the two is true: the configuration loader refuses
	// a hardened environment with neither.
	//
	// TLS 1.2 is the floor rather than 1.3 because the handsets this serves
	// include Android 6 devices, whose system TLS stack does not speak 1.3 --
	// and a farmer with an old phone being unable to file is a worse outcome
	// than a modern cipher suite. Go's own preference ordering picks 1.3 with
	// everything that can.
	if cfg.TLSCertFile != "" {
		httpSrv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Only forward-secret suites, so a key recovered later cannot open
			// traffic captured today. Go orders the TLS 1.3 suites itself and
			// ignores this list for them.
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			},
		}
	}

	errCh := make(chan error, 1)
	go func() {
		scheme := "http"
		if cfg.TLSCertFile != "" {
			scheme = "https"
		}
		log.Info("listening",
			"addr", cfg.Addr,
			"scheme", scheme,
			"satellite", cfg.SatelliteURL,
			"ai", cfg.AIServiceURL,
			"cors_origins", cfg.CORSOrigins)

		var err error
		if cfg.TLSCertFile != "" {
			err = httpSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String())
	}

	// Stopped before the HTTP drain, so a message is not claimed and left in
	// "sending" by a process that is about to exit.
	stopDispatch()

	// Long enough for an assessment in flight to finish rather than be cut
	// off after the model call has already been paid for.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
	}

	return nil
}

// bootstrapAdmin creates the first administrator when the roll is empty.
func bootstrapAdmin(
	ctx context.Context, cfg *config.Config, ident *identity.Store, log *slog.Logger,
) error {
	n, err := ident.Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		if cfg.BootstrapAdminPassword != "" {
			log.Warn("ESURVEY_BOOTSTRAP_ADMIN_PASSWORD is still set but accounts " +
				"already exist; remove it from the environment")
		}
		return nil
	}

	if cfg.BootstrapAdminUsername == "" || cfg.BootstrapAdminPassword == "" {
		return config.ErrNoBootstrap
	}
	// The first administrator signs in with an email address like every other
	// officer. Refused here with the variable named, rather than at the insert
	// with a message about a login.
	if _, err := identity.NormalizeEmail(cfg.BootstrapAdminUsername); err != nil {
		return fmt.Errorf("ESURVEY_BOOTSTRAP_ADMIN_USERNAME must be the "+
			"administrator's email address: %w", err)
	}
	if err := auth.CheckPasswordPolicy(
		cfg.BootstrapAdminPassword, cfg.BootstrapAdminUsername, "Bootstrap Administrator",
	); err != nil {
		return err
	}

	hash, err := auth.HashPassword(cfg.BootstrapAdminPassword)
	if err != nil {
		return err
	}
	return identity.Bootstrap(ctx, ident, log, cfg.BootstrapAdminUsername, hash)
}

func newLogger(env config.Environment) *slog.Logger {
	level := slog.LevelInfo
	if env == config.Development {
		level = slog.LevelDebug
	}

	// JSON in a real deployment, because something downstream parses it;
	// text on a laptop, because a person reads it.
	if env.Hardened() {
		return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// openEvidenceStore chooses where farmer-captured media is kept.
//
// A named bucket wins. There is no automatic fallback from a configured
// bucket to the local disk: silently writing evidence somewhere other than
// where an operator believes it is going is worse than failing to start.
func openEvidenceStore(ctx context.Context, cfg *config.Config) (blob.Store, error) {
	if cfg.EvidenceBucket != "" {
		return blob.NewGCS(ctx, cfg.EvidenceBucket, cfg.EvidencePrefix)
	}
	return blob.NewDir(cfg.EvidenceDir)
}

// warnIfRedisFilling logs when Redis is past three quarters of its memory.
func warnIfRedisFilling(ctx context.Context, rdb *redis.Client, log *slog.Logger) {
	info, err := rdb.Info(ctx, "memory").Result()
	if err != nil {
		return
	}
	var used, limit int64
	for _, line := range strings.Split(info, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch k {
		case "used_memory":
			used, _ = strconv.ParseInt(v, 10, 64)
		case "maxmemory":
			limit, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if limit > 0 && used*4 > limit*3 {
		log.Warn("redis is past 75% of its memory; sessions and one-time codes "+
			"will be refused when it is full -- raise REDIS_MAXMEMORY",
			"used_bytes", used, "max_bytes", limit)
	}
}

func hostnameOr(fallback string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return fallback
}

// healthURL is a service's health endpoint, or empty when the service is not
// configured.
func healthURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + "/healthz"
}
