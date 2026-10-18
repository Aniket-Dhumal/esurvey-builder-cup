package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// exportAgentData returns an officer's own claims as the document they asked for.
//
// # Why this is not the fill endpoint
//
// The fill endpoint answers an office that already has a format: their workbook
// goes up, their layout comes back with figures in it, and it refuses anything but
// .xlsx because a PDF is a layout rather than a form and cannot be written back
// into. That reasoning is about editing somebody else's document and it still
// stands.
//
// This answers the officer who has no format and just asked the assistant for the
// figures. Nothing is being written back into, so the question of what can be
// produced is a different question with a different answer: a workbook, a CSV, a
// PDF, a Word document or a deck, each built from our own data.
//
// # What limits what it can contain
//
// The caller's own rbac.Scope, and nothing else is possible. The request carries a
// format, a choice of columns out of the closed vocabulary in officeragent/fields.go
// and the same three filters the claim list takes -- and no claim, no report id and
// no scope. The rows are read inside officeragent.BuildExport by RowsFor, from the
// scope passed here, through the store call the console's own list uses. So an
// export cannot carry a claim this officer could not already open, because there is
// no parameter through which one could arrive.
//
// farmer_id and Aadhaar are absent from that vocabulary and stay absent. The
// document is rendered server-side from fields the officer may already read, which
// is why it may legitimately name a farmer -- but only the fields fields.go admits.
//
// # Why it needs no model
//
// Because the assistant's part is over by the time this is called. The model may
// have helped the officer decide which columns they wanted; the columns arrive here
// as names, and the figures are read and written by this service. An export works
// on a deployment with no AI service configured at all, which is the right
// behaviour for a download button.
//
// Nothing here writes. An export reads claims and produces bytes: no claim changes
// state, no figure changes, and the approval ladder is untouched. See
// officeragent/agent.go for why that boundary is structural rather than promised.
func (s *Server) exportAgentData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in first")
		return
	}

	// A farmer holds report:read for their own claims, so the route guard alone
	// does not keep a handset out. Refused here because this document is an
	// office artefact -- a statement of a jurisdiction's claims, in a format a
	// taluka circulates -- and a farmer's own claim already has a printed form of
	// its own on the report routes.
	if p.FarmerID != "" {
		writeError(w, http.StatusForbidden, "OFFICER_ONLY",
			"a farmer's own claim is printed from the claim itself")
		return
	}

	var req struct {
		Format string   `json:"format"`
		Fields []string `json:"fields,omitempty"`
		Title  string   `json:"title,omitempty"`
		Note   string   `json:"note,omitempty"`

		EventID       string `json:"event_id,omitempty"`
		State         string `json:"state,omitempty"`
		AwaitingLevel string `json:"awaiting_level,omitempty"`
		Search        string `json:"search,omitempty"`
	}
	// Bounded, and generously: this body is a format, a dozen column names and a
	// drafted line of prose. Anything larger is a caller trying to send rows,
	// which this deliberately has nowhere to put.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).
		Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST",
			"the export request could not be read")
		return
	}

	format, err := officeragent.ParseFormat(req.Format)
	if err != nil {
		// Named, with the list: an officer who typed "xls" or "sheet" needs to be
		// told what to type instead, and a console wiring this up should get the
		// list from the server rather than from a comment.
		writeError(w, http.StatusBadRequest, "UNSUPPORTED_FORMAT",
			"that format is not one this produces; "+
				officeragent.SupportedFormatList())
		return
	}

	// The PDF converter, where there is one. Passed rather than reached for, so
	// that the only thing officeragent knows about PDFs is that something can
	// turn a page into one -- and so that the converter the printed report
	// already goes through is the converter an export goes through.
	var conv officeragent.PDFConverter
	if s.PDF != nil && s.PDF.Available() {
		conv = s.PDF
	}

	out, err := officeragent.BuildExport(ctx, s.Reports, p.Scope(), conv,
		officeragent.ExportRequest{
			Format:        format,
			Fields:        req.Fields,
			Title:         req.Title,
			Note:          req.Note,
			EventID:       req.EventID,
			State:         req.State,
			AwaitingLevel: req.AwaitingLevel,
			Search:        req.Search,
		})
	if err != nil {
		s.writeExportError(w, err)
		return
	}

	// Recorded like any other read of a district's claims, and with what an
	// auditor would want to know about a file that has left the building: who,
	// which format, how many rows, which columns.
	//
	// The search text is not recorded, for the same reason askAgent does not
	// record the question: it can quote a farmer, and the audit log is read by
	// more people and kept longer than the download.
	s.Audit.Record(ctx, audit.Event{
		Action: "officer.agent.export", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "officer_agent",
		Detail: map[string]any{
			"format":  string(format),
			"rows":    out.Rows,
			"columns": out.Fields,
			"bytes":   len(out.Bytes),
			"state":   req.State,
			"event":   req.EventID,
		},
	}.FromContext(ctx))

	w.Header().Set("Content-Type", out.ContentType)
	w.Header().Set("Content-Disposition", contentDisposition(out.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(out.Bytes)))
	// What the officer is holding, for a console that wants to say so.
	w.Header().Set("X-Esurvey-Rows", strconv.Itoa(out.Rows))
	w.Header().Set("X-Esurvey-Format", string(format))

	if _, err := w.Write(out.Bytes); err != nil {
		// The headers have gone; there is no status left to change. Recorded and
		// dropped, which is what a truncated download looks like to the officer.
		s.Log.Warn("writing an export failed", "error", err, "format", format)
	}
}

