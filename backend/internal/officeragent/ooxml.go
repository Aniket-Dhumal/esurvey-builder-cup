package officeragent

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// A Word document and a slide deck, written as OOXML by hand.
//
// # Why by hand
//
// Because both formats are a zip of XML parts and this needs a small, known
// subset of each: headings, paragraphs, one table. The alternative is a library
// that brings a template engine, a style model and a few megabytes of
// dependency to produce the same four files -- and the API image is distroless
// and static on purpose, which pdf-service/Dockerfile says in as many words. A
// document writer is not the thing to spend that property on.
//
// What is given up is everything beyond the subset: no themes to speak of, no
// charts, no revision marks. An officer who needs those has an editable
// document to put them in, which is the point.
//
// # Devanagari, which is the thing most likely to come out wrong
//
// A Marathi name in a Word document is not a matter of getting the bytes into
// the file; the bytes are UTF-8 and go in correctly whatever one does. It is a
// matter of which font the consumer picks for them. Word chooses a font per run
// from w:rFonts, and it chooses separately for each script class: w:ascii for
// Latin, w:hAnsi for the high range, w:eastAsia, and w:cs for complex scripts --
// which is what Devanagari is. A run that names only w:ascii leaves Devanagari
// to the complex-script default, and on a machine whose default has no
// Devanagari that is a row of empty boxes. The same goes for size and weight:
// w:sz and w:b apply to the Latin run, w:szCs and w:bCs to the complex-script
// one, and setting only the first gives a heading that is bold in English and
// not in Marathi.
//
// So every run here names all four font slots, both sizes, both weights, and
// w:lang with w:bidi set, and the document defaults in styles.xml say the same
// thing again for anything that slips through. PowerPoint's equivalent is
// a:latin and a:cs on every a:rPr, plus a Deva entry in the theme's font
// scheme.

// Namespace URIs, named once because every part repeats them.
const (
	nsContentTypes = "http://schemas.openxmlformats.org/package/2006/content-types"
	nsPackageRels  = "http://schemas.openxmlformats.org/package/2006/relationships"
	nsOfficeRels   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsWordML       = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsDrawingML    = "http://schemas.openxmlformats.org/drawingml/2006/main"
	nsPresentML    = "http://schemas.openxmlformats.org/presentationml/2006/main"

	xmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"
)

// --- Word ---------------------------------------------------------------

// maxDocxPortraitColumns is how many columns fit on A4 portrait.
//
// Past this the document is turned on its side rather than the type shrunk to
// something nobody can read. The PDF makes the opposite choice, and says why:
// it has to file beside the claim and the assessment report, which are portrait.
// A Word document is a draft somebody edits and prints themselves, so it can
// afford to be landscape.
const maxDocxPortraitColumns = 6

