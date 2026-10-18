package pdf

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// What matters here is not that a PDF comes back -- that is Chromium's job --
// but that a deployment without a converter, or with a broken one, still
// issues reports.

func TestNoConverterIsNotAnError(t *testing.T) {
	// The ordinary shape of a deployment that has not stood one up. The
	// caller falls back to HTML, which is what a taluka office prints from
	// anyway.
	_, err := New("").Render(context.Background(), []byte("<html></html>"), "REF-1")

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if New("").Available() {
		t.Error("an unconfigured renderer should not claim to be available")
	}
}

func TestAnUnreachableConverterReportsUnavailable(t *testing.T) {
	// Same fallback as unconfigured, because the caller does the same thing:
	// a converter that is down must not stop a farmer's report being issued.
	r := New("http://127.0.0.1:1")

	_, err := r.Render(context.Background(), []byte("<html></html>"), "REF-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for an unreachable converter, got %v", err)
	}
}

func TestARefusalKeepsTheReason(t *testing.T) {
	// A converter that answered and said no is a different problem from one
	// that is not there, and the caller logs it differently. The reason has
	// to survive or nobody can act on it.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("index.html is malformed"))
		}))
	defer srv.Close()

	_, err := New(srv.URL).Render(context.Background(), []byte("x"), "REF-1")
	if err == nil {
		t.Fatal("a refusal should be an error")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("a refusal is not the same as an absent converter")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("the converter's reason was lost: %v", err)
	}
}

func TestAnEmptyDocumentIsRejected(t *testing.T) {
	// A zero-byte PDF saved to the evidence store would be discovered by a
	// farmer opening it, months later, in a grievance hearing.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()

	_, err := New(srv.URL).Render(context.Background(), []byte("x"), "REF-1")
	if err == nil {
		t.Fatal("an empty conversion should be an error")
	}
}

func TestTheDocumentAndItsFurnitureAreSent(t *testing.T) {
	var got struct {
		files  map[string]string
		fields map[string]string
	}
	got.files = map[string]string{}
	got.fields = map[string]string{}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
			if err != nil {
				t.Errorf("bad content type: %v", err)
				return
			}
			mr := multipart.NewReader(req.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("reading the request: %v", err)
					return
				}
				body, _ := io.ReadAll(part)
				if part.FileName() != "" {
					got.files[part.FileName()] = string(body)
				} else {
					got.fields[part.FormName()] = string(body)
				}
			}
			_, _ = w.Write([]byte("%PDF-1.7 ..."))
		}))
	defer srv.Close()

	out, err := New(srv.URL).Render(context.Background(),
		[]byte("<html>अहवाल</html>"), "ASMT-2026-000431")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "%PDF") {
		t.Errorf("the converted bytes were not returned: %q", string(out))
	}

	for _, name := range []string{"index.html", "header.html", "footer.html"} {
		if _, ok := got.files[name]; !ok {
			t.Errorf("%s was not sent", name)
		}
	}
	if !strings.Contains(got.files["index.html"], "अहवाल") {
		t.Error("the document reached the converter altered")
	}

	// The page count belongs on every page: a record with a page missing is
	// only detectable if the pages say how many there should be.
	if !strings.Contains(got.files["footer.html"], "totalPages") {
		t.Error("the footer carries no page count")
	}
	// And the reference, so a loose page can be put back with its report.
	for _, name := range []string{"header.html", "footer.html"} {
		if !strings.Contains(got.files[name], "ASMT-2026-000431") {
			t.Errorf("%s does not carry the report number", name)
		}
	}

	if got.fields["paperWidth"] != "8.27" || got.fields["paperHeight"] != "11.7" {
		t.Errorf("the page is not A4: %s x %s",
			got.fields["paperWidth"], got.fields["paperHeight"])
	}
	if got.fields["printBackground"] != "true" {
		t.Error("without backgrounds the role markers and rules disappear")
	}
}

