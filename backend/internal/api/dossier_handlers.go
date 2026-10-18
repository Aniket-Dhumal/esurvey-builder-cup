package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/dossier"
	"github.com/agribid/esurvey-platform/backend/internal/evidence"
	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/store"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// The assessment as a document.
//
// # Why this did not exist
//
// internal/dossier builds a complete bilingual assessment -- instrument rows,
// what each source said, what it could not say, the caveats, the provenance
// -- and nothing called it. Not one caller in the application, and none in
// its own tests outside the package. A full rendering engine, unwired.
//
// The practical cost showed up the first time somebody needed to send an
// assessment to a colleague: the only way to produce one was to point a
// headless browser at the console and print the page. That works, and it
// means the layout is the web app's, the page breaks fall wherever they fall,
// and there is no server-side artefact to archive or attach to a file.
//
// # Why it renders from stored state and never re-gathers
//
// The evidence packet is persisted with the assessment -- the `baseline`
// column, kept "because an assessment has to be reproducible at an appeal".
// Re-gathering would produce a different document each time it was opened:
// NDVI moves, radar gets another pass, the weather archive answers or does
// not. A document that changes when nobody changed anything is not a record
// of a decision.
//
// So this reads what was stored and renders it. An assessment printed today
// and the same assessment printed at an appeal next year are the same page.

func (s *Server) assessmentDossier(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	// Scoped in the read, like every other assessment fetch: out of scope and
	// non-existent answer identically, so a run of ids cannot be turned into a
	// map of which villages have been assessed.
	a, err := s.Store.Get(ctx, r.PathValue("id"), p.Scope())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "ASSESSMENT_NOT_FOUND",
			"no assessment with that id")
		return
	}
	if err != nil {
		s.Log.Error("reading an assessment for its dossier", "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "")
		return
	}

	in := s.dossierInputs(ctx, a, a.CropLossReportID, p.Scope())

	d := dossier.Build(in)

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "assessment.dossier_printed", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "assessment", ResourceID: a.ID,
		JurisdictionCode: a.VillageLGDCode,
	}.FromContext(ctx))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`inline; filename="assessment-`+a.ID+`.html"`)
	if err := d.Print(w); err != nil {
		// The header is already written, so there is no status left to send.
		s.Log.Error("rendering the dossier", "assessment", a.ID, "error", err)
	}
}