func (d document) docx() ([]byte, error) {
	landscape := len(d.Fields) > maxDocxPortraitColumns

	var body strings.Builder
	body.WriteString(docxParagraph(d.Title, true, 28))
	body.WriteString(docxParagraph(fmt.Sprintf(
		"%d नोंदी / claims · तयार / prepared %s IST",
		len(d.Rows), d.Prepared.Format("02-01-2006 15:04")), false, 16))
	for _, line := range strings.Split(d.Note, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			body.WriteString(docxParagraph(line, false, 20))
		}
	}
	if len(d.Rows) == 0 {
		body.WriteString(docxParagraph(
			"या निकषांवर कोणताही दावा आढळला नाही / "+
				"no claims matched these filters.", false, 20))
	} else {
		body.WriteString(docxTable(d.headings(), d.textRows(), landscape))
	}
	body.WriteString(docxSectPr(landscape))

	docXML := xmlDecl +
		`<w:document xmlns:w="` + nsWordML + `"><w:body>` +
		body.String() +
		`</w:body></w:document>`

	return zipParts([]zipPart{
		{"[Content_Types].xml", xmlDecl +
			`<Types xmlns="` + nsContentTypes + `">` +
			`<Default Extension="rels" ContentType="application/vnd.` +
			`openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.` +
			`openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
			`<Override PartName="/word/styles.xml" ContentType="application/vnd.` +
			`openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
			`</Types>`},
		{"_rels/.rels", xmlDecl +
			`<Relationships xmlns="` + nsPackageRels + `">` +
			`<Relationship Id="rId1" Type="` + nsOfficeRels + `/officeDocument" ` +
			`Target="word/document.xml"/>` +
			`</Relationships>`},
		{"word/_rels/document.xml.rels", xmlDecl +
			`<Relationships xmlns="` + nsPackageRels + `">` +
			`<Relationship Id="rId1" Type="` + nsOfficeRels + `/styles" ` +
			`Target="styles.xml"/>` +
			`</Relationships>`},
		{"word/styles.xml", docxStyles()},
		{"word/document.xml", docXML},
	})
}

// docxStyles sets the document defaults.
//
// Belt and braces: every run below names its own fonts, and this says the same
// thing once more for anything Word synthesises on its own -- a paragraph the
// officer adds after the table, for instance, which inherits from here and not
// from our runs.
func docxStyles() string {
	return xmlDecl +
		`<w:styles xmlns:w="` + nsWordML + `">` +
		`<w:docDefaults><w:rPrDefault><w:rPr>` +
		docxFonts() +
		`<w:sz w:val="20"/><w:szCs w:val="20"/>` +
		docxLang() +
		`</w:rPr></w:rPrDefault>` +
		`<w:pPrDefault><w:pPr>` +
		`<w:spacing w:after="80" w:line="252" w:lineRule="auto"/>` +
		`</w:pPr></w:pPrDefault></w:docDefaults>` +
		`<w:style w:type="paragraph" w:default="1" w:styleId="Normal">` +
		`<w:name w:val="Normal"/><w:qFormat/></w:style>` +
		`</w:styles>`
}

// docxFonts names all four script slots with the one font that has Devanagari.
//
// w:cs is the load-bearing one -- see the file comment. w:eastAsia is named too
// because Word falls back to it for anything it cannot classify.
func docxFonts() string {
	return `<w:rFonts w:ascii="` + devanagariFontName +
		`" w:hAnsi="` + devanagariFontName +
		`" w:eastAsia="` + devanagariFontName +
		`" w:cs="` + devanagariFontName + `"/>`
}

// docxLang declares the languages, with w:bidi for the complex-script run.
//
// Without w:bidi the Marathi text is spell-checked and hyphenated as English,
// which in a document an officer edits means a wall of red underlines under
// every correctly spelled word.
func docxLang() string {
	return `<w:lang w:val="en-IN" w:eastAsia="en-IN" w:bidi="mr-IN"/>`
}

func docxRunProps(bold bool, halfPoints int) string {
	weight := ""
	if bold {
		// Both, or the heading is bold in English and not in Marathi.
		weight = `<w:b/><w:bCs/>`
	}
	return `<w:rPr>` + docxFonts() + weight +
		fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/>`,
			halfPoints, halfPoints) +
		docxLang() + `</w:rPr>`
}

func docxParagraph(text string, bold bool, halfPoints int) string {
	return `<w:p><w:r>` + docxRunProps(bold, halfPoints) +
		`<w:t xml:space="preserve">` + xmlText(text) + `</w:t></w:r></w:p>`
}

// docxTable is a real table, not a grid of text boxes.
//
// The reason the whole format was asked for: an officer adds a column, deletes a
// village, totals a figure and circulates it. A table of text boxes looks right
// and cannot be edited, which would make this the wrong answer dressed as the
// right one.
func docxTable(headings []string, rows [][]string, landscape bool) string {
	// The printable width in twentieths of a point: A4 less the margins set in
	// docxSectPr. The grid is stated explicitly because a fixed layout is what
	// keeps a fourteen-column table from collapsing its narrow columns to
	// nothing.
	total := 9921
	if landscape {
		total = 14853
	}
	columns := len(headings)
	if columns == 0 {
		return ""
	}
	each := total / columns

	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr>` +
		`<w:tblW w:w="` + itoa(total) + `" w:type="dxa"/>` +
		`<w:tblLayout w:type="fixed"/><w:tblBorders>`)
	for _, edge := range []string{"top", "left", "bottom", "right",
		"insideH", "insideV"} {
		b.WriteString(`<w:` + edge +
			` w:val="single" w:sz="4" w:space="0" w:color="C9CED8"/>`)
	}
	b.WriteString(`</w:tblBorders></w:tblPr><w:tblGrid>`)
	for range headings {
		b.WriteString(`<w:gridCol w:w="` + itoa(each) + `"/>`)
	}
	b.WriteString(`</w:tblGrid>`)

	// tblHeader repeats the headings at the top of every page, which is the
	// difference between a thirty-page statement and thirty pages of unlabelled
	// figures.
	b.WriteString(`<w:tr><w:trPr><w:tblHeader/><w:cantSplit/></w:trPr>`)
	for _, h := range headings {
		b.WriteString(docxCell(h, each, true))
	}
	b.WriteString(`</w:tr>`)

	for _, row := range rows {
		b.WriteString(`<w:tr>`)
		for i := range headings {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			b.WriteString(docxCell(cell, each, false))
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl>`)
	return b.String()
}

func docxCell(text string, width int, header bool) string {
	shading := ""
	size := 18
	if header {
		shading = `<w:shd w:val="clear" w:color="auto" w:fill="F4F6F9"/>`
		size = 16
	}
	return `<w:tc><w:tcPr><w:tcW w:w="` + itoa(width) + `" w:type="dxa"/>` +
		shading + `</w:tcPr>` +
		`<w:p><w:pPr><w:spacing w:after="0"/></w:pPr><w:r>` +
		docxRunProps(header, size) +
		`<w:t xml:space="preserve">` + xmlText(text) + `</w:t>` +
		`</w:r></w:p></w:tc>`
}

// docxSectPr is the page: A4, with margins that leave room for a treasury tag
// down the left edge, the same as the printed report's.
func docxSectPr(landscape bool) string {
	size := `<w:pgSz w:w="11906" w:h="16838"/>`
	if landscape {
		size = `<w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/>`
	}
	return `<w:sectPr>` + size +
		`<w:pgMar w:top="1134" w:right="851" w:bottom="1134" w:left="1134" ` +
		`w:header="709" w:footer="709" w:gutter="0"/></w:sectPr>`
}

// --- PowerPoint ---------------------------------------------------------

// The deck's geometry, in English Metric Units, which is what OOXML measures
// slides in. 12192000 x 6858000 is the 16:9 slide every projector in a
// collectorate meeting room is set up for.
const (
	slideWidthEMU  = 12192000
	slideHeightEMU = 6858000
	slideMarginEMU = 457200

	// maxDeckColumns and maxDeckRows are what fits on a slide and can be read
	// from the back of a room.
	//
	// A deck is a summary for a review meeting, not the statement -- the
	// statement is the xlsx or the PDF. So an export of fourteen columns becomes
	// a deck of the first six, and the title slide says so rather than leaving
	// somebody to wonder which columns they are looking at.
	maxDeckColumns = 6
	maxDeckRows    = 12
)

func (d document) pptx() ([]byte, error) {
	fields := d.Fields
	trimmed := false
	if len(fields) > maxDeckColumns {
		fields = fields[:maxDeckColumns]
		trimmed = true
	}

	headings := make([]string, 0, len(fields))
	for _, f := range fields {
		headings = append(headings, Heading(f.Name))
	}

	summary := fmt.Sprintf("%d नोंदी / claims · %d स्तंभ / columns · "+
		"तयार / prepared %s IST",
		len(d.Rows), len(d.Fields), d.Prepared.Format("02-01-2006 15:04"))
	if trimmed {
		summary += fmt.Sprintf(" · पहिले %d स्तंभ दाखवले / "+
			"showing the first %d columns; the full table is in the "+
			"spreadsheet export", maxDeckColumns, maxDeckColumns)
	}

	slides := []string{pptxTitleSlide(d.Title, summary, d.Note)}

	// One table per slide, chunked. A slide with a scrollbar does not exist, so
	// the only honest way to put a thousand rows in a deck is to make it a
	// thousand rows' worth of slides.
	rows := d.textRows()
	for start := 0; start < len(rows); start += maxDeckRows {
		end := start + maxDeckRows
		if end > len(rows) {
			end = len(rows)
		}
		page := (start / maxDeckRows) + 1
		pages := (len(rows) + maxDeckRows - 1) / maxDeckRows
		chunk := make([][]string, 0, end-start)
		for _, row := range rows[start:end] {
			if len(row) > len(headings) {
				row = row[:len(headings)]
			}
			chunk = append(chunk, row)
		}
		slides = append(slides, pptxTableSlide(
			fmt.Sprintf("%s (%d/%d)", d.Title, page, pages), headings, chunk))
	}

	parts := []zipPart{
		{"[Content_Types].xml", pptxContentTypes(len(slides))},
		{"_rels/.rels", xmlDecl +
			`<Relationships xmlns="` + nsPackageRels + `">` +
			`<Relationship Id="rId1" Type="` + nsOfficeRels + `/officeDocument" ` +
			`Target="ppt/presentation.xml"/>` +
			`</Relationships>`},
		{"ppt/presentation.xml", pptxPresentation(len(slides))},
		{"ppt/_rels/presentation.xml.rels", pptxPresentationRels(len(slides))},
		{"ppt/slideMasters/slideMaster1.xml", pptxSlideMaster()},
		{"ppt/slideMasters/_rels/slideMaster1.xml.rels", xmlDecl +
			`<Relationships xmlns="` + nsPackageRels + `">` +
			`<Relationship Id="rId1" Type="` + nsOfficeRels + `/slideLayout" ` +
			`Target="../slideLayouts/slideLayout1.xml"/>` +
			`<Relationship Id="rId2" Type="` + nsOfficeRels + `/theme" ` +
			`Target="../theme/theme1.xml"/>` +
			`</Relationships>`},
		{"ppt/slideLayouts/slideLayout1.xml", pptxSlideLayout()},
		{"ppt/slideLayouts/_rels/slideLayout1.xml.rels", xmlDecl +
			`<Relationships xmlns="` + nsPackageRels + `">` +
			`<Relationship Id="rId1" Type="` + nsOfficeRels + `/slideMaster" ` +
			`Target="../slideMasters/slideMaster1.xml"/>` +
			`</Relationships>`},
		{"ppt/theme/theme1.xml", pptxTheme()},
		{"ppt/tableStyles.xml", pptxTableStyles()},
	}
	for i, slide := range slides {
		n := itoa(i + 1)
		parts = append(parts,
			zipPart{"ppt/slides/slide" + n + ".xml", slide},
			zipPart{"ppt/slides/_rels/slide" + n + ".xml.rels", xmlDecl +
				`<Relationships xmlns="` + nsPackageRels + `">` +
				`<Relationship Id="rId1" Type="` + nsOfficeRels +
				`/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>` +
				`</Relationships>`})
	}
	return zipParts(parts)
}

