package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/assessjob"
	"github.com/agribid/esurvey-platform/backend/internal/platform/gate"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// Asking for an assessment, and being told when it is ready.
//
// # Why the request no longer produces the answer
//
// Producing one assessment is minutes of Earth Engine and model work, and the
// engine admits a handful of jobs at once. Run inside the request it made an
// officer's browser hold a connection for minutes, lost everything in flight
// whenever the API restarted, and reported "the engine is busy" as though the
// claim had been refused. At a lakh of assessments it does not finish at all
// (docs/PLATFORM_SCALE_REVIEW.md, A1).
//
// So the request checks what it can check immediately -- the farmer exists,
// the parcel is theirs, the officer is posted where the land is -- records the
// intent, and answers 202 with somewhere to look. Workers produce the
// assessment at the rate the engine allows.

// assessFailure is why an assessment could not be produced.
//
// Retryable separates the two kinds, and the separation is the whole point: a
// busy engine is a bad minute and the job waits; a parcel with no boundary
// will be refused again however many times it is tried, so the officer is told
// now rather than in half an hour.
type assessFailure struct {
	Code      string
	Message   string
	Retryable bool
	// Busy means the work never started: every slot on an upstream stayed
	// taken for as long as the job could wait. Requeued without spending an
	// attempt.
	Busy bool
}

// busyFailure reports whether err is a wait for an upstream slot that ran out.
func busyFailure(err error) *assessFailure {
	if !errors.Is(err, gate.ErrBusy) {
		return nil
	}
	return &assessFailure{Code: "UPSTREAM_BUSY", Retryable: true, Busy: true,
		Message: "every analysis slot stayed busy; the assessment is waiting its turn"}
}

func (f *assessFailure) Error() string { return f.Code + ": " + f.Message }

// refuse is a failure that trying again cannot fix.
func refuse(code, message string) *assessFailure {
	return &assessFailure{Code: code, Message: message}
}

// upstreamFailure classifies a satellite error.
func upstreamFailure(module string, err error) *assessFailure {
	if f := busyFailure(err); f != nil {
		return f
	}
	if errors.Is(err, satellite.ErrUpstreamDown) {
		return &assessFailure{Code: "SATELLITE_DOWN", Retryable: true,
			Message: "the satellite analysis service is not responding"}
	}
	var se *satellite.Error
	if errors.As(err, &se) {
		// A refused geometry is refused every time; everything else -- a
		// quota, an engine outage, a timeout -- is a moment.
		permanent := se.HTTPStatus == http.StatusBadRequest ||
			se.HTTPStatus == http.StatusRequestEntityTooLarge
		return &assessFailure{
			Code:      "SATELLITE_" + se.Code,
			Message:   fmt.Sprintf("%s module: %s", module, se.Message),
			Retryable: !permanent,
		}
	}
	return &assessFailure{Code: "SATELLITE_UNAVAILABLE", Retryable: true,
		Message: fmt.Sprintf("the %s service is not responding", module)}
}

// agentFailure classifies a model-panel error.
func agentFailure(err error) *assessFailure {
	if f := busyFailure(err); f != nil {
		return f
	}
	var ae *ai.Error
	if errors.As(err, &ae) {
		return &assessFailure{Code: "AI_UNAVAILABLE", Message: ae.Detail,
			Retryable: true}
	}
	return &assessFailure{Code: "AI_UNREACHABLE", Retryable: true,
		Message: "the assessment panel is not responding"}
}

