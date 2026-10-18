package api

import (
	"context"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/assessjob"
)

// Whose authority an automatic assessment runs under.
//
// Found on the first run against a real handset: the worker looked up the
// officer who requested every job, and the claim pipeline's jobs have none. It
// failed, retried until the attempts ran out, and gave up -- and the pipeline,
// built so a failing assessment does not hold a claim, sent every claim on
// without one. No automatic assessment ever ran, and nothing said so.

func TestASystemJobWithNowhereToBeScopedIsRefused(t *testing.T) {
	// A job nobody requested and nothing can place would otherwise run with
	// no scope at all -- which for an officer principal means seeing nothing,
	// or, were Scope ever to treat empty as unrestricted, everything.
	s := &Server{}
	_, err := s.assessmentPrincipal(context.Background(), &assessjob.Job{
		ID: "j1", RequestedBy: "", VillageLGDCode: "",
	})
	if err == nil {
		t.Error("a job with no requester and no village got a principal")
	}
}
