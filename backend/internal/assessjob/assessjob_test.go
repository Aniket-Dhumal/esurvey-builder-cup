package assessjob

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The queue that decides whether an assessment is ever produced.
//
// Database-backed, because every guarantee here is one: only one worker may
// claim a job, a crashed worker's job comes back, and a second request for the
// same question does not become a second piece of Earth Engine work.

func testJobs(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the assessment queue tests")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)

	clean := func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM assessment_jobs WHERE farmer_id LIKE 'F-JOB-%'`)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE username = 'job.officer@esurvey-test.in'`)
	}
	clean()
	t.Cleanup(clean)
	return New(pool), pool
}

// officer returns an account id to hang requests on; jobs reference users.
func officer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO users (username, email, full_name, kind, status, password_hash)
		VALUES ('job.officer@esurvey-test.in', 'job.officer@esurvey-test.in', 'Job Officer', 'officer', 'active',
		        '$argon2id$v=19$m=65536,t=3,p=4$c2VlZHNlZWRzZWVk$notarealhash')
		ON CONFLICT (username) DO UPDATE SET full_name = EXCLUDED.full_name
		RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seeding an officer: %v", err)
	}
	return id
}

func request(who, farmer string) Request {
	return Request{
		FarmerID:       farmer,
		FarmID:         "FARM-1",
		IncidentDate:   "2026-08-01",
		RequestedBy:    who,
		VillageLGDCode: "V-1",
	}
}

func TestAQueuedRequestWaitsToBeWorked(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	job, err := s.Enqueue(ctx, request(who, "F-JOB-1"))
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateQueued || job.Existing {
		t.Fatalf("a new request is %s (existing=%v); want queued", job.State, job.Existing)
	}

	read, err := s.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !read.Waiting() {
		t.Fatal("a queued job does not report itself as waiting")
	}
}

