package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Quadrat counts, recorded in the field.
//
// # Why an officer would bother
//
// The figure they record today is a judgement written as a number, and at an
// appeal there is no way to tell a careful one from a hurried one. Counting
// plants in a square metre against what the spacing expects takes a minute
// and produces something the next officer can check. The response shows the
// arithmetic back to them immediately, so the count is worth taking rather
// than merely required.
//
// # Why it is not required
//
// An officer in a flooded field at dusk cannot count anything, and refusing
// their finding without measurements would produce invented ones. What the
// record keeps is which findings rest on counts.

type measurementRequest struct {
	LineNo int `json:"line_no,omitempty"`

	QuadratSideM float64 `json:"quadrat_side_m"`

	PlantsExpected *int `json:"plants_expected,omitempty"`
	PlantsPresent  *int `json:"plants_present,omitempty"`
	PlantsLodged   *int `json:"plants_lodged,omitempty"`

	OrganSampled *int `json:"organ_sampled,omitempty"`
	OrganDamaged *int `json:"organ_damaged,omitempty"`

	SoilType string `json:"soil_type,omitempty"`
	Notes    string `json:"notes,omitempty"`

	// CaptureID is the quadrat photograph, which must have a scale in frame.
	// Optional, and its absence is recorded: a count with a photograph is
	// checkable and one without is testimony.
	CaptureID string `json:"capture_id,omitempty"`
}

type measurementResponse struct {
	ID       string             `json:"id"`
	Derived  fieldwork.Derived  `json:"derived"`
	Line     *fieldwork.Summary `json:"line_summary,omitempty"`
	Recorded int                `json:"quadrats_on_this_line"`
}

// recordMeasurement stores one quadrat.
func (s *Server) recordMeasurement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	if s.Fieldwork == nil {
		writeError(w, http.StatusNotImplemented, "NO_MEASUREMENT_STORE",
			"field measurements are not enabled on this deployment")
		return
	}

	var req measurementRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if !fieldwork.ValidSoilType(req.SoilType) {
		writeError(w, http.StatusBadRequest, "UNKNOWN_SOIL_TYPE",
			"soil type must be one of the listed classes; free text here becomes "+
				"a dozen spellings of the same soil and nothing can be aggregated")
		return
	}

	// Scoped read first: an officer may only measure a report they can see.
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	// And only one they were allocated. A measurement from somebody who was
	// never sent is a count nobody can place.
	alloc, err := s.Reports.AllocationFor(ctx, rep.ID, p.UserID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	if !alloc.Assigned {
		writeError(w, http.StatusForbidden, "NOT_ALLOCATED",
			"you are not the allocated authority for this report, so a "+
				"measurement from you could not be placed in its record")
		return
	}

	m := &fieldwork.Measurement{
		ReportID:       rep.ID,
		LineNo:         req.LineNo,
		RecordedBy:     p.UserID,
		QuadratSideM:   req.QuadratSideM,
		PlantsExpected: req.PlantsExpected,
		PlantsPresent:  req.PlantsPresent,
		PlantsLodged:   req.PlantsLodged,
		OrganSampled:   req.OrganSampled,
		OrganDamaged:   req.OrganDamaged,
		SoilType:       req.SoilType,
		Notes:          req.Notes,
		CaptureID:      req.CaptureID,
	}

	id, err := s.Fieldwork.Record(ctx, m)
	switch {
	case errors.Is(err, fieldwork.ErrNothingMeasured):
		writeError(w, http.StatusBadRequest, "NOTHING_MEASURED",
			"record a plant count, an organ sample, or both; a measurement that "+
				"measured nothing is a row nobody can use")
		return
	case errors.Is(err, fieldwork.ErrBadQuadrat):
		writeError(w, http.StatusBadRequest, "BAD_QUADRAT", err.Error())
		return
	case err != nil:
		s.Log.Error("recording a field measurement", "error", err)
		writeError(w, http.StatusInternalServerError, "MEASUREMENT_FAILED",
			"the measurement could not be recorded")
		return
	}
	m.ID = id

	// The officer's own finding is now one that rests on counts. Best effort:
	// they may be measuring before they record a finding, in which case this
	// affects nothing and the finding will be marked when it arrives.
	if err := s.Fieldwork.MarkVerificationMeasured(ctx, nil, rep.ID, p.UserID); err != nil {
		s.Log.Warn("marking a verification as measured", "error", err)
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionMeasurementRecorded, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "field_measurement", ResourceID: id,
		JurisdictionCode: rep.Place.VillageCode,
		Detail: map[string]any{
			"report":    rep.Reference,
			"line_no":   req.LineNo,
			"quadrat_m": req.QuadratSideM,
			"has_photo": req.CaptureID != "",
		},
	}.FromContext(ctx))

	out := measurementResponse{ID: id, Derived: fieldwork.Derive(m)}

	// The whole line back, so the officer sees their quadrats accumulating
	// rather than three disconnected forms.
	if ms, err := s.Fieldwork.ForLine(ctx, rep.ID, req.LineNo); err == nil {
		sum := fieldwork.Summarise(ms, areaOfLine(rep, req.LineNo))
		out.Line, out.Recorded = &sum, len(ms)
	}

	writeJSON(w, http.StatusCreated, out)
}

