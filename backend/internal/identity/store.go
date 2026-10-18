package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Store reads and writes accounts, jurisdictions and grants.
type Store struct{ pool *db.Pool }

// NewStore builds a store over a connection pool.
func NewStore(pool *db.Pool) *Store { return &Store{pool: pool} }

// userColumns is the projection every user read shares. Written once so a
// column added to one query cannot go missing from another.
const userColumns = `
	u.id, u.kind, u.status, u.full_name, COALESCE(u.email, ''),
	COALESCE(u.username, ''), COALESCE(u.password_hash, ''),
	u.must_change_password, COALESCE(u.mobile_e164, ''),
	COALESCE(u.agristack_farmer_id, ''), COALESCE(u.aadhaar_last4, ''),
	COALESCE(u.aadhaar_hash, ''),
	u.password_changed_at, u.last_login_at, u.created_at, u.updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Kind, &u.Status, &u.FullName, &u.Email,
		&u.Username, &u.PasswordHash, &u.MustChangePassword, &u.MobileE164,
		&u.AgriStackFarmerID, &u.AadhaarLast4, &u.AadhaarHash,
		&u.PasswordChangedAt, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ByUsername looks up an officer. Grants are loaded with it: an account
// without its grants is an account whose permissions default to none, and a
// caller that forgot the second query would see that as a silent denial
// rather than an error.
// Either the username or the email address, because officers are told to sign
// in with the second.
//
// Accounts created since the invitation flow have their email as their
// username, so for them the two are the same string. Accounts created before
// it do not, and the invitation and reset emails both say "sign in with your
// email address" -- which was false for exactly the officers most likely to
// have forgotten their password.
//
// An exact username match wins. Two accounts could in principle collide, one
// person's username being another's address; ordering makes that resolve the
// same way every time instead of depending on the plan.
func (s *Store) ByUsername(ctx context.Context, username string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userColumns+`
		   FROM users u
		  WHERE lower(u.username) = lower($1)
		     OR lower(COALESCE(u.email, '')) = lower($1)
		  ORDER BY (lower(u.username) = lower($1)) DESC
		  LIMIT 1`,
		strings.TrimSpace(username)))
	if err != nil {
		return nil, err
	}
	return s.withGrants(ctx, u)
}

// ByMobile looks up a farmer by handset.
func (s *Store) ByMobile(ctx context.Context, e164 string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u WHERE u.mobile_e164 = $1`, e164))
	if err != nil {
		return nil, err
	}
	return s.withGrants(ctx, u)
}

// ByID looks up any account.
func (s *Store) ByID(ctx context.Context, id string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u WHERE u.id = $1`, id))
	if err != nil {
		return nil, err
	}
	return s.withGrants(ctx, u)
}

// ByFarmerID looks up a farmer by their registry identifier.
func (s *Store) ByFarmerID(ctx context.Context, farmerID string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u WHERE u.agristack_farmer_id = $1`, farmerID))
	if err != nil {
		return nil, err
	}
	return s.withGrants(ctx, u)
}

// Count returns the number of accounts, used to decide whether a deployment
// needs bootstrapping.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) withGrants(ctx context.Context, u *User) (*User, error) {
	grants, err := s.Grants(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	u.Grants = grants
	return u, nil
}

