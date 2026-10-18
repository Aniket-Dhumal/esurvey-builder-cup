package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// askAgent answers an officer's question about their own claims.
//
// # Why this is gated on report:read and nothing more
//
// Because that is all it does. The agent has no tool that writes, so it can reach
// exactly what a GET on the report routes would reach, through the same store calls
// with the same scope. Asking for a higher permission would imply it can do more.
//
// An officer who cannot read reports gets the ordinary 403 from the middleware
// before any of this runs, which is the right answer: there is nothing to talk
// about.
//
// A file attached to the question does not change that. It is material the officer
// is showing the model, and the model has no more power with it than without it.
func (s *Server) askAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in first")
		return
	}

	if s.Agent == nil {
		// Checked before the body is read, so that an officer whose assistant is
		// not configured is told so without first sending sixteen megabytes of
		// scan up a district line.
		writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE",
			"the assistant is not configured")
		return
	}

	req, err := readAskRequest(w, r)
	if err != nil {
		// The attempt is recorded before it is refused. A file disguised as
		// another kind of file, or one too large to send, is exactly the entry an
		// inquiry into this feature would be looking for, and a refusal that left
		// no trace would be the one thing here that nobody can audit.
		var refused *attachmentRefusal
		if errors.As(err, &refused) {
			for _, note := range refused.notes {
				s.recordAttachment(ctx, r, note, audit.OutcomeDeny)
			}
		}
		var refusal *officeragent.Refusal
		if errors.As(err, &refusal) {
			writeError(w, statusForRefusal(refusal.Code),
				refusal.Code, refusal.Reason)
			return
		}
		writeError(w, http.StatusBadRequest, "BAD_REQUEST",
			"the question could not be read")
		return
	}
	if req.Question == "" {
		writeError(w, http.StatusBadRequest, "NO_QUESTION", "ask something")
		return
	}

	// One record per attachment, written before the model is called rather than
	// after it answers. The auditable act is an officer putting a file in front of
	// a model that runs on somebody else's computers, and that has happened by
	// now, whether or not an answer comes back.
	//
	// What the file contained is not recorded, for the same reason the question
	// itself is not: the audit log is read by more people and kept longer than the
	// conversation. The name, the type, the size and the request id are what an
	// auditor needs, and the request id is what ties these records to the one
	// question they were attached to.
	for _, note := range req.notes {
		s.recordAttachment(ctx, r, note, audit.OutcomeAllow)
	}

	// The tools that produce a document, offered for the span of this one
	// question. See exportTools for why they cannot be wired in at startup like
	// the read-only ones.
	agent := s.Agent
	if tools := s.exportTools(r, p, req); len(tools) > 0 {
		agent = agent.WithTools(tools...)
	}

	answer, err := agent.Ask(ctx, p.Scope(), req.Question, req.History,
		req.attachments)
	if err != nil {
		if errors.Is(err, officeragent.ErrNoModel) {
			// Said as unavailability, not as refusal. The question was fine; the
			// model is not there.
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE",
				"the assistant could not be reached")
			return
		}
		writeError(w, http.StatusInternalServerError, "AGENT_FAILED",
			"the assistant failed")
		return
	}

	// Recorded like any other read of a district's claims.
	//
	// What was asked is not recorded, and deliberately: the question can quote a
	// farmer, and the audit log is read by more people and kept longer than the
	// conversation. That a particular officer used the assistant, and how many
	// steps it took, is what an auditor needs.
	s.Audit.Record(ctx, audit.Event{
		Action: "officer.agent.ask", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "officer_agent",
		Detail: map[string]any{
			"turns": answer.Turns, "steps": len(answer.Steps),
			"attachments": len(req.attachments),
			// How many documents left the building with this answer. Each has a
			// record of its own naming the format, the rows and the id; this is
			// so that the one entry an auditor finds first says there were any.
			"prepared": len(answer.PreparedExports),
		},
	}.FromContext(ctx))

	// The marker the model uses to propose an action is machinery, and is taken
	// off before the officer reads the reply.
	answer.Text = officeragent.StripProposal(answer.Text)

	writeJSON(w, http.StatusOK, answer)
}

