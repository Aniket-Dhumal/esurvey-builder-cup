package officeragent

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// What an officer may attach to a question, and how it reaches the model.
//
// # Why a file at all
//
// Because the questions officers actually have arrive on paper. A circular from
// the Collectorate that nobody has retyped, a photograph of a panchnama filled in
// by hand, a sheet of figures a tehsil office keeps in its own shape. An assistant
// that can only be told about those in words has to be told in words, and an
// officer who has already read the circular closely enough to summarise it did not
// need the assistant.
//
// # What is sent as it is, and what is turned into text first
//
// Images and PDFs go to the model untouched: it reads both natively, and a scan put
// through an OCR of our own would arrive worse than the original with nothing to
// say so. A workbook cannot go untouched -- a .xlsx is a zip of XML, so the model
// would be reading the container and not the figures -- and a CSV does not go
// unexamined either, because the bound and the shape have to be ours. So the cells
// are read out here and the table travels as text, named as having come from that
// file.
//
// Reading them here rather than asking the model to is the same choice the report
// filler makes about arithmetic: a figure reconstructed from a binary by a model is
// a figure nobody can check against the file it came from.
//
// # What is deliberately not attempted
//
// Deciding whether a photograph is somebody's Aadhaar card. It cannot be done
// reliably, and a filter that works most of the time teaches an officer that the
// system is protecting them when it is not, which is worse than telling them
// plainly that the decision is theirs. So an attachment is audited by name, type
// and size, the console warns before the upload, and the officer decides. Text is a
// different matter: text is read, and every piece of it passes the redaction layer
// in the AI service like any other string.