func pptxContentTypes(slides int) string {
	var b strings.Builder
	b.WriteString(xmlDecl + `<Types xmlns="` + nsContentTypes + `">` +
		`<Default Extension="rels" ContentType="application/vnd.` +
		`openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.` +
		`openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
		`<Override PartName="/ppt/slideMasters/slideMaster1.xml" ` +
		`ContentType="application/vnd.openxmlformats-officedocument.` +
		`presentationml.slideMaster+xml"/>` +
		`<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ` +
		`ContentType="application/vnd.openxmlformats-officedocument.` +
		`presentationml.slideLayout+xml"/>` +
		`<Override PartName="/ppt/theme/theme1.xml" ` +
		`ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>` +
		`<Override PartName="/ppt/tableStyles.xml" ` +
		`ContentType="application/vnd.openxmlformats-officedocument.` +
		`presentationml.tableStyles+xml"/>`)
	for i := 1; i <= slides; i++ {
		b.WriteString(`<Override PartName="/ppt/slides/slide` + itoa(i) +
			`.xml" ContentType="application/vnd.openxmlformats-officedocument.` +
			`presentationml.slide+xml"/>`)
	}
	b.WriteString(`</Types>`)
	return b.String()
}

func pptxPresentation(slides int) string {
	var ids strings.Builder
	for i := 1; i <= slides; i++ {
		// Slide ids start at 256 by convention and the relationship ids are
		// offset by one, because rId1 is the slide master.
		ids.WriteString(fmt.Sprintf(
			`<p:sldId id="%d" r:id="rId%d"/>`, 255+i, i+1))
	}
	return xmlDecl +
		`<p:presentation xmlns:a="` + nsDrawingML + `" xmlns:r="` + nsOfficeRels +
		`" xmlns:p="` + nsPresentML + `">` +
		`<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/>` +
		`</p:sldMasterIdLst>` +
		`<p:sldIdLst>` + ids.String() + `</p:sldIdLst>` +
		fmt.Sprintf(`<p:sldSz cx="%d" cy="%d"/>`,
			slideWidthEMU, slideHeightEMU) +
		`<p:notesSz cx="6858000" cy="9144000"/>` +
		`</p:presentation>`
}