func TestTheSameQuestionIsOnePieceOfWork(t *testing.T) {
	// Two officers asking about one parcel, or a console retrying after a
	// lost reply. Earth Engine time is the thing being protected.
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	first, err := s.Enqueue(ctx, request(who, "F-JOB-2"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Enqueue(ctx, request(who, "F-JOB-2"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.Existing {
		t.Fatalf("the second request queued %s; want the first job %s", again.ID, first.ID)
	}

	// Once it has been answered, asking again is a new question: the evidence
	// may have moved on, and an officer asking twice a week apart means it.
	if err := s.Done(ctx, first.ID, assessmentFor(t, pool)); err != nil {
		t.Fatal(err)
	}
	third, err := s.Enqueue(ctx, request(who, "F-JOB-2"))
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID {
		t.Fatal("a request after the answer was folded into the finished job")
	}
}

func TestOnlyOneWorkerGetsAJob(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	const jobs = 12
	for i := 0; i < jobs; i++ {
		r := request(who, "F-JOB-3")
		r.FarmID = "FARM-" + string(rune('a'+i))
		if _, err := s.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 4; i++ {
				claimed, err := s.Claim(ctx, "worker", 1)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				mu.Lock()
				for _, j := range claimed {
					seen[j.ID]++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != jobs {
		t.Fatalf("%d jobs were claimed; %d were queued", len(seen), jobs)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("job %s was claimed %d times", id, n)
		}
	}
}

func TestAMomentsFailureIsTriedAgainAndARefusalIsNot(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	busy, err := s.Enqueue(ctx, request(who, "F-JOB-4"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "worker", 1); err != nil {
		t.Fatal(err)
	}
	again, err := s.Retry(ctx, busy.ID, "the satellite service is not responding")
	if err != nil {
		t.Fatal(err)
	}
	if !again {
		t.Fatal("a first failure gave up instead of waiting")
	}
	after, _ := s.Get(ctx, busy.ID)
	if after.State != StateQueued {
		t.Fatalf("a deferred job is %s; want queued", after.State)
	}
	if after.Attempts != 1 {
		t.Fatalf("attempts = %d; want the one that was made", after.Attempts)
	}
	// Waiting, not due: the backoff is in the column.
	if claimed, err := s.Claim(ctx, "worker", 1); err != nil || len(claimed) != 0 {
		t.Fatalf("a job waiting out its backoff was claimed again (%v)", err)
	}

	refused, err := s.Enqueue(ctx, request(who, "F-JOB-5"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(ctx, refused.ID, "this parcel has no boundary"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, refused.ID)
	if got.State != StateFailed || got.Error == "" {
		t.Fatalf("a refused job is %s with error %q", got.State, got.Error)
	}
	if got.Waiting() {
		t.Fatal("a refused job still reports itself as waiting")
	}
}

func TestAJobIsGivenUpOnAfterItsAttempts(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	job, err := s.Enqueue(ctx, request(who, "F-JOB-6"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxAttempts+1; i++ {
		if _, err := pool.Exec(ctx, `
			UPDATE assessment_jobs SET state = 'queued', next_attempt_at = now()
			 WHERE id = $1::uuid`, job.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Claim(ctx, "worker", 1); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Retry(ctx, job.ID, "the engine is busy"); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(ctx, job.ID)
	if got.State != StateFailed {
		t.Fatalf("after %d attempts the job is %s; want failed with a reason",
			got.Attempts, got.State)
	}
	if got.Error == "" {
		t.Fatal("a job given up on says nothing about why")
	}
}

func TestWorkFromADeadWorkerComesBack(t *testing.T) {
	// A deploy, an OOM, a power cut. Without this the officer waits for an
	// assessment nobody is producing.
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	job, err := s.Enqueue(ctx, request(who, "F-JOB-7"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "doomed-worker", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE assessment_jobs SET claimed_at = now() - interval '1 hour'
		  WHERE id = $1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}

	n, err := s.ReleaseStale(ctx, Lease)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("a job whose worker died was not released")
	}
	claimed, err := s.Claim(ctx, "new-worker", 1)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("the released job was not claimable again (%v)", err)
	}
}

func TestTheBacklogIsReadable(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	for i := 0; i < 3; i++ {
		r := request(who, "F-JOB-8")
		r.FarmID = "FARM-B" + string(rune('a'+i))
		if _, err := s.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Claim(ctx, "worker", 1); err != nil {
		t.Fatal(err)
	}

	b, err := s.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.Queued < 2 || b.Running < 1 {
		t.Fatalf("backlog says queued=%d running=%d; three were queued and one claimed",
			b.Queued, b.Running)
	}
	if b.Oldest == nil {
		t.Fatal("the backlog does not say how long the oldest has waited")
	}
}

func TestAJobIdNobodyHasIsNotFound(t *testing.T) {
	s, _ := testJobs(t)
	_, err := s.Get(context.Background(), "3f2a1c4e-1111-4222-8333-444455556666")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// assessmentFor returns the id of some assessment, so Done has something valid
// to point at. The queue's foreign key is what makes a finished job readable.
func assessmentFor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM assessments LIMIT 1`).Scan(&id)
	if err == nil {
		return id
	}
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO assessments (farmer_id, farm_id, survey_number,
			village_lgd_code, sowing_date, incident_date, crop_name,
			owner_extent_hectares, assessment, satellite, terrain,
			triage_tier, crop_presence, confidence)
		VALUES ('F-JOB-1', 'FARM-1', '1', 'V-1',
			'2026-06-01', '2026-08-01', 'Soybean', 1.0,
			'{}'::jsonb, '{}'::jsonb, '{}'::jsonb, 'auto', 'present', 0.5)
		RETURNING id::text`).Scan(&id); err != nil {
		t.Skipf("no assessment to point a finished job at: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM assessments WHERE id = $1::uuid`, id)
	})
	return id
}

// A worker asking for one job gets one. The rest stay queued for the workers
// that have room, rather than sitting "running" on one that has none until the
// dead-worker sweep hands them back.
func TestAClaimTakesNoMoreThanItAskedFor(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	for i := 0; i < 5; i++ {
		r := request(who, "F-JOB-9")
		r.FarmID = "FARM-C" + string(rune('a'+i))
		if _, err := s.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int{1, 2} {
		got, err := s.Claim(ctx, "worker", want)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != want {
			t.Fatalf("asked for %d jobs, claimed %d", want, len(got))
		}
	}
}
