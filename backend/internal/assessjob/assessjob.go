// Package assessjob is the queue of assessments waiting to be produced.
//
// # Why a queue
//
// Producing one assessment is minutes of work against services that are
// rate-limited by contract: Earth Engine admits a handful of jobs at once, and
// the model panel costs money per call. Run inline, that made an officer's
// browser hold a connection open for minutes, lost everything in flight on a
// deploy, and turned "the engine is busy" into "your assessment failed".
//
// So the request records what was asked for and returns. Workers drain the
// queue at the rate the engine actually allows, retry what was a bad minute
// rather than a bad request, and record where they got to. The console polls.
//
// The shape -- claim with SKIP LOCKED, lease, backoff in a column, give up
// only on a refusal -- is the notification outbox's, and the phone queue's.
// It is the shape this platform uses for anything that must not be lost.
package assessjob

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// ErrNotFound is returned for a job id nobody has, or that the caller may not
// see.
var ErrNotFound = errors.New("no such assessment job")

// States a job can be in.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// MaxAttempts is how many times a job is tried before it is given up on.
//
// Five, with the backoff below, spans about half an hour: long enough to ride
// out an Earth Engine outage or a model provider's bad afternoon, short enough
// that an officer waiting for a figure is told something within a shift.
const MaxAttempts = 5

// Lease is how long a claimed job is left with a worker that has stopped
// saying it is still working on it.
//
// Short, because the worker renews it (Extend) every HeartbeatEvery while the
// job runs. It was fifteen minutes with no renewal -- longer than the slowest
// assessment -- which meant every job on an instance that died waited a
// quarter of an hour before anyone else picked it up. After a hailstorm that
// is thousands of claims.
const Lease = 3 * time.Minute

// HeartbeatEvery is how often a working worker renews its lease.
const HeartbeatEvery = time.Minute

// Request is what to assess.
type Request struct {
	FarmerID         string
	FarmID           string
	IncidentDate     string
	SowingDate       string
	CropLossReportID string

	// SurveyEventID is the calamity the officer chose on the console. Empty
	// where the pipeline queued this for a claim, which names its own event.
	SurveyEventID string

	RequestedBy    string
	VillageLGDCode string
}

