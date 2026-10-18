package dossier

import (
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	"sync"
)

//go:embed fonts/*.woff2 fonts/OFL.txt
var fontFS embed.FS

// The report carries its own type.
//
// # Why the fonts are embedded rather than named
//
// The stack used to read "Noto Sans Devanagari, Nirmala UI, Mangal, ..." and
// hope. Three things go wrong with that, and all of them land on the Marathi
// half:
//
//   - A machine without any of those faces falls back to something with no
//     Devanagari coverage, and the farmer's half of the report prints as empty
//     boxes. That failure is invisible on a developer's laptop, which has
//     Nirmala UI, and total in a village office that does not.
//   - Which face is picked changes the line breaks, so the same report runs to
//     a different number of pages in different offices -- and a record whose
//     page count is not stable cannot be checked for a missing page.
//   - The PDF converter refuses every remote fetch, deliberately. A linked
//     webfont would simply not arrive.
//
// Inlining the files as data URIs settles all three: the document is one
// file, it looks the same everywhere, and it needs nothing from the network
// or the host.
//
// # Why a serif, and why this one
//
// It is a record that gets filed, photocopied and read in a hearing, not a
// screen. Noto Serif Devanagari and Noto Serif are drawn as one family across
// the two scripts, with harmonised weights and vertical metrics -- which is
// the whole reason Noto exists, and it means the Marathi and English halves
// of this document are set in the same voice rather than two.
//
// # Why one family name for both
//
// Each face is declared under the same CSS family with its own unicode-range,
// so a Devanagari codepoint takes the Devanagari file and a Latin one takes
// the Latin file, automatically, in the middle of a sentence. The stylesheet
// never has to know which language a cell holds -- which matters here, because
// most cells hold both.
//
// Both files are variable fonts spanning the full weight axis, so bold costs
// no second download.
//
// Licensed under the SIL Open Font License 1.1; fonts/OFL.txt travels with
// them, as that licence requires.
const (
	latinRange = "U+0000-00FF, U+0131, U+0152-0153, U+02BB-02BC, U+02C6, " +
		"U+02DA, U+02DC, U+0304, U+0308, U+0329, U+2000-206F, U+20AC, " +
		"U+2122, U+2191, U+2193, U+2212, U+2215, U+FEFF, U+FFFD"

	devanagariRange = "U+0900-097F, U+1CD0-1CF9, U+200C-200D, U+20A8, " +
		"U+20B9, U+20F0, U+25CC, U+A830-A839, U+A8E0-A8FF, U+11B00-11B09"
)

// FamilyName is what the stylesheet asks for. Both scripts answer to it.
const FamilyName = "eSurvey Serif"

var (
	fontsOnce sync.Once
	fontsCSS  template.CSS
)

// fontFaces returns the @font-face rules with the fonts inlined.
//
// Built once: base64 of 160 KB on every report would be work done repeatedly
// to produce a byte-for-byte identical result.
func fontFaces() template.CSS {
	fontsOnce.Do(func() {
		var b strings.Builder

		for _, f := range []struct {
			file  string
			rng   string
			label string
		}{
			{"fonts/NotoSerifDevanagari-devanagari.woff2", devanagariRange, "Devanagari"},
			{"fonts/NotoSerif-latin.woff2", latinRange, "Latin"},
		} {
			data, err := fontFS.ReadFile(f.file)
			if err != nil {
				// Never fatal. A report set in the host's fonts is worse than
				// one set in its own and far better than no report at all, and
				// the fallback stack below still names something plausible.
				continue
			}

			fmt.Fprintf(&b, `
/* %s -- Noto Serif, SIL OFL 1.1, variable weight axis. */
@font-face {
  font-family: '%s';
  src: url(data:font/woff2;base64,%s) format('woff2');
  font-weight: 100 900;
  font-style: normal;
  font-display: block;
  unicode-range: %s;
}`, f.label, FamilyName, base64.StdEncoding.EncodeToString(data), f.rng)
		}

		fontsCSS = template.CSS(b.String())
	})
	return fontsCSS
}

// FontStack is the family declaration the stylesheet uses.
//
// The embedded family first, then faces a host might have. The fallbacks are
// not expected to be reached; they exist so that a build with the embed
// stripped still renders Devanagari on a machine that has any of them, rather
// than boxes.
var FontStack = template.CSS(
	`'` + FamilyName + `', 'Noto Serif Devanagari', 'Noto Serif', ` +
		`'Nirmala UI', 'Mangal', Georgia, 'Times New Roman', serif`)