// requestAssessment validates what can be validated now and queues the rest.
func (s *Server) requestAssessment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "ASSESSMENTS_UNAVAILABLE",
			"assessments are not configured on this deployment")
		return
	}

	var req createAssessmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if req.FarmerID == "" || req.FarmID == "" || req.IncidentDate == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"farmer_id, farm_id and incident_date are required")
		return
	}

	// Checked here, not in the worker: an officer typing a farmer id that does
	// not exist should be told while they are still looking at the form.
	rec, err := s.Registry.FarmerRecord(ctx, req.FarmerID)
	if err != nil {
		s.writeFarmerRecordError(w, err)
		return
	}
	parcel, ok := rec.ParcelByFarmID(req.FarmID)
	if !ok {
		writeError(w, http.StatusNotFound, "PARCEL_NOT_FOUND",
			fmt.Sprintf("farmer %s has no parcel %s", req.FarmerID, req.FarmID))
		return
	}

	// Permission was checked at the route; this is the other half. Assessing a
	// parcel in another taluka is the fraud this system exists to make hard,
	// so an officer must be posted where the land is -- and that is decided
	// now, against the officer who asked, not later against a worker.
	principal := rbac.FromContext(ctx)
	if err := s.assertInScope(r, principal, parcel.VillageLGDCode, "parcel", req.FarmID); err != nil {
		s.writeScopeError(w, err)
		return
	}

	job, err := s.Jobs.Enqueue(ctx, assessjob.Request{
		FarmerID:         req.FarmerID,
		FarmID:           req.FarmID,
		IncidentDate:     req.IncidentDate,
		SowingDate:       req.SowingDate,
		CropLossReportID: req.CropLossReportID,
		// The calamity the officer chose. Every other hop carried it and
		// this one did not, so the choice reached the API and stopped at the
		// queue: two screenings run today were stored against no event and
		// their weather specialist was left saying the calamity was not
		// stated.
		SurveyEventID:  req.SurveyEventID,
		RequestedBy:    principal.UserID,
		VillageLGDCode: parcel.VillageLGDCode,
	})
	if err != nil {
		if badRequestFromDB(w, err) {
			return
		}
		s.Log.Error("queueing an assessment", "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "")
		return
	}

	w.Header().Set("Location", "/api/v1/assessment-jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

// assessmentJob answers "is it ready yet".
func (s *Server) assessmentJob(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "ASSESSMENTS_UNAVAILABLE", "")
		return
	}
	job, err := s.Jobs.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, assessjob.ErrNotFound) {
		writeError(w, http.StatusNotFound, "JOB_NOT_FOUND",
			"no assessment request with that id")
		return
	}
	if err != nil {
		if badRequestFromDB(w, err) {
			return
		}
		s.Log.Error("reading an assessment job", "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "")
		return
	}

	// Scoped like the assessment it will become: an officer may see the
	// progress of work in their own jurisdiction, or work they asked for.
	p := rbac.FromContext(r.Context())
	if job.RequestedBy == "" || job.RequestedBy != p.UserID {
		// A job with no village is one this server cannot place, and an
		// unplaceable job used to skip the scope check altogether -- so
		// anybody holding assessment:read could read it, and farmers hold
		// assessment:read. Failing closed instead: a job that cannot be
		// placed is not in anybody's jurisdiction.
		if job.VillageLGDCode == "" {
			writeError(w, http.StatusNotFound, "JOB_NOT_FOUND",
				"no assessment request with that id")
			return
		}
		if err := s.assertInScope(r, p, job.VillageLGDCode, "assessment", job.ID); err != nil {
			s.writeScopeError(w, err)
			return
		}
	}

	// Still waiting: say roughly how long, so a console can pace its polling
	// and an officer knows the queue is moving rather than stuck.
	if job.Waiting() {
		w.Header().Set("Retry-After", "3")
	}
	writeJSON(w, http.StatusOK, job)
}

