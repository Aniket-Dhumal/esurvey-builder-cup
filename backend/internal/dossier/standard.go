package dossier

// The furniture that makes this a record rather than a printout.
//
// # Why any of this matters
//
// A crop-loss assessment ends up in three places a web page never goes: a
// taluka file, a grievance hearing, and occasionally a court. Each of those
// asks the same questions of a document before reading a word of it -- who
// issued it, under what authority, which page is missing, who signed, and
// whether this copy is the one that was issued. A document that cannot answer
// them is argued about instead of read.
//
// So the report carries a cover, a contents list, page numbering that says
// how many pages there should be, the authority it was made under, a
// signature block, and a digest tying it to its inputs. None of it is
// decoration; each item answers one of those questions.

// Issuer is who the report goes out under.
//
// Left at its default this is the platform itself. It becomes a government
// office only when a government office sets it -- see [DefaultStandard] for
// why that distinction is load-bearing rather than cosmetic.
type Issuer struct {
	// Government is the top line of the masthead: the platform by default,
	// or the state where an office has adopted the form as its own.
	Government Text
	// Department is the second line.
	Department Text
	// Office is the issuing office itself, where there is one.
	Office Text
	// Place is the town the office sits in, printed above the signatures.
	Place string
}

// Signatory is one person who signs the printed copy.
//
// The block is printed empty, with room for a wet signature and a seal. A
// digitally produced document that shows a name where a signature belongs
// invites the question of who actually checked it, and the honest answer at
// the moment of printing is nobody.
type Signatory struct {
	Name        string
	Designation Text
	Office      Text
}

// Standard is everything above, together, so a caller sets it in one place.
type Standard struct {
	Issuer Issuer

	// Title overrides the default heading where a district uses its own
	// wording for the form.
	Title Text

	// LegalBasis is the authority the assessment is made under: the
	// resolution, the scheme, the section. Printed on the cover, because a
	// reader in a hearing looks for it before anything else.
	LegalBasis Texts

	// Signatories are printed in the order given, which is the order they
	// sign in.
	Signatories []Signatory

	// FormCode is the office's own designation for this form, if it has one.
	FormCode string
}

// coverView is the first page.
type coverView struct {
	Government string
	Department string
	Office     string
	Title      string
	FormCode   string

	Stamp    string
	StampKey string

	Reference  string
	Supersedes string

	Fields []labelled

	LegalHeading string
	LegalBasis   []string

	GeneratedAt   string
	DigestLabel   string
	Digest        string
	ComputerMade  string
	ContentsLabel string
	Contents      []contentsLine
}

type labelled struct {
	Label string
	Value string
}

type contentsLine struct {
	Number string
	Title  string
}

type signatureView struct {
	Heading string
	Place   string
	Date    string
	Blocks  []signatureBlock
	Note    string
}

type signatureBlock struct {
	Name        string
	Designation string
	Office      string
	SignLabel   string
	DateLabel   string
	SealLabel   string
}