func pptxPresentationRels(slides int) string {
	var b strings.Builder
	b.WriteString(xmlDecl + `<Relationships xmlns="` + nsPackageRels + `">` +
		`<Relationship Id="rId1" Type="` + nsOfficeRels + `/slideMaster" ` +
		`Target="slideMasters/slideMaster1.xml"/>`)
	for i := 1; i <= slides; i++ {
		b.WriteString(fmt.Sprintf(
			`<Relationship Id="rId%d" Type="%s/slide" `+
				`Target="slides/slide%d.xml"/>`, i+1, nsOfficeRels, i))
	}
	b.WriteString(fmt.Sprintf(
		`<Relationship Id="rId%d" Type="%s/theme" Target="theme/theme1.xml"/>`,
		slides+2, nsOfficeRels))
	b.WriteString(fmt.Sprintf(
		`<Relationship Id="rId%d" Type="%s/tableStyles" `+
			`Target="tableStyles.xml"/>`, slides+3, nsOfficeRels))
	b.WriteString(`</Relationships>`)
	return b.String()
}

// emptySpTree is the shape tree a slide, layout or master opens with.
const emptySpTree = `<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/>` +
	`<p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm>` +
	`<a:off x="0" y="0"/><a:ext cx="0" cy="0"/>` +
	`<a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`

