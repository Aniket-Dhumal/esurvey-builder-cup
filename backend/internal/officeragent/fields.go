package officeragent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// The fields a report format can ask for.
//
// # Why this list is closed
//
// Because the model chooses from it rather than inventing. Asked to map a heading
// to "whatever field that is", a model will confidently name a field that does not
// exist, and the column then fills with nothing -- or worse, the code is written to
// tolerate unknown names and something has to guess. A closed vocabulary turns an
// unmappable heading into an empty column, which is visible and honest.
//
// It is also the data boundary for this feature. Nothing outside this list can
// reach a filled report, which is why farmer_id and Aadhaar are simply absent
// rather than filtered later.

// istZone is the timezone a report is read in.
//
// The same reasoning as report/render.go: a hailstorm at 16:40 local is not a
// useful fact printed as 11:10 UTC on a form an officer signs.
var istZone = time.FixedZone("IST", 5*60*60+30*60)

// Field is one thing a column can hold.
type Field struct {
	Name string
	// Means is read by the model, so it describes what the value is rather than
	// where it comes from.
	Means string
	Of    func(s report.Summary) any
}

// Fields is the closed vocabulary, in the order it is offered to the model.
var Fields = []Field{
	{"reference", "the claim's reference number, like CLR-2026-496-00001",
		func(s report.Summary) any { return s.Reference }},
	{"farmer_name", "the farmer's name",
		func(s report.Summary) any { return s.FarmerName }},
	{"village", "the village name",
		func(s report.Summary) any { return s.VillageName }},
	{"circle", "the revenue circle, where one is recorded",
		func(s report.Summary) any { return s.CircleName }},
	{"sub_district", "the taluka or sub-district",
		func(s report.Summary) any { return s.SubDistrictName }},
	{"calamity", "the calamity claimed for: drought, hailstorm, flood",
		func(s report.Summary) any { return s.CalamityType }},
	{"loss_category", "what was lost: standing crop, harvested produce",
		func(s report.Summary) any { return s.LossCategory }},
	{"occurred_on", "the date the loss happened, as YYYY-MM-DD",
		func(s report.Summary) any { return s.OccurredAt.In(istZone).Format("2006-01-02") }},
	{"reported_on", "the date the claim was filed, as YYYY-MM-DD",
		func(s report.Summary) any { return s.ReportedAt.In(istZone).Format("2006-01-02") }},
	{"filed_late", "whether it was filed outside the intimation window",
		func(s report.Summary) any { return yesNo(s.FiledLate) }},
	{"state", "where the claim has reached: submitted, assigned, approved, rejected",
		func(s report.Summary) any { return s.State }},
	{"area_sown_ha", "area sown, in hectares",
		func(s report.Summary) any { return s.AreaSownHa }},
	{"area_affected_ha", "area damaged, in hectares",
		func(s report.Summary) any { return s.AreaAffectedHa }},
	{"damage_percent", "percentage of the crop lost, as the farmer claimed it",
		func(s report.Summary) any { return s.DamagePercent }},
	{"lines", "how many crop lines the claim covers",
		func(s report.Summary) any { return s.Lines }},
	{"serial", "a running count down the report, starting at 1",
		nil}, // supplied by the filler, which is the only thing that knows the row
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// FieldNames is the vocabulary as a set, for validating what the model answered.
func FieldNames() map[string]bool {
	out := make(map[string]bool, len(Fields))
	for _, f := range Fields {
		out[f.Name] = true
	}
	return out
}

// Row turns one claim into the values a format can ask for.
func Row(s report.Summary, serial int) map[string]any {
	out := make(map[string]any, len(Fields))
	for _, f := range Fields {
		if f.Of != nil {
			out[f.Name] = f.Of(s)
		}
	}
	out["serial"] = serial
	return out
}

// MapHeadings asks the model which field each column wants.
//
// Only the headings are sent. No claim, no farmer, no figure: a blank format
// carries nobody's data, so this is the one part of the feature where the model is
// given the officer's own document and it costs nothing.
func (a *Agent) MapHeadings(
	ctx context.Context, headings []string,
) ([]string, error) {
	if a.model == nil {
		return nil, ErrNoModel
	}

	var vocab strings.Builder
	for _, f := range Fields {
		fmt.Fprintf(&vocab, "  %s — %s\n", f.Name, f.Means)
	}

	var cols strings.Builder
	for i, h := range headings {
		fmt.Fprintf(&cols, "  %d: %q\n", i, h)
	}

	res, err := a.model.Turn(ctx, TurnRequest{
		SystemInstruction: `You map the columns of a crop-loss report format onto
known fields. The format was designed in a Maharashtra revenue or agriculture
office, so headings may be in Marathi, in English, or abbreviated.

Answer with JSON only: an object whose keys are column numbers as strings and whose
values are field names from the list. Omit a column you cannot match confidently --
an omitted column is left alone, which is correct, whereas a wrong match puts the
wrong figures under a heading an officer will sign.

Never invent a field name. Only these exist:
` + vocab.String(),
		Messages: []Message{{
			Role: "user",
			Text: "Columns:\n" + cols.String(),
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoModel, err)
	}

	mapping := make([]string, len(headings))
	raw := strings.TrimSpace(res.Text)
	// Models fence JSON more often than not, and a fenced answer is not a failed
	// one.
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")

	var byColumn map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &byColumn); err != nil {
		return nil, fmt.Errorf("the mapping could not be read: %w", err)
	}

	known := FieldNames()
	for key, field := range byColumn {
		var col int
		if _, err := fmt.Sscanf(key, "%d", &col); err != nil {
			continue
		}
		if col < 0 || col >= len(mapping) {
			continue
		}
		// A field that does not exist becomes an empty column rather than an
		// error: the rest of the mapping is probably right, and one unfilled
		// column is obvious to whoever reads the report.
		if !known[field] {
			a.log.Warn("the model named a field that does not exist",
				"field", field, "column", col)
			continue
		}
		mapping[col] = field
	}
	return mapping, nil
}

// RowsFor gathers the claims a filled report should carry.
//
// Scoped, like every other read: an officer filling a format gets their own
// district and no one else's, decided by the same store call the listing uses.
func RowsFor(
	ctx context.Context, reports Reports, scope rbac.Scope, f report.Filter, limit int,
) ([]map[string]any, error) {
	if limit <= 0 || limit > 2000 {
		// Two thousand rows is a district's season. Beyond that a spreadsheet
		// stops being something an officer reads and the request is better served
		// by the database directly.
		limit = 2000
	}
	f.Scope = scope
	f.Page.Limit = limit

	res, err := reports.List(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("reading the claims: %w", err)
	}

	out := make([]map[string]any, 0, len(res.Items))
	for i, s := range res.Items {
		out = append(out, Row(s, i+1))
	}
	return out, nil
}
