package api

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
)

// How a question arrives, with files and without.
//
// Two content types on one route: the JSON body that every caller already sends,
// and a multipart body when there are files. The first must be exactly what it was
// -- the console has been sending it since before attachments existed -- and the
// second must refuse what it cannot send before anything reaches the model.

type attached struct {
	field, name, contentType string
	data                     []byte
}

func askForm(t *testing.T, question, history string, files ...attached) *http.Request {
	t.Helper()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if question != "" {
		if err := w.WriteField("question", question); err != nil {
			t.Fatal(err)
		}
	}
	if history != "" {
		if err := w.WriteField("history", history); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{fmt.Sprintf(
			`form-data; name=%q; filename=%q`, f.field, f.name)}
		if f.contentType != "" {
			h["Content-Type"] = []string{f.contentType}
		}
		part, err := w.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/officer/agent/ask", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r
}

// A PDF header is enough: what is under examination here is the route, and the
// classifier has its own tests over real files.
func pdfBytes() []byte {
	return []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n")
}

func TestAQuestionWithNoFilesIsStillTheJSONBodyItAlwaysWas(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/officer/agent/ask",
		strings.NewReader(`{"question":"how many are waiting on me?",
			"history":[{"role":"user","text":"earlier"}]}`))
	r.Header.Set("Content-Type", "application/json")

	req, err := readAskRequest(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatal(err)
	}
	if req.Question != "how many are waiting on me?" {
		t.Errorf("question %q", req.Question)
	}
	if len(req.History) != 1 || req.History[0].Text != "earlier" {
		t.Errorf("history %+v", req.History)
	}
	if len(req.attachments) != 0 || len(req.notes) != 0 {
		t.Error("a JSON question carried attachments; files arrive as multipart")
	}
}

func TestAFileSentInTheHistoryIsDroppedInBothShapesOfRequest(t *testing.T) {
	// The way round the checks, if there were one. Everything that bounds an
	// attachment -- the size cap, the sniffer, the audit record -- happens to a
	// file arriving as an upload, and none of it has anything to inspect in a
	// message a client has composed for itself.
	smuggled := `[{"role":"user","text":"earlier","attachments":[{` +
		`"filename":"payload.png","kind":"image","media_type":"image/png",` +
		`"data_base64":"aGVsbG8=","bytes":5}]}]`

	r := httptest.NewRequest(http.MethodPost, "/api/v1/officer/agent/ask",
		strings.NewReader(`{"question":"hello","history":`+smuggled+`}`))
	r.Header.Set("Content-Type", "application/json")

	for _, req := range []*http.Request{
		r,
		askForm(t, "hello", smuggled),
	} {
		got, err := readAskRequest(httptest.NewRecorder(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.History) != 1 {
			t.Fatalf("history = %d", len(got.History))
		}
		if len(got.History[0].Attachments) != 0 {
			t.Error("a file composed into the history reached the model " +
				"without being sniffed, bounded or audited")
		}
		if got.History[0].Text != "earlier" {
			t.Errorf("the words of the history did not survive: %q",
				got.History[0].Text)
		}
	}
}

func TestAMultipartQuestionCarriesItsFilesAndItsHistory(t *testing.T) {
	r := askForm(t, "what does this circular say about late filing?",
		`[{"role":"user","text":"earlier"}]`,
		attached{field: "file", name: "circular.pdf",
			contentType: "application/pdf", data: pdfBytes()},
		attached{field: "file", name: "figures.csv",
			contentType: "text/csv", data: []byte("Village,Area\nMalegaon,1.6\n")},
	)

	req, err := readAskRequest(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatal(err)
	}
	if req.Question == "" {
		t.Error("the question did not survive the form")
	}
	if len(req.History) != 1 {
		t.Errorf("history %+v", req.History)
	}
	if len(req.attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(req.attachments))
	}
	if req.attachments[0].Kind != officeragent.KindDocument {
		t.Errorf("the PDF reached the model as %q", req.attachments[0].Kind)
	}
	if req.attachments[1].Kind != officeragent.KindText ||
		!strings.Contains(req.attachments[1].Text, "Malegaon\t1.6") {
		t.Errorf("the CSV reached the model as %q: %q",
			req.attachments[1].Kind, req.attachments[1].Text)
	}
}

func TestEveryAttachmentGetsItsOwnLineForTheAuditLog(t *testing.T) {
	// One record per attachment, not one per request: an inquiry asks what was
	// sent, and "three files" is not an answer to that.
	r := askForm(t, "read these", "",
		attached{field: "file", name: "circular.pdf",
			contentType: "application/pdf", data: pdfBytes()},
		attached{field: "file", name: "figures.csv",
			contentType: "text/csv", data: []byte("a,b\n1,2\n")},
	)

	req, err := readAskRequest(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.notes) != len(req.attachments) {
		t.Fatalf("%d audit lines for %d attachments",
			len(req.notes), len(req.attachments))
	}
	for i, note := range req.notes {
		if note.Filename == "" || note.Kind == "" || note.Bytes == 0 {
			t.Errorf("line %d is incomplete: %+v", i, note)
		}
		if note.Resolved == "" {
			t.Errorf("line %d does not say what the bytes turned out to be", i)
		}
		if note.Declared == "" {
			t.Errorf("line %d does not say what the client claimed to send", i)
		}
	}
	if req.notes[0].Declared != "application/pdf" ||
		req.notes[0].Resolved != officeragent.MediaPDF {
		t.Errorf("the PDF line reads %+v", req.notes[0])
	}
}