// dossierInputs gathers everything a claim's assessment document is built from.
//
// Shared by the officer's print of an assessment and the farmer's copy of a
// decided claim, so the two are the same document: a farmer holding their
// copy at an appeal is holding what the office printed.
func (s *Server) dossierInputs(
	ctx context.Context, a *store.Assessment, reportID string, scope rbac.Scope,
) dossier.Inputs {
	in := dossier.Inputs{
		State:       dossier.Provisional,
		Reference:   reportID,
		GeneratedAt: time.Now(),
		Standard:    ptr(dossier.DefaultStandard()),
	}

	// The packet, exactly as it was when the figure was produced.
	var pkt evidence.Packet
	if a != nil {
		in.Reference = a.ID
	}
	if a != nil && len(a.Baseline) > 0 {
		if err := json.Unmarshal(a.Baseline, &pkt); err != nil {
			// A packet that will not parse is a stored record this build no
			// longer understands. The document is still worth producing from
			// what remains, and saying so beats refusing to print at all.
			s.Log.Warn("stored evidence packet did not parse",
				"assessment", a.ID, "error", err)
		} else {
			in.Denominators = pkt.Denominators
			in.Thermal = pkt.Thermal
			in.Susceptibility = pkt.Susceptibility
			in.Sowing = pkt.SowingCheck
			in.SARReading = pkt.Radar
			in.GroundCV = pkt.GroundCV
			if pkt.Radar != nil && len(pkt.Radar.Findings) > 0 {
				in.SARFinding = &pkt.Radar.Findings[0]
			}
			// The weather window that matters depends on what is claimed. A
			// hailstorm is a fortnight's question; a drought is a season's,
			// and printing the fortnight before a drought claim would show a
			// dry two weeks where the point is a dry four months.
			if pkt.Weather != nil {
				in.Weather = &pkt.Weather.Incident
			}
			// The state's own gauges, which outrank the reanalysis on the
			// amount and are the only source carrying a normal.
			in.OfficialRainfall = pkt.OfficialRainfall
			if pkt.History != nil {
				in.Signals = pkt.History.Signals
			}
			// What could not be gathered is part of the record, not a
			// footnote. A reader has to be able to tell an assessment that
			// weighed four sources from one that weighed one.
			in.Moisture = pkt.Moisture
			in.MoistureUnavailable = absentReason(pkt.Absent, "moisture")
			in.SARUnavailable = absentReason(pkt.Absent, "radar")
			in.GroundCVUnavailable = absentReason(pkt.Absent, "ground vision")
			// The optical reading at the spots that were filmed. Read from the
			// stored packet like everything else here, so an appeal next year
			// prints the same comparison rather than a fresh one.
			in.PointNDVI = pkt.PointNDVI
		}
	}

	// Where the farmer was directed, and what came back from each point.
	//
	// The section that answers a selective-evidence challenge: without it the
	// photographs carry an authority nobody checked. It is read from the
	// points frozen against the report at the time of the walk, so an
	// assessment printed at an appeal next year shows the same four places
	// (docs/EVIDENCE_REVALIDATION.md, V2).
	var coverage []survey.Coverage
	if reportID != "" && s.Captures != nil {
		var err error
		coverage, err = s.Captures.CoverageFor(ctx, reportID)
		if err != nil {
			// A dossier without the section is weaker than one with it, and
			// far better than no dossier: the officer is reading it because
			// they have a decision to make.
			s.Log.Warn("reading sampling coverage for a dossier",
				"report", reportID, "error", err)
		}
		for _, c := range coverage {
			in.Sampling = append(in.Sampling, dossier.StationCoverage{
				StationNo: c.Station.StationNo,
				Role:      c.Station.Role,
				Placement: c.Station.Placement,
				Lat:       c.Station.Lat,
				Lon:       c.Station.Lon,
				Captured:  c.Captured,
				OffsetM:   c.OffsetM,
				CapturedAt: func() string {
					if c.CapturedAt.IsZero() {
						return ""
					}
					return c.CapturedAt.UTC().Format("2006-01-02 15:04")
				}(),
				CaptureID: c.CaptureID,
			})
		}

	}

	// The panel's own account, printed in full. §11 is deliberately part of
	// the farmer's copy: withholding the machine's reasoning invites the
	// belief that a machine decided in secret.
	//
	// A claim decided before assessments ran automatically has none, and its
	// document says so in §11 rather than being refused.
	if a != nil {
		in.AI = &dossier.AIAssessment{
			LossPercent:   a.Assessment.DamagePercent,
			Reasoning:     a.Assessment.Reasoning,
			Citations:     a.Assessment.EvidenceCited,
			Model:         a.Assessment.Model,
			PromptVersion: a.Assessment.PromptVersion,
			RanAt:         a.CreatedAt.In(ist).Format("2 January 2006"),
		}

		in.Satellite = &a.Satellite
		in.Terrain = &a.Terrain
		in.Triage = a.Triage
		in.AssessedLossPercent = a.Assessment.DamagePercent
	}

	// The claim this was about, and the officers' own findings on it.
	if reportID != "" {
		rep, err := s.Reports.Get(ctx, reportID, scope)
		if err == nil {
			in.Report = rep
			if len(rep.Items) > 0 {
				in.Item = &rep.Items[0]

				// The coverage above, drawn. A table of four coordinates
				// answers "was this representative?" only for a reader
				// willing to hold four coordinates in their head; a shape
				// with four dots on it answers it at a glance, which is what
				// an officer does first whatever the order on the page.
				//
				// Here rather than beside the table, because the parcel is
				// named by the claim and a station row does not carry it.
				in.Walk = s.walkFigure(ctx, in.Item.FarmID, coverage, in.GroundCV)
				in.WalkSpacingM = s.walkSpacing(ctx, in.Item.FarmID)
			}
			if a != nil {
				in.OwnerExtentHa = a.OwnerExtentHectares
			}

			// Final once the claim has been adjudicated, provisional
			// until then -- and Build enforces the consequence, which is
			// that a provisional document carries no figure at all. A
			// provisional number becomes the number everybody remembers,
			// and no later correction catches up with it.
			if rep.State == report.StateAdjudicated ||
				rep.State == report.StateClosed {
				in.State = dossier.Final
				// The decided figure, not the model's. A final document that
				// printed the computer's estimate as "the assessed loss" would
				// contradict the decision it is the record of.
				if rep.AdjudicatedPercent != nil {
					in.AssessedLossPercent = rep.AdjudicatedPercent
				}
			}

			// Drought is argued over the season, everything else over the
			// fortnight before the loss.
			if pkt.Weather != nil && isDrought(rep.CalamityType) {
				in.Weather = &pkt.Weather.Season
			}

			// The photographs. With no field visit these are not supporting
			// material -- they are the observation, and §13 marks them as
			// determining. Listed with their geofence result and their hash,
			// so a farmer disputing the assessment can see exactly which
			// images it rested on and prove one has not been swapped.
			if s.Captures != nil {
				if caps, err := s.Captures.ForReport(ctx, reportID); err == nil {
					in.Evidence = evidenceItems(caps)
				}
			}

			// The quadrats an officer counted, read live rather than from
			// the packet. Unlike NDVI these do not drift: a quadrat is
			// what somebody wrote down standing in the field, and one
			// recorded after the assessment is still fieldwork on this
			// claim.
			if s.Fieldwork != nil {
				if ms, err := s.Fieldwork.ForReport(ctx, reportID); err == nil {
					for _, m := range ms {
						if m != nil {
							in.Measurements = append(in.Measurements, *m)
						}
					}
				}
			}
			if findings, err := s.Reports.Findings(ctx, reportID); err == nil {
				in.Findings = officerFindings(findings)
				if c := report.Reconcile(findings); c.Complete {
					dc := dossier.Consensus{Route: string(c.Route)}
					if c.Divergence != nil {
						dc.DeltaPoints = *c.Divergence
					}
					// Who settled it, and when. The pair reconciling on their
					// own is a decision too, and naming the route without
					// naming the decider would read as though nobody had.
					if d := adjudication(findings, c); d != nil {
						dc.DecidedBy, dc.DecidedAt = d.by, d.at
					}
					in.Consensus = &dc
				}
			}
		}
	}

	return in
}