// writeExportError turns a build failure into the answer the officer can act on.
func (s *Server) writeExportError(w http.ResponseWriter, err error) {
	var tooMany officeragent.TooManyRowsError
	var tooLarge officeragent.TooLargeError
	var unknown officeragent.UnknownFieldError

	switch {
	case errors.As(err, &unknown):
		writeError(w, http.StatusUnprocessableEntity, "UNKNOWN_COLUMN",
			unknown.Error()+"; the columns are the ones the assistant offers")
	case errors.Is(err, officeragent.ErrNoFields):
		writeError(w, http.StatusUnprocessableEntity, "NO_COLUMNS",
			"choose at least one column to export")
	case errors.As(err, &tooMany):
		// Refused with the number in it, rather than truncated. A statement that
		// stopped at row one thousand without saying so is one an officer signs
		// believing it complete.
		writeError(w, http.StatusUnprocessableEntity, "TOO_MANY_ROWS",
			tooMany.Error())
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusUnprocessableEntity, "EXPORT_TOO_LARGE",
			tooLarge.Error())
	case errors.Is(err, officeragent.ErrPDFUnavailable):
		// A fixed sentence rather than the converter's own words, which can carry
		// an address and a port. The officer's remedy is the same whether it is
		// unconfigured, unreachable or refused the document.
		writeError(w, http.StatusServiceUnavailable, "PDF_UNAVAILABLE",
			"the PDF converter is not available; ask for the same export as "+
				"xlsx, csv, docx or pptx")
	case errors.Is(err, officeragent.ErrUnknownFormat):
		writeError(w, http.StatusBadRequest, "UNSUPPORTED_FORMAT",
			"that format is not one this produces; "+
				officeragent.SupportedFormatList())
	default:
		s.Log.Warn("building an export failed", "error", err)
		writeError(w, http.StatusInternalServerError, "EXPORT_FAILED",
			"the export could not be produced")
	}
}

// contentDisposition builds the download header for a filename we do not trust.
//
// # Why a filename is not trusted
//
// Because it is built from a title the caller sent, and a Content-Disposition is
// a header. A title carrying a quote closes the quoted string and everything after
// it becomes header syntax; a title carrying a carriage return and a newline ends
// the header and begins another one, which is response splitting -- a caller
// choosing what the browser is told, up to and including a second response. Go's
// net/http does reject some of this on its way out, but a filename that depends on
// the standard library noticing is a filename that breaks the day something else
// writes the response.
//
// So nothing structural survives: control characters, quotes, backslashes,
// semicolons and path separators are removed rather than escaped, because none of
// them has any business in a download name and removing them cannot be got wrong.
//
// # Why both forms
//
// filename= is ASCII only, by the grammar. A Marathi title would arrive as a
// filename of underscores, which is a worse answer than it looks: an officer who
// downloads four of these in a morning has four files they cannot tell apart. So
// the UTF-8 form of RFC 5987 carries the real name for every browser made in the
// last decade, and the ASCII form stays as the fallback the grammar requires.
func contentDisposition(name string) string {
	base, ext := splitExtension(name)
	base = safeFilenameBase(base)
	if base == "" {
		base = "export"
	}
	full := base + ext

	return "attachment; filename=\"" + asciiFilename(base) + ext + "\"" +
		"; filename*=UTF-8''" + percentEncode(full)
}

// splitExtension separates a trailing extension we recognise as one.
//
// Kept apart from the name so that trimming a long title cannot eat the
// extension, and so that only letters and digits can survive in it -- an
// extension is the one part of a filename an operating system acts on.
func splitExtension(name string) (string, string) {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot == len(name)-1 || len(name)-dot > 6 {
		// No dot, a trailing dot with nothing after it, or something too long to
		// be an extension. All three are part of the name.
		return name, ""
	}
	ext := name[dot+1:]
	for _, r := range ext {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return name, ""
		}
	}
	return name[:dot], "." + strings.ToLower(ext)
}

// safeFilenameBase keeps the readable part of a name and nothing else.
func safeFilenameBase(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			// Control characters, which includes the carriage return and the
			// newline a header injection needs.
			continue
		case strings.ContainsRune(`"\/;:*?<>|`+"\u0000", r):
			// Header syntax and path syntax. Dropped, not escaped.
			continue
		case r == '.':
			// A leading dot hides a file and "../" climbs a directory. The
			// extension is handled separately, so no dot is needed here at all.
			continue
		default:
			b.WriteRune(r)
		}
	}
	// Collapse the whitespace the removals left behind, and bound the length:
	// some filesystems stop at 255 bytes and a Devanagari rune is three of them.
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > 60 {
		out = strings.TrimSpace(string(r[:60]))
	}
	return out
}

// asciiFilename is the fallback for the quoted form.
//
// Non-ASCII runes become underscores rather than disappearing, so that a title
// that is entirely Marathi still yields a name of the right shape. If nothing
// readable is left, the extension alone identifies the file.
func asciiFilename(s string) string {
	var b strings.Builder
	readable := false
	for _, r := range s {
		if r > unicode.MaxASCII {
			b.WriteByte('_')
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			readable = true
		}
		b.WriteRune(r)
	}
	if !readable {
		return "export"
	}
	return b.String()
}

// percentEncode encodes a filename for the RFC 5987 UTF-8 parameter.
//
// Only unreserved characters are left alone. More cautious than RFC 5987's
// attr-char requires, which costs a few percent signs in a name no human reads
// and leaves nothing that could be read as header syntax.
func percentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