// Job is a request with what has happened to it.
type Job struct {
	ID string `json:"id"`

	FarmerID         string `json:"farmer_id"`
	FarmID           string `json:"farm_id"`
	IncidentDate     string `json:"incident_date"`
	SowingDate       string `json:"sowing_date,omitempty"`
	CropLossReportID string `json:"crop_loss_report_id,omitempty"`

	RequestedBy string `json:"requested_by"`
	// SurveyEventID is the calamity this was requested against.
	SurveyEventID  string `json:"survey_event_id,omitempty"`
	VillageLGDCode string `json:"village_lgd_code,omitempty"`

	State    string `json:"state"`
	Attempts int    `json:"attempts"`

	// AssessmentID is set once the work is done. It is what the console
	// navigates to.
	AssessmentID string `json:"assessment_id,omitempty"`
	// Error is why it was given up on, in words an officer can act on.
	Error string `json:"error,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Existing is true when an identical request was already queued or
	// running, and this is that job rather than a new one.
	Existing bool `json:"existing,omitempty"`
}

// Waiting reports whether the answer is still to come.
func (j Job) Waiting() bool { return j.State == StateQueued || j.State == StateRunning }

// Store is the queue.
type Store struct {
	pool *db.Pool
}

func New(pool *db.Pool) *Store { return &Store{pool: pool} }

const columns = `
	id::text, farmer_id, farm_id, to_char(incident_date, 'YYYY-MM-DD'),
	COALESCE(to_char(sowing_date, 'YYYY-MM-DD'), ''),
	COALESCE(crop_loss_report_id::text, ''),
	-- Empty means the claim pipeline queued it on receipt, with no officer to
	-- name. See migration 0042.
	COALESCE(requested_by::text, ''),
	COALESCE(survey_event_id::text, ''),
	village_lgd_code, state::text, attempts,
	COALESCE(assessment_id::text, ''), COALESCE(error, ''),
	created_at, updated_at, finished_at`

func scan(row pgx.Row) (*Job, error) {
	var j Job
	if err := row.Scan(&j.ID, &j.FarmerID, &j.FarmID, &j.IncidentDate,
		&j.SowingDate, &j.CropLossReportID, &j.RequestedBy, &j.SurveyEventID,
		&j.VillageLGDCode,
		&j.State, &j.Attempts, &j.AssessmentID, &j.Error,
		&j.CreatedAt, &j.UpdatedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// Enqueue records a request, or returns the live job that already asks it.
//
// Two officers asking the same question, or one console retrying after a lost
// reply, is one piece of Earth Engine work. The unique index does the deciding
// (migration 0036); this reads back what won.
func (s *Store) Enqueue(ctx context.Context, r Request) (*Job, error) {
	return s.enqueue(ctx, s.pool, r)
}

// enqueue is Enqueue on whatever q is: the pool, or a caller's transaction so
// the job commits or vanishes with the change that asked for it.
func (s *Store) enqueue(ctx context.Context, q db.Querier, r Request) (*Job, error) {
	job, err := scan(q.QueryRow(ctx, `
		INSERT INTO assessment_jobs (
			farmer_id, farm_id, incident_date, sowing_date,
			crop_loss_report_id, requested_by, village_lgd_code,
			survey_event_id)
		VALUES ($1, $2, $3::date, NULLIF($4, '')::date,
		        NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, $7,
		        NULLIF($8, '')::uuid)
		ON CONFLICT DO NOTHING
		RETURNING `+columns,
		r.FarmerID, r.FarmID, r.IncidentDate, r.SowingDate,
		r.CropLossReportID, r.RequestedBy, r.VillageLGDCode,
		r.SurveyEventID))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("queueing an assessment: %w", err)
	}

	existing, err := scan(q.QueryRow(ctx, `
		SELECT `+columns+` FROM assessment_jobs
		 WHERE farmer_id = $1 AND farm_id = $2 AND incident_date = $3::date
		   AND COALESCE(crop_loss_report_id::text, '') = $4
		   AND state IN ('queued', 'running')`,
		r.FarmerID, r.FarmID, r.IncidentDate, r.CropLossReportID))
	if errors.Is(err, pgx.ErrNoRows) {
		// It finished between the insert and this read. Rare, and the honest
		// answer is to ask again rather than to invent a job.
		return s.enqueue(ctx, q, r)
	}
	if err != nil {
		return nil, fmt.Errorf("reading a queued assessment: %w", err)
	}
	existing.Existing = true
	return existing, nil
}

// Claim takes up to n due jobs for this worker.
//
// SKIP LOCKED, so workers -- in this process or another replica -- divide the
// queue rather than fighting over it.
func (s *Store) Claim(ctx context.Context, worker string, n int) ([]*Job, error) {
	if n <= 0 {
		n = 1
	}
	rows, err := s.pool.Query(ctx, `
		-- MATERIALIZED: the pick runs exactly once. As a bare IN (subquery)
		-- the planner may run it again per row, and each run skips the rows
		-- the last one locked, so LIMIT 1 claimed every queued job.
		WITH picked AS MATERIALIZED (
			SELECT id FROM assessment_jobs
			 WHERE state = 'queued' AND next_attempt_at <= now()
			 ORDER BY next_attempt_at
			 LIMIT $2
			 FOR UPDATE SKIP LOCKED)
		UPDATE assessment_jobs SET
			state = 'running', attempts = attempts + 1,
			claimed_by = $1, claimed_at = now()
		WHERE id IN (SELECT id FROM picked)
		RETURNING `+columns, worker, n)
	if err != nil {
		return nil, fmt.Errorf("claiming assessments: %w", err)
	}
	defer rows.Close()

	var out []*Job
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Done records the assessment a job produced.
func (s *Store) Done(ctx context.Context, id, assessmentID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE assessment_jobs
		   SET state = 'done', assessment_id = $2::uuid, error = NULL,
		       run_ms = `+runMS+`,
		       claimed_by = NULL, claimed_at = NULL, finished_at = now()
		 WHERE id = $1::uuid`, id, assessmentID)
	if err != nil {
		return fmt.Errorf("recording a finished assessment: %w", err)
	}
	s.wakeClaim(ctx, id)
	return nil
}

// Retry puts a job back for another attempt, or gives up once the attempts are
// spent.
//
// For failures that say nothing about the request: the engine was busy, the
// model provider had a bad minute, the network went. Reports whether it will
// be tried again.
func (s *Store) Retry(ctx context.Context, id, because string) (bool, error) {
	var attempts int
	if err := s.pool.QueryRow(ctx,
		`SELECT attempts FROM assessment_jobs WHERE id = $1::uuid`, id).
		Scan(&attempts); err != nil {
		return false, err
	}
	if attempts >= MaxAttempts {
		return false, s.Fail(ctx, id, because)
	}

	// Exponential, from a minute: 1, 2, 4, 8, 16. Long enough for an engine
	// quota window to reopen, short enough that an officer is not left
	// waiting a shift for a figure.
	backoff := time.Duration(1<<uint(min(attempts, 6))) * time.Minute
	_, err := s.pool.Exec(ctx, `
		UPDATE assessment_jobs
		   SET state = 'queued', error = $2,
		       next_attempt_at = now() + $3::interval,
		       claimed_by = NULL, claimed_at = NULL
		 WHERE id = $1::uuid`, id, because, backoff.String())
	if err != nil {
		return false, fmt.Errorf("requeueing an assessment: %w", err)
	}
	return true, nil
}

