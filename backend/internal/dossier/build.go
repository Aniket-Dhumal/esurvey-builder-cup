package dossier

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/agronomy"
	"github.com/agribid/esurvey-platform/backend/internal/assessment"
	"github.com/agribid/esurvey-platform/backend/internal/baseline"
	"github.com/agribid/esurvey-platform/backend/internal/claims"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
	"github.com/agribid/esurvey-platform/backend/internal/moisture"
	"github.com/agribid/esurvey-platform/backend/internal/pointndvi"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/sar"
	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// Inputs is everything the report is built from.
//
// One structure rather than a long argument list, because it is also what
// gets hashed: the digest on the finished document covers exactly the values
// that produced it, so a past report can be re-derived and shown to be
// unaltered. An input passed some other way would be outside the digest and
// therefore outside the guarantee.
type Inputs struct {
	State       State     `json:"state"`
	Reference   string    `json:"reference"`
	Supersedes  string    `json:"supersedes,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`

	Report        *report.Report      `json:"report,omitempty"`
	Item          *report.Item        `json:"item,omitempty"`
	OwnerExtentHa float64             `json:"owner_extent_ha,omitempty"`
	Boundary      *BoundaryProvenance `json:"boundary,omitempty"`

	Thermal        *agronomy.Thermal        `json:"thermal,omitempty"`
	Susceptibility *agronomy.Susceptibility `json:"susceptibility,omitempty"`
	Sowing         *agronomy.SowingCheck    `json:"sowing,omitempty"`

	Measurements []fieldwork.Measurement `json:"measurements,omitempty"`

	Weather *weather.Window `json:"weather,omitempty"`
	// OfficialRainfall is the state's own gauge record for this parcel's unit,
	// which is the amount of record where it is present.
	OfficialRainfall *weather.Official     `json:"official_rainfall,omitempty"`
	Satellite        *assessment.Satellite `json:"satellite,omitempty"`

	SARFinding     *sar.Finding `json:"sar_finding,omitempty"`
	SARReading     *sar.Reading `json:"sar_reading,omitempty"`
	SARUnavailable string       `json:"sar_unavailable,omitempty"`

	// GroundCV is what the farmer's survey video measured. Absent until a
	// vision pipeline exists, and said out loud when it is: this is the
	// heaviest input in the specification's own fusion, and a report that
	// quietly omits it reads far stronger than it is.
	GroundCV            *groundcv.Score `json:"ground_cv,omitempty"`
	GroundCVUnavailable string          `json:"ground_cv_unavailable,omitempty"`

	// PointNDVI is the optical record at the spots the footage came from,
	// beside the parcel's own. An addition: Satellite above is unchanged and
	// still what the loss figure derives from.
	PointNDVI *pointndvi.Comparison `json:"point_ndvi,omitempty"`

	// Moisture is leaf water and soil moisture. omitempty on both, so every
	// dossier printed before this existed keeps the digest it was issued
	// with: the digest covers exactly the inputs that produced a document,
	// and a field that silently changed it would make an unaltered past
	// report look altered.
	Moisture            *moisture.Reading `json:"moisture,omitempty"`
	MoistureUnavailable string            `json:"moisture_unavailable,omitempty"`

	Terrain      *assessment.Terrain    `json:"terrain,omitempty"`
	Denominators *baseline.Denominators `json:"denominators,omitempty"`
	Signals      []claims.Signal        `json:"signals,omitempty"`

	AI        *AIAssessment    `json:"ai,omitempty"`
	Findings  []OfficerFinding `json:"findings,omitempty"`
	Consensus *Consensus       `json:"consensus,omitempty"`

	Evidence []EvidenceItem `json:"evidence,omitempty"`

	// Sampling is the plan the server placed and what came back from each
	// point. The farmer is not required to stand on them; see SamplingCoverage
	// for what the walk does enforce. Empty on a claim filed before planned
	// sampling existed, or on a parcel with no boundary to place points on --
	// and the section says so rather than staying silent, because silence
	// would read as the stronger claim.
	Sampling []StationCoverage `json:"sampling,omitempty"`

	// Walk is the map of the parcel and the points, and one photograph from
	// each with what it shows. Nil where there is no boundary to draw or no
	// evidence to print, and the section is then the table alone.
	Walk *Figure `json:"-"`

	// WalkSpacingM is how far apart the walk's recordings were required to be,
	// on this parcel's size. Printed because it is the whole of what the walk
	// enforced: the farmer chooses where to stand, and this is the one
	// condition on that choice. Zero where it is not known, and the section
	// then states the rule without the figure rather than inventing one.
	WalkSpacingM float64 `json:"walk_spacing_m,omitempty"`

	AssessedLossPercent *float64 `json:"assessed_loss_percent,omitempty"`
	Triage              string   `json:"triage,omitempty"`
	AppealWindow        string   `json:"appeal_window,omitempty"`

	// Standard is the issuing office and signature block. Left zero, the
	// defaults apply -- a report is never issued blank at the top.
	Standard *Standard `json:"standard,omitempty"`
}