// absentReason pulls one source's line out of the packet's absent list.
//
// The list is "source: why" strings, which is the form the assessment screen
// reads. The dossier wants the reason on its own, beside the row it explains.
func absentReason(absent []string, source string) string {
	for _, line := range absent {
		if after, ok := strings.CutPrefix(line, source+": "); ok {
			return after
		}
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

// officerFindings turns the pair's recorded findings into document rows.
//
// Only the village pair. The rungs above them approve or send back; they do
// not record a figure of their own, and listing them here beside two numbers
// would suggest four opinions where there are two.
func officerFindings(findings []report.Finding) []dossier.OfficerFinding {
	out := make([]dossier.OfficerFinding, 0, len(findings))
	for _, f := range findings {
		if f.Tier != report.TierVillagePair {
			continue
		}
		out = append(out, dossier.OfficerFinding{
			Role:        string(f.Role),
			Name:        f.OfficerName,
			LossPercent: f.DamagePercent,
			RecordedAt:  f.SubmittedAt.In(ist).Format("2 January 2006"),
			// The dossier's own warning: a figure recorded after seeing the
			// other officer's is not a second opinion, and the document says
			// so rather than presenting the two as independent.
			Blind: !f.SawPeerFinding,
		})
	}
	return out
}

type decider struct{ by, at string }

// adjudication names whoever settled the pair, when anybody had to.
//
// On the consensus route nobody arbitrated: the two figures were close enough
// that the mean stands, and the decision belongs to the pair jointly, dated by
// the later of the two findings.
func adjudication(findings []report.Finding, c report.Consensus) *decider {
	if c.Route == report.RouteConsensus {
		var last time.Time
		for _, f := range findings {
			if f.Tier == report.TierVillagePair && f.SubmittedAt.After(last) {
				last = f.SubmittedAt
			}
		}
		if last.IsZero() {
			return nil
		}
		return &decider{by: "Village pair, in agreement",
			at: last.Format("2006-01-02")}
	}
	// Arbitration and escalation are decided above the village. The finding
	// that carries it is the latest one not from the pair.
	var d *decider
	for _, f := range findings {
		if f.Tier == report.TierVillagePair {
			continue
		}
		name := f.OfficerName
		if name == "" {
			name = string(f.Role)
		}
		d = &decider{by: name, at: f.SubmittedAt.Format("2006-01-02")}
	}
	if d == nil && c.AdjudicatedBy != "" {
		// Nobody has decided yet; the route still says who must.
		return &decider{by: "Awaiting " + string(c.AdjudicatedBy)}
	}
	return d
}

// ist is the timezone every date in this document is read in. Dates on a
// disaster claim are argued about; rendering them in UTC would put a late
// evening filing on the wrong day.
var ist = time.FixedZone("IST", 5*3600+1800)

// isDrought says whether the claim is argued over a season rather than a day.
func isDrought(calamity string) bool {
	c := strings.ToLower(calamity)
	return strings.Contains(c, "drought") || strings.Contains(c, "dry spell")
}

// evidenceItems lists the captures as the document's evidence register.
func evidenceItems(caps []survey.Capture) []dossier.EvidenceItem {
	out := make([]dossier.EvidenceItem, 0, len(caps))
	for _, c := range caps {
		it := dossier.EvidenceItem{
			Kind:       string(c.Kind),
			CapturedAt: c.CapturedAt.In(ist).Format("2 January 2006, 3:04 pm"),
			SHA256:     c.SHA256,
			Geofence:   geofenceResult(c),
			// What the assistant asked for, so the row can say the clip was
			// directed rather than chosen. Empty for most clips.
			AskedFor: c.GuidanceAskedFor,
		}
		if c.AccuracyM != nil {
			it.AccuracyM = *c.AccuracyM
		}
		out = append(out, it)
	}
	return out
}

// geofenceResult is the boundary test in words.
//
// Outside the parcel is not fraud and must not read as an accusation: a farmer
// photographing their own field from the bund stands outside the polygon. The
// distance is the fact; what it means is the officer's call.
func geofenceResult(c survey.Capture) string {
	switch {
	case c.InsideParcel == nil:
		return "Not tested -- no boundary was on record"
	case *c.InsideParcel:
		return "Inside the parcel boundary"
	case c.DistanceFromParcelM != nil:
		return fmt.Sprintf("%.0f m outside the parcel boundary", *c.DistanceFromParcelM)
	default:
		return "Outside the parcel boundary"
	}
}

// The farmer's copy of a decided claim's assessment.
//
// # Why it waits for the decision
//
// The officers examine a claim blind to each other, and the farmer is shown
// the loss figure only once both village officers have recorded (see
// ProgressFor). An assessment document opened before the claim is decided
// would hand the farmer the computer's figure mid-review -- and, through the
// farmer, hand it to the second officer. So the copy opens once the claim is
// adjudicated or closed, and not before.
//
// # Why it names roles and not officers
//
// The progress a farmer already sees says "Talathi", not a person, and this
// copy says the same: the remarks and figures explain the decision, and a
// name mostly tells somebody who to go and find. The office's own print of
// the same assessment still names them.
func (s *Server) claimAssessmentReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	scope, err := s.actingScope(r, p)
	if err != nil {
		writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
		return
	}
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), scope)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	// A claim decided before assessments ran automatically has none. Its
	// document is still the record of the decision -- the findings, the
	// photographs, the decided figure -- and says in its own section that no
	// computer assessment was run.
	a, err := s.Store.LatestForClaim(ctx, rep.ID)
	if errors.Is(err, store.ErrNotFound) {
		a, err = nil, nil
	}
	if err != nil {
		s.Log.Error("reading a claim's assessment", "report", rep.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "STORE_FAILED", "")
		return
	}

	// The farmer reads it as soon as there is something to read.
	//
	// It used to be withheld until the claim was decided, on the reasoning
	// that a provisional figure becomes the figure everybody remembers. That
	// reasoning is sound and is enforced somewhere better: dossier.Build
	// refuses to print a decided figure on a provisional document, and the
	// page is stamped PROVISIONAL across the head. What the farmer gets before
	// the decision is what each instrument found about their field and what it
	// could not -- which is the part they can correct while correcting it is
	// still cheap, rather than after a decision, through an appeal.
	//
	// §11 is deliberately in the farmer's copy for the same reason: withholding
	// the machine's reasoning invites the belief that a machine decided in
	// secret.
	decided := rep.State == report.StateAdjudicated || rep.State == report.StateClosed
	if p.IsFarmer() && !decided && a == nil {
		writeError(w, http.StatusConflict, "ASSESSMENT_NOT_READY",
			"the assessment of this claim has not been made yet; the report "+
				"appears once it has")
		return
	}

	in := s.dossierInputs(ctx, a, rep.ID, scope)
	// The claim's own number, which is what the farmer was given and tracks
	// it by, rather than an assessment id they have never seen.
	in.Reference = rep.Reference
	if p.IsFarmer() {
		for i := range in.Findings {
			in.Findings[i].Name = ""
		}
		if in.Consensus != nil && in.Consensus.DecidedBy != "" &&
			!strings.HasPrefix(in.Consensus.DecidedBy, "Village pair") {
			in.Consensus.DecidedBy = "Adjudicating officer"
		}
	}

	d := dossier.Build(in)

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "report.assessment_report_viewed", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail:           map[string]any{"assessment": assessmentIDOf(a)},
	}.FromContext(ctx))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`inline; filename="`+rep.Reference+`-assessment.html"`)
	w.Header().Set("Cache-Control", "private, no-store")
	if err := d.Print(w); err != nil {
		s.Log.Error("rendering a claim's assessment report", "report", rep.ID, "error", err)
	}
}

