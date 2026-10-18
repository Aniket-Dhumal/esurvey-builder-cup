package db

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// Static checks over the migration set, needing no database.
//
// # Why these exist
//
// The migration tests that apply to a real database are the real check, and
// they only run where one is reachable. These catch the subset of errors that
// are visible in the text alone -- and that subset is not academic: it is
// exactly what has gone wrong here twice.
//
//   - A retention sweep was written against `assessment_images.id`. That
//     table is keyed by assessment_id and has no id column.
//   - The same sweep set survey_captures.lat to NULL, on a column that was
//     NOT NULL, so the anonymiser could never have run.
//
// Both were caught by reading the migration files by hand. Neither would have
// been caught by anything automatic until a query ran in production. The
// checks below are ordering and existence: a later migration must not depend
// on something no earlier migration creates.

var (
	reCreateTable = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	reCreateType  = regexp.MustCompile(`(?i)CREATE\s+TYPE\s+([a-z_][a-z0-9_]*)`)
	reCreateFunc  = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([a-z_][a-z0-9_]*)`)
	reCreateSeq   = regexp.MustCompile(`(?i)CREATE\s+SEQUENCE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)

	reAlterTable = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([a-z_][a-z0-9_]*)`)
	reIndexOn    = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?[a-z_][a-z0-9_]*\s+ON\s+([a-z_][a-z0-9_]*)`)
	// The word boundary matters: "notification_preferences" ends in
	// "references", so without it the word after that table name is read as a
	// foreign key target. That was the first thing these checks got wrong
	// about themselves.
	reReferences  = regexp.MustCompile(`(?i)\bREFERENCES\s+([a-z_][a-z0-9_]*)`)
	reTriggerOn   = regexp.MustCompile(`(?i)CREATE\s+TRIGGER\s+[a-z_][a-z0-9_]*\s+[\s\S]*?\bON\s+([a-z_][a-z0-9_]*)`)
	reExecuteFunc = regexp.MustCompile(`(?i)EXECUTE\s+(?:FUNCTION|PROCEDURE)\s+([a-z_][a-z0-9_]*)`)
	reCastToType  = regexp.MustCompile(`::([a-z_][a-z0-9_]*)`)
)

// builtinTypes are the casts that need no CREATE TYPE.
var builtinTypes = map[string]bool{
	"text": true, "int": true, "integer": true, "bigint": true, "smallint": true,
	"numeric": true, "decimal": true, "real": true, "boolean": true, "bool": true,
	"uuid": true, "jsonb": true, "json": true, "date": true, "timestamptz": true,
	"timestamp": true, "interval": true, "bytea": true, "inet": true, "float8": true,
	"double": true, "geometry": true, "geography": true, "regclass": true,
	"varchar": true, "char": true, "time": true, "timetz": true, "money": true,
}

// tablesProvidedByExtensions are created by CREATE EXTENSION, not by us.
var extensionObjects = map[string]bool{
	"spatial_ref_sys": true, "geometry_columns": true, "geography_columns": true,
}

// stripSQL removes comments and string literals.
//
// Without this, a table name inside a comment or a policy description reads as
// a reference — and the retention migration's `basis` text is full of prose
// that mentions table names.
func stripSQL(s string) string {
	// Line comments.
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	out := b.String()

	// Single-quoted literals, including doubled-quote escapes.
	var kept strings.Builder
	inLiteral := false
	for i := 0; i < len(out); i++ {
		if out[i] == '\'' {
			inLiteral = !inLiteral
			continue
		}
		if !inLiteral {
			kept.WriteByte(out[i])
		}
	}
	return kept.String()
}

func names(re *regexp.Regexp, sql string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(sql, -1) {
		out = append(out, strings.ToLower(m[1]))
	}
	return out
}

// danglingTableRefs returns every reference to a table no earlier migration
// creates.
//
// Separated from the test so it can be run against deliberately broken input
// below. A check that cannot fail reports success.
func danglingTableRefs(migrations []Migration) []string {
	known := map[string]string{}
	for k := range extensionObjects {
		known[k] = "extension"
	}

	var problems []string
	for _, m := range migrations {
		sql := stripSQL(m.SQL)

		// Everything this file creates is available to itself: a migration
		// routinely creates a table and indexes it in the same file.
		for _, tbl := range names(reCreateTable, sql) {
			known[tbl] = m.Name
		}

		check := func(kind string, found []string) {
			for _, tbl := range found {
				if _, ok := known[tbl]; !ok {
					problems = append(problems, fmt.Sprintf(
						"%s: %s %q, which no migration creates", m.Name, kind, tbl))
				}
			}
		}
		check("ALTER TABLE", names(reAlterTable, sql))
		check("CREATE INDEX ON", names(reIndexOn, sql))
		check("REFERENCES", names(reReferences, sql))
		check("CREATE TRIGGER ON", names(reTriggerOn, sql))
	}
	return problems
}

func TestEveryReferencedTableIsCreatedFirst(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range danglingTableRefs(migrations) {
		t.Error(p)
	}
}

func TestTheDanglingReferenceCheckActuallyFires(t *testing.T) {
	// The four shapes it is meant to catch, each against a table nothing
	// creates. If any of these passes, the check is decorative.
	cases := map[string]string{
		"foreign key": `CREATE TABLE a (id uuid REFERENCES nowhere (id));`,
		"alter":       `ALTER TABLE nowhere ADD COLUMN x text;`,
		"index":       `CREATE INDEX i ON nowhere (x);`,
		"trigger":     `CREATE TRIGGER t BEFORE UPDATE ON nowhere FOR EACH ROW EXECUTE FUNCTION f();`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			got := danglingTableRefs([]Migration{{Name: "test", SQL: sql}})
			if len(got) == 0 {
				t.Errorf("a reference to a table nothing creates was accepted: %s", sql)
			}
		})
	}
}