// exportTools are the tools that turn this question into a document.
//
// # Why they are built here and not at startup
//
// Because two of the things they need belong to the question. The asking
// officer's user id is what a prepared document is filed under, and therefore
// what decides who can download it; and an .xlsx attached to this question is
// the only thing fill_attached_format has to fill. An agent shared by every
// request in the process can hold neither.
//
// # Why a farmer is not offered them
//
// For the reason exportAgentData refuses one: a statement of a jurisdiction's
// claims is an office artefact, and a farmer's own claim already prints from the
// claim itself. A handset holds report:read, so the route guard alone does not
// decide this.
//
// Returning nothing is a complete answer. The assistant then replies in prose,
// as it did before this existed, and is not told about a tool it has not got.
func (s *Server) exportTools(
	r *http.Request, p *rbac.Principal, req *askRequest,
) []officeragent.Tool {
	if s.PreparedExports == nil || p.FarmerID != "" {
		return nil
	}

	// The PDF converter where there is one, passed rather than reached for, so
	// that a document prepared in a conversation is converted by the same
	// Chromium service the printed report already goes through.
	var conv officeragent.PDFConverter
	if s.PDF != nil && s.PDF.Available() {
		conv = s.PDF
	}

	// The agent itself reads the headings of an attached format, in a turn of its
	// own that is shown the headings and nothing else -- see
	// officeragent/fields.go for why no claim travels to fill a format.
	//
	// Assigned through a variable rather than inline because an unset *Agent put
	// straight into an interface field is not nil. It is an interface holding a
	// nil pointer, which passes the check ExportTools makes before offering the
	// filler and then panics on the first call. askAgent has already refused a
	// question when there is no assistant, so this guards the next caller rather
	// than this one.
	var mapper officeragent.HeadingMapper
	if s.Agent != nil {
		mapper = s.Agent
	}

	return officeragent.ExportTools{
		Reports: s.Reports,
		Store:   s.PreparedExports,
		PDF:     conv,
		Owner:   p.UserID,
		Formats: req.formats,
		Mapper:  mapper,
		Record: func(
			ctx context.Context, prepared officeragent.Prepared, via string,
		) {
			// One record per document, written where it was prepared rather than
			// where it is downloaded: the officer has had the figures the moment
			// this runs, whether or not they ever press the button.
			s.Audit.Record(ctx, audit.Event{
				Action:       "officer.agent.export_prepared",
				Outcome:      audit.OutcomeAllow,
				ActorIP:      clientIP(r, s.TrustedProxyHops),
				RequestID:    RequestID(ctx),
				ResourceType: "officer_agent",
				ResourceID:   prepared.ID,
				Detail: map[string]any{
					"format": prepared.Format,
					"rows":   prepared.Rows,
					"bytes":  prepared.Bytes,
					"via":    via,
				},
			}.FromContext(ctx))
		},
	}.Tools()
}

// askRequest is one question as it arrived.
type askRequest struct {
	Question string
	History  []officeragent.Message

	// attachments is what will go to the model, prepared.
	attachments []officeragent.Attachment

	// formats are the attached .xlsx workbooks, with their bytes.
	//
	// Kept beside the attachments rather than inside them, because the two go to
	// different places: the attachment is the table read out of the workbook,
	// which is what the model is shown, and this is the file itself, which only
	// the filler touches. Putting the bytes on the Attachment would send a
	// whole workbook to the model for nothing.
	formats []officeragent.AttachedFormat

	// notes is one line per attachment for the audit log, in the same order.
	// Kept beside the attachments rather than inside them because what the client
	// declared the type to be is worth recording and is not worth sending.
	notes []attachmentNote
}

// attachmentNote is what the audit log is told about one file.
type attachmentNote struct {
	Filename string
	Declared string
	Resolved string
	Kind     string
	Bytes    int64
	Refusal  string
}

// attachmentRefusal carries the files a refusal was about, so that the attempt can
// be recorded before it is answered.
type attachmentRefusal struct {
	notes []attachmentNote
	err   error
}

func (e *attachmentRefusal) Error() string { return e.err.Error() }
func (e *attachmentRefusal) Unwrap() error { return e.err }

// maxAskJSONBytes bounds a question with no files attached.
//
// A conversation is resent whole on every turn, so an unbounded history is an
// unbounded prompt, paid for per token on every question.
const maxAskJSONBytes = 64 << 10

// askFormOverheadBytes is the room left for everything in a multipart body that is
// not an attachment: the question, the history, and the part headers between them.
const askFormOverheadBytes = 1 << 20

// maxAskHistory bounds how much of a conversation is resent.
const maxAskHistory = 20

