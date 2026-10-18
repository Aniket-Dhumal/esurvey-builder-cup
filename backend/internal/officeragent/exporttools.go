package officeragent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Making the conversation produce the document.
//
// # Why this is a tool and not another button
//
// Because "give me the pending Barshi claims as a PDF" is one sentence. Before
// this it was answered in prose, beside a row of download buttons the officer
// then had to press, hoping the filters behind them meant what they had just
// said -- and when the two disagreed, nothing said so. The format, the columns
// and the narrowing are all in the sentence. Reading them out of it is what the
// model is for; producing the document is what export.go is for; and the officer
// should not have to be the wire between them.
//
// It also makes the thing correctable, which is the part that matters in a
// conversation. "Make that Excel, and add the village" is one more call with two
// arguments changed, not a second trip through a dialog.
//
// # What a tool is allowed to hand back
//
// A sentence. Not the bytes: a megabyte of base64 in a tool result is resent to
// the model on every later turn of the conversation, and buys nothing, since a
// model has no use for a workbook it cannot open. And not an address either --
// the id of a prepared document never reaches the model at all. It travels from
// Keep straight to the Answer through a value the loop in agent.go takes apart,
// so there is no turn in which the model holds one. Nothing written in an
// attached document can ask it to name an id it was never given.
//
// # Why the vocabulary is not widened here
//
// Every column comes from fields.go and every row from RowsFor, exactly as the
// export endpoint's do: these tools are a way of calling BuildExport, not a
// second renderer. farmer_id and Aadhaar are not in that vocabulary, and a model
// that asks for one is refused by name rather than told there is no such column
// -- otherwise it tries farmer_identifier, then aadhaar_no, then registry_id,
// and the officer watches it spend its turns on three spellings of the same
// refusal.
//
// # Why an export cannot widen what the officer can see
//
// For the reason export.go gives, unchanged by putting a model in front of it.
// There is no parameter through which a claim, a report id, a farmer or a scope
// could arrive: the tools below declare a format, a choice of columns and the
// same three narrowings the claim list takes, and the rows are fetched by RowsFor
// from the rbac.Scope the agent loop passes in -- the asking officer's own. A
// sentence inside an attached circular can therefore say anything it likes. The
// most it can achieve is a document of the claims that officer could already
// open, which is also the most the officer themselves could achieve.

// ExportTools is what the tools that produce a document need, for the span of
// one question.
//
// Built per question rather than once at startup, because two of the things here
// belong to the question rather than to the process: who is asking, which
// decides where a prepared document is filed and therefore who can fetch it, and
// what they attached, which decides whether there is a format to fill at all.
type ExportTools struct {
	Reports Reports

	// Store is where a prepared document waits. Without one there is nowhere to
	// put a file, and no tool is offered.
	Store *Exports

	// PDF may be nil, which costs the PDF format and nothing else.
	PDF PDFConverter

	// Owner is the asking officer's user id. The store composes its key from it,
	// which is what makes a prepared document theirs and nobody else's.
	Owner string

	// Formats are the .xlsx workbooks attached to this question, bytes and all.
	//
	// Not read out of the Attachment list, which carries a workbook as the table
	// extracted from it: the model is shown the figures, and the filler needs
	// the file. See attachment.go.
	Formats []AttachedFormat

	// Mapper works out which field each heading of an office's own format wants.
	// Nil leaves fill_attached_format off the list, there being nothing to map
	// the headings with.
	Mapper HeadingMapper

	// Record writes the audit line for a document that was prepared. Nil records
	// nothing, which is right in a test and wrong in a deployment.
	Record func(ctx context.Context, p Prepared, via string)
}

// AttachedFormat is an .xlsx the officer attached to this question.
type AttachedFormat struct {
	Filename string
	Data     []byte
}

// HeadingMapper reads an office's own column headings. Satisfied by *Agent.
type HeadingMapper interface {
	MapHeadings(ctx context.Context, headings []string) ([]string, error)
}

// The names the model knows these by.
const (
	ToolExportClaims       = "export_claims"
	ToolFillAttachedFormat = "fill_attached_format"
)

// Tools are the ones to offer alongside ReadOnlyTools.
//
// Nothing is offered that cannot work. No store means there is nowhere for a
// prepared document to wait; no attached workbook means there is no format to
// fill. A tool the model is told about and then refused is a turn spent on an
// apology, and the officer reads the apology.
func (t ExportTools) Tools() []Tool {
	if t.Store == nil || t.Reports == nil || t.Owner == "" {
		return nil
	}
	out := []Tool{t.exportClaims()}
	if len(t.Formats) > 0 && t.Mapper != nil {
		out = append(out, t.fillAttachedFormat())
	}
	return out
}

