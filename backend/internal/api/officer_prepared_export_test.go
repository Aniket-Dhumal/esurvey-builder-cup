package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Downloading the document the assistant prepared while answering.
//
// What belongs to this layer is who may have it and what the browser is told.
// The store's own limits -- the lifetime, the caps, the owner in the key -- are
// tested in officeragent against the same policy this route calls, and the
// documents themselves in its export tests. So nothing here needs a database or
// a Redis, and nothing here skips.

func preparedTestServer(store *officeragent.Exports) *Server {
	return &Server{
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		PreparedExports: store,
	}
}

// preparedFixture files one document under one officer and hands back its id.
func preparedFixture(t *testing.T, owner string) (*officeragent.Exports, string) {
	t.Helper()
	store := officeragent.NewMemoryExports()
	p, err := store.Keep(context.Background(), owner, officeragent.FormatXLSX,
		&officeragent.Export{
			Bytes:       []byte("PK\x03\x04 a workbook"),
			ContentType: officeragent.ContentType(officeragent.FormatXLSX),
			Filename:    "बार्शी \"pending\" claims.xlsx",
			Rows:        42,
		})
	if err != nil {
		t.Fatal(err)
	}
	return store, p.ID
}

// downloadRequest asks for a prepared export as the given principal, through the
// part of the handler that is settled before a byte is written.
func downloadRequest(
	t *testing.T, s *Server, p *rbac.Principal, id string,
) (*httptest.ResponseRecorder, *officeragent.PreparedFile) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/officer/agent/export/"+id, nil)
	r.SetPathValue("id", id)
	if p != nil {
		r = r.WithContext(rbac.WithPrincipal(r.Context(), p))
	}
	rec := httptest.NewRecorder()
	return rec, s.preparedExportFor(rec, r)
}

func officerPrincipal(id string) *rbac.Principal {
	return &rbac.Principal{UserID: id, Kind: rbac.KindOfficer}
}

func TestAPreparedExportIsHandedToTheOfficerWhoPreparedIt(t *testing.T) {
	store, id := preparedFixture(t, "officer-1")

	_, file := downloadRequest(t,
		preparedTestServer(store), officerPrincipal("officer-1"), id)
	if file == nil {
		t.Fatal("the officer who prepared it was refused their own document")
	}
	if string(file.Body) != "PK\x03\x04 a workbook" {
		t.Errorf("the bytes came back as %q", file.Body)
	}
	if file.Rows != 42 || file.Format != "xlsx" {
		t.Errorf("what the console is told reads %+v", file.Prepared)
	}

	// And the name the browser is given is safe, by the same handling the export
	// route's filename gets: this one carries a quote and Devanagari.
	header := contentDisposition(file.Filename)
	if strings.Count(header, `"`) != 2 || strings.ContainsAny(header, "\r\n") {
		t.Errorf("Content-Disposition reads %q", header)
	}
	if !strings.Contains(header, "filename*=UTF-8''") {
		t.Errorf("a Marathi name cannot survive this header: %q", header)
	}
}