// Fail gives up on a job, with a reason an officer can read.
//
// For a refusal: a parcel with no boundary, a window with no clear satellite
// observation, a request the pipeline will refuse however many times it is
// tried.
func (s *Store) Fail(ctx context.Context, id, because string) error {
	if because == "" {
		because = "the assessment could not be produced"
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE assessment_jobs
		   SET state = 'failed', error = $2,
		       run_ms = `+runMS+`,
		       claimed_by = NULL, claimed_at = NULL, finished_at = now()
		 WHERE id = $1::uuid`, id, because)
	if err != nil {
		return fmt.Errorf("recording a failed assessment: %w", err)
	}
	s.wakeClaim(ctx, id)
	return nil
}

// wakeClaim makes the claim waiting on a job due now.
//
// The claim pipeline does not have to poll a job to learn it ended: the job
// says so. Polling every waiting claim every minute was what starved the
// pipeline -- twenty thousand claims waiting on assessments cost 333 checks a
// second, more than one loop can do, and the receipts of new claims queued
// behind them. The pipeline still looks at waiting claims now and then, which
// covers a process that dies between the job's update and this one.
//
// A separate statement, after the job's own has committed, and best effort.
// Folding it into the job's statement would hold the job row while waiting on
// the claim row, which the pipeline may be holding while it queues a job for
// the same parcel -- a deadlock waiting for its day.
func (s *Store) wakeClaim(ctx context.Context, jobID string) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET pipeline_next_at = now()
		 WHERE id = (SELECT crop_loss_report_id FROM assessment_jobs
		              WHERE id = $1::uuid)
		   AND assessment_job_id = $1::uuid
		   AND assessment_state = 'queued'
		   AND state IN ('submitted', 'acknowledged')
		   AND pipeline_next_at > now()`, jobID)
}

// runMS is how long the last attempt ran, from its claim.
const runMS = `CASE WHEN claimed_at IS NULL THEN NULL
	ELSE (extract(epoch FROM now() - claimed_at) * 1000)::integer END`

// Extend renews a worker's lease on a running job.
//
// Reports false when the job is no longer this worker's -- released as stale
// after a stall, or finished elsewhere -- and the worker must stop: carrying
// on would produce a second assessment of the same claim.
func (s *Store) Extend(ctx context.Context, id, worker string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE assessment_jobs SET claimed_at = now()
		 WHERE id = $1::uuid AND claimed_by = $2 AND state = 'running'`, id, worker)
	if err != nil {
		return false, fmt.Errorf("renewing an assessment lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Defer puts a job back without spending an attempt.
//
// For a job that never got to try: every upstream slot was taken for as long
// as it could wait. That says the deployment is busy, not that the request is
// bad, and counting it would fail claims after a hailstorm for no reason but
// the queue in front of them.
func (s *Store) Defer(ctx context.Context, id, because string, after time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE assessment_jobs
		   SET state = 'queued', error = $2,
		       attempts = GREATEST(attempts - 1, 0),
		       next_attempt_at = now() + $3::interval,
		       claimed_by = NULL, claimed_at = NULL
		 WHERE id = $1::uuid AND state = 'running'`, id, because, after.String())
	if err != nil {
		return fmt.Errorf("deferring an assessment: %w", err)
	}
	return nil
}

// Throughput is what the workers have got through recently, for sizing.
type Throughput struct {
	DoneLastHour   int     `json:"done_last_hour"`
	FailedLastHour int     `json:"failed_last_hour"`
	RunP50MS       float64 `json:"run_p50_ms"`
	RunP95MS       float64 `json:"run_p95_ms"`
	// PerDay is the last hour's finished jobs times 24: what this deployment
	// would get through in a day at the rate it is going now. Only meaningful
	// while there is a backlog -- an idle queue finishes what it is given.
	PerDay int `json:"per_day_at_this_rate"`
}