// exportClaims is the tool that makes a document out of the officer's own claims.
func (t ExportTools) exportClaims() Tool {
	return Tool{
		Name: ToolExportClaims,
		Description: "Prepare the officer's own claims as a document they can " +
			"download: a spreadsheet, a CSV, a PDF, a Word document or a slide " +
			"deck. Use this whenever they ask for a file, a list to keep, or " +
			"something to circulate, instead of listing the claims in your " +
			"reply. Call it again with different arguments when they ask for it " +
			"changed. The reply they read carries the document itself, so say " +
			"what you prepared and do not offer a link or a name for it.",
		Parameters: map[string]any{
			"type": "OBJECT",
			"properties": map[string]any{
				"format": map[string]any{
					"type":        "STRING",
					"description": "One of: " + strings.Join(formatNames(), ", "),
				},
				"fields": map[string]any{
					"type":  "ARRAY",
					"items": map[string]any{"type": "STRING"},
					"description": "The columns, in the order they should " +
						"appear. Choose from: " + exportFieldVocabulary() +
						". Omit for the columns a taluka statement usually " +
						"carries.",
				},
				"state": map[string]any{
					"type": "STRING",
					"description": "Narrow to one state: submitted, assigned, " +
						"verified, approved, rejected, returned. Omit for all.",
				},
				"awaiting_level": map[string]any{
					"type": "STRING",
					"description": "Narrow to the claims waiting at one rung " +
						"of the approval ladder.",
				},
				"search": map[string]any{
					"type": "STRING",
					"description": "Free text, as the claim list takes it: a " +
						"village name, a farmer name.",
				},
			},
			"required": []any{"format"},
		},
		Run: func(
			ctx context.Context, scope rbac.Scope, args map[string]any,
		) (any, error) {
			format, err := ParseFormat(stringArg(args, "format"))
			if err != nil {
				return nil, err
			}
			fields, err := chosenFields(args)
			if err != nil {
				return nil, err
			}

			state := strings.TrimSpace(stringArg(args, "state"))
			awaiting := strings.TrimSpace(stringArg(args, "awaiting_level"))
			search := strings.TrimSpace(stringArg(args, "search"))

			out, err := BuildExport(ctx, t.Reports, scope, t.PDF, ExportRequest{
				Format: format,
				Fields: fields,
				// Built here from the narrowings rather than taken from the
				// model, which has no argument for it. It is what makes three
				// documents downloaded in one morning tell themselves apart,
				// and it cannot say anything the filters did not.
				Title:         preparedTitle(state, awaiting, search),
				State:         state,
				AwaitingLevel: awaiting,
				Search:        search,
			})
			if err != nil {
				return nil, correctable(err)
			}
			return t.keep(ctx, format, out, ToolExportClaims,
				fmt.Sprintf("prepared: %s, %s, %s", claimCount(out.Rows),
					format, humanBytes(int64(len(out.Bytes)))))
		},
	}
}