// pptxSlideMaster is deliberately empty of placeholders.
//
// A master with placeholder shapes is a master that decides where a title goes,
// and then every slide inherits a layout nobody here designed. These slides
// place their own shapes, so the master exists only because the format requires
// one: it carries the colour map and nothing else.
func pptxSlideMaster() string {
	return xmlDecl +
		`<p:sldMaster xmlns:a="` + nsDrawingML + `" xmlns:r="` + nsOfficeRels +
		`" xmlns:p="` + nsPresentML + `">` +
		`<p:cSld><p:spTree>` + emptySpTree + `</p:spTree></p:cSld>` +
		`<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" ` +
		`accent2="accent2" accent3="accent3" accent4="accent4" ` +
		`accent5="accent5" accent6="accent6" hlink="hlink" ` +
		`folHlink="folHlink"/>` +
		`<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/>` +
		`</p:sldLayoutIdLst>` +
		`</p:sldMaster>`
}

func pptxSlideLayout() string {
	return xmlDecl +
		`<p:sldLayout xmlns:a="` + nsDrawingML + `" xmlns:r="` + nsOfficeRels +
		`" xmlns:p="` + nsPresentML + `" type="blank" preserve="1">` +
		`<p:cSld name="Blank"><p:spTree>` + emptySpTree + `</p:spTree></p:cSld>` +
		`<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>` +
		`</p:sldLayout>`
}

// pptxTheme carries the font scheme, which is the part that matters here.
//
// a:cs and the Deva script entry are PowerPoint's w:cs: without them a Marathi
// village name is drawn in whatever the complex-script default is, which on a
// machine without Devanagari is nothing at all.
func pptxTheme() string {
	accent := func(n int, hex string) string {
		return fmt.Sprintf(`<a:accent%d><a:srgbClr val="%s"/></a:accent%d>`,
			n, hex, n)
	}
	font := func(tag string) string {
		return `<a:` + tag + `><a:latin typeface="` + devanagariFontName +
			`"/><a:ea typeface=""/><a:cs typeface="` + devanagariFontName +
			`"/><a:font script="Deva" typeface="` + devanagariFontName +
			`"/></a:` + tag + `>`
	}
	line := `<a:ln w="6350" cap="flat" cmpd="sng" algn="ctr">` +
		`<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>` +
		`<a:prstDash val="solid"/></a:ln>`
	fill := `<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>`

	return xmlDecl +
		`<a:theme xmlns:a="` + nsDrawingML + `" name="Export">` +
		`<a:themeElements>` +
		`<a:clrScheme name="Export">` +
		`<a:dk1><a:srgbClr val="16181C"/></a:dk1>` +
		`<a:lt1><a:srgbClr val="FFFFFF"/></a:lt1>` +
		`<a:dk2><a:srgbClr val="5D6470"/></a:dk2>` +
		`<a:lt2><a:srgbClr val="F4F6F9"/></a:lt2>` +
		accent(1, "7B838F") + accent(2, "4F6D7A") + accent(3, "8A4B00") +
		accent(4, "2E5E4E") + accent(5, "6B5B95") + accent(6, "9A3B3B") +
		`<a:hlink><a:srgbClr val="0B5394"/></a:hlink>` +
		`<a:folHlink><a:srgbClr val="674EA7"/></a:folHlink>` +
		`</a:clrScheme>` +
		`<a:fontScheme name="Export">` +
		font("majorFont") + font("minorFont") +
		`</a:fontScheme>` +
		`<a:fmtScheme name="Export">` +
		`<a:fillStyleLst>` + fill + fill + fill + `</a:fillStyleLst>` +
		`<a:lnStyleLst>` + line + line + line + `</a:lnStyleLst>` +
		`<a:effectStyleLst>` +
		`<a:effectStyle><a:effectLst/></a:effectStyle>` +
		`<a:effectStyle><a:effectLst/></a:effectStyle>` +
		`<a:effectStyle><a:effectLst/></a:effectStyle>` +
		`</a:effectStyleLst>` +
		`<a:bgFillStyleLst>` + fill + fill + fill + `</a:bgFillStyleLst>` +
		`</a:fmtScheme>` +
		`</a:themeElements></a:theme>`
}