func TestTheRunningFurnitureCarriesNoDevanagari(t *testing.T) {
	// Chrome renders print headers in an isolated context that does not
	// reliably pick up the document's fonts. A Devanagari string there is not
	// merely unstyled -- it can vanish, and a page number missing from the
	// copy filed at the taluka office is worse than one with no word in front
	// of it.
	for _, s := range []string{headerHTML("REF-1"), footerHTML("REF-1")} {
		for _, r := range s {
			if r >= 0x0900 && r <= 0x097F {
				t.Errorf("running furniture contains Devanagari: %q", r)
				break
			}
		}
	}
}

func TestArchivalIsRequestedOnlyWhenAsked(t *testing.T) {
	// PDF/A costs a second conversion step. It is what a record kept for
	// years should be, and not what a draft an officer is checking needs.
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			_ = req.ParseMultipartForm(1 << 20)
			for k, v := range req.MultipartForm.Value {
				seen[k] = v[0]
			}
			_, _ = w.Write([]byte("%PDF-1.7"))
		}))
	defer srv.Close()

	r := New(srv.URL)
	if _, err := r.Render(context.Background(), []byte("x"), "REF-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen["pdfa"]; ok {
		t.Error("archival conversion was requested without being asked for")
	}

	r.Archival = true
	if _, err := r.Render(context.Background(), []byte("x"), "REF-1"); err != nil {
		t.Fatal(err)
	}
	if seen["pdfa"] == "" {
		t.Error("archival conversion was asked for and not requested")
	}
}

func TestAReferenceCannotCloseATag(t *testing.T) {
	// References are generated by this system and contain nothing dangerous,
	// which is exactly the assumption that stops being true one schema change
	// from now.
	out := headerHTML(`REF"><script>alert(1)</script>`)
	if strings.Contains(out, "<script>") {
		t.Error("a reference escaped into markup")
	}
}

// TestAgainstARealConverter proves the thing no unit test can: that Marathi
// survives the conversion.
//
// Devanagari failing looks like success everywhere else -- the request is
// accepted, a PDF comes back, and the bytes are the right length. Only
// opening it, or reading the font table as this does, shows whether the
// glyphs are text or boxes.
//
// Off by default: set ESURVEY_PDF_URL to a running converter.
func TestAgainstARealConverter(t *testing.T) {
	base := os.Getenv("ESURVEY_PDF_URL")
	if base == "" {
		t.Skip("set ESURVEY_PDF_URL to a running converter")
	}

	html := []byte(`<html><head><meta charset="utf-8"></head><body>` +
		`<p>शेतातील प्रत्यक्ष मोजणी</p><p>Field measurement</p></body></html>`)

	out, err := New(base).Render(context.Background(), html, "ASMT-TEST")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Fatalf("not a PDF: %q", out[:min(16, len(out))])
	}

	// A Devanagari face must be embedded. Without one Chromium falls back to
	// a font with no coverage and the farmer's half of the report prints as
	// empty boxes -- which is indistinguishable from success at every other
	// layer.
	if !bytes.Contains(out, []byte("Devanagari")) {
		t.Error("no Devanagari font was embedded; the Marathi half will be boxes")
	}
}

// TestWriteSamplePDF converts a rendered report through the real client, so
// the page furniture on the sample is the furniture a farmer would receive.
//
// Set ESURVEY_PDF_URL and ESURVEY_SAMPLE_HTML/ESURVEY_SAMPLE_PDF.
func TestWriteSamplePDF(t *testing.T) {
	base := os.Getenv("ESURVEY_PDF_URL")
	in := os.Getenv("ESURVEY_SAMPLE_HTML")
	out := os.Getenv("ESURVEY_SAMPLE_PDF")
	if base == "" || in == "" || out == "" {
		t.Skip("set ESURVEY_PDF_URL, ESURVEY_SAMPLE_HTML and ESURVEY_SAMPLE_PDF")
	}

	html, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}

	pdf, err := New(base).Render(context.Background(), html, "ASMT-2026-000431")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes)", out, len(pdf))
}