// fillAttachedFormat is the tool that fills the office's own workbook.
//
// Offered only when there is one attached to this very question, which is what
// makes "their format" unambiguous: the officer is looking at the file they just
// sent. A format attached to an earlier question is not available, for the reason
// trimHistory gives in the api package -- an attachment is part of the question
// it came with, and nothing replays one.
func (t ExportTools) fillAttachedFormat() Tool {
	return Tool{
		Name: ToolFillAttachedFormat,
		Description: "Fill the report format the officer attached to this " +
			"question with their own claims, keeping their office's columns, " +
			"headings and layout. Attached now: " +
			strings.Join(t.formatNames(), ", ") + ". Use this when they ask " +
			"for their own format filled rather than for a document of ours.",
		Parameters: map[string]any{
			"type": "OBJECT",
			"properties": map[string]any{
				"filename": map[string]any{
					"type": "STRING",
					"description": "Which attached format to fill. Omit when " +
						"only one is attached.",
				},
				"state": map[string]any{
					"type": "STRING",
					"description": "Narrow to one state: submitted, assigned, " +
						"verified, approved, rejected, returned. Omit for all.",
				},
				"awaiting_level": map[string]any{
					"type": "STRING",
					"description": "Narrow to the claims waiting at one rung " +
						"of the approval ladder.",
				},
				"search": map[string]any{
					"type":        "STRING",
					"description": "Free text: a village name, a farmer name.",
				},
			},
		},
		Run: func(
			ctx context.Context, scope rbac.Scope, args map[string]any,
		) (any, error) {
			format, err := t.formatNamed(stringArg(args, "filename"))
			if err != nil {
				return nil, err
			}

			tmpl, err := ReadTemplate(bytes.NewReader(format.Data))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", format.Filename, err)
			}
			mapping, err := t.Mapper.MapHeadings(ctx, tmpl.Headings)
			if err != nil {
				return nil, fmt.Errorf(
					"the columns of %s could not be worked out", format.Filename)
			}
			filled := 0
			for _, f := range mapping {
				if f != "" {
					filled++
				}
			}
			if filled == 0 {
				// Refused rather than returned unchanged, which would look like
				// the tool had worked. The vocabulary is named so the model can
				// tell the officer what their format would have to say.
				return nil, fmt.Errorf("none of the %d column headings in %s "+
					"matched anything this can fill; the columns it knows are %s",
					len(tmpl.Headings), format.Filename, exportFieldVocabulary())
			}

			// One past the cap, as BuildExport does, so that "more than the cap
			// matched" is a fact rather than a guess -- and so that a format is
			// never handed back quietly holding the first thousand of two
			// thousand claims.
			rows, err := RowsFor(ctx, t.Reports, scope, report.Filter{
				State:         strings.TrimSpace(stringArg(args, "state")),
				AwaitingLevel: strings.TrimSpace(stringArg(args, "awaiting_level")),
				Page: page.Params{
					Query: strings.TrimSpace(stringArg(args, "search")),
				},
			}, MaxExportRows+1)
			if err != nil {
				return nil, correctable(err)
			}
			if len(rows) > MaxExportRows {
				return nil, correctable(TooManyRowsError{Limit: MaxExportRows})
			}

			book, err := Fill(bytes.NewReader(format.Data), tmpl, mapping, rows)
			if err != nil {
				return nil, fmt.Errorf(
					"%s could not be written: %w", format.Filename, err)
			}
			defer book.Close()

			var buf bytes.Buffer
			if err := book.Write(&buf); err != nil {
				return nil, fmt.Errorf(
					"%s could not be written: %w", format.Filename, err)
			}

			out := &Export{
				Bytes:       buf.Bytes(),
				ContentType: ContentType(FormatXLSX),
				Filename:    filledFormatName(format.Filename),
				Rows:        len(rows),
				Fields:      mapping,
			}
			return t.keep(ctx, FormatXLSX, out, ToolFillAttachedFormat,
				fmt.Sprintf("prepared: %s filled with %s in %d of its %d "+
					"columns, %s", format.Filename, claimCount(len(rows)),
					filled, len(tmpl.Headings), humanBytes(int64(buf.Len()))))
		},
	}
}

// keep files a finished document and splits what it is into two audiences.
//
// [summary] is the sentence the model gets. The Prepared beside it is what the
// console gets, and the agent loop is where the two part company -- which is the
// mechanism by which the model is never told an id.
func (t ExportTools) keep(
	ctx context.Context, format Format, out *Export, via, summary string,
) (any, error) {
	p, err := t.Store.Keep(ctx, t.Owner, format, out)
	if err != nil {
		return nil, correctable(err)
	}
	if t.Record != nil {
		t.Record(ctx, p, via)
	}
	return preparedResult{prepared: p, summary: summary}, nil
}

// preparedResult is a tool result with a document behind it.
//
// Unexported, and deliberately: nothing outside this package can hand the agent
// loop one, so a prepared document in an Answer is always one this file put
// there.
type preparedResult struct {
	prepared Prepared
	summary  string
}

// formatNamed picks the attached workbook the model asked for.
func (t ExportTools) formatNamed(name string) (AttachedFormat, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if len(t.Formats) == 1 {
			return t.Formats[0], nil
		}
		return AttachedFormat{}, fmt.Errorf("more than one format is attached; "+
			"say which one: %s", strings.Join(t.formatNames(), ", "))
	}
	for _, f := range t.Formats {
		if strings.EqualFold(f.Filename, name) {
			return f, nil
		}
	}
	return AttachedFormat{}, fmt.Errorf("no format called %q is attached to "+
		"this question; the ones attached are %s",
		name, strings.Join(t.formatNames(), ", "))
}

func (t ExportTools) formatNames() []string {
	out := make([]string, 0, len(t.Formats))
	for _, f := range t.Formats {
		out = append(out, f.Filename)
	}
	return out
}

// filledFormatName marks the copy.
//
// An officer with the original and the filled one in the same folder has to be
// able to tell them apart. The same mark the fill endpoint puts on its own answer,
// repeated rather than shared because it is one line and the other lives in the
// api package.
func filledFormatName(original string) string {
	base := strings.TrimSuffix(original, ".xlsx")
	base = strings.TrimSuffix(base, ".XLSX")
	return base + " (filled).xlsx"
}