// assessmentBacklog is the queue's depth, for operators.
func (s *Server) assessmentBacklog(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "ASSESSMENTS_UNAVAILABLE", "")
		return
	}
	// The area's queue, not the platform's: the figure used to count every
	// state's claims into every officer's console.
	b, err := s.Jobs.WaitingIn(r.Context(),
		rbac.FromContext(r.Context()).Scope().JurisdictionIDs)
	if err != nil {
		s.Log.Error("reading the assessment backlog", "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// --- the workers ------------------------------------------------------------

// AssessmentWorkers is how many assessments are produced at once by default.
//
// Twenty-four, and not the limit on anything upstream. It was four, to match
// the satellite service's Earth Engine concurrency -- which tied the model
// quota to the satellite one and multiplied both by the instance count. Each
// upstream now has its own gate counted across every instance (see
// internal/platform/gate), so a worker waiting for a slot costs a goroutine
// and the workers only need to be enough to keep every gate full.
const AssessmentWorkers = 24

// assessmentCeiling bounds one job, waiting for slots included. A job still
// waiting at a gate when it runs out is deferred without spending an attempt.
const assessmentCeiling = 20 * time.Minute

// idleWait is how long a worker sleeps when the queue is empty.
const idleWait = 5 * time.Second

// RunAssessments drains the assessment queue until the context is cancelled.
//
// Each worker claims one job at a time: an assessment takes minutes, and a
// worker holding a batch would leave the rest of it unworked if the process
// died.
func (s *Server) RunAssessments(ctx context.Context, workers int) {
	if s.Jobs == nil {
		s.Log.Warn("assessment workers idle: no job store")
		return
	}
	if workers <= 0 {
		workers = AssessmentWorkers
	}
	host, _ := os.Hostname()

	s.Log.Info("assessment workers started", "workers", workers)
	for i := 0; i < workers; i++ {
		go func(n int) {
			name := fmt.Sprintf("%s-%d", host, n)
			for {
				if ctx.Err() != nil {
					return
				}
				worked, err := s.workOneAssessment(ctx, name)
				switch {
				case err != nil:
					s.Log.Error("claiming assessment work", "error", err)
					sleep(ctx, idleWait)
				case !worked:
					sleep(ctx, idleWait)
				}
			}
		}(i)
	}

	// Work whose worker died is taken back. Without this an officer waits for
	// an assessment nobody is producing.
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := s.Jobs.ReleaseStale(ctx, assessjob.Lease); err != nil {
					s.Log.Warn("releasing stale assessment jobs", "error", err)
				} else if n > 0 {
					s.Log.Info("released assessment jobs from a dead worker", "jobs", n)
				}
			}
		}
	}()
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// workOneAssessment claims a job and produces it. Reports whether there was
// anything to do.
func (s *Server) workOneAssessment(ctx context.Context, worker string) (bool, error) {
	jobs, err := s.Jobs.Claim(ctx, worker, 1)
	if err != nil || len(jobs) == 0 {
		return false, err
	}
	job := jobs[0]

	// Whose authority the assessment runs under.
	principal, err := s.assessmentPrincipal(ctx, job)
	if err != nil {
		s.Log.Error("building the principal for an assessment",
			"job", job.ID, "error", err)
		// An account being edited, or a village not yet in the tree: neither
		// is a verdict on the work.
		_, _ = s.Jobs.Retry(ctx, job.ID, "the assessment's authority could not be read")
		return true, nil
	}

	// The lease, renewed while the job runs. A worker that loses it -- a stall
	// long enough for the job to be released and taken by another -- stops,
	// rather than producing a second assessment of the same claim.
	jobCtx, cancel := context.WithTimeout(ctx, assessmentCeiling)
	defer cancel()
	var lost atomic.Bool
	go func() {
		t := time.NewTicker(assessjob.HeartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-t.C:
				ok, err := s.Jobs.Extend(jobCtx, job.ID, worker)
				if err == nil && !ok {
					s.Log.Warn("assessment lease lost; abandoning the job",
						"job", job.ID, "worker", worker)
					lost.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	began := time.Now()
	id, failure := s.produceAssessment(jobCtx, createAssessmentRequest{
		FarmerID:         job.FarmerID,
		FarmID:           job.FarmID,
		IncidentDate:     job.IncidentDate,
		SowingDate:       job.SowingDate,
		CropLossReportID: job.CropLossReportID,
		SurveyEventID:    job.SurveyEventID,
	}, principal)

	cancel()
	switch {
	case lost.Load():
		// Another worker has it. Anything written now would overwrite theirs.
	case failure == nil:
		if err := s.Jobs.Done(ctx, job.ID, id); err != nil {
			s.Log.Error("recording a finished assessment", "job", job.ID, "error", err)
		}
		s.Log.Info("assessment job done", "job", job.ID, "assessment", id,
			"took", time.Since(began).Round(time.Second).String(),
			"attempt", job.Attempts)
	case failure.Busy:
		if err := s.Jobs.Defer(ctx, job.ID, failure.Message, time.Minute); err != nil {
			s.Log.Error("deferring an assessment", "job", job.ID, "error", err)
		}
		s.Log.Info("assessment job waiting for an upstream slot", "job", job.ID)
	case failure.Retryable:
		again, err := s.Jobs.Retry(ctx, job.ID, failure.Message)
		if err != nil {
			s.Log.Error("requeueing an assessment", "job", job.ID, "error", err)
		}
		s.Log.Warn("assessment job deferred", "job", job.ID,
			"code", failure.Code, "attempt", job.Attempts, "will_retry", again)
	default:
		if err := s.Jobs.Fail(ctx, job.ID, failure.Message); err != nil {
			s.Log.Error("recording a refused assessment", "job", job.ID, "error", err)
		}
		s.Log.Info("assessment job refused", "job", job.ID, "code", failure.Code)
	}
	return true, nil
}

// assessmentPrincipal is whose authority an assessment job runs under.
//
// # An officer's job
//
// The officer who asked, rebuilt: their scope decides which photographs the
// panel may be shown, and the assessment records them as its author.
//
// # A job nobody asked for
//
// The claim pipeline queues an assessment on complete receipt, and there is no
// officer behind it. The worker used to look the requester up regardless --
// ByID("") -- fail, retry "the requesting officer could not be read" until the
// attempts ran out, and give up. The pipeline, correctly built so that a
// failing assessment does not hold a claim, then sent it on without one. So
// every automatic assessment failed, every claim reached its officers with
// none, and nothing said so: found on the first run against a real handset.
//
// The principal it gets instead is the narrowest that can do the work: no
// user, no role, no permission, and a scope of exactly one village -- the
// claim's own. That is enough to read the farmer's photographs of their own
// field, which is all the panel needs, and nothing else anywhere. The
// assessment and its audit record carry no author, which is the truth.
func (s *Server) assessmentPrincipal(ctx context.Context, job *assessjob.Job) (*rbac.Principal, error) {
	if job.RequestedBy != "" {
		user, err := s.Identity.ByID(ctx, job.RequestedBy)
		if err != nil {
			return nil, err
		}
		return user.Principal(""), nil
	}

	if job.VillageLGDCode == "" {
		return nil, errors.New("a job nobody requested has no village to be scoped to")
	}
	village, err := s.Identity.JurisdictionByCode(ctx, rbac.LevelVillage, job.VillageLGDCode)
	if err != nil {
		return nil, fmt.Errorf("finding village %s: %w", job.VillageLGDCode, err)
	}
	return &rbac.Principal{
		Kind: rbac.KindOfficer,
		Name: "eSurvey (automatic)",
		Grants: []rbac.Grant{{
			JurisdictionID:    village.ID,
			JurisdictionCode:  village.Code,
			JurisdictionLevel: rbac.LevelVillage,
		}},
	}, nil
}
