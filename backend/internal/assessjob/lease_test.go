package assessjob

import (
	"context"
	"testing"
	"time"
)

// Leases, deferral and run time: what lets many workers and several instances
// share the queue without losing work or wasting attempts.

func TestARenewedLeaseIsNotTakenFromASlowWorker(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	job, err := s.Enqueue(ctx, request(who, "F-JOB-L1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "slow-worker", 1); err != nil {
		t.Fatal(err)
	}
	// Claimed long ago, as a slow satellite analysis would be...
	if _, err := pool.Exec(ctx, `UPDATE assessment_jobs
		SET claimed_at = now() - interval '1 hour' WHERE id = $1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	// ...but still renewing.
	ok, err := s.Extend(ctx, job.ID, "slow-worker")
	if err != nil || !ok {
		t.Fatalf("renewing its own lease: ok=%v err=%v", ok, err)
	}
	if n, err := s.ReleaseStale(ctx, Lease); err != nil || n != 0 {
		t.Fatalf("released %d jobs (%v); a worker renewing its lease is alive", n, err)
	}
}

func TestAWorkerThatLostItsLeaseIsToldSo(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	job, err := s.Enqueue(ctx, request(who, "F-JOB-L2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "stalled-worker", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE assessment_jobs
		SET claimed_at = now() - interval '1 hour' WHERE id = $1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseStale(ctx, Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "other-worker", 1); err != nil {
		t.Fatal(err)
	}

	ok, err := s.Extend(ctx, job.ID, "stalled-worker")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a worker whose job was taken by another was allowed to carry on; " +
			"the claim would be assessed twice")
	}
}

func TestWaitingForASlotDoesNotSpendAnAttempt(t *testing.T) {
	// After a hailstorm every slot is busy for hours. A job that timed out in
	// that queue five times would be given up on without ever being tried.
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	job, err := s.Enqueue(ctx, request(who, "F-JOB-L3"))
	if err != nil {
		t.Fatal(err)
	}
	for range MaxAttempts + 2 {
		if _, err := pool.Exec(ctx, `UPDATE assessment_jobs
			SET next_attempt_at = now() WHERE id = $1::uuid`, job.ID); err != nil {
			t.Fatal(err)
		}
		claimed, err := s.Claim(ctx, "busy-worker", 1)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("claiming: %d jobs, %v", len(claimed), err)
		}
		if err := s.Defer(ctx, job.ID, "busy", time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateQueued || got.Attempts != 0 {
		t.Errorf("after waiting %d times: state %s, attempts %d; want queued, 0",
			MaxAttempts+2, got.State, got.Attempts)
	}
}

func TestHowLongAJobRanIsRecorded(t *testing.T) {
	s, pool := testJobs(t)
	ctx := context.Background()
	who := officer(t, pool)

	job, err := s.Enqueue(ctx, request(who, "F-JOB-L4"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "timed-worker", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE assessment_jobs
		SET claimed_at = now() - interval '90 seconds' WHERE id = $1::uuid`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail(ctx, job.ID, "no clear observation"); err != nil {
		t.Fatal(err)
	}

	var ran int
	if err := pool.QueryRow(ctx, `SELECT run_ms FROM assessment_jobs WHERE id = $1::uuid`,
		job.ID).Scan(&ran); err != nil {
		t.Fatal(err)
	}
	if ran < 89_000 || ran > 120_000 {
		t.Errorf("run_ms = %d; the job ran about ninety seconds", ran)
	}

	tp, err := s.RecentThroughput(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tp.FailedLastHour < 1 || tp.RunP50MS <= 0 || tp.PerDay < 24 {
		t.Errorf("throughput does not count the job: %+v", tp)
	}
}
