package report

import (
	"embed"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

var printTemplate = template.Must(template.New("crop_loss_report.gohtml").Funcs(
	template.FuncMap{
		"ha":       func(v float64) string { return trim(v, 4) },
		"pct":      func(v float64) string { return trim(v, 2) },
		"date":     func(t time.Time) string { return t.In(ist).Format("02-01-2006") },
		"datetime": func(t time.Time) string { return t.In(ist).Format("02-01-2006 15:04") },
		"dateptr": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			return t.In(ist).Format("02-01-2006 15:04")
		},
		"or": func(v, fallback string) string {
			if v == "" {
				return fallback
			}
			return v
		},
		"label":    label,
		"upper":    strings.ToUpper,
		"joinDocs": joinDocs,
	}).ParseFS(templateFS, "templates/*.gohtml"))

// ist is the timezone the report is read in. A hailstorm at 16:40 local is not
// a useful fact rendered as 11:10 UTC on a form an officer signs.
var ist = time.FixedZone("IST", 5*60*60+30*60)

// Print writes the report as a printable HTML page.
//
// HTML with print styles rather than generated PDF. A PDF library is a
// dependency, a font-embedding problem and a layout engine to fight; a browser
// already has all three, prints to PDF on every officer's machine, and lets the
// page be read on a phone in a field. The trade is worth naming because it
// looks like a shortcut and is not.
func (r *Report) Print(w io.Writer) error {
	return printTemplate.Execute(w, printView{
		Report:          r,
		TotalSown:       r.TotalSownHa(),
		TotalAffected:   r.TotalAffectedHa(),
		WeightedPercent: r.WeightedDamagePercent(),
		PrintedAt:       time.Now().In(ist),
	})
}

type printView struct {
	*Report
	TotalSown       float64
	TotalAffected   float64
	WeightedPercent float64
	PrintedAt       time.Time
}

// label renders a value bilingually, Marathi first.
//
// The form an officer and a farmer sign is read in Marathi. English beside it
// is for the district and state offices that aggregate these, and for anyone
// reading the record who does not read Devanagari. Neither audience is served
// by picking one.
func label(kind, value string) template.HTML {
	m, ok := labels[kind+":"+value]
	if !ok {
		return template.HTML(template.HTMLEscapeString(value))
	}
	return template.HTML(
		template.HTMLEscapeString(m[0]) +
			` <span class="en">` + template.HTMLEscapeString(m[1]) + `</span>`)
}

// labels are the Marathi and English renderings of every enumerated value that
// appears on the printed form.
var labels = map[string][2]string{
	"season:kharif": {"खरीप", "Kharif"},
	"season:rabi":   {"रब्बी", "Rabi"},
	"season:summer": {"उन्हाळी", "Summer"},

	"category:standing_crop": {"स्थानिक नैसर्गिक आपत्ती", "Localised natural calamity"},
	"category:post_harvest":  {"काढणी पश्चात नुकसान", "Post-harvest loss"},

	"calamity:flood":           {"पुराचे पाणी शेतात शिरून नुकसान", "Inundation"},
	"calamity:hailstorm":       {"गारपीट", "Hailstorm"},
	"calamity:landslide":       {"भूस्खलन", "Landslide"},
	"calamity:cyclone":         {"चक्रीवादळ", "Cyclone"},
	"calamity:unseasonal_rain": {"अवकाळी पाऊस", "Unseasonal rain"},
	"calamity:cloudburst":      {"ढगफुटी", "Cloudburst"},
	"calamity:drought":         {"दुष्काळ", "Drought"},
	"calamity:pest_attack":     {"किड रोग प्रादुर्भाव", "Pest attack"},
	"calamity:frost":           {"थंडी / हिम", "Frost"},
	"calamity:fire":            {"आग", "Fire"},

	"state:draft":        {"मसुदा", "Draft"},
	"state:submitted":    {"सादर", "Submitted"},
	"state:acknowledged": {"पोहोच दिली", "Acknowledged"},
	"state:inspected":    {"तपासणी पूर्ण", "Reviewed"},
	"state:closed":       {"निकाली", "Closed"},
	"state:withdrawn":    {"मागे घेतले", "Withdrawn"},

	"signature:signature": {"सही", "Signature"},
	"signature:thumb":     {"अंगठा", "Thumb impression"},

	"channel:field_officer": {"क्षेत्रीय अधिकारी", "Field officer"},
	"channel:farmer_app":    {"शेतकरी अ‍ॅप", "Farmer app"},
	"channel:call_centre":   {"मदत केंद्र", "Call centre"},
}

// documentLabels name the proofs of title the farmer undertakes to produce.
// The insurance premium receipt the original form also demanded is absent.
var documentLabels = map[string][2]string{
	"7_12":                   {"७/१२ उतारा", "7/12 extract"},
	"8a":                     {"८-अ उतारा", "8-A extract"},
	"lease_agreement":        {"भाडेपट्टी करारनामा", "Lease agreement"},
	"sharecropper_agreement": {"भागधारक करारनामा", "Sharecropper agreement"},
	"identity_proof":         {"ओळखपत्र", "Identity proof"},
}

// KnownDocuments returns the documents a report may promise, for a form.
func KnownDocuments() map[string][2]string { return documentLabels }

func joinDocs(docs []string) template.HTML {
	if len(docs) == 0 {
		return template.HTML(`<span class="muted">—</span>`)
	}
	parts := make([]string, 0, len(docs))
	for _, d := range docs {
		if m, ok := documentLabels[d]; ok {
			parts = append(parts, template.HTMLEscapeString(m[0])+
				` <span class="en">`+template.HTMLEscapeString(m[1])+`</span>`)
			continue
		}
		parts = append(parts, template.HTMLEscapeString(d))
	}
	return template.HTML(strings.Join(parts, " · "))
}

// trim formats a float without trailing zeros, so 1.6 prints as 1.6 rather
// than 1.6000 on a form somebody reads.
func trim(v float64, places int) string {
	s := strconv.FormatFloat(v, 'f', places, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return s
}
