// Package pdf turns a rendered report into a PDF.
//
// # Why this is a service and not a library
//
// The report is half Marathi, and Devanagari is not a font problem. Rendering
// it correctly means reordering matras that are stored after their consonant
// and drawn before it, forming conjuncts from consonant clusters, and placing
// the shirorekha across the result -- complex text layout, of the kind
// HarfBuzz does. No pure-Go PDF library has it. fpdf, gopdf and maroto all
// embed a TrueType font and draw its glyphs in codepoint order, which for
// Devanagari produces text that is not merely ugly but reordered into
// different words.
//
// A wrong figure in this document costs a farmer money. A wrong word in it
// costs them the ability to argue, and neither is worth saving a container
// for. So the HTML that the console already renders is converted by a real
// browser engine, and this package is the client for that.
//
// # Why it degrades rather than fails
//
// A report that cannot be turned into a PDF is still a complete report: the
// HTML prints from any browser, which is how it will be printed in a taluka
// office anyway. So every failure here returns an error the caller can fall
// back from, and none of them is worth refusing to issue a report over.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// ErrUnavailable means no converter is configured or reachable.
//
// Distinguished from a conversion failure because the caller does different
// things with them: an unconfigured converter is an ordinary deployment
// without one, and a reachable converter that refused the document is worth
// logging loudly.
var ErrUnavailable = errors.New("no PDF converter is available")

// Renderer converts HTML to PDF.
type Renderer struct {
	BaseURL string
	HTTP    *http.Client

	// Archival asks for PDF/A, which is what a record kept for years is
	// supposed to be: fonts embedded, no external references, and readable by
	// a viewer that does not exist yet. It costs a second conversion step, so
	// it is a choice rather than the default.
	Archival bool
}

// New builds a renderer. An empty base URL yields one that reports
// ErrUnavailable, so a deployment without a converter needs no special case
// at the call site.
func New(baseURL string) *Renderer {
	return &Renderer{
		BaseURL: strings.TrimRight(baseURL, "/"),
		// Generous: a fifteen-section report with two language halves is a
		// large document for a browser to lay out, and the alternative to
		// waiting is handing back an error for a conversion that would have
		// succeeded.
		HTTP: &http.Client{Timeout: 2 * time.Minute},
	}
}

// Available reports whether a converter is configured.
func (r *Renderer) Available() bool { return r != nil && r.BaseURL != "" }

// Render converts a complete HTML document to PDF bytes.
func (r *Renderer) Render(ctx context.Context, html []byte, reference string) ([]byte, error) {
	if !r.Available() {
		return nil, ErrUnavailable
	}

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)

	if err := writeFile(w, "index.html", html); err != nil {
		return nil, err
	}
	if err := writeFile(w, "header.html", []byte(headerHTML(reference))); err != nil {
		return nil, err
	}
	if err := writeFile(w, "footer.html", []byte(footerHTML(reference))); err != nil {
		return nil, err
	}

	// A4 in inches, because that is the unit the API takes. The margins leave
	// room for the running header and footer and for a treasury tag down the
	// left edge, which is how these end up in a taluka file.
	fields := map[string]string{
		"paperWidth":              "8.27",
		"paperHeight":             "11.7",
		"marginTop":               "0.6",
		"marginBottom":            "0.55",
		"marginLeft":              "0.5",
		"marginRight":             "0.35",
		"printBackground":         "true",
		"preferCssPageSize":       "false",
		"generateDocumentOutline": "true",
		"emulatedMediaType":       "print",
	}
	if r.Archival {
		fields["pdfa"] = "PDF/A-3b"
		fields["pdfua"] = "true"
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("building the conversion request: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("building the conversion request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.BaseURL+"/forms/chromium/convert/html", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	res, err := r.HTTP.Do(req)
	if err != nil {
		// Unreachable rather than refusing: the same case as unconfigured, so
		// the caller falls back the same way.
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return nil, fmt.Errorf("the converter refused the document (%s): %s",
			res.Status, strings.TrimSpace(string(detail)))
	}

	out, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading the converted document: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("the converter returned an empty document")
	}
	return out, nil
}

func writeFile(w *multipart.Writer, name string, content []byte) error {
	part, err := w.CreateFormFile("files", name)
	if err != nil {
		return fmt.Errorf("attaching %s: %w", name, err)
	}
	if _, err := part.Write(content); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	return nil
}

// headerHTML and footerHTML are the running furniture on every page.
//
// # Why they carry no Marathi
//
// Chrome renders print headers in an isolated context that does not reliably
// pick up the document's fonts, and a Devanagari string that falls back to a
// font without the script is not just unstyled -- it is unreadable or absent.
// A page number that vanishes on the copy filed at the taluka office is worse
// than one with no word in front of it.
//
// So the header carries the report number and the footer carries "1 / 15",
// which reads identically in both halves of the document and cannot be
// mis-shaped. The words for "page" appear in the body, where the fonts are
// under the document's control.
func headerHTML(reference string) string {
	return `<html><head><style>
	  body { margin: 0; font-family: system-ui, sans-serif; }
	  .h { width: 100%; font-size: 7pt; color: #55595e;
	       padding: 0 12mm; display: flex; justify-content: space-between; }
	</style></head><body>
	  <div class="h"><span>` + escape(reference) + `</span><span class="date"></span></div>
	</body></html>`
}

func footerHTML(reference string) string {
	return `<html><head><style>
	  body { margin: 0; font-family: system-ui, sans-serif; }
	  .f { width: 100%; font-size: 7pt; color: #55595e;
	       padding: 0 12mm; display: flex; justify-content: space-between; }
	</style></head><body>
	  <div class="f">
	    <span>` + escape(reference) + `</span>
	    <span><span class="pageNumber"></span> / <span class="totalPages"></span></span>
	  </div>
	</body></html>`
}

// escape keeps a reference number from closing a tag. References are
// generated by this system and contain nothing dangerous, which is exactly
// the assumption that stops being true one schema change from now.
func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