// readAskRequest reads a question, with or without files attached.
//
// # Why attachments arrive as multipart and not as base64 in the JSON body
//
// Because base64 costs a third on the way up, and the way up is a district
// office's line. An officer attaching an eight megabyte scan would send eleven,
// and they are already waiting.
//
// It also keeps the bound honest. The JSON body is capped at sixty-four kilobytes
// -- which is what makes an unbounded history impossible -- and admitting
// base64-encoded files into it would mean raising that cap to twenty-two megabytes
// for every question, including the overwhelming majority that attach nothing. A
// multipart body is bounded as a whole and refused as it is read, so an officer who
// attaches a two-hundred-page scan is told before it has all arrived.
//
// And it costs the console nothing: a browser builds a multipart body from a file
// input without being asked. So the JSON shape is exactly what it was, every
// existing caller keeps working, and files are a second content type on the same
// route rather than a new route.
func readAskRequest(w http.ResponseWriter, r *http.Request) (*askRequest, error) {
	if mt := r.Header.Get("Content-Type"); strings.HasPrefix(mt, "multipart/") {
		return readAskForm(w, r)
	}

	var body struct {
		Question string                 `json:"question"`
		History  []officeragent.Message `json:"history,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAskJSONBytes)).
		Decode(&body); err != nil {
		return nil, err
	}
	return &askRequest{
		Question: strings.TrimSpace(body.Question),
		History:  trimHistory(body.History),
	}, nil
}

// readAskForm reads a question with files attached.
func readAskForm(w http.ResponseWriter, r *http.Request) (*askRequest, error) {
	// Bounded before anything is parsed. ParseMultipartForm reads the whole body
	// whatever it is told, spilling past its memory budget into temporary files,
	// so the cap has to be on the body itself.
	r.Body = http.MaxBytesReader(w, r.Body,
		officeragent.MaxAttachmentsTotalBytes+askFormOverheadBytes)

	// The memory budget is the whole allowance, which keeps the attachments out of
	// temporary files. A scanned panchnama left in a spool directory is a copy of
	// somebody's claim lying about on a disk that nothing is watching, for no gain
	// -- the bytes are bounded, and what is bounded can be held.
	if err := r.ParseMultipartForm(officeragent.MaxAttachmentsTotalBytes); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, &attachmentRefusal{err: &officeragent.Refusal{
				Code: officeragent.CodeTotalTooLarge,
				Reason: "that question and its attachments came to more than one " +
					"question may carry. Attach fewer files, or smaller ones.",
			}}
		}
		return nil, err
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}

	out := &askRequest{Question: strings.TrimSpace(r.FormValue("question"))}

	if raw := strings.TrimSpace(r.FormValue("history")); raw != "" {
		// The same history the JSON shape carries, as a JSON array in a form
		// field. A second encoding of the conversation would be a second thing to
		// keep in step with the Go struct.
		//
		// Held to the same sixty-four kilobytes the JSON body is, and for the same
		// reason: the conversation is resent to the model on every turn, so this
		// field is the one part of a multipart request whose size is paid for over
		// and over. A megabyte of history is a quarter of a million tokens a
		// question. Refused rather than trimmed, because a history that large is a
		// console sending something it should not and not an officer doing
		// anything.
		if len(raw) > maxAskJSONBytes {
			return nil, errors.New("the history is longer than a conversation")
		}
		var history []officeragent.Message
		if err := json.Unmarshal([]byte(raw), &history); err != nil {
			return nil, err
		}
		out.History = trimHistory(history)
	}

	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		return out, nil
	}

	// The bounds on the set come first, from the part headers, before a single
	// file is read. Reading twenty workbooks in order to say there were too many
	// of them is work done for an answer already known -- and when the set is
	// refused, the names and sizes of all of it are what the audit record should
	// carry.
	var total int64
	notes := make([]attachmentNote, 0, len(files))
	for _, h := range files {
		notes = append(notes, attachmentNote{
			Filename: h.Filename,
			Declared: h.Header.Get("Content-Type"),
			Bytes:    h.Size,
		})
		total += h.Size
	}
	if err := officeragent.CheckAttachmentSet(len(files), total); err != nil {
		return nil, refuseAll(notes, err)
	}

	for i, h := range files {
		if h.Size > officeragent.MaxAttachmentBytes {
			// Refused on the declared size, before the bytes are read. The size
			// is checked again below against what was actually read, because a
			// part header is the client speaking.
			return nil, refuseOne(notes[i],
				officeragent.TooLargeRefusal(h.Filename, h.Size))
		}
		data, err := readPart(h)
		if err != nil {
			return nil, refuseOne(notes[i], err)
		}
		att, err := officeragent.ClassifyAttachment(h.Filename, data)
		if err != nil {
			return nil, refuseOne(notes[i], err)
		}
		notes[i].Filename = att.Filename
		notes[i].Resolved = att.MediaType
		notes[i].Kind = att.Kind
		notes[i].Bytes = int64(att.Bytes)
		out.attachments = append(out.attachments, att)

		if att.MediaType == officeragent.MediaXLSX {
			// The workbook's own bytes, kept for the filler. The classifier has
			// already opened it and read the cells out as the table the model
			// sees; this is the file, which is what has to be written back into
			// if the officer asks for their format filled. It costs nothing to
			// keep -- the bytes are already read, already bounded, and already
			// in this process's memory.
			out.formats = append(out.formats, officeragent.AttachedFormat{
				Filename: att.Filename, Data: data,
			})
		}
	}
	out.notes = notes
	return out, nil
}

// readPart reads one attachment, and no more of it than is allowed.
//
// A byte past the cap is read deliberately: the limit reader would otherwise hand
// back exactly the maximum and the file would be classified as though it had been
// read whole, which for a truncated scan means sending the model half a document
// and telling it nothing.
func readPart(h *multipart.FileHeader) ([]byte, error) {
	f, err := h.Open()
	if err != nil {
		return nil, &officeragent.Refusal{Code: officeragent.CodeUnreadable,
			Reason: "that attachment could not be read."}
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, officeragent.MaxAttachmentBytes+1))
	if err != nil {
		return nil, &officeragent.Refusal{Code: officeragent.CodeUnreadable,
			Reason: "that attachment could not be read. The upload may have " +
				"been interrupted; try attaching it again."}
	}
	return data, nil
}

func refuseOne(note attachmentNote, err error) error {
	return refuseAll([]attachmentNote{note}, err)
}

func refuseAll(notes []attachmentNote, err error) error {
	var refusal *officeragent.Refusal
	if errors.As(err, &refusal) {
		for i := range notes {
			notes[i].Refusal = refusal.Code
		}
	}
	return &attachmentRefusal{notes: notes, err: err}
}

// trimHistory bounds the conversation and takes the files out of it.
//
// The bound first: trimmed to the most recent rather than refused, because an
// officer in a long conversation should not be stopped, and the older half of it is
// the part they have stopped referring to.
//
// Then the files, which are dropped from every message whatever they are. History
// is words -- what was asked and what was answered -- and an attachment arriving in
// it is one of two things. Either a console is replaying an upload, which costs the
// officer their line to send the model something it has already read; or it is a
// file trying to reach the model without passing the size cap, the sniffer or the
// audit log, all of which happen on the way in and none of which have anything to
// check here. An attachment is only ever something attached to the question being
// asked now.
func trimHistory(history []officeragent.Message) []officeragent.Message {
	if len(history) > maxAskHistory {
		history = history[len(history)-maxAskHistory:]
	}
	out := make([]officeragent.Message, len(history))
	for i, m := range history {
		m.Attachments = nil
		out[i] = m
	}
	return out
}

// recordAttachment writes the one audit entry an attachment gets.
//
// Both the type the client declared and the type the bytes turned out to be, since
// a disagreement between them is the only evidence that somebody tried.
func (s *Server) recordAttachment(
	ctx context.Context, r *http.Request, note attachmentNote, outcome string,
) {
	detail := map[string]any{
		"filename":   note.Filename,
		"bytes":      note.Bytes,
		"declared":   note.Declared,
		"media_type": note.Resolved,
		"kind":       note.Kind,
	}
	if note.Refusal != "" {
		detail["refused"] = note.Refusal
	}
	s.Audit.Record(ctx, audit.Event{
		Action: "officer.agent.attachment", Outcome: outcome,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "officer_agent",
		Detail:       detail,
	}.FromContext(ctx))
}

// statusForRefusal is the status an attachment refusal goes out with.
//
// Named by what went wrong rather than collapsed into one code, because the console
// says different things about each: a file too large can be resent smaller, a file
// of the wrong kind cannot be resent at all, and a file this could not read may
// simply be damaged.
func statusForRefusal(code string) int {
	switch code {
	case officeragent.CodeTooLarge, officeragent.CodeTotalTooLarge:
		return http.StatusRequestEntityTooLarge
	case officeragent.CodeUnsupported, officeragent.CodeMismatch:
		return http.StatusUnsupportedMediaType
	case officeragent.CodeUnreadable:
		return http.StatusUnprocessableEntity
	}
	// A count is not a size, whatever the status named for sizes is called.
	return http.StatusBadRequest
}