func assessmentIDOf(a *store.Assessment) string {
	if a == nil {
		return ""
	}
	return a.ID
}

// walkFigure draws the parcel with its points, and prints one photograph from
// each.
//
// Everything here is already recorded: the boundary the assessment used, the
// position and accuracy each capture carried, and the still taken at each
// station before its clip. None of it was reaching the reader.
//
// Returns nil rather than a half-drawn figure. A map with no boundary is a
// scatter of dots with no field around them, which says less than the table
// it would sit above.
func (s *Server) walkFigure(
	ctx context.Context, farmID string, coverage []survey.Coverage,
	ground *groundcv.Score,
) *dossier.Figure {
	if farmID == "" || len(coverage) == 0 || s.Captures == nil {
		return nil
	}

	b, err := s.Captures.BoundaryFor(ctx, farmID)
	if err != nil || b == nil || len(b.GeoJSON) == 0 {
		return nil
	}
	rings := ringsOf(b.GeoJSON)
	if len(rings) == 0 {
		return nil
	}
	// One frame out of each clip.
	//
	// The walk records only video now. It used to take a photograph as well and
	// upload both, which doubled what a farmer's phone had to push over a
	// village connection for two records of the same moment -- and for the
	// whole life of the feature the photograph arrived carrying no station
	// number, so the report could never place it.
	return s.walkFigureFrom(rings, coverage, dossier.StationScores(ground),
		func(c survey.Coverage) (string, error) {
			// A claim from before this change has a stored photograph of its
			// own, already placed. Printing it is better than re-deriving one.
			if c.StillID != "" && s.Captures != nil {
				if uri, err := s.Captures.DataURI(ctx, c.StillID); err == nil {
					return uri, nil
				}
			}
			if s.Frames == nil {
				return "", errNoFrameSource
			}
			return s.Frames.Frame(ctx, c.CaptureID, c.SHA256)
		})
}