// tableGridStyle is PowerPoint's built-in "Table Grid".
//
// Named rather than defined: the style is built into PowerPoint, so declaring it
// as the default here gets a table with visible borders without this file
// carrying a style definition of its own. A table with no style at all draws no
// borders, and a borderless table of figures on a projector is unreadable.
const tableGridStyle = "{5940675A-B579-460E-94D1-54222C63F5DA}"

func pptxTableStyles() string {
	return xmlDecl + `<a:tblStyleLst xmlns:a="` + nsDrawingML +
		`" def="` + tableGridStyle + `"/>`
}

func pptxTitleSlide(title, summary, note string) string {
	var body strings.Builder
	body.WriteString(pptxTextBox(2, "Title",
		slideMarginEMU, 1600200, slideWidthEMU-2*slideMarginEMU, 1200000,
		[]pptxLine{{Text: title, Size: 3200, Bold: true}}))

	lines := []pptxLine{{Text: summary, Size: 1400}}
	for _, line := range strings.Split(note, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, pptxLine{Text: line, Size: 1600})
		}
	}
	body.WriteString(pptxTextBox(3, "Summary",
		slideMarginEMU, 3000000, slideWidthEMU-2*slideMarginEMU, 2400000, lines))

	return pptxSlide(body.String())
}

func pptxTableSlide(title string, headings []string, rows [][]string) string {
	var body strings.Builder
	body.WriteString(pptxTextBox(2, "Title",
		slideMarginEMU, 274320, slideWidthEMU-2*slideMarginEMU, 700000,
		[]pptxLine{{Text: title, Size: 2000, Bold: true}}))
	body.WriteString(pptxTable(3, headings, rows))
	return pptxSlide(body.String())
}

func pptxSlide(shapes string) string {
	return xmlDecl +
		`<p:sld xmlns:a="` + nsDrawingML + `" xmlns:r="` + nsOfficeRels +
		`" xmlns:p="` + nsPresentML + `">` +
		`<p:cSld><p:spTree>` + emptySpTree + shapes + `</p:spTree></p:cSld>` +
		`<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>` +
		`</p:sld>`
}

// pptxLine is one paragraph in a text box.
type pptxLine struct {
	Text string
	Size int // hundredths of a point, which is what a:sz takes
	Bold bool
}

func pptxTextBox(
	id int, name string, x, y, cx, cy int, lines []pptxLine,
) string {
	var paragraphs strings.Builder
	for _, line := range lines {
		paragraphs.WriteString(`<a:p><a:r>` +
			pptxRunProps(line.Size, line.Bold) +
			`<a:t>` + xmlText(line.Text) + `</a:t></a:r></a:p>`)
	}
	return fmt.Sprintf(
		`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/>`+
			`<p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr>`+
			`<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/>`+
			`</a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>`+
			`<p:txBody><a:bodyPr wrap="square"><a:normAutofit/></a:bodyPr>`+
			`<a:lstStyle/>%s</p:txBody></p:sp>`,
		id, xmlAttr(name), x, y, cx, cy, paragraphs.String())
}

