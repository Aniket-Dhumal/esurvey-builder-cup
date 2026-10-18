package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// A malformed identifier is the caller's mistake, not the server's.
//
// # What this fixes
//
// Every id in this API is a uuid, and every one of them reaches a query
// eventually. Postgres refuses a malformed one with SQLSTATE 22P02, which
// arrived at the error writers as an unrecognised error and came back as
// 500 INTERNAL with "the request could not be completed" -- and a line in the
// error log, on every one of them.
//
// Both halves of that are wrong. A 500 tells a client the server broke and
// that retrying might work, when nothing is broken and retrying cannot: the
// id in their hand is not an id. And an operator reading the logs after a
// real incident has to wade through a page of "errors" that were somebody
// pasting half a reference into a URL bar.
//
// # Why 400 and not 404
//
// 404 would be defensible -- no such record, because no such record could
// exist. 400 is the more useful answer, because the two failures have
// different remedies: a 404 says "look somewhere else for it", and what the
// caller actually needs to hear is "that is not the shape of an identifier;
// you have probably truncated or mistyped it."
//
// # Why it is caught here rather than at each path parameter
//
// A guard on r.PathValue would cover the ids in URLs and miss the ones in
// request bodies, which are just as easy to get wrong and produce exactly the
// same 500. Catching the database's own refusal covers both, and cannot drift
// out of step with a handler that grows a new id field.

// malformedIDCode is Postgres's invalid_text_representation. A uuid column
// handed something that is not a uuid raises it, as does an enum handed a
// value it does not have -- both are the caller sending a value the schema
// cannot hold, and both are 400s.
const malformedIDCode = "22P02"

// badRequestFromDB answers a caller error that surfaced as a database error,
// and reports whether it did.
//
// Called first in each write*Error switch, so that the writers keep their
// shape: named errors above, an honest 500 below, and this in between.
func badRequestFromDB(w http.ResponseWriter, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != malformedIDCode {
		return false
	}
	writeError(w, http.StatusBadRequest, "INVALID_ID",
		"that identifier is not valid. Check it was copied in full.")
	return true
}