// RecentThroughput reads the last hour.
func (s *Store) RecentThroughput(ctx context.Context) (*Throughput, error) {
	var t Throughput
	var p50, p95 *float64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state = 'done'),
		       count(*) FILTER (WHERE state = 'failed'),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY run_ms),
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY run_ms)
		  FROM assessment_jobs
		 WHERE finished_at > now() - interval '1 hour'`).
		Scan(&t.DoneLastHour, &t.FailedLastHour, &p50, &p95); err != nil {
		return nil, fmt.Errorf("reading assessment throughput: %w", err)
	}
	if p50 != nil {
		t.RunP50MS = *p50
	}
	if p95 != nil {
		t.RunP95MS = *p95
	}
	t.PerDay = (t.DoneLastHour + t.FailedLastHour) * 24
	return &t, nil
}

// ReleaseStale returns jobs whose worker died to the queue.
//
// A process killed mid-assessment leaves 'running' behind. Without this the
// job is never worked again and the officer waits for something nobody is
// doing.
func (s *Store) ReleaseStale(ctx context.Context, lease time.Duration) (int, error) {
	if lease <= 0 {
		lease = Lease
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE assessment_jobs
		   SET state = 'queued', claimed_by = NULL, claimed_at = NULL,
		       next_attempt_at = now()
		 WHERE state = 'running' AND claimed_at < now() - $1::interval`,
		lease.String())
	if err != nil {
		return 0, fmt.Errorf("releasing stale assessments: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Get reads one job.
func (s *Store) Get(ctx context.Context, id string) (*Job, error) {
	j, err := scan(s.pool.QueryRow(ctx,
		`SELECT `+columns+` FROM assessment_jobs WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading an assessment job: %w", err)
	}
	return j, nil
}

// Backlog is what an operator needs to know about the queue.
type Backlog struct {
	Queued  int        `json:"queued"`
	Running int        `json:"running"`
	Failed  int        `json:"failed_last_24h"`
	Oldest  *time.Time `json:"oldest_queued_at,omitempty"`
}

// Waiting reports the queue's depth, for readiness and for the console.
func (s *Store) Waiting(ctx context.Context) (*Backlog, error) {
	var b Backlog
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state = 'queued'),
		       count(*) FILTER (WHERE state = 'running'),
		       count(*) FILTER (WHERE state = 'failed'
		                          AND finished_at > now() - interval '24 hours'),
		       min(created_at) FILTER (WHERE state = 'queued')
		  FROM assessment_jobs`).
		Scan(&b.Queued, &b.Running, &b.Failed, &b.Oldest); err != nil {
		return nil, fmt.Errorf("reading the assessment backlog: %w", err)
	}
	return &b, nil
}

// WaitingIn is the queue's depth for claims inside an area: villages at or
// below the given jurisdictions. The console's figure for an officer, who
// should not be reading another state's workload as their own.
func (s *Store) WaitingIn(ctx context.Context, jurisdictionIDs []string) (*Backlog, error) {
	var b Backlog
	if len(jurisdictionIDs) == 0 {
		return &b, nil
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE q.state = 'queued'),
		       count(*) FILTER (WHERE q.state = 'running'),
		       count(*) FILTER (WHERE q.state = 'failed'
		                          AND q.finished_at > now() - interval '24 hours'),
		       min(q.created_at) FILTER (WHERE q.state = 'queued')
		  FROM assessment_jobs q
		  JOIN jurisdictions v ON v.level = 'village' AND v.code = q.village_lgd_code
		 WHERE v.id = ANY($1::uuid[]) OR v.ancestor_ids && $1::uuid[]`,
		jurisdictionIDs).
		Scan(&b.Queued, &b.Running, &b.Failed, &b.Oldest); err != nil {
		return nil, fmt.Errorf("reading the assessment backlog: %w", err)
	}
	return &b, nil
}

// Queue records an assessment that nobody asked for.
//
// The claim pipeline calls this on complete receipt: an officer should not
// have to remember to request the evidence pack for a claim the office has
// just been given. Idempotent through Enqueue, which answers with the job
// already queued for the same parcel and incident rather than making a second.
//
// RequestedBy is deliberately empty. No officer decided this, and naming one
// would put a name in the audit trail that did nothing.
func (s *Store) Queue(
	ctx context.Context, q db.Querier, reportID, farmerID, farmID, villageCode string,
	incidentAt time.Time,
) (string, error) {
	job, err := s.enqueue(ctx, q, Request{
		FarmerID:     farmerID,
		FarmID:       farmID,
		IncidentDate: incidentAt.UTC().Format("2006-01-02"),
		// Where the parcel is, which is what decides who may read the job.
		// Queued without it, every pipeline job was unplaceable -- and the
		// read check skipped the scope test entirely for a job it could not
		// place, so anybody holding assessment:read could read it. Farmers
		// hold assessment:read.
		VillageLGDCode:   villageCode,
		CropLossReportID: reportID,
	})
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

// Ended reports whether a job has stopped, either way.
//
// A failed assessment is an answer: the claim goes to the officers without one
// rather than waiting for a service to recover. A job id nobody has counts as
// ended too -- it cannot be waited for, and a claim must not be stuck behind a
// row that is not there.
func (s *Store) Ended(ctx context.Context, jobID string) (bool, error) {
	if jobID == "" {
		return true, nil
	}
	var state string
	err := s.pool.QueryRow(ctx,
		`SELECT state::text FROM assessment_jobs WHERE id = $1::uuid`, jobID).
		Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading an assessment job: %w", err)
	}
	return state == StateDone || state == StateFailed, nil
}
