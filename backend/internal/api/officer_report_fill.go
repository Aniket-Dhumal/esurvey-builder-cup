package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// fillReportFormat takes an officer's own report format and returns it with real
// figures in it.
//
// # What travels where
//
// The uploaded workbook's column headings go to the model, and nothing else does.
// The model answers with a mapping from column to field; this service reads the
// claims -- scoped to the asking officer, by the same store call the listing uses
// -- and writes the values itself.
//
// So no claim, no farmer and no figure reaches the model in order to fill a report.
// A blank format carries nobody's data, which is what makes sending it free.
//
// The arithmetic staying here is the other half. A model asked to total a column
// will occasionally total it wrongly, and a wrong total on a form an officer signs
// is the worst thing this feature could produce.
func (s *Server) fillReportFormat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in first")
		return
	}
	if s.Agent == nil {
		writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE",
			"the assistant is not configured")
		return
	}

	// The upload. Bounded well below what a populated report would be: a format is
	// a handful of heading rows, and anything larger is somebody's filled report
	// sent by mistake, which there is no reason to hold in memory.
	if err := r.ParseMultipartForm(officeragent.MaxTemplateBytes); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_UPLOAD",
			"the file could not be read")
		return
	}
	file, header, err := r.FormFile("format")
	if err != nil {
		writeError(w, http.StatusBadRequest, "NO_FILE",
			"attach the report format as 'format'")
		return
	}
	defer file.Close()

	name := header.Filename
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		// PDF and the older .xls are refused here rather than half-supported.
		//
		// A PDF is a layout, not a form: editing one generically means embedding
		// fonts and fighting a layout engine, which is the same reasoning that
		// keeps this service rendering print-styled HTML and letting the browser
		// make the PDF. An office that has only a PDF format can be answered, but
		// the answer is a workbook and it should say so rather than return
		// something that looks like their document and is not.
		writeError(w, http.StatusUnsupportedMediaType, "NEEDS_XLSX",
			"send the format as .xlsx; a PDF format cannot be written back into "+
				"and would come back as a different document")
		return
	}

	// Read once into memory: the headings are needed before the fill, and the
	// fill needs the original bytes again to keep the office's own layout.
	raw, err := io.ReadAll(io.LimitReader(file, officeragent.MaxTemplateBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_UPLOAD",
			"the file could not be read")
		return
	}

	tmpl, err := officeragent.ReadTemplate(bytes.NewReader(raw))
	if err != nil {
		if errors.Is(err, officeragent.ErrNoHeadings) {
			writeError(w, http.StatusUnprocessableEntity, "NO_HEADINGS",
				"no column headings were found in that workbook")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "BAD_WORKBOOK",
			"that workbook could not be read")
		return
	}

	mapping, err := s.Agent.MapHeadings(ctx, tmpl.Headings)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE",
			"the assistant could not work out what the columns want")
		return
	}

	// Nothing recognised. Refused rather than returned unchanged, which would look
	// like the feature had worked.
	filled := 0
	for _, f := range mapping {
		if f != "" {
			filled++
		}
	}
	if filled == 0 {
		writeError(w, http.StatusUnprocessableEntity, "NO_COLUMNS_MATCHED",
			"none of those column headings matched anything this can fill")
		return
	}

	limit, _ := strconv.Atoi(r.FormValue("limit"))
	rows, err := officeragent.RowsFor(ctx, s.Reports, p.Scope(), report.Filter{
		EventID:       r.FormValue("event_id"),
		State:         r.FormValue("state"),
		AwaitingLevel: r.FormValue("awaiting_level"),
	}, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "READ_FAILED",
			"the claims could not be read")
		return
	}

	out, err := officeragent.Fill(bytes.NewReader(raw), tmpl, mapping, rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILL_FAILED",
			"the workbook could not be written")
		return
	}
	defer out.Close()

	s.Audit.Record(ctx, audit.Event{
		Action: "officer.agent.fill_format", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "officer_agent",
		Detail: map[string]any{
			"rows": len(rows), "columns_filled": filled,
			"columns_seen": len(tmpl.Headings),
		},
	}.FromContext(ctx))

	w.Header().Set("Content-Type",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	// Their own filename, marked. An officer with the original and the filled copy
	// in one folder must be able to tell them apart.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename=%q`, filledName(name)))
	// What was and was not understood, for a caller that wants to show it.
	w.Header().Set("X-Esurvey-Columns-Filled", strconv.Itoa(filled))
	w.Header().Set("X-Esurvey-Rows", strconv.Itoa(len(rows)))

	if err := out.Write(w); err != nil {
		// The headers have gone; there is no status left to change. Recorded and
		// dropped, which is what a truncated download looks like to the officer.
		s.Log.Warn("writing a filled format failed", "error", err)
	}
}

func filledName(original string) string {
	base := strings.TrimSuffix(original, ".xlsx")
	base = strings.TrimSuffix(base, ".XLSX")
	return base + " (filled).xlsx"
}