// Grants returns a user's live role grants.
func (s *Store) Grants(ctx context.Context, userID string) ([]rbac.Grant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ur.role,
		       COALESCE(j.id::text, ''), COALESCE(j.code, ''),
		       COALESCE(j.level::text, ''), COALESCE(j.name, '')
		FROM user_roles ur
		LEFT JOIN jurisdictions j ON j.id = ur.jurisdiction_id
		WHERE ur.user_id = $1 AND ur.revoked_at IS NULL
		ORDER BY ur.granted_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("reading grants: %w", err)
	}
	defer rows.Close()

	var out []rbac.Grant
	for rows.Next() {
		var g rbac.Grant
		var role, level string
		if err := rows.Scan(&role, &g.JurisdictionID, &g.JurisdictionCode,
			&level, &g.JurisdictionName); err != nil {
			return nil, err
		}
		g.Role = rbac.Role(role)
		g.JurisdictionLevel = rbac.Level(level)
		if !g.Role.Valid() {
			// The enum in the database and the constants in Go have drifted.
			// Dropping the grant fails closed, which is the only safe
			// direction, but it is a deployment error and says so.
			return nil, fmt.Errorf(
				"account %s holds role %q, which this build does not know",
				userID, role)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CreateOfficer inserts an officer account.
func (s *Store) CreateOfficer(ctx context.Context, in NewOfficer) (*User, error) {
	id, err := insertOfficer(ctx, s.pool, in)
	if err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

// insertOfficer is the row itself, on any Querier, so that account creation
// can run inside the transaction that also grants the posting and queues the
// invitation.
func insertOfficer(ctx context.Context, q db.Querier, in NewOfficer) (string, error) {
	// The login is the email address, and nothing else. Whatever the caller
	// put in Username and Email, the row gets one validated address in both
	// -- so no path into this store, bootstrap included, can create an
	// officer who signs in with anything but their email. The database holds
	// the same rule (migration 0046).
	addr := in.Email
	if addr == "" {
		addr = in.Username
	}
	normal, err := NormalizeEmail(addr)
	if err != nil {
		return "", err
	}
	if in.Username != "" && !strings.EqualFold(strings.TrimSpace(in.Username), normal) {
		return "", fmt.Errorf("%w: the login must be the email address itself", ErrInvalidEmail)
	}
	in.Email, in.Username = normal, normal

	var createdBy *string
	if in.CreatedBy != "" {
		createdBy = &in.CreatedBy
	}
	var email *string
	if in.Email != "" {
		email = &in.Email
	}
	// An invited account has no password yet, and NULL is how the schema says
	// so -- an empty string would be a hash that no password can produce and
	// every comparison would fail with "wrong password" rather than "this
	// account has not been claimed".
	var hash *string
	if in.PasswordHash != "" {
		hash = &in.PasswordHash
	}
	// password_changed_at belongs to a password. Recording now() for an
	// account that has none would date a credential that does not exist, and
	// any future password-age policy would read it as freshly set.
	var changedAt *time.Time
	if hash != nil {
		t := time.Now()
		changedAt = &t
	}

	var id string
	err = q.QueryRow(ctx, `
		INSERT INTO users (kind, full_name, email, username, password_hash,
		                   must_change_password, password_changed_at, created_by)
		VALUES ('officer', $1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		in.FullName, email, in.Username, hash,
		in.MustChangePassword, changedAt, createdBy).Scan(&id)
	if err != nil {
		return "", wrapConstraint(err)
	}
	return id, nil
}

// InvitedOfficer is one account, its postings, and the invitation that lets
// its owner claim it -- created together or not at all.
type InvitedOfficer struct {
	Officer NewOfficer

	// Postings is every place this officer is posted at once. A Talathi with
	// four villages is four rows, and creating three of them and failing on
	// the fourth would leave an officer who cannot see part of their own
	// charge and no record of why.
	Postings []Posting

	// MobileE164 is optional for an officer: useful for reaching them, and
	// not a credential -- officers sign in with a password, never an OTP.
	MobileE164 string
}

// Posting is one role in one place.
type Posting struct {
	Role           rbac.Role
	JurisdictionID string
}

// CreateInvitedOfficer creates the account, its postings and its invitation in
// one transaction, and calls queueEmail inside it.
//
// The callback is the outbox: the intent to email has to commit with the
// account that caused it. Queued separately and the process dies in between,
// an account exists that nobody was ever told about and nobody can claim --
// the administrator sees it in the list, its owner never hears, and the only
// remedy is noticing.
func (s *Store) CreateInvitedOfficer(
	ctx context.Context,
	in InvitedOfficer,
	queueEmail func(ctx context.Context, q db.Querier, token string, expires time.Time) error,
) (*User, error) {
	if len(in.Postings) == 0 {
		return nil, fmt.Errorf("%w: an officer must be posted somewhere", ErrNoPosting)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	id, err := insertOfficer(ctx, tx, in.Officer)
	if err != nil {
		return nil, err
	}

	if in.MobileE164 != "" {
		if _, err := tx.Exec(ctx,
			`UPDATE users SET mobile_e164 = $2 WHERE id = $1`, id, in.MobileE164); err != nil {
			return nil, wrapConstraint(err)
		}
	}

	for _, p := range in.Postings {
		var jur *string
		if p.JurisdictionID != "" {
			jur = &p.JurisdictionID
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_roles (user_id, role, jurisdiction_id, granted_by)
			VALUES ($1, $2, $3, $4)`,
			id, string(p.Role), jur, nullable(in.Officer.CreatedBy)); err != nil {
			return nil, wrapConstraint(err)
		}
	}

	token, expires, err := s.CreateInvite(ctx, tx, id, in.Officer.Email, in.Officer.CreatedBy)
	if err != nil {
		return nil, err
	}
	if queueEmail != nil {
		if err := queueEmail(ctx, tx, token, expires); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ErrHandsetTaken is a handset already acting as a different farmer.
var ErrHandsetTaken = errors.New("that handset already belongs to another farmer")

// NameFarmer attaches a registry identity to an account that has none.
//
// A handset can now sign in before anybody has enrolled it, holding nothing
// but its own number until it proves a Farmer ID and a date of birth. When an
// officer later enrols the farmer whose registry number that is, there is
// already an account on it -- and the mobile column is unique, so creating a
// second one fails. This fills in the first instead, which is also the right
// answer: it is the same person and the same handset, and their existing
// claims stay theirs.
//
// Refuses to overwrite an identity that is already there. Two farmer ids on
// one account is not a migration, it is a mistake with somebody's land in it.
func (s *Store) NameFarmer(ctx context.Context, userID string, in NewFarmer) (*User, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE users
		   SET full_name           = $2,
		       agristack_farmer_id = $3,
		       aadhaar_hash        = COALESCE(NULLIF($4, ''), aadhaar_hash),
		       aadhaar_last4       = COALESCE(NULLIF($5, ''), aadhaar_last4),
		       updated_at          = now()
		 WHERE id = $1
		   AND kind = 'farmer'
		   AND (agristack_farmer_id IS NULL OR agristack_farmer_id = ''
		        OR agristack_farmer_id = $3)`,
		userID, in.FullName, in.AgriStackFarmerID, in.AadhaarHash, in.AadhaarLast4)
	if err != nil {
		return nil, wrapConstraint(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrHandsetTaken
	}
	return s.ByID(ctx, userID)
}

// SetMobile puts a handset on an account, or takes it off with "".
//
// The number is a second factor for an officer (see auth.LoginOfficer), so it
// arrives here already verified by a code sent to it. Empty clears it, which
// is what a lost phone needs: an officer who cannot receive a code must be
// able to fall back to their password rather than telephone an administrator.
func (s *Store) SetMobile(ctx context.Context, userID, mobile string) (*User, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE users SET mobile_e164 = NULLIF($2, ''), updated_at = now()
		 WHERE id = $1`, userID, mobile)
	if err != nil {
		return nil, wrapConstraint(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.ByID(ctx, userID)
}

// CreateFarmer enrols a farmer account from registry data.
func (s *Store) CreateFarmer(ctx context.Context, in NewFarmer) (*User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO users (kind, full_name, mobile_e164, agristack_farmer_id,
		                   aadhaar_hash, aadhaar_last4)
		-- Every one of these is NULL when absent rather than empty. The farmer
		-- id matters most: the column is UNIQUE, so a second account with ''
		-- collides with the first, and a handset that signs in before anybody
		-- has enrolled it has no farmer id yet.
		VALUES ('farmer', $1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''))
		RETURNING id`,
		in.FullName, in.MobileE164, in.AgriStackFarmerID,
		in.AadhaarHash, in.AadhaarLast4).Scan(&id)
	if err != nil {
		return nil, wrapConstraint(err)
	}

	// A farmer's grant carries no jurisdiction: their authority is over their
	// own records rather than over a place.
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_roles (user_id, role) VALUES ($1, 'farmer')`, id); err != nil {
		return nil, wrapConstraint(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.ByID(ctx, id)
}

// SetPassword replaces an officer's password.
func (s *Store) SetPassword(ctx context.Context, userID, hash string, mustChange bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE users
		SET password_hash = $2, must_change_password = $3, password_changed_at = now()
		WHERE id = $1 AND kind = 'officer'`, userID, hash, mustChange)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchLastLogin records a successful sign-in.
func (s *Store) TouchLastLogin(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return err
}

// SetStatus suspends, deactivates or restores an account.
func (s *Store) SetStatus(ctx context.Context, userID string, status Status) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET status = $2 WHERE id = $1`, userID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GrantRole posts a user to a role in a jurisdiction.
func (s *Store) GrantRole(
	ctx context.Context, userID string, role rbac.Role, jurisdictionID, grantedBy string,
) error {
	var jur, by *string
	if jurisdictionID != "" {
		jur = &jurisdictionID
	}
	if grantedBy != "" {
		by = &grantedBy
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role, jurisdiction_id, granted_by)
		VALUES ($1, $2, $3, $4)`, userID, string(role), jur, by)
	return wrapConstraint(err)
}

// RevokeRole ends a posting.
//
// The row is marked, never deleted. Who could see what, and when, is exactly
// the question an investigation asks after the fact.
func (s *Store) RevokeRole(ctx context.Context, userID string, role rbac.Role, jurisdictionID string) error {
	var jur *string
	if jurisdictionID != "" {
		jur = &jurisdictionID
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE user_roles SET revoked_at = now()
		WHERE user_id = $1 AND role = $2
		  AND jurisdiction_id IS NOT DISTINCT FROM $3
		  AND revoked_at IS NULL`, userID, string(role), jur)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFilter narrows a user listing.
type ListFilter struct {
	Kind rbac.Kind
	Role rbac.Role
	// RolesIn restricts to accounts holding at least one of these roles. The
	// product administrator's view of the roll: State Officers and other
	// administrators, never the officers a state appoints itself.
	RolesIn []rbac.Role
	// Status narrows to active, suspended or deactivated. Empty is every
	// status, which is what the roll shows by default -- a suspended officer
	// hidden from the list is one nobody can re-enable.
	Status Status
	Query  string
	// JurisdictionIDs restricts to accounts posted inside the caller's own
	// reach. An officer listing users sees their own district, not the state.
	JurisdictionIDs []string
	Page            page.Params
}

// userMatches is the whole WHERE, shared by the count and the page so a total
// cannot be computed against different predicates than the rows it describes.
//
// The DISTINCT and the count(DISTINCT u.id) go together: the role join
// multiplies a user by their grants, so an officer holding two roles would
// otherwise be counted twice and the pager would promise rows that are not
// there.
const userMatches = `($1::text = '' OR u.kind::text = $1)
	  AND ($2::text = '' OR ur.role::text = $2)
	  AND ($3::text = '' OR u.full_name ILIKE $3 OR u.username ILIKE $3
	                     OR COALESCE(u.email, '') ILIKE $3
	                     OR COALESCE(u.mobile_e164, '') ILIKE $3)
	  AND ($4::uuid[] IS NULL
	       OR j.id = ANY($4)
	       OR j.ancestor_ids && $4)
	  AND ($5::text = '' OR u.status::text = $5)
	  AND ($R::text[] IS NULL OR EXISTS (
	       SELECT 1 FROM user_roles rr
	        WHERE rr.user_id = u.id AND rr.revoked_at IS NULL
	          AND rr.role::text = ANY($R::text[])))`

const userFrom = `FROM users u
		LEFT JOIN user_roles ur ON ur.user_id = u.id AND ur.revoked_at IS NULL
		LEFT JOIN jurisdictions j ON j.id = ur.jurisdiction_id`

// List returns accounts matching the filter.
func (s *Store) List(ctx context.Context, f ListFilter) (page.Result[*User], error) {
	jur := nullableUUIDs(f.JurisdictionIDs)
	search := ""
	if q := page.CleanQuery(f.Query); q != "" {
		// Escaped rather than interpolated. A search for "50_percent" was
		// matching "50Xpercent", because _ is a wildcard nobody typed.
		search = page.Like(q)
	}

	var roles any
	if len(f.RolesIn) > 0 {
		rs := make([]string, len(f.RolesIn))
		for i, r := range f.RolesIn {
			rs[i] = string(r)
		}
		roles = rs
	}

	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT u.id) `+userFrom+`
		WHERE `+strings.ReplaceAll(userMatches, "$R", "$6"),
		string(f.Kind), string(f.Role), search, jur, string(f.Status), roles).Scan(&total); err != nil {
		return page.Result[*User]{}, fmt.Errorf("counting users: %w", err)
	}
	if total == 0 {
		return page.Wrap([]*User{}, 0, f.Page), nil
	}

	// Scoping is part of the query rather than a filter applied afterwards.
	// Loading the state and trimming it in Go is one forgotten early return
	// away from a leak, and it does not paginate correctly either.
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT `+userColumns+`
		`+userFrom+`
		WHERE `+strings.ReplaceAll(userMatches, "$R", "$8")+`
		ORDER BY u.created_at DESC, u.id DESC
		LIMIT $6 OFFSET $7`,
		string(f.Kind), string(f.Role), search, jur, string(f.Status),
		f.Page.Limit, f.Page.Offset, roles)
	if err != nil {
		return page.Result[*User]{}, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	out := []*User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.Kind, &u.Status, &u.FullName, &u.Email,
			&u.Username, &u.PasswordHash, &u.MustChangePassword, &u.MobileE164,
			&u.AgriStackFarmerID, &u.AadhaarLast4, &u.AadhaarHash,
			&u.PasswordChangedAt, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return page.Result[*User]{}, err
		}
		out = append(out, &u)
	}
	if err := rows.Err(); err != nil {
		return page.Result[*User]{}, err
	}

	// One query per user for their grants. Bounded by the page size, which is
	// the reason the page size is bounded rather than left to the caller.
	for _, u := range out {
		g, err := s.Grants(ctx, u.ID)
		if err != nil {
			return page.Result[*User]{}, err
		}
		u.Grants = g
	}
	return page.Wrap(out, total, f.Page), nil
}

// --- jurisdictions ---------------------------------------------------------

// JurisdictionByID reads one node.
func (s *Store) JurisdictionByID(ctx context.Context, id string) (*Jurisdiction, error) {
	return scanJurisdiction(s.pool.QueryRow(ctx, `
		SELECT id, level, code, name, COALESCE(parent_id::text, '')
		FROM jurisdictions WHERE id = $1`, id))
}

// JurisdictionByCode reads one node by level and code.
func (s *Store) JurisdictionByCode(
	ctx context.Context, level rbac.Level, code string,
) (*Jurisdiction, error) {
	return scanJurisdiction(s.pool.QueryRow(ctx, `
		SELECT id, level, code, name, COALESCE(parent_id::text, '')
		FROM jurisdictions WHERE level = $1 AND code = $2`, string(level), code))
}

func scanJurisdiction(row pgx.Row) (*Jurisdiction, error) {
	var j Jurisdiction
	var level string
	err := row.Scan(&j.ID, &level, &j.Code, &j.Name, &j.ParentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.Level = rbac.Level(level)
	return &j, nil
}

// ListJurisdictions returns the children of a node, or the roots when parent
// is empty.
// ListJurisdictions returns one page of a node's children.
//
// # Why this one needed a page
//
// It is a drill-down, so the natural assumption is that a node has few
// children and the whole list is safe to return. That holds for a state's
// districts and a district's talukas, and stops holding one level lower: the
// largest taluka in Maharashtra has 327 villages, twenty-three have more than
// two hundred, and the average is a hundred and fifteen.
//
// So this was the one listing in the service returning more rows than
// page.MaxLimit allows anybody else to ask for, and it did it on the picker an
// officer uses to choose where to post somebody. A search matters as much as
// the page: nobody finds their village by scrolling three hundred names.
func (s *Store) ListJurisdictions(
	ctx context.Context, parentID, level string, p page.Params,
) (page.Result[*Jurisdiction], error) {
	search := ""
	if p.Query != "" {
		search = page.Like(p.Query)
	}

	// One predicate for both queries. parent_id IS NULL selects the roots,
	// which is how the tree is entered.
	// The level filter is what makes this usable as a picker. A taluka's
	// children are its villages -- 327 of them in the largest -- and a form
	// choosing between talukas and circles wants neither the villages nor a
	// page of them. Without it the caller would have to ask for everything
	// under a node and throw most of it away, which is the same query with
	// the waste moved.
	// LVL is substituted per query, because the count and the page number
	// their placeholders differently and Postgres refuses a statement whose
	// parameters it cannot all see -- an unreferenced $3 in the count is an
	// error, not a spare argument.
	const where = `
		($1::uuid IS NULL AND parent_id IS NULL OR parent_id = $1::uuid)
		AND ($2::text = '' OR name ILIKE $2 OR code ILIKE $2)
		AND (LVL::text = '' OR level::text = LVL)`

	var parent any
	if parentID != "" {
		parent = parentID
	}

	var total int
	if err := s.pool.QueryRow(ctx,
		page.CountSQL(`SELECT 1 FROM jurisdictions WHERE `+
			strings.ReplaceAll(where, "LVL", "$3")),
		parent, search, level).Scan(&total); err != nil {
		return page.Result[*Jurisdiction]{}, fmt.Errorf("counting jurisdictions: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, level, code, name, COALESCE(parent_id::text, '')
		FROM jurisdictions WHERE `+strings.ReplaceAll(where, "LVL", "$5")+`
		ORDER BY name, code
		LIMIT $3 OFFSET $4`, parent, search, p.Limit, p.Offset, level)
	if err != nil {
		return page.Result[*Jurisdiction]{}, fmt.Errorf("listing jurisdictions: %w", err)
	}
	defer rows.Close()

	out := []*Jurisdiction{}
	for rows.Next() {
		var j Jurisdiction
		var level string
		if err := rows.Scan(&j.ID, &level, &j.Code, &j.Name, &j.ParentID); err != nil {
			return page.Result[*Jurisdiction]{}, err
		}
		j.Level = rbac.Level(level)
		out = append(out, &j)
	}
	if err := rows.Err(); err != nil {
		return page.Result[*Jurisdiction]{}, err
	}
	return page.Wrap(out, total, p), nil
}

// VillageInScope reports whether a village code lies inside any of the given
// jurisdictions.
//
// The containment test is an array overlap against the materialised ancestor
// chain, which the GIN index answers directly. A recursive walk per request
// would not survive a district-wide listing.
func (s *Store) VillageInScope(
	ctx context.Context, villageCode string, jurisdictionIDs []string,
) (bool, error) {
	if len(jurisdictionIDs) == 0 {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM jurisdictions j
			WHERE j.level = 'village' AND j.code = $1
			  AND (j.id = ANY($2) OR j.ancestor_ids && $2)
		)`, villageCode, jurisdictionIDs).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("checking jurisdiction: %w", err)
	}
	return ok, nil
}

// VillagesInScope narrows a set of village codes to those inside a posting.
//
// The bulk form of VillageInScope, and it exists because the per-village
// version cannot be used for a listing: asking it once per farmer is one
// query per row, and the alternative -- not filtering at all -- is what the
// farmer listing was doing. A Talathi posted to one village could enumerate
// every farmer in the registry.
//
// Returns the subset that is in scope. An empty input is an empty answer, not
// "everything": a filter that fails open on an empty list is the kind of
// mistake that only shows up as a leak.
func (s *Store) VillagesInScope(
	ctx context.Context, villageCodes []string, jurisdictionIDs []string,
) (map[string]bool, error) {
	out := map[string]bool{}
	if len(villageCodes) == 0 || len(jurisdictionIDs) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT j.code FROM jurisdictions j
		WHERE j.level = 'village' AND j.code = ANY($1)
		  AND (j.id = ANY($2) OR j.ancestor_ids && $2)`,
		villageCodes, jurisdictionIDs)
	if err != nil {
		return nil, fmt.Errorf("checking jurisdictions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		out[code] = true
	}
	return out, rows.Err()
}

// nullableUUIDs turns an empty slice into a SQL NULL, so "no filter" and
// "filter matching nothing" stay distinguishable in the query.
func nullableUUIDs(ids []string) any {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// wrapConstraint turns a unique-violation into ErrConflict, naming the field
// that collided rather than surfacing a constraint name to an API client.
func wrapConstraint(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch {
		case strings.Contains(pgErr.ConstraintName, "username"):
			return fmt.Errorf("%w: username is taken", ErrConflict)
		case strings.Contains(pgErr.ConstraintName, "mobile"):
			return fmt.Errorf("%w: that mobile number is already registered", ErrConflict)
		case strings.Contains(pgErr.ConstraintName, "agristack_farmer_id"):
			return fmt.Errorf("%w: that farmer is already enrolled", ErrConflict)
		case strings.Contains(pgErr.ConstraintName, "user_roles_live"):
			return fmt.Errorf("%w: that role is already held in that jurisdiction", ErrConflict)
		}
		return ErrConflict
	}
	return err
}

// WithinAny reports whether a jurisdiction is one of the given nodes or sits
// beneath one of them.
//
// The containment half of a delegation check: rank says a Tehsildar may create
// a Talathi, and this says the village they are posting them to is inside that
// Tehsildar's own tehsil rather than the one next door.
//
// Answered from ancestor_ids, which the tree maintains, rather than by walking
// parents in Go -- a loop that issues one query per level is a loop somebody
// eventually bounds at "two or three should be enough".
func (s *Store) WithinAny(ctx context.Context, jurisdictionID string, ancestors []string) (bool, error) {
	if jurisdictionID == "" || len(ancestors) == 0 {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT j.id = ANY($2::uuid[]) OR j.ancestor_ids && $2::uuid[]
		  FROM jurisdictions j
		 WHERE j.id = $1`, jurisdictionID, ancestors).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return ok, err
}

// SearchJurisdictions finds nodes at one level anywhere inside a set of
// ancestors.
//
// # Why this is not ListJurisdictions with the parent left out
//
// That one walks the tree a level at a time: it answers "the children of this
// node", and with no parent it answers "the roots". Correct for a drill-down,
// useless for a picker -- a Tehsildar posting a Talathi types "Alipur", and
// the village they mean is two levels below them, not a child of anything
// they have selected.
//
// # Why the scope is the authorization, not a filter
//
// The ancestors passed in are the caller's own postings. A Tehsildar searching
// villages gets the villages of their tehsil and no others, which is the same
// boundary the delegation check applies when the posting is submitted -- so
// the picker cannot offer a place the create would then refuse.
func (s *Store) SearchJurisdictions(
	ctx context.Context, level string, ancestors []string, p page.Params,
) (page.Result[*Jurisdiction], error) {
	if level == "" {
		return page.Wrap([]*Jurisdiction{}, 0, p), nil
	}
	search := ""
	if p.Query != "" {
		search = page.Like(p.Query)
	}
	// A nil ancestor set means "everywhere", which is what a system
	// administrator gets: they hold no posting and manage the whole roll.
	scope := nullableUUIDs(ancestors)

	const where = `
		level::text = LVL
		AND ($2::text = '' OR name ILIKE $2 OR code ILIKE $2)
		AND ($3::uuid[] IS NULL
		     OR id = ANY($3)
		     OR ancestor_ids && $3)`

	var total int
	if err := s.pool.QueryRow(ctx,
		page.CountSQL(`SELECT 1 FROM jurisdictions WHERE `+
			strings.ReplaceAll(where, "LVL", "$1")),
		level, search, scope).Scan(&total); err != nil {
		return page.Result[*Jurisdiction]{}, fmt.Errorf("counting jurisdictions: %w", err)
	}
	if total == 0 {
		return page.Wrap([]*Jurisdiction{}, 0, p), nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, level, code, name, COALESCE(parent_id::text, '')
		FROM jurisdictions WHERE `+strings.ReplaceAll(where, "LVL", "$1")+`
		ORDER BY name, code
		LIMIT $4 OFFSET $5`, level, search, scope, p.Limit, p.Offset)
	if err != nil {
		return page.Result[*Jurisdiction]{}, fmt.Errorf("searching jurisdictions: %w", err)
	}
	defer rows.Close()

	out := []*Jurisdiction{}
	for rows.Next() {
		var j Jurisdiction
		var lvl string
		if err := rows.Scan(&j.ID, &lvl, &j.Code, &j.Name, &j.ParentID); err != nil {
			return page.Result[*Jurisdiction]{}, err
		}
		j.Level = rbac.Level(lvl)
		out = append(out, &j)
	}
	return page.Wrap(out, total, p), rows.Err()
}