// walkFigureFrom is the drawing itself, given the outline, the coverage, and a
// way to fetch a photograph.
//
// Separate from walkFigure so what the figure claims can be tested without a
// database: every plate printed beside this map is an assertion about where a
// photograph was taken, and the pairing of photograph to point was silently
// broken for the whole of this feature's life before anyone looked.
func (s *Server) walkFigureFrom(
	rings [][]dossier.MapPoint, coverage []survey.Coverage,
	scores map[int]*groundcv.StationScore,
	still func(c survey.Coverage) (string, error),
) *dossier.Figure {
	// A scatter of dots with no field around them says less than the table it
	// would sit above. The rule lives here rather than only in the caller, so
	// it holds however the figure is reached.
	if len(rings) == 0 || len(coverage) == 0 {
		return nil
	}

	m := &dossier.FieldMap{Rings: rings}
	fig := &dossier.Figure{
		Caption: dossier.T(
			"शेताची हद्द, प्रणालीने सुचवलेली चार ठिकाणे, आणि शेतकरी "+
				"प्रत्यक्ष कुठे उभा होता. सुचवलेल्या ठिकाणी उभे राहणे "+
				"बंधनकारक नाही. भरीव वर्तुळ म्हणजे मोबाइलने नोंदवलेली "+
				"अचूकता; तुटक वर्तुळ म्हणजे त्या ठिकाणाहून साधारण किती शेत "+
				"व्हिडिओत येऊ शकते याचा अंदाज -- ते मोजमाप नाही.",
			"The parcel, the four points the system suggested, and where the "+
				"farmer actually stood. Standing on a suggested point is not "+
				"required. The solid circle is the accuracy the handset itself "+
				"recorded; the dashed circle is an estimate of how much field a "+
				"clip from that spot can show, and is an assumption rather "+
				"than a measurement.",
		),
		Legend: []dossier.LegendItem{
			{Key: "suggested", Label: dossier.T("सुचवलेले ठिकाण", "Suggested point")},
			{Key: "stood", Label: dossier.T("प्रत्यक्ष जिथे उभे होते", "Where they stood")},
			{Key: "accuracy", Label: dossier.T("मोबाइलची अचूकता", "Handset accuracy")},
			{Key: "coverage", Label: dossier.T("अंदाजे व्याप्ती", "Assumed coverage")},
		},
	}

	for _, c := range coverage {
		st := dossier.MapStation{
			StationNo: c.Station.StationNo,
			Suggested: dossier.MapPoint{Lat: c.Station.Lat, Lon: c.Station.Lon},
			AccuracyM: c.AccuracyM,
		}
		if c.Lat != nil && c.Lon != nil {
			st.Actual = &dossier.MapPoint{Lat: *c.Lat, Lon: *c.Lon}
		}
		m.Stations = append(m.Stations, st)

		if c.CaptureID == "" && c.StillID == "" {
			continue
		}
		uri, err := still(c)
		if err != nil {
			// A plate that will not load is not worth a broken image in a
			// document somebody prints; the map and the table still stand.
			if s.Log != nil {
				s.Log.Warn("reading a frame for a dossier plate",
					"capture", c.CaptureID, "error", err)
			}
			continue
		}
		fig.Plates = append(fig.Plates, dossier.Plate{
			Src:   template.URL(uri), //nolint:gosec // a data URI this service produced
			Title: plateTitle(c.Station.StationNo),
			// Looked up by the point's own number, never by position in the
			// list: a walk whose second clip failed to upload produces three
			// readings, and sliding the third one under the second
			// photograph would be a false statement about the field.
			Finding: dossier.StationFinding(scores[c.Station.StationNo]),
			Meta:    plateMeta(c),
		})
	}

	fig.SVG = m.SVG()
	if fig.SVG == "" && len(fig.Plates) == 0 {
		return nil
	}
	return fig
}

