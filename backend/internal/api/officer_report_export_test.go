package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The parts of an export that belong to the HTTP layer.
//
// The documents themselves are tested in officeragent, against a fake store and
// no database. What is left here is what only this layer does: the download
// header, and the two refusals that happen before a single claim is read. Both of
// those run without a store, so nothing in this file skips.

func exportTestServer() *Server {
	return &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func exportRequest(t *testing.T, p *rbac.Principal, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/officer/agent/export",
		strings.NewReader(string(raw)))
	r = r.WithContext(rbac.WithPrincipal(r.Context(), p))

	rec := httptest.NewRecorder()
	exportTestServer().exportAgentData(rec, r)
	return rec
}

func TestAnExportInAFormatThisDoesNotProduceIsRefusedWithTheListOfOnesItDoes(t *testing.T) {
	// Refused before any claim is read, and with the list in the message: an
	// officer who typed "xls" needs to be told what to type instead, and a
	// console should get the list from the server rather than from a comment.
	officer := &rbac.Principal{UserID: "u1", Kind: rbac.KindOfficer}

	for _, asked := range []string{"xls", "odt", "html", "", "exe"} {
		rec := exportRequest(t, officer, map[string]any{"format": asked})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q came back %d", asked, rec.Code)
		}
		body := rec.Body.String()
		for _, f := range officeragent.ExportFormats {
			if !strings.Contains(body, string(f)) {
				t.Errorf("refusing %q did not name %s: %s", asked, f, body)
			}
		}
	}
}

func TestAnExportIsRefusedToAFarmersHandsetEvenThoughItHoldsReportRead(t *testing.T) {
	// A farmer holds report:read for their own claims, so the route guard alone
	// does not keep a handset out. This document is an office artefact -- a
	// statement of a jurisdiction's claims in a format a taluka circulates -- and
	// a farmer's own claim is printed from the claim itself.
	rec := exportRequest(t,
		&rbac.Principal{UserID: "u2", Kind: rbac.KindFarmer, FarmerID: "F1"},
		map[string]any{"format": "xlsx"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a handset got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OFFICER_ONLY") {
		t.Errorf("body %s", rec.Body.String())
	}
}

func TestAnAnonymousExportIsRefusedBeforeAnythingIsRead(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/officer/agent/export",
		strings.NewReader(`{"format":"csv"}`))
	rec := httptest.NewRecorder()
	exportTestServer().exportAgentData(rec, r)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous export got %d", rec.Code)
	}
}

func TestAFilenameWithAQuoteANewlineAndDevanagariProducesASafeHeader(t *testing.T) {
	// The filename is built from a title the caller sent, and a
	// Content-Disposition is a header. A quote closes the quoted string and
	// everything after it becomes header syntax; a carriage return and a newline
	// end the header and begin another one, which is a caller choosing what the
	// browser is told.
	got := contentDisposition(
		"बार्शी \"pending\"\r\nX-Injected: yes\nclaims.xlsx")

	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("the header carries a line break: %q", got)
	}
	if strings.Contains(got, "X-Injected:") {
		t.Fatalf("an injected header name survived: %q", got)
	}
	// Two quotes exactly: the pair around the ASCII fallback, and none from the
	// title. A third would mean the quoted string closed early and the rest of
	// the name became header syntax.
	if n := strings.Count(got, `"`); n != 2 {
		t.Fatalf("%d quote characters in %q", n, got)
	}
	if !strings.HasPrefix(got, "attachment; filename=\"") {
		t.Fatalf("header %q", got)
	}
	if !strings.Contains(got, "filename*=UTF-8''") {
		t.Fatalf("no UTF-8 form, so a Marathi name cannot survive: %q", got)
	}
	if !strings.HasSuffix(got, ".xlsx") && !strings.Contains(got, ".xlsx") {
		t.Fatalf("the extension was lost: %q", got)
	}
	// The Marathi survives, percent-encoded, in the form made for it.
	if !strings.Contains(got, "%E0%A4%AC") {
		t.Fatalf("the Devanagari did not survive into the UTF-8 form: %q", got)
	}
	// And a browser that only understands the ASCII form still gets something
	// shaped like a filename.
	if !strings.Contains(got, "pending") {
		t.Fatalf("the readable part of the name was lost: %q", got)
	}
}

func TestADownloadNameIsNeverEmptyAndNeverAPath(t *testing.T) {
	// A name made entirely of characters that are removed would otherwise
	// produce filename="" -- which some browsers save as the URL's last segment
	// and others refuse outright.
	for _, name := range []string{
		`"`, `/`, `\`, `../../etc/passwd`, ``, `   `, "\x00\x01",
		`..\..\windows\system32`, `;;;`,
	} {
		got := contentDisposition(name)
		if strings.Contains(got, `filename=""`) {
			t.Errorf("%q produced an empty filename: %q", name, got)
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("%q produced a line break: %q", name, got)
		}
		// The separators are what make a path a path. Whatever words were
		// between them are left alone: a flat name reading "etcpasswd" is
		// harmless, and nothing here is ever opened by this name anyway.
		for _, bad := range []string{"/", `\`, ".."} {
			if strings.Contains(got, bad) {
				t.Errorf("%q left %q in the header: %q", name, bad, got)
			}
		}
		// Two semicolons exactly: the header's own two parameter separators,
		// and none from the name, which would otherwise begin a third.
		if n := strings.Count(got, ";"); n != 2 {
			t.Errorf("%q produced %d semicolons: %q", name, n, got)
		}
	}
}

func TestADownloadNameKeepsItsExtensionAndNothingElseGetsOne(t *testing.T) {
	// The extension is the one part of a filename an operating system acts on, so
	// only letters and digits survive in it -- and a title with a dot in the
	// middle must not be read as having one.
	for name, want := range map[string]string{
		"Barshi claims.xlsx":  ".xlsx",
		"Barshi claims.csv":   ".csv",
		"Barshi claims.pptx":  ".pptx",
		"Barshi v1.2 claims":  "",
		"Barshi claims.x sx":  "",
		"Barshi claims.<sh>":  "",
		"Barshi claims.PDF":   ".pdf",
		"Barshi 2026 claims.": "",
	} {
		got := contentDisposition(name)
		if want == "" {
			if strings.Contains(got, `."`) {
				t.Errorf("%q gained an extension: %q", name, got)
			}
			continue
		}
		if !strings.Contains(got, want+`"`) {
			t.Errorf("%q lost its %s: %q", name, want, got)
		}
	}
}