// pptxRunProps names the Latin and complex-script fonts on every run.
//
// lang and altLang are set the other way round from the Word document's: a
// slide's text is mostly the Marathi heading, and PowerPoint's proofing reads
// lang first.
func pptxRunProps(size int, bold bool) string {
	weight := ""
	if bold {
		weight = ` b="1"`
	}
	return fmt.Sprintf(
		`<a:rPr lang="mr-IN" altLang="en-IN" sz="%d"%s dirty="0">`+
			`<a:latin typeface="%s"/><a:cs typeface="%s"/></a:rPr>`,
		size, weight, devanagariFontName, devanagariFontName)
}

func pptxTable(id int, headings []string, rows [][]string) string {
	columns := len(headings)
	if columns == 0 {
		return ""
	}
	width := slideWidthEMU - 2*slideMarginEMU
	each := width / columns
	// Row height is a minimum rather than a maximum: PowerPoint grows a row
	// whose text wraps, which is what a long village name does.
	const rowHeight = 370840
	height := rowHeight * (len(rows) + 1)

	var b strings.Builder
	b.WriteString(fmt.Sprintf(
		`<p:graphicFrame><p:nvGraphicFramePr>`+
			`<p:cNvPr id="%d" name="Claims"/>`+
			`<p:cNvGraphicFramePr><a:graphicFrameLocks noGrp="1"/>`+
			`</p:cNvGraphicFramePr><p:nvPr/></p:nvGraphicFramePr>`+
			`<p:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></p:xfrm>`+
			`<a:graphic><a:graphicData uri="%s/table">`+
			`<a:tbl><a:tblPr firstRow="1" bandRow="1">`+
			`<a:tableStyleId>%s</a:tableStyleId></a:tblPr><a:tblGrid>`,
		id, slideMarginEMU, 1150000, width, height,
		nsDrawingML, tableGridStyle))
	for range headings {
		b.WriteString(fmt.Sprintf(`<a:gridCol w="%d"/>`, each))
	}
	b.WriteString(`</a:tblGrid>`)

	b.WriteString(fmt.Sprintf(`<a:tr h="%d">`, rowHeight))
	for _, h := range headings {
		b.WriteString(pptxCell(h, 1000, true))
	}
	b.WriteString(`</a:tr>`)

	for _, row := range rows {
		b.WriteString(fmt.Sprintf(`<a:tr h="%d">`, rowHeight))
		for i := range headings {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			b.WriteString(pptxCell(cell, 1000, false))
		}
		b.WriteString(`</a:tr>`)
	}
	b.WriteString(`</a:tbl></a:graphicData></a:graphic></p:graphicFrame>`)
	return b.String()
}

func pptxCell(text string, size int, header bool) string {
	return `<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r>` +
		pptxRunProps(size, header) +
		`<a:t>` + xmlText(text) + `</a:t></a:r></a:p></a:txBody>` +
		`<a:tcPr marL="45720" marR="45720" marT="27432" marB="27432" ` +
		`anchor="t"/></a:tc>`
}

// --- the package itself -------------------------------------------------

type zipPart struct {
	name string
	body string
}

// zipParts writes the parts as an OOXML package.
//
// [Content_Types].xml is written first because that is where a consumer looks
// first, and a package whose content types arrive last is one some readers give
// up on.
func zipParts(parts []zipPart) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, part := range parts {
		f, err := w.Create(part.name)
		if err != nil {
			return nil, fmt.Errorf("adding %s: %w", part.name, err)
		}
		if _, err := f.Write([]byte(part.body)); err != nil {
			return nil, fmt.Errorf("writing %s: %w", part.name, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("closing the package: %w", err)
	}
	return buf.Bytes(), nil
}

// xmlText escapes a value for an XML text node, and drops what XML cannot hold.
//
// The escaping is the obvious part. The dropping is not: XML 1.0 has no way to
// represent most control characters, not even as a numeric reference, and a
// farmer name carrying one -- which happens, because these names arrive from
// registries older than this platform -- would produce a document that no reader
// will open at all rather than one with an odd character in it.
func xmlText(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		case '\t', '\n', '\r':
			// Allowed by XML, and meaningful: a newline inside a cell is how a
			// registry stores a two-line address.
			b.WriteRune(r)
		default:
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0xfffe || r == 0xffff {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// xmlAttr escapes a value for an attribute. Same rules; named separately
// because a reader of the call site should not have to know they are the same.
func xmlAttr(s string) string { return xmlText(s) }

func itoa(n int) string { return strconv.Itoa(n) }