// chosenFields reads the columns the model asked for.
//
// Nothing is dropped silently. A column that does not exist comes back with the
// list of the ones that do, so the model can fix it in the same turn, and the
// two that are absent on purpose come back saying so.
func chosenFields(args map[string]any) ([]string, error) {
	raw, ok := args["fields"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		if single, ok := raw.(string); ok {
			// A model asked for one column sometimes sends the string rather
			// than a list of one. Taken as meant: refusing it would spend a turn
			// teaching the model JSON.
			list = []any{single}
		} else {
			return nil, errors.New(
				"fields must be a list of column names, as strings")
		}
	}

	known := FieldNames()
	out := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, errors.New(
				"fields must be a list of column names, as strings")
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if withheld := withheldFieldRefusal(name); withheld != nil {
			return nil, withheld
		}
		if !known[name] {
			return nil, fmt.Errorf("there is no column called %q; the columns "+
				"are %s", name, exportFieldVocabulary())
		}
		out = append(out, name)
	}
	return out, nil
}

// withheldFieldRefusal answers for the columns that are absent on purpose.
//
// A farmer's registry id and their Aadhaar number are not in fields.go and will
// not be: an officer reads a claim by reference and a farmer by name, and a
// document circulated through a taluka office is the last place either belongs.
// Named here so that the refusal says which it was and what to use instead,
// rather than inviting the model to guess at a spelling that works.
func withheldFieldRefusal(name string) error {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "aadhaar"), strings.Contains(lower, "aadhar"),
		strings.Contains(lower, "uid"):
		return fmt.Errorf("%q is not a column this exports and will not be: an "+
			"Aadhaar number must not travel in a claims document. Nothing here "+
			"can put one in a file", name)
	case strings.Contains(lower, "farmer_id"), lower == "farmerid",
		strings.Contains(lower, "registry_id"):
		return fmt.Errorf("%q is not a column this exports and will not be: a "+
			"farmer's registry id is outside what a claims document may carry. "+
			"Use farmer_name to name the farmer, and reference to name the "+
			"claim", name)
	}
	return nil
}

// correctable turns a build or store failure into a sentence the model can act
// on in the same turn.
//
// This is the difference between an assistant that recovers and one that
// apologises. Each of these has a remedy the model can reach for by itself --
// fewer columns, a narrower set of claims, another format -- and a refusal that
// does not name the remedy spends the rest of the turn budget on guesses.
func correctable(err error) error {
	var unknown UnknownFieldError
	var tooMany TooManyRowsError
	var tooLarge TooLargeError
	var tooBig PreparedTooLargeError

	switch {
	case errors.As(err, &unknown):
		return fmt.Errorf("there is no column called %q; the columns are %s",
			unknown.Name, exportFieldVocabulary())
	case errors.Is(err, ErrNoFields):
		return fmt.Errorf("choose at least one column; the columns are %s",
			exportFieldVocabulary())
	case errors.As(err, &tooMany):
		// Rewritten rather than passed through, because the sentence export.go
		// writes offers narrowing by event and this tool has no such argument.
		// A remedy the caller cannot reach is not a remedy.
		return fmt.Errorf("more than %d claims match, which is more than one "+
			"document should carry; narrow it with state, awaiting_level or "+
			"search and prepare it again", tooMany.Limit)
	case errors.As(err, &tooLarge), errors.As(err, &tooBig):
		return err
	case errors.Is(err, ErrPDFUnavailable):
		return errors.New("the PDF converter is not available on this " +
			"deployment; prepare the same document as xlsx, csv, docx or pptx " +
			"and tell the officer why")
	}
	return err
}

// preparedTitle heads the document and names the file.
//
// Built from the narrowings because that is all that distinguishes one of these
// from another, and an officer who downloads three in a morning must be able to
// tell them apart in a folder. The model has no argument for a title: a sentence
// it chose would be a sentence an attached document could have chosen.
func preparedTitle(state, awaiting, search string) string {
	var bits []string
	if state != "" {
		bits = append(bits, state)
	}
	if awaiting != "" {
		bits = append(bits, "awaiting "+awaiting)
	}
	if search != "" {
		bits = append(bits, search)
	}
	if len(bits) == 0 {
		// Empty takes export.go's own default, which is the bilingual heading a
		// statement carries when nothing narrowed it.
		return ""
	}
	return "पीक नुकसान दावे / Crop loss claims · " + strings.Join(bits, " · ")
}

// exportFieldVocabulary is the closed list, for a description and for a refusal.
//
// One function, so that a column added to fields.go appears in the tool's
// description and in the refusal that names the columns, and cannot appear in
// only one of them.
func exportFieldVocabulary() string {
	names := make([]string, 0, len(Fields))
	for _, f := range Fields {
		names = append(names, f.Name)
	}
	return strings.Join(names, ", ")
}

// claimCount says how many claims in words that read.
//
// This sentence is written for the model to relay, and an officer told that a
// statement holds "1 claims" is reading our label rather than their language.
func claimCount(n int) string {
	if n == 1 {
		return "1 claim"
	}
	return fmt.Sprintf("%d claims", n)
}

func formatNames() []string {
	out := make([]string, 0, len(ExportFormats))
	for _, f := range ExportFormats {
		out = append(out, string(f))
	}
	return out
}