// listMeasurements returns every quadrat on a report, with the arithmetic.
func (s *Server) listMeasurements(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	// Officers only, for the same reason as the findings: these are the
	// counts the loss figure is built from, recorded while the claim is under
	// examination.
	if p.IsFarmer() {
		writeError(w, http.StatusForbidden, "FORBIDDEN",
			"field measurements are available to the examining officers")
		return
	}

	if s.Fieldwork == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"items": []any{}, "enabled": false,
		})
		return
	}

	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	ms, err := s.Fieldwork.ForReport(ctx, rep.ID)
	if err != nil {
		s.Log.Error("reading field measurements", "error", err)
		writeError(w, http.StatusInternalServerError, "MEASUREMENTS_FAILED",
			"the measurements could not be read")
		return
	}

	type row struct {
		*fieldwork.Measurement
		Derived fieldwork.Derived `json:"derived"`
	}
	items := make([]row, 0, len(ms))
	for _, m := range ms {
		items = append(items, row{Measurement: m, Derived: fieldwork.Derive(m)})
	}

	// Per line, because that is the unit an officer records a figure against.
	lines := map[int]fieldwork.Summary{}
	for _, item := range rep.Items {
		byLine, err := s.Fieldwork.ForLine(ctx, rep.ID, item.LineNo)
		if err != nil {
			continue
		}
		lines[item.LineNo] = fieldwork.Summarise(byLine, item.AreaSownHa)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"by_line":    lines,
		"soil_types": fieldwork.SoilTypes,
		"enabled":    true,
	})
}

// expectedStand answers what a quadrat should hold at a given spacing.
//
// A convenience the form offers and the officer confirms. What is stored is
// what they confirmed, because spacing varies by variety and by what the
// farmer actually did, and a figure the system asserted would be a
// denominator nobody chose.
func (s *Server) expectedStand(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	row := floatParam(q.Get("row_cm"))
	plant := floatParam(q.Get("plant_cm"))
	side := floatParam(q.Get("quadrat_m"))
	if side == 0 {
		side = 1
	}

	n, err := fieldwork.ExpectedStand(row, plant, side)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_SPACING", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plants_expected": n,
		"row_cm":          row,
		"plant_cm":        plant,
		"quadrat_m":       side,
		"note": "a suggestion from the recommended spacing. Confirm it against " +
			"what was actually sown; the figure you confirm is the one stored.",
	})
}

// areaOfLine is the sown area of one crop line, for the sampling fraction.
//
// Zero where the line is not on the report, which makes the fraction silent
// rather than wrong: an officer told they sampled 0.015% of a field whose
// size the system guessed is worse off than one told nothing.
func areaOfLine(rep *report.Report, lineNo int) float64 {
	if rep == nil {
		return 0
	}
	for _, it := range rep.Items {
		if it.LineNo == lineNo {
			return it.AreaSownHa
		}
	}
	// A measurement recorded against the report rather than a line covers the
	// whole of it.
	if lineNo == 0 {
		return rep.TotalSownHa()
	}
	return 0
}

func floatParam(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}