func TestTheCheckToleratesWhatIsLegitimate(t *testing.T) {
	// A migration that creates a table and immediately indexes it is the
	// normal shape, and must not be reported.
	sql := `
		CREATE TABLE thing (id uuid PRIMARY KEY, parent uuid REFERENCES thing (id));
		CREATE INDEX thing_idx ON thing (id);
		ALTER TABLE thing ADD COLUMN note text;`
	if got := danglingTableRefs([]Migration{{Name: "test", SQL: sql}}); len(got) > 0 {
		t.Errorf("legitimate SQL was reported: %v", got)
	}
}

func TestCommentsAndLiteralsAreNotReadAsSQL(t *testing.T) {
	// The retention migration stores policy prose that names tables. Reading
	// a comment or a quoted string as a reference would make this check cry
	// wolf until somebody turned it off.
	sql := `
		-- REFERENCES nowhere_in_a_comment (id)
		CREATE TABLE policy (basis text);
		INSERT INTO policy (basis) VALUES ('this text mentions nowhere_in_a_literal');`
	if got := danglingTableRefs([]Migration{{Name: "test", SQL: sql}}); len(got) > 0 {
		t.Errorf("prose was read as SQL: %v", got)
	}
}

func TestEveryCastTypeIsCreatedFirst(t *testing.T) {
	// A ::grievance_state cast against a type created in a later migration
	// fails at apply time, and only on a fresh database -- never on a
	// developer's, where the type already exists.
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}

	known := map[string]bool{}
	for _, m := range migrations {
		sql := stripSQL(m.SQL)
		for _, ty := range names(reCreateType, sql) {
			known[ty] = true
		}
		// A composite type is implied by a table of the same name.
		for _, tbl := range names(reCreateTable, sql) {
			known[tbl] = true
		}

		for _, ty := range names(reCastToType, sql) {
			ty = strings.TrimSuffix(ty, "[]")
			if builtinTypes[ty] || known[ty] {
				continue
			}
			t.Errorf("%s: casts to %q, which no migration creates", m.Name, ty)
		}
	}
}

func TestEveryTriggerFunctionIsCreatedFirst(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}

	known := map[string]bool{}
	for _, m := range migrations {
		sql := stripSQL(m.SQL)
		for _, fn := range names(reCreateFunc, sql) {
			known[fn] = true
		}
		for _, fn := range names(reExecuteFunc, sql) {
			if !known[fn] {
				t.Errorf("%s: trigger calls %q(), which no migration creates",
					m.Name, fn)
			}
		}
	}
}

func TestEverySequenceUsedIsCreatedFirst(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}

	known := map[string]bool{}
	reNextval := regexp.MustCompile(`(?i)nextval\(\s*'?([a-z_][a-z0-9_]*)`)

	for _, m := range migrations {
		sql := stripSQL(m.SQL)
		for _, seq := range names(reCreateSeq, sql) {
			known[seq] = true
		}
		// bigserial and serial create their own sequence implicitly.
		for _, m2 := range reNextval.FindAllStringSubmatch(sql, -1) {
			seq := strings.ToLower(m2[1])
			if !known[seq] {
				t.Errorf("%s: nextval on %q, which no migration creates",
					m.Name, seq)
			}
		}
	}
}

// TestMigrationsDoNotDropWithoutSaying is a guard on the one thing that
// cannot be undone.
//
// A DROP in a migration is sometimes right -- 0006 dropped an enum-bound
// CHECK to change a column's type. But it should never appear without a
// comment, because the next person reading it has to know whether the data
// went with it.
func TestDestructiveStatementsAreExplained(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}

	destructive := regexp.MustCompile(`(?i)\b(DROP\s+TABLE|DROP\s+COLUMN|TRUNCATE)\b`)

	for _, m := range migrations {
		for i, line := range strings.Split(m.SQL, "\n") {
			if !destructive.MatchString(line) || strings.Contains(line, "--") {
				continue
			}
			// Look for a comment in the five lines above it.
			explained := false
			lines := strings.Split(m.SQL, "\n")
			for j := max(0, i-5); j < i; j++ {
				if strings.Contains(lines[j], "--") {
					explained = true
					break
				}
			}
			if !explained {
				t.Errorf("%s:%d drops data with no comment above it:\n  %s",
					m.Name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestNewTablesCarryTheirIndexes is a soft check with a hard reason.
//
// Every listing in this system is scoped by jurisdiction and ordered by time.
// A table holding rows that are listed, without an index supporting that, is
// a sequential scan that nobody notices until the table is large -- which is
// how the search boxes were built before migration 0008.
func TestListedTablesHaveAnOrderingIndex(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}

	all := strings.Builder{}
	for _, m := range migrations {
		all.WriteString(stripSQL(m.SQL))
		all.WriteString("\n")
	}
	sql := all.String()

	indexed := map[string]bool{}
	for _, tbl := range names(reIndexOn, sql) {
		indexed[tbl] = true
	}

	// The tables the console pages through.
	for _, tbl := range []string{
		"crop_loss_reports", "assessments", "survey_events", "users",
		"survey_captures", "grievances", "notifications", "audit_log",
	} {
		if !indexed[tbl] {
			t.Errorf("%s is listed by the console and has no index", tbl)
		}
	}
}
