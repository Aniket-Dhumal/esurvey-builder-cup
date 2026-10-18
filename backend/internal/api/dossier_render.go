package api

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/agribid/esurvey-platform/backend/internal/dossier"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/store"
)

// RenderClaimReport writes a claim's assessment document to w.
//
// The same builder the officer's print and the farmer's copy go through, so what
// this writes is what the office would hand over -- not a second rendering that
// can drift from it.
//
// It exists for the render-report subcommand, which prints a report for
// inspection without a browser session. The runtime image is distroless and
// carries no shell, so an operator checking what a report actually says had
// otherwise to mint a session by hand against Redis. Reviewing the wording of a
// document that decides somebody's compensation should not need that.
//
// The scope is the caller's to supply rather than this function's to assume. A
// renderer that granted itself the whole state would be a second, quieter
// authorisation path into the same documents, and the one place worth keeping
// free of those is the code that prints somebody's compensation record.
func (s *Server) RenderClaimReport(
	ctx context.Context, reference string, scope rbac.Scope, w io.Writer,
) error {
	if s.Reports == nil || s.Store == nil {
		return errors.New("the report and assessment stores are both required")
	}

	rep, err := s.Reports.ByReference(ctx, reference, scope)
	if err != nil {
		return fmt.Errorf("reading claim %s: %w", reference, err)
	}

	a, err := s.Store.LatestForClaim(ctx, rep.ID)
	if errors.Is(err, store.ErrNotFound) {
		// A claim with no assessment still has a document: the findings, the
		// photographs, the decision. It says so in its own section.
		a, err = nil, nil
	}
	if err != nil {
		return fmt.Errorf("reading the assessment for %s: %w", reference, err)
	}

	in := s.dossierInputs(ctx, a, rep.ID, scope)
	if rep.State == report.StateAdjudicated || rep.State == report.StateClosed {
		in.State = dossier.Final
	}
	return dossier.Build(in).Print(w)
}
