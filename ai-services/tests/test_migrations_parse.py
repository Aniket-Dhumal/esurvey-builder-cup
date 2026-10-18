"""Every migration must parse against the real PostgreSQL grammar.

# Why this lives in the Python suite

It is a Go concern, and it is here because `pglast` wraps libpg_query — the
actual PostgreSQL parser, the same C code the server uses. Nothing in the Go
toolchain gets that without cgo, and a hand-written check cannot approach it:
the regex checks in internal/platform/db catch ordering and existence, not
syntax.

# What it buys, and when

Migrations apply on startup, inside a transaction each, against a real
database — so a syntax error is caught the first time anyone runs the stack.
That is fine until the day the stack cannot be run: a wedged Docker, a
developer without one, a CI job whose database service failed to come up. On
30 August 2026 that day lasted several hours, with three unapplied
migrations sitting in the tree.

This is not a substitute for applying them. It cannot know that a column
exists, that a constraint is satisfiable, or that an enum value is spelled
the way the Go constant spells it. It knows the SQL is SQL, which is the one
thing that can be established with no server at all.
"""

from __future__ import annotations

import pathlib

import pytest
from pglast import parse_sql
from pglast.parser import ParseError

MIGRATIONS = pathlib.Path(__file__).resolve().parents[2] / (
    "backend/internal/platform/db/migrations"
)


def migration_files() -> list[pathlib.Path]:
    return sorted(MIGRATIONS.glob("*.sql"))


def test_the_migration_directory_is_where_we_think():
    # A path that silently stops matching would make every test below pass by
    # finding nothing — the failure mode this whole file exists to avoid.
    assert MIGRATIONS.is_dir(), f"no migrations at {MIGRATIONS}"
    assert len(migration_files()) >= 11


@pytest.mark.parametrize(
    "path", migration_files(), ids=lambda p: p.name
)
def test_migration_parses(path: pathlib.Path):
    sql = path.read_text(encoding="utf-8")
    try:
        statements = parse_sql(sql)
    except ParseError as exc:
        pytest.fail(f"{path.name} does not parse: {exc}")

    assert statements, f"{path.name} contains no statements"


def test_no_migration_is_accidentally_empty():
    # An empty file records itself as applied and does nothing, which is worse
    # than failing: the checksum is stored and the schema silently lacks
    # whatever the migration was meant to add.
    for path in migration_files():
        assert path.read_text(encoding="utf-8").strip(), f"{path.name} is empty"