// plateTitle names the point a photograph came from.
func plateTitle(n int) dossier.Text {
	return dossier.Tf("ठिकाण %d", "Point %d", n)
}

// plateMeta is when it was taken and how well the handset knew where it was.
//
// The accuracy belongs next to the photograph and not only on the map: a
// frame recorded with a thirty-metre fix may be of the neighbouring field,
// and a reader looking at the image is exactly the person who should be told.
func plateMeta(c survey.Coverage) dossier.Text {
	when := ""
	if !c.CapturedAt.IsZero() {
		when = c.CapturedAt.UTC().Format("2006-01-02 15:04")
	}
	switch {
	case c.AccuracyM != nil && when != "":
		return dossier.Tf("%s · ±%.0f मी", "%s · ±%.0f m", when, *c.AccuracyM)
	case when != "":
		return dossier.T(when, when)
	case c.AccuracyM != nil:
		return dossier.Tf("±%.0f मी", "±%.0f m", *c.AccuracyM)
	}
	return dossier.Text{}
}

// ringsOf reads the outline out of a GeoJSON geometry.
//
// Polygon and MultiPolygon only, and the outer ring of each: an inner ring is
// a hole in a parcel, which this drawing does not distinguish and should not
// pretend to.
func ringsOf(raw json.RawMessage) [][]dossier.MapPoint {
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil
	}

	read := func(ring [][2]float64) []dossier.MapPoint {
		out := make([]dossier.MapPoint, 0, len(ring))
		for _, p := range ring {
			out = append(out, dossier.MapPoint{Lon: p[0], Lat: p[1]})
		}
		return out
	}

	switch g.Type {
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil || len(rings) == 0 {
			return nil
		}
		return [][]dossier.MapPoint{read(rings[0])}
	case "MultiPolygon":
		var polys [][][][2]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return nil
		}
		var out [][]dossier.MapPoint
		for _, poly := range polys {
			if len(poly) == 0 {
				continue
			}
			out = append(out, read(poly[0]))
		}
		return out
	}
	return nil
}

// walkSpacing is how far apart this parcel's recordings had to be.
//
// The report prints it because it is the whole of what the walk required of
// the farmer: they choose where to stand, and this is the single condition on
// that choice. Without the figure the table of distances reads as a column of
// shortfalls against points nobody was held to.
//
// Returns 0 where the parcel's area is not recorded, and the section then
// states the rule without a figure rather than printing a requirement that was
// never applied.
func (s *Server) walkSpacing(ctx context.Context, farmID string) float64 {
	if farmID == "" || s.Captures == nil {
		return 0
	}
	b, err := s.Captures.BoundaryFor(ctx, farmID)
	if err != nil || b == nil || b.PolygonAreaHa <= 0 {
		return 0
	}
	return survey.SpacingFor(b.PolygonAreaHa * 10_000)
}

// errNoFrameSource means nothing can produce a picture for a plate.
//
// Not a fault: a deployment with no vision module prints the map and the table,
// which is what it printed before there were pictures at all.
var errNoFrameSource = errors.New("no frame source is configured")