func TestAFileDisguisedAsAnotherIsRefusedAndStillRecorded(t *testing.T) {
	// The entry an inquiry into this feature would actually be looking for. A
	// refusal that left no trace would be the one thing here nobody can audit.
	r := askForm(t, "read this", "",
		attached{field: "file", name: "scan.png",
			contentType: "image/png", data: pdfBytes()},
	)

	_, err := readAskRequest(httptest.NewRecorder(), r)
	if err == nil {
		t.Fatal("a PDF named .png was accepted")
	}

	var refused *attachmentRefusal
	if !errors.As(err, &refused) {
		t.Fatalf("the refusal carries nothing to record: %v", err)
	}
	if len(refused.notes) != 1 {
		t.Fatalf("%d lines to record", len(refused.notes))
	}
	note := refused.notes[0]
	if note.Filename != "scan.png" || note.Declared != "image/png" ||
		note.Refusal != officeragent.CodeMismatch {
		t.Errorf("the record reads %+v", note)
	}

	var refusal *officeragent.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("no refusal for the officer to read: %v", err)
	}
	if !strings.Contains(refusal.Reason, "scan.png") {
		t.Errorf("the refusal does not name the file: %q", refusal.Reason)
	}
}

func TestASetRefusedForItsSizeIsRecordedFileByFile(t *testing.T) {
	// Refused from the part headers, before a byte is read -- so the names and
	// sizes are all there is to record, and all that is worth recording.
	files := make([]attached, 0, officeragent.MaxAttachments+2)
	for i := 0; i < officeragent.MaxAttachments+2; i++ {
		files = append(files, attached{field: "file",
			name:        fmt.Sprintf("page-%d.pdf", i),
			contentType: "application/pdf", data: pdfBytes()})
	}

	_, err := readAskRequest(httptest.NewRecorder(), askForm(t, "read these", "",
		files...))

	var refused *attachmentRefusal
	if !errors.As(err, &refused) {
		t.Fatalf("the refusal carries nothing to record: %v", err)
	}
	if len(refused.notes) != len(files) {
		t.Fatalf("%d lines for %d files", len(refused.notes), len(files))
	}
	for _, note := range refused.notes {
		if note.Refusal != officeragent.CodeTooMany {
			t.Errorf("%s was recorded as %q", note.Filename, note.Refusal)
		}
	}
}

func TestEachKindOfRefusalGoesOutWithItsOwnStatus(t *testing.T) {
	// The console says different things about each: a file too large can be
	// resent smaller, a file of the wrong kind cannot be resent at all, and a
	// file this could not read may simply be damaged.
	for code, want := range map[string]int{
		officeragent.CodeTooLarge:      http.StatusRequestEntityTooLarge,
		officeragent.CodeTotalTooLarge: http.StatusRequestEntityTooLarge,
		officeragent.CodeUnsupported:   http.StatusUnsupportedMediaType,
		officeragent.CodeMismatch:      http.StatusUnsupportedMediaType,
		officeragent.CodeUnreadable:    http.StatusUnprocessableEntity,
		officeragent.CodeTooMany:       http.StatusBadRequest,
	} {
		if got := statusForRefusal(code); got != want {
			t.Errorf("%s went out as %d, want %d", code, got, want)
		}
	}
}

func TestAFileSentUnderTheWrongFieldNameIsSimplyNotAnAttachment(t *testing.T) {
	// Not an error. A console that posts an unrelated field should get its
	// question answered, not a refusal about a field name no officer chose.
	req, err := readAskRequest(httptest.NewRecorder(), askForm(t, "hello", "",
		attached{field: "something_else", name: "circular.pdf",
			contentType: "application/pdf", data: pdfBytes()},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.attachments) != 0 {
		t.Errorf("attachments = %d", len(req.attachments))
	}
	if req.Question != "hello" {
		t.Errorf("question %q", req.Question)
	}
}

func TestALongConversationIsTrimmedInBothShapesOfRequest(t *testing.T) {
	// Trimmed to the most recent rather than refused: an officer in a long
	// conversation should not be stopped, and the older half is the part they have
	// stopped referring to.
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < maxAskHistory+5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"role":"user","text":"turn %d"}`, i)
	}
	b.WriteString("]")

	req, err := readAskRequest(httptest.NewRecorder(),
		askForm(t, "and now?", b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.History) != maxAskHistory {
		t.Fatalf("history = %d, want %d", len(req.History), maxAskHistory)
	}
	if req.History[len(req.History)-1].Text != fmt.Sprintf("turn %d",
		maxAskHistory+4) {
		t.Error("the wrong end of the conversation was kept")
	}
}