func TestAPreparedExportIsNotHandedToADifferentOfficer(t *testing.T) {
	// The id alone opens nothing: the store composes its key from the user id of
	// whoever is asking, so another officer reads a key that was never written.
	// Not found rather than forbidden, deliberately -- the other answer would
	// tell them a document exists that is not theirs.
	store, id := preparedFixture(t, "officer-1")

	rec, file := downloadRequest(t,
		preparedTestServer(store), officerPrincipal("officer-2"), id)
	if file != nil {
		t.Fatal("another officer was handed the document")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("another officer got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "NO_PREPARED_EXPORT") {
		t.Errorf("body %s", rec.Body.String())
	}
}

func TestAnIdForNothingIsAnOrdinaryFourOhFour(t *testing.T) {
	// Expired, evicted, never existed and somebody else's all answer the same.
	store, _ := preparedFixture(t, "officer-1")
	s := preparedTestServer(store)

	for _, id := range []string{
		"0123456789abcdef0123456789abcdef", "", "not-an-id", "../../etc/passwd",
	} {
		rec, file := downloadRequest(t, s, officerPrincipal("officer-1"), id)
		if file != nil {
			t.Fatalf("%q produced a document", id)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%q answered %d", id, rec.Code)
		}
	}
}

func TestAnAnonymousDownloadOfAPreparedExportIsRefused(t *testing.T) {
	store, id := preparedFixture(t, "officer-1")

	rec, file := downloadRequest(t, preparedTestServer(store), nil, id)
	if file != nil {
		t.Fatal("an anonymous caller was handed a document")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous download got %d", rec.Code)
	}
}

func TestAPreparedExportIsRefusedToAFarmersHandsetEvenThoughItHoldsReportRead(t *testing.T) {
	// A farmer holds report:read for their own claims, so the route guard alone
	// does not keep a handset out. Nothing here prepares a document for a farmer
	// in the first place -- see exportTools -- and this is the other half of
	// that.
	store, id := preparedFixture(t, "officer-1")

	rec, file := downloadRequest(t, preparedTestServer(store),
		&rbac.Principal{UserID: "u9", Kind: rbac.KindFarmer, FarmerID: "F1"}, id)
	if file != nil {
		t.Fatal("a handset was handed an office document")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a handset got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OFFICER_ONLY") {
		t.Errorf("body %s", rec.Body.String())
	}
}

func TestADeploymentWithNoPreparedStoreSaysSoRatherThanNotFound(t *testing.T) {
	// A console may be offering a button this deployment cannot honour, and
	// "not found" would have the officer trying again.
	rec, file := downloadRequest(t, preparedTestServer(nil),
		officerPrincipal("officer-1"), "0123456789abcdef0123456789abcdef")
	if file != nil {
		t.Fatal("a document came from nowhere")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("answered %d", rec.Code)
	}
}

func TestTheToolsThatProduceADocumentAreOfferedOnlyWhenTheyCanWork(t *testing.T) {
	// Who is offered a file, decided before the model is asked anything: an
	// officer with somewhere to put a document, and nobody else. A farmer is
	// refused here rather than by the tool, for the reason exportAgentData
	// refuses one.
	store, _ := preparedFixture(t, "officer-1")
	r := httptest.NewRequest(http.MethodPost, "/api/v1/officer/agent/ask", nil)

	withStore := preparedTestServer(store)
	if tools := withStore.exportTools(
		r, officerPrincipal("officer-1"), &askRequest{},
	); len(tools) != 1 {
		t.Errorf("an officer was offered %d tools, want export_claims alone",
			len(tools))
	}
	if tools := withStore.exportTools(r,
		&rbac.Principal{UserID: "u9", Kind: rbac.KindFarmer, FarmerID: "F1"},
		&askRequest{}); tools != nil {
		t.Errorf("a farmer was offered %d export tools", len(tools))
	}
	if tools := preparedTestServer(nil).exportTools(
		r, officerPrincipal("officer-1"), &askRequest{},
	); tools != nil {
		t.Errorf("tools were offered with nowhere to put a document: %d",
			len(tools))
	}
}

func TestAnAttachedWorkbookKeepsItsBytesForTheFillerWithoutSendingThemToTheModel(t *testing.T) {
	// The two halves of an attached format go to different places. The model is
	// shown the table read out of it, which is what it can reason about; the
	// filler needs the file itself, to write into the office's own layout. A
	// workbook on the Attachment would be a workbook sent to the model for
	// nothing, on every turn of the conversation.
	req, err := readAskRequest(httptest.NewRecorder(), askForm(t,
		"fill our format with the pending ones", "",
		attached{field: "file", name: "barshi.xlsx", data: officeFormatBytes(t)},
	))
	if err != nil {
		t.Fatal(err)
	}

	if len(req.attachments) != 1 {
		t.Fatalf("attachments = %d", len(req.attachments))
	}
	if req.attachments[0].Kind != officeragent.KindText ||
		len(req.attachments[0].Data) != 0 {
		t.Errorf("the workbook itself was prepared for the model: kind %q, "+
			"%d bytes", req.attachments[0].Kind, len(req.attachments[0].Data))
	}
	if len(req.formats) != 1 {
		t.Fatalf("formats kept for the filler = %d", len(req.formats))
	}
	if req.formats[0].Filename != "barshi.xlsx" || len(req.formats[0].Data) == 0 {
		t.Errorf("the format kept reads %q, %d bytes",
			req.formats[0].Filename, len(req.formats[0].Data))
	}

	// And with one attached, the filler is offered.
	store, _ := preparedFixture(t, "officer-1")
	r := httptest.NewRequest(http.MethodPost, "/api/v1/officer/agent/ask", nil)
	s := preparedTestServer(store)
	// Mapper is the agent itself, which this server has not got, so the filler
	// stays off: there would be nothing to read the headings with.
	if tools := s.exportTools(r, officerPrincipal("officer-1"), req); len(
		tools) != 1 {
		t.Errorf("tools offered with no assistant to map headings: %d",
			len(tools))
	}
}

// officeFormatBytes is a real workbook, because the classifier opens one: the
// bytes have to sniff as an archive and then read as a spreadsheet, which is the
// whole of how a .xlsx is told from anything else named .xlsx.
func officeFormatBytes(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()

	for ref, value := range map[string]string{
		"A1": "Sr.", "B1": "Claim No.", "C1": "Farmer", "D1": "Village",
	} {
		if err := f.SetCellValue("Sheet1", ref, value); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