// Attachment is one file the officer attached, prepared for the model.
//
// Exactly one of Data and Text is set. Kind says which, and how the AI service
// should put it to the model. MediaType stays the type of the file the officer
// actually attached, even where what is sent is text read out of it, so that the
// audit record and the label the model is shown both name the real file.
type Attachment struct {
	Filename  string `json:"filename"`
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`

	// Data is the file itself, for a kind the model reads natively. It crosses to
	// the AI service base64-encoded, which is what encoding/json does with a byte
	// slice and what the field name on the other end says.
	Data []byte `json:"data_base64,omitempty"`

	// Text is what was read out of a file the model cannot read itself.
	Text string `json:"text,omitempty"`

	// Bytes is the size of the original upload, kept for the audit record after
	// the bytes themselves have been replaced by a table.
	Bytes int `json:"bytes"`
}

// How an attachment reaches the model.
const (
	// KindImage is an inline image part.
	KindImage = "image"
	// KindDocument is an inline document part -- a PDF, which the model reads
	// page by page without help from us.
	KindDocument = "document"
	// KindText is a text part: a text file, or a table read out of a spreadsheet
	// on this side.
	KindText = "text"
)

// Media types this will carry, by what the bytes turn out to be.
const (
	MediaPNG      = "image/png"
	MediaJPEG     = "image/jpeg"
	MediaWebP     = "image/webp"
	MediaPDF      = "application/pdf"
	MediaCSV      = "text/csv"
	MediaMarkdown = "text/markdown"
	MediaText     = "text/plain"
	MediaXLSX     = "application/vnd.openxmlformats-officedocument." +
		"spreadsheetml.sheet"
)

// MaxAttachmentBytes bounds one file.
//
// Eight mebibytes, which is the cap the platform already puts on a field
// photograph, so an officer who can photograph a panchnama on the handset can
// attach that same photograph here. A 300 dpi colour scan of an A4 page is around
// a megabyte and a half, so a circular of several pages fits comfortably and a
// scan of a whole file does not.
//
// It is also the only bound available on how many pages of PDF a question carries.
// The model charges a PDF by the page, and counting pages without a PDF parser is
// guesswork -- page objects sit inside compressed object streams in anything
// produced this decade, so a scan of the raw bytes would report none and let the
// file through. A two-hundred-page scan is tens of megabytes and is refused by
// this; a two-hundred-page PDF that is only text is small, cheap, and not the case
// the officer needed protecting from.
const MaxAttachmentBytes = 8 << 20

// MaxAttachmentsTotalBytes bounds one question.
//
// Two large files, or four ordinary ones. The whole set is held in memory here,
// base64-encoded onto the AI service, and then -- this is the part that decides the
// number -- resent on every turn of the loop, because the attachments ride on the
// officer's own message and the whole conversation goes to the model each time. At
// MaxTurns, a sixteen mebibyte question is sixteen mebibytes six times over.
const MaxAttachmentsTotalBytes = 16 << 20

// MaxAttachments bounds how many files one question may carry.
//
// A circular, two photographs and a sheet of figures is a real question. More than
// that is a folder, and a folder is not a question: the model attends to the whole
// of a short prompt and skims a long one, so the fifth file makes the answer worse
// as well as dearer.
const MaxAttachments = 4

// MaxExtractedChars bounds the table read out of a spreadsheet.
//
// Sixty-four kibibytes is around four hundred rows of a dozen columns, which is
// more than any officer's working sheet, and roughly fifteen thousand tokens --
// paid for again on every turn. Beyond it the table is cut off at a row boundary
// and said to be cut off, in the same breath as a warning not to total it, because
// a column silently truncated is how a model comes to report a plausible wrong sum.
const MaxExtractedChars = 64 << 10

// MaxExtractedRows bounds the rows read out of one spreadsheet.
//
// The character budget would stop it in the end. This stops it sooner, so that a
// sheet with a hundred thousand nearly-empty rows -- which is what a spreadsheet
// somebody has scrolled to the bottom of looks like -- is not walked in full to
// produce nothing.
const MaxExtractedRows = 2000

// Refusal is an attachment this will not send, and why, in words meant for the
// officer who attached it.
//
// Carried as a code and a sentence because both travel: the code is what the
// console switches on, and the sentence is what the officer reads. A refusal here
// is rarely a fault of theirs and should not read as though it were.
type Refusal struct {
	Code   string
	Reason string
}

func (e *Refusal) Error() string { return e.Reason }

// Refusal codes.
const (
	CodeUnsupported   = "UNSUPPORTED_ATTACHMENT"
	CodeMismatch      = "ATTACHMENT_MISMATCH"
	CodeUnreadable    = "UNREADABLE_ATTACHMENT"
	CodeTooLarge      = "ATTACHMENT_TOO_LARGE"
	CodeTotalTooLarge = "ATTACHMENTS_TOO_LARGE"
	CodeTooMany       = "TOO_MANY_ATTACHMENTS"
)

// supportedList is the sentence every refusal of an unsupported file ends with.
//
// One string, so that adding a type here cannot leave a refusal still telling
// officers about the set as it used to be.
const supportedList = "Attach a PNG, JPEG or WebP image, a PDF, an .xlsx " +
	"workbook, a CSV, or a plain text or Markdown file."

// ClassifyAttachment decides what one uploaded file is, and prepares it.
//
// # Why the bytes decide and the name does not
//
// Because the name is the client's and the bytes are the file. A browser sends
// whatever content type the operating system associates with an extension, and an
// officer renaming a file to get it past an upload box is a thing that happens
// with no ill intent at all. Sending an archive to the model as an image would at
// best waste a call and at worst have the model describe whatever it made of the
// bytes as though it had read the document.
//
// So the extension says what the file claims to be, the first bytes say what it is,
// and a disagreement is refused by name. What the client declared the type to be is
// not consulted here at all; it is kept for the audit record, where what was
// claimed is worth having beside what was actually sent.
func ClassifyAttachment(filename string, data []byte) (Attachment, error) {
	name := cleanFilename(filename)
	if name == "" {
		return Attachment{}, &Refusal{Code: CodeUnsupported,
			Reason: "an attachment arrived with no filename, so there is no " +
				"saying what it is. " + supportedList}
	}
	if len(data) == 0 {
		return Attachment{}, &Refusal{Code: CodeUnreadable,
			Reason: fmt.Sprintf("%q is empty.", name)}
	}
	if len(data) > MaxAttachmentBytes {
		return Attachment{}, tooLarge(name, int64(len(data)))
	}

	att := Attachment{Filename: name, Bytes: len(data)}
	found := sniff(data)

	switch ext := extensionOf(name); ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		want := map[string]string{
			".png":  MediaPNG,
			".jpg":  MediaJPEG,
			".jpeg": MediaJPEG,
			".webp": MediaWebP,
		}[ext]
		if found != want {
			return Attachment{}, mismatch(name, ext, found)
		}
		att.Kind, att.MediaType, att.Data = KindImage, want, data
		return att, nil

	case ".pdf":
		if found != MediaPDF {
			return Attachment{}, mismatch(name, ext, found)
		}
		att.Kind, att.MediaType, att.Data = KindDocument, MediaPDF, data
		return att, nil

	case ".xlsx":
		// A .xlsx is a zip, so sniffing can only get as far as "an archive".
		// Whether it is a workbook is answered by opening it, which has to happen
		// anyway to read the cells.
		if found != mediaZip {
			return Attachment{}, mismatch(name, ext, found)
		}
		table, err := extractWorkbook(data)
		if err != nil {
			return Attachment{}, named(err, name)
		}
		att.Kind, att.MediaType, att.Text = KindText, MediaXLSX, table
		return att, nil

	case ".xls":
		// Refused by name rather than half-supported. The old binary format is a
		// different file entirely -- excelize does not open it, and a reader for
		// it would be a dependency carried for a format Excel itself has been
		// saving away from since 2007. An office that has one can convert it in
		// the program they already have open, and being told that is better than
		// a generic refusal they have to guess at.
		return Attachment{}, &Refusal{Code: CodeUnsupported,
			Reason: fmt.Sprintf("%q is in the old .xls format, which this cannot "+
				"read. Open it and save it as .xlsx, then attach that.", name)}

	case ".csv":
		if !strings.HasPrefix(found, "text/") {
			return Attachment{}, mismatch(name, ext, found)
		}
		table, err := extractCSV(data)
		if err != nil {
			return Attachment{}, named(err, name)
		}
		att.Kind, att.MediaType, att.Text = KindText, MediaCSV, table
		return att, nil

	case ".txt", ".md", ".markdown":
		if !strings.HasPrefix(found, "text/") {
			return Attachment{}, mismatch(name, ext, found)
		}
		if !utf8.Valid(data) {
			return Attachment{}, &Refusal{Code: CodeUnreadable,
				Reason: fmt.Sprintf("%q is not UTF-8 text. Save it as UTF-8 and "+
					"attach it again.", name)}
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			return Attachment{}, &Refusal{Code: CodeUnreadable,
				Reason: fmt.Sprintf("%q has nothing in it to read.", name)}
		}
		media := MediaMarkdown
		if ext == ".txt" {
			media = MediaText
		}
		att.Kind, att.MediaType, att.Text = KindText, media, truncate(text)
		return att, nil
	}

	return Attachment{}, &Refusal{Code: CodeUnsupported,
		Reason: fmt.Sprintf("%q is not a kind of file this can read. %s",
			name, supportedList)}
}

// CheckAttachmentSet applies the bounds that are about the question rather than
// about any one file.
//
// Checked before anything is classified, so that a refusal costs nothing: reading
// twenty workbooks in order to say there were too many of them is work done for an
// answer already known.
func CheckAttachmentSet(count int, total int64) error {
	if count > MaxAttachments {
		return &Refusal{Code: CodeTooMany,
			Reason: fmt.Sprintf("a question may carry at most %d attachments, "+
				"and that one had %d. Ask about them a few at a time.",
				MaxAttachments, count)}
	}
	if total > MaxAttachmentsTotalBytes {
		return &Refusal{Code: CodeTotalTooLarge,
			Reason: fmt.Sprintf("those attachments come to %s together, and one "+
				"question may carry at most %s in all.",
				humanBytes(total), humanBytes(MaxAttachmentsTotalBytes))}
	}
	return nil
}

// TooLargeRefusal is the per-file refusal on its own, so that the handler can
// refuse on the size a multipart part declares without first reading it.
func TooLargeRefusal(filename string, size int64) error {
	return tooLarge(cleanFilename(filename), size)
}

func tooLarge(name string, size int64) *Refusal {
	return &Refusal{Code: CodeTooLarge,
		Reason: fmt.Sprintf("%q is %s, and one attachment may be at most %s.",
			name, humanBytes(size), humanBytes(MaxAttachmentBytes))}
}

func mismatch(name, ext, found string) *Refusal {
	return &Refusal{Code: CodeMismatch,
		Reason: fmt.Sprintf("%q is not %s: its contents are %s. Attach the file "+
			"in the format its name claims.", name, claimOf(ext), describe(found))}
}

// named fills an extractor's refusal in with the filename.
//
// The extractors do not know the name -- they are handed bytes -- and a refusal
// that does not say which of four files it is about is a refusal the officer has to
// work out for themselves.
func named(err error, name string) error {
	var r *Refusal
	if errors.As(err, &r) {
		return &Refusal{Code: r.Code, Reason: fmt.Sprintf(r.Reason, name)}
	}
	return err
}

// mediaZip is what a .xlsx sniffs as, and the reason it is not among the supported
// types above: an archive as such is never sent anywhere.
const mediaZip = "application/zip"

// sniff names what the first bytes actually are.
//
// http.DetectContentType is the standard library's implementation of the WHATWG
// sniffing algorithm and already knows every signature this cares about, which is
// better than a table maintained here. Its answer for anything textual carries a
// charset, so that is trimmed off; its answer for anything it does not recognise is
// application/octet-stream, which reaches the officer as "not a kind of file this
// can read".
func sniff(data []byte) string {
	t := http.DetectContentType(data)
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t
}

// describe names a sniffed type the way an officer would say it.
func describe(media string) string {
	switch media {
	case MediaPNG:
		return "a PNG image"
	case MediaJPEG:
		return "a JPEG image"
	case MediaWebP:
		return "a WebP image"
	case MediaPDF:
		return "a PDF"
	case mediaZip:
		return "a ZIP archive"
	}
	if strings.HasPrefix(media, "text/") {
		return "plain text"
	}
	return "none of the kinds of file this recognises"
}

// claimOf names what an extension claims, for the sentence saying the bytes
// disagree with it.
func claimOf(ext string) string {
	switch ext {
	case ".png":
		return "a PNG"
	case ".jpg", ".jpeg":
		return "a JPEG"
	case ".webp":
		return "a WebP image"
	case ".pdf":
		return "a PDF"
	case ".xlsx":
		return "an .xlsx workbook"
	case ".csv":
		return "a CSV"
	}
	return "what its name says"
}

// extractWorkbook reads a workbook's cells into a tab-separated table.
//
// Every sheet, headed by its name. An officer's figures are as often on the second
// sheet as the first, and a reader that took only the first would answer
// confidently about the wrong half of their file.
func extractWorkbook(data []byte) (string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", &Refusal{Code: CodeUnreadable,
			Reason: "%q could not be read as a workbook. It may be damaged, or " +
				"it may be some other kind of file saved under that name."}
	}
	defer f.Close()

	var b strings.Builder
	rowsRead, cut := 0, false

	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			// One unreadable sheet is not an unreadable file. Skipped, and the
			// rest of the workbook still answers the officer.
			continue
		}
		titled := false
		for _, row := range rows {
			if rowsRead >= MaxExtractedRows || b.Len() >= MaxExtractedChars {
				cut = true
				break
			}
			line := strings.TrimRight(strings.Join(row, "\t"), "\t")
			if strings.TrimSpace(line) == "" {
				// A blank row is formatting, and a spreadsheet is mostly blank
				// rows. Dropped, so the budget goes on figures.
				continue
			}
			if !titled {
				b.WriteString("# sheet: " + sheet + "\n")
				titled = true
			}
			b.WriteString(line)
			b.WriteByte('\n')
			rowsRead++
		}
		if cut {
			break
		}
	}

	if rowsRead == 0 {
		return "", &Refusal{Code: CodeUnreadable,
			Reason: "%q has no cells with anything in them."}
	}
	return withCutNote(b.String(), cut), nil
}

// extractCSV reads a CSV into the same tab-separated table a workbook becomes.
//
// Parsed rather than passed through, although a CSV is already text, for three
// reasons that have nothing to do with the delimiter: the row bound and the
// truncation note have to apply to it as they do to a workbook, a quoted field
// containing a newline would otherwise reach the model as two broken rows, and a
// file this cannot parse is better refused here than guessed at there.
func extractCSV(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", &Refusal{Code: CodeUnreadable,
			Reason: "%q is not UTF-8 text. Save it as UTF-8 and attach it again."}
	}

	// The byte-order mark Excel writes in front of a CSV it saved as UTF-8. Left
	// in place it becomes part of the first heading, and the first heading is the
	// one the model matches on.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	r := csv.NewReader(bytes.NewReader(data))
	// A real CSV from a real office is ragged and quoted by hand. Both are
	// tolerated: the alternative is refusing a file the officer can plainly read
	// themselves, which reads to them as the feature being broken.
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	var b strings.Builder
	rowsRead, cut := 0, false
	for {
		if rowsRead >= MaxExtractedRows || b.Len() >= MaxExtractedChars {
			cut = true
			break
		}
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", &Refusal{Code: CodeUnreadable,
				Reason: "%q could not be read as a CSV."}
		}
		line := strings.TrimRight(strings.Join(record, "\t"), "\t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		rowsRead++
	}

	if rowsRead == 0 {
		return "", &Refusal{Code: CodeUnreadable,
			Reason: "%q has no rows with anything in them."}
	}
	return withCutNote(b.String(), cut), nil
}

// withCutNote says plainly when a table was cut short.
//
// The warning not to total it is the point. A model shown four hundred of eight
// hundred rows and asked for a total will produce one, and a wrong total that looks
// right is the worst thing this feature could hand an officer.
func withCutNote(table string, cut bool) string {
	table = strings.TrimRight(table, "\n")
	if !cut {
		return table
	}
	return table + "\n[cut short: only the first part of this file is shown, so " +
		"it must not be totalled or counted]"
}

// truncate applies the same budget to a text file, on a line boundary.
func truncate(text string) string {
	if len(text) <= MaxExtractedChars {
		return text
	}
	cut := text[:MaxExtractedChars]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	} else {
		// One enormous line, so there is no boundary to cut on. Trimmed back to
		// the last whole rune instead: half a rune is invalid UTF-8, and invalid
		// UTF-8 is a request the AI service rejects rather than a file read short.
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
	}
	return withCutNote(cut, true)
}

// cleanFilename keeps the name and discards the path.
//
// Nothing is ever opened by this name, so a traversal in it is not a traversal.
// But it is written into the audit log and shown to the model, and both read
// better without a stranger's directory tree in them -- and a control character in
// a filename is a forged line in a log file and a forged instruction in a prompt.
//
// filepath.Base is deliberately not used: it follows the separator rules of
// whichever platform this was built for, and the client's separators are not ours.
func cleanFilename(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if len(name) > 120 {
		name = name[:120]
	}
	return strings.TrimSpace(name)
}

func extensionOf(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	return strings.ToLower(name[i:])
}

// humanBytes writes a size the way a refusal should read.
//
// Megabytes rounded, not mebibytes exact. The bounds above are powers of two
// because that is the unit a buffer is measured in; an officer being told why their
// scan was refused needs a number they can compare with the one their file manager
// showed them.
func humanBytes(n int64) string {
	const (
		kb = 1 << 10
		mb = 1 << 20
	)
	switch {
	case n >= mb:
		return fmt.Sprintf("%.0f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.0f KB", float64(n)/kb)
	}
	// Below a kilobyte, said in bytes. The refusals in this file never reach
	// here -- they are about files of megabytes -- but the sentence describing a
	// prepared document does, and a one-claim CSV reported as "0 KB" reads like
	// a document that failed to be written.
	return fmt.Sprintf("%d bytes", n)
}