func (d *Dossier) cover(l Lang) coverView {
	s := d.Std

	title := s.Title
	if title.Empty() {
		title = T("पीक नुकसान मूल्यांकन अहवाल", "Crop Loss Assessment Report")
	}

	c := coverView{
		Government: s.Issuer.Government.In(l),
		Department: s.Issuer.Department.In(l),
		Office:     s.Issuer.Office.In(l),
		Title:      title.In(l),
		FormCode:   s.FormCode,

		Stamp:    d.State.Label().In(l),
		StampKey: stampKey(d.State),

		Reference:  d.Reference,
		Supersedes: d.Supersedes,

		LegalHeading: pick(l, "कायदेशीर आधार", "Made under"),
		LegalBasis:   s.LegalBasis.In(l),

		GeneratedAt: day(d.GeneratedAt),
		DigestLabel: pick(l, "नोंदींचा पडताळणी क्रमांक", "Inputs digest"),
		Digest:      d.InputsDigest,

		// Said on the cover rather than in small print at the end. A reader
		// who assumes a person checked every figure because the document
		// looks official is reading it wrongly, and the correction belongs
		// where they start.
		ComputerMade: pick(l,
			"हा अहवाल संगणकाद्वारे तयार केलेला आहे. यातील आकडे कोणत्या "+
				"आधारावर काढले, ते प्रत्येक विभागात दिलेले आहे. स्वाक्षरीशिवाय "+
				"हा अहवाल अंतिम मानला जाऊ नये.",
			"This report was produced by a computer system. The basis for every "+
				"figure is stated in the section it appears in. It should not be "+
				"treated as final without the signatures below."),

		ContentsLabel: pick(l, "अनुक्रमणिका", "Contents"),
	}

	c.Fields = []labelled{
		{pick(l, "शेतकरी", "Farmer"), orDash(d.FarmerName, "—")},
		{pick(l, "गाव, तालुका, जिल्हा", "Village, taluka, district"), d.Place.In(l)},
		{pick(l, "सर्व्हे क्रमांक", "Survey number"), orDash(d.SurveyNumber, "—")},
		{pick(l, "पीक", "Crop"), d.Crop.In(l)},
		{pick(l, "कालामत", "Calamity"), d.Calamity.In(l)},
		{pick(l, "घटनेचा दिनांक", "Date of the event"), day(d.OccurredAt)},
		{pick(l, "अहवाल दिनांक", "Report date"), day(d.GeneratedAt)},
	}

	for _, sec := range d.Sections {
		c.Contents = append(c.Contents, contentsLine{
			Number: sec.Number,
			Title:  sec.Title.In(l),
		})
	}

	return c
}

func (d *Dossier) signatures(l Lang) signatureView {
	v := signatureView{
		Heading: pick(l, "स्वाक्षऱ्या", "Signatures"),
		Place:   pick(l, "ठिकाण", "Place") + ": " + orDash(d.Std.Issuer.Place, "—"),
		Date:    pick(l, "दिनांक", "Date") + ": " + day(d.GeneratedAt),
		Note: pick(l,
			"खालील स्वाक्षऱ्यांशिवाय हा अहवाल अपूर्ण आहे.",
			"This report is incomplete without the signatures below."),
	}

	for _, s := range d.Std.Signatories {
		v.Blocks = append(v.Blocks, signatureBlock{
			Name:        orDash(s.Name, ""),
			Designation: s.Designation.In(l),
			Office:      s.Office.In(l),
			SignLabel:   pick(l, "स्वाक्षरी", "Signature"),
			DateLabel:   pick(l, "दिनांक", "Date"),
			SealLabel:   pick(l, "शिक्का", "Seal"),
		})
	}

	return v
}

func stampKey(s State) string {
	if s == Final {
		return "final"
	}
	return "provisional"
}

// DefaultStandard is the furniture for a report with nothing configured.
//
// # Why the masthead is the platform and not a government
//
// It said "Government of Maharashtra, Revenue and Forest Department" and
// claimed to be made under Government Resolutions. That was wrong, and wrong
// in the way that matters most: a document carrying a state masthead and a
// legal basis it does not have is a forged official record, whatever the
// intent behind the template. A farmer would have no way to tell it apart
// from one the Collector actually issued, and neither would a court.
//
// So the default names the system that produced it. Where a revenue office
// genuinely adopts this platform, that office sets [Issuer] and [LegalBasis]
// to its own -- which is a decision an officer takes, on their own authority,
// and not a string this package ships with.
func DefaultStandard() Standard {
	return Standard{
		Issuer: Issuer{
			Government: T("eSurvey", "eSurvey"),
			Department: T("पीक नुकसान मूल्यांकन प्रणाली",
				"Crop Loss Assessment Platform"),
		},
		LegalBasis: Texts{
			T(
				"हा अहवाल eSurvey प्रणालीने तयार केलेला आहे. ही प्रणाली "+
					"शेतातील मोजणी, हवामान नोंदी आणि उपग्रह निरीक्षणे एकत्र करून "+
					"नुकसानीचे मूल्यांकन मांडते. नुकसानभरपाईचा निर्णय सक्षम "+
					"अधिकाऱ्यांचा असतो; हा अहवाल त्यांच्यासाठी पुरावा आणि "+
					"विश्लेषण एकत्र करतो.",
				"This report was produced by the eSurvey platform, which brings "+
					"field measurement, weather records and satellite observation "+
					"together into one assessment. The decision on compensation "+
					"rests with the competent authority; this report assembles the "+
					"evidence and the analysis for them.",
			),
		},
	}
}