// Build assembles the report.
//
// Section order is the printed order and is deliberate: the claim, then the
// parcel, then the crop, then the one measurement, then everything that
// corroborates it, then the people who decided, then what the document cannot
// say, then how to argue with it. A reader who stops early has still read the
// parts that matter most.
func Build(in Inputs) *Dossier {
	std := DefaultStandard()
	if in.Standard != nil {
		std = *in.Standard
	}

	d := &Dossier{
		State:       in.State,
		Std:         std,
		Reference:   in.Reference,
		Supersedes:  in.Supersedes,
		GeneratedAt: in.GeneratedAt,
		Denominator: DenominatorOf(in.Denominators),
		Triage:      T(in.Triage, in.Triage),
	}

	if in.Report != nil {
		d.FarmerName = in.Report.FarmerName
		d.Place = T(placeLine(in.Report.Place), placeLine(in.Report.Place))
		d.Calamity = T(in.Report.CalamityType, in.Report.CalamityType)
		d.OccurredAt = in.Report.OccurredAt
	}
	if in.Item != nil {
		d.SurveyNumber = in.Item.SurveyNumber
		d.Crop = T(in.Item.CropName, in.Item.CropName)
	}

	// A provisional report carries no figure, whatever it was given.
	//
	// Not an estimate, not a range: a provisional number becomes the number
	// everybody remembers, and no later correction ever catches up with it.
	// Enforced here rather than trusted to the caller, because the caller
	// that gets it wrong will be the one under time pressure during a real
	// event.
	if in.State == Final {
		d.AssessedLossPercent = in.AssessedLossPercent
		if in.Consensus != nil {
			d.DecidedBy = Texts{T(in.Consensus.DecidedBy, in.Consensus.DecidedBy)}
			if t, err := time.Parse("2006-01-02", in.Consensus.DecidedAt); err == nil {
				d.DecidedAt = &t
			}
		}
	}

	// What the claim is about, assembled once and given to every instrument
	// section.
	//
	// Without it a section can only explain what its parameter means in
	// general, which is how §8 came to explain waterlogging on a drought report
	// and §7 came to call standing water corroboration of a claim that the
	// field had gone dry.
	c := CaseOf(in)

	d.Add(Claim(in.Report, in.Item))
	d.Add(Parcel(in.Item, placeOf(in.Report), in.OwnerExtentHa, in.Boundary))
	d.Add(CropAndStage(in.Thermal, in.Susceptibility, in.Sowing))
	d.Add(FieldMeasurement(in.Measurements, c))
	d.Add(Weather(in.Weather, in.OfficialRainfall, c))
	d.Add(Optical(in.Satellite, c))
	d.Add(Moisture(in.Moisture, in.MoistureUnavailable))
	d.Add(PointOptical(in.PointNDVI))
	d.Add(Radar(in.SARFinding, in.SARReading, in.SARUnavailable, c))
	d.Add(GroundVision(in.GroundCV, in.GroundCVUnavailable))
	d.Add(TerrainSection(in.Terrain, c))
	d.Add(Baseline(in.Denominators))
	d.Add(ContextNotes(in.Signals))
	d.Add(Assessment(in.AI, decidedByName(in.Consensus)))
	d.Add(Verification(in.Findings, in.Consensus))

	// Before the evidence list, not after: it governs how that list is read.
	// A reader who sees the photographs first has already formed a view of
	// them by the time they learn who chose where to stand.
	d.Add(SamplingCoverage(in.Sampling, in.Walk, in.WalkSpacingM))
	d.Add(Evidence(in.Evidence))

	// Built last because it reads what every section above admitted.
	d.Add(Limits(d))
	d.Add(Objecting(in.AppealWindow))

	d.Headline = headline(d)
	d.InputsDigest = Digest(in)

	return d
}

// headline is the two or three rows that most influenced the outcome.
//
// The determining rows, and nothing else. A summary that led with a striking
// NDVI figure would put a corroborating number on the page a farmer reads
// first, which is the whole error the role markers exist to prevent.
func headline(d *Dossier) []Row {
	rows := d.Determining()
	if len(rows) > 3 {
		rows = rows[:3]
	}
	return rows
}

func decidedByName(c *Consensus) string {
	if c == nil {
		return ""
	}
	return c.DecidedBy
}

func placeOf(r *report.Report) report.Place {
	if r == nil {
		return report.Place{}
	}
	return r.Place
}

func placeLine(p report.Place) string {
	parts := []string{p.VillageName, p.SubDistrictName, p.DistrictName}
	out := ""
	for _, s := range parts {
		if s == "" {
			continue
		}
		if out != "" {
			out += ", "
		}
		out += s
	}
	if out == "" {
		return "—"
	}
	return out
}

// Digest is a hash over the inputs a report was built from.
//
// M7's exit criterion is that an auditor can verify no record was altered
// after the fact, and a report is where that has to become checkable by
// somebody who is not a database administrator. Re-running Build over the
// recorded inputs must produce this same digest; if it does not, either the
// inputs or the report has moved.
//
// json.Marshal orders struct fields as declared and map keys lexically, so
// the encoding is stable for a given Inputs. An input that cannot be encoded
// returns an empty digest rather than a wrong one -- a digest that silently
// covered less than it claimed would be worse than none.
func Digest(in Inputs) string {
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
