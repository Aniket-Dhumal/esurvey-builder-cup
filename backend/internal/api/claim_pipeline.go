package api

import (
	"context"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Running the claim pipeline.
//
// A few loops, not one. Each step is a short transaction -- an update, a
// queue insert, two assignment rows -- and the loops divide the claims with
// SKIP LOCKED rather than contend for them. One loop did about 176 steps a
// second, which the evening a district takes thirty thousand claims is not
// enough to keep receipts coming (see report/pipeline_scale_test.go). Each
// loop holds one database connection while it steps, so the count stays well
// under the pool.
//
// The sweep is separate and rare: it is the only thing that moves a claim
// whose evidence stopped arriving, and it must not be starved by a busy
// pipeline.

// PipelineWorkers is how many loops advance claims in one instance by default.
const PipelineWorkers = 4

// pipelineIdle is how long the worker sleeps when nothing is due.
const pipelineIdle = 5 * time.Second

// sweepEvery is how often claims waiting on evidence are checked against the
// deadline. Ten minutes: the deadline is measured in days, and a claim
// released a few minutes late is nobody's problem.
const sweepEvery = 10 * time.Minute

// RunClaimPipeline advances claims until the context is cancelled.
//
// Acknowledge on complete receipt, queue the assessment, then allocate to the
// village pair. See report.AdvanceOne for what each step is and why the
// claim's own state drives it.
func (s *Server) RunClaimPipeline(ctx context.Context, workers int) {
	if s.Reports == nil {
		s.Log.Warn("claim pipeline idle: no report store")
		return
	}

	// Nil is allowed and means assessments are not produced here. The claim
	// still advances: it is acknowledged, marked as having no assessment, and
	// allocated to the officers who were always the ones deciding it.
	var assessor report.Assessor
	if s.Jobs != nil {
		assessor = s.Jobs
	} else {
		s.Log.Warn("claim pipeline running without an assessment queue")
	}

	if workers <= 0 {
		workers = PipelineWorkers
	}
	s.Log.Info("claim pipeline started", "workers", workers)
	for range workers {
		go func() {
			for {
				if ctx.Err() != nil {
					return
				}
				step, worked, err := s.Reports.AdvanceOne(ctx, assessor)
				switch {
				case err != nil:
					s.Log.Error("advancing a claim", "error", err)
					sleep(ctx, pipelineIdle)
				case !worked:
					sleep(ctx, pipelineIdle)
				case step != report.StepNone && step != report.StepAssessWait:
					s.Log.Info("claim advanced", "step", string(step))
				}
			}
		}()
	}

	go func() {
		t := time.NewTicker(sweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := s.Reports.ReleaseOnDeadline(ctx, report.EvidenceDeadline)
				if err != nil {
					s.Log.Warn("releasing claims on the evidence deadline", "error", err)
					continue
				}
				if n > 0 {
					// Worth a line each time. These are claims going to an
					// officer with less evidence than the farmer meant to
					// send, and a district seeing this every day has an
					// upload problem rather than a farmer problem.
					s.Log.Info("claims released without all their evidence", "claims", n)
				}
			}
		}
	}()
}
