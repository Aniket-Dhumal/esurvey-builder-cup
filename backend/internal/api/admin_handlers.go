package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/mail"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// createOfficerRequest is the account form.
//
// # Why there is no password field any more
//
// There was one, and whoever filled this form chose it. That put a working
// credential for somebody else's account into a WhatsApp message before its
// owner had touched it, and left it valid until they got round to changing
// it. The account is now created without a password and its owner sets one
// from a single-use link; see identity/invite.go.
//
// # Why the email is the username
//
// An officer transferred between two districts had two usernames under two
// naming conventions and no way to notice they were one person. An email
// address is already unique, already known, and is the thing they will be
// sent the invitation at -- so it is not a second identifier to keep in step
// with the first.
type createOfficerRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	// Mobile is for reaching an officer, not for signing in: officers
	// authenticate with a password, never an OTP.
	Mobile string `json:"mobile_number,omitempty"`

	// Designation is the role. Named as the form names it.
	Role string `json:"role"`

	// Postings is where the officer works. A Talathi or Krushi Sahayak
	// routinely holds several villages, and a model that could express only
	// one is how two people end up sharing a login.
	Postings []postingRequest `json:"postings"`

	// The single-posting form the mobile client and the tests still send.
	// Folded into Postings when present.
	JurisdictionLevel string `json:"jurisdiction_level,omitempty"`
	JurisdictionCode  string `json:"jurisdiction_code,omitempty"`
}

type postingRequest struct {
	JurisdictionLevel string `json:"jurisdiction_level"`
	JurisdictionCode  string `json:"jurisdiction_code"`
}

func (s *Server) createOfficer(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())

	var req createOfficerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	email := normaliseEmail(req.Email)
	if req.FullName == "" || email == "" || req.Role == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"a name, an email address and a designation are required")
		return
	}
	if _, err := identity.NormalizeEmail(email); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_EMAIL", err.Error())
		return
	}
	if !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "INVALID_EMAIL",
			"that does not look like an email address")
		return
	}

	mobile, err := normaliseMobile(req.Mobile)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_MOBILE", err.Error())
		return
	}

	role := rbac.Role(req.Role)
	if !rbac.Assignable(role) {
		writeError(w, http.StatusBadRequest, "ROLE_NOT_ASSIGNABLE",
			"role must be one of: "+joinRoles(rbac.AssignableRoles))
		return
	}

	// Accept either shape: a list of postings, or the single jurisdiction the
	// mobile client and the older tests send.
	wanted := req.Postings
	if len(wanted) == 0 && (req.JurisdictionLevel != "" || req.JurisdictionCode != "") {
		wanted = []postingRequest{{
			JurisdictionLevel: req.JurisdictionLevel,
			JurisdictionCode:  req.JurisdictionCode,
		}}
	}
	if len(wanted) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"an officer must be posted to at least one place")
		return
	}

	postings := make([]identity.Posting, 0, len(wanted))
	seen := map[string]bool{}
	var codes, ids []string
	for _, p := range wanted {
		jur, err := s.resolveJurisdiction(r, role, p.JurisdictionLevel, p.JurisdictionCode)
		if err != nil {
			s.writeIdentityError(w, err)
			return
		}
		// Rank and place, per posting. A form that lists four villages must
		// be refused as a whole if one of them is in somebody else's tehsil,
		// not created with three.
		if err := s.mayPost(r.Context(), actor, role, jur.ID); err != nil {
			if writeDelegationError(w, err) {
				return
			}
			s.writeIdentityError(w, err)
			return
		}
		if seen[jur.ID] {
			continue // the same village named twice is one posting
		}
		seen[jur.ID] = true
		postings = append(postings, identity.Posting{Role: role, JurisdictionID: jur.ID})
		codes = append(codes, jur.Code)
		ids = append(ids, jur.ID)
	}

	user, err := s.Identity.CreateInvitedOfficer(r.Context(), identity.InvitedOfficer{
		Officer: identity.NewOfficer{
			// The email is the username. One identifier, already unique,
			// already the address the invitation goes to.
			Username: email,
			FullName: req.FullName,
			Email:    email,
			// No password and nothing to change: the account cannot be signed
			// in to until its owner accepts the invitation and sets one.
			MustChangePassword: false,
			CreatedBy:          actor.UserID,
		},
		Postings:   postings,
		MobileE164: mobile,
	}, func(ctx context.Context, q db.Querier, token string, expires time.Time) error {
		return s.Mail.Queue(ctx, q, mail.Message{
			To:           email,
			Subject:      "Set up your eSurvey account",
			Body:         inviteBody(req.FullName, s.ConsoleBaseURL, token, expires),
			Purpose:      mail.PurposeInvite,
			ResourceType: "user",
		})
	})
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}

	_ = s.Audit.RecordSync(r.Context(), audit.Event{
		Action: audit.ActionUserCreated, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: user.ID,
		JurisdictionCode: firstOr(codes, ""),
		JurisdictionID:   firstOr(ids, ""),
		Detail: map[string]any{
			"email":              email,
			"role":               req.Role,
			"jurisdiction_codes": codes,
			// The control that replaced separation of duties is this record.
			// Who staffed which village, and when, is the whole of it.
			"invited": true,
		},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusCreated, map[string]any{
		"user": viewUser(user),
		// An administrator setting the platform up before a mail server
		// exists needs to be told the invitation is sitting in a queue, not
		// that it is on its way.
		"mail_configured": s.Mail.Configured(),
	})
}

// resendInvite issues a fresh invitation, superseding any live one.
//
// The one remedy for an invitation sent to a mistyped address: the old token
// stops working the moment this one is issued.
func (s *Server) resendInvite(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())
	userID := r.PathValue("userID")

	user, err := s.Identity.ByID(r.Context(), userID)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}
	if err := s.mayManage(r.Context(), actor, user); err != nil {
		if writeDelegationError(w, err) {
			return
		}
		s.writeIdentityError(w, err)
		return
	}
	if user.Email == "" {
		writeError(w, http.StatusBadRequest, "NO_EMAIL",
			"that account has no email address to send an invitation to")
		return
	}
	// An account already in use does not get a fresh way in. Resetting a
	// forgotten password is a different act with a different audit record,
	// and conflating them would make an invitation a password reset that
	// anyone holding user:update could issue silently.
	if user.PasswordChangedAt != nil {
		writeError(w, http.StatusConflict, "ALREADY_CLAIMED",
			"that account has already been set up")
		return
	}

	if err := s.Identity.ReinviteOfficer(r.Context(), user, actor.UserID,
		func(ctx context.Context, q db.Querier, token string, expires time.Time) error {
			return s.Mail.Queue(ctx, q, mail.Message{
				To:           user.Email,
				Subject:      "Set up your eSurvey account",
				Body:         inviteBody(user.FullName, s.ConsoleBaseURL, token, expires),
				Purpose:      mail.PurposeInvite,
				ResourceType: "user",
				ResourceID:   user.ID,
			})
		}); err != nil {
		s.writeIdentityError(w, err)
		return
	}

	_ = s.Audit.RecordSync(r.Context(), audit.Event{
		Action: audit.ActionUserCreated, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: user.ID,
		Detail: map[string]any{"reinvited": true, "email": user.Email},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "invited",
		"mail_configured": s.Mail.Configured(),
	})
}

// inviteBody is the email an officer receives.
//
// Plain text and short. It is read on a phone, often on a slow connection, by
// someone who was told to expect it -- so it says who it is from, what it is
// for, and when it stops working, and then gets out of the way.
func inviteBody(name, consoleURL, token string, expires time.Time) string {
	return fmt.Sprintf(`%s,

An eSurvey account has been created for you on the Post Disaster Crop Loss
Assessment Platform.

Set your password here:

%s/set-password?token=%s

This link can be used once and stops working on %s. If it has expired, ask the
officer who created your account to send another.

If you were not expecting this, you can ignore it -- the account cannot be
used until a password is set.

-- eSurvey
`, name, strings.TrimRight(consoleURL, "/"), token,
		expires.Format("2 January 2006 at 3:04 pm"))
}

func firstOr(list []string, fallback string) string {
	if len(list) == 0 {
		return fallback
	}
	return list[0]
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())
	q := r.URL.Query()

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	f := identity.ListFilter{
		Kind:   rbac.Kind(q.Get("kind")),
		Role:   rbac.Role(q.Get("role")),
		Status: identity.Status(q.Get("status")),
		Query:  pg.Query,
		Page:   pg,
	}

	// The product administrator sees the accounts it manages -- State
	// Officers and administrators -- and none of the officers a state appoints
	// or the farmers it serves. Everyone else sees the accounts inside their
	// own reach, which the query enforces rather than a filter applied
	// afterwards.
	if actor.HasRole(rbac.RoleSystemAdmin) {
		f.Kind = rbac.KindOfficer
		f.RolesIn = []rbac.Role{rbac.RoleStateOfficer, rbac.RoleSystemAdmin}
	} else {
		f.JurisdictionIDs = actor.Scope().JurisdictionIDs
		if len(f.JurisdictionIDs) == 0 {
			writeJSON(w, http.StatusOK, page.Wrap([]userView{}, 0, pg))
			return
		}
	}

	result, err := s.Identity.List(r.Context(), f)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}

	out := make([]userView, 0, len(result.Items))
	for _, u := range result.Items {
		out = append(out, viewUser(u))
	}
	writeJSON(w, http.StatusOK, page.Wrap(out, result.Total, pg))
}

type grantRequest struct {
	Role              string `json:"role"`
	JurisdictionLevel string `json:"jurisdiction_level"`
	JurisdictionCode  string `json:"jurisdiction_code"`
}

func (s *Server) grantRole(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())
	userID := r.PathValue("userID")

	var req grantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	role := rbac.Role(req.Role)
	if !rbac.Assignable(role) {
		writeError(w, http.StatusBadRequest, "ROLE_NOT_ASSIGNABLE",
			"role must be one of: "+joinRoles(rbac.AssignableRoles))
		return
	}

	jur, err := s.resolveJurisdiction(r, role, req.JurisdictionLevel, req.JurisdictionCode)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}

	// Two checks, not one. mayPost says the caller may create this role in
	// this place; mayManage says the account they are adding it to is one
	// they could have created in the first place. Without the second, a
	// Tehsildar could add a Talathi posting in their own tehsil to the
	// Collector's account and thereby edit an account far above them.
	target, err := s.Identity.ByID(r.Context(), userID)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}
	for _, check := range []error{
		s.mayManage(r.Context(), actor, target),
		s.mayPost(r.Context(), actor, role, jur.ID),
	} {
		if check != nil {
			if writeDelegationError(w, check) {
				return
			}
			s.writeIdentityError(w, check)
			return
		}
	}

	if err := s.Identity.GrantRole(r.Context(), userID, role, jur.ID, actor.UserID); err != nil {
		s.writeIdentityError(w, err)
		return
	}

	// Every session is ended. The grant is carried inside the access token,
	// so a live session would keep the old reach until it expired; forcing a
	// sign-in makes the change take effect when it was made.
	if err := s.Auth.Sessions.RevokeAllForUser(r.Context(), userID); err != nil {
		s.Log.Warn("revoking sessions after grant", "user_id", userID, "error", err)
	}

	_ = s.Audit.RecordSync(r.Context(), audit.Event{
		Action: audit.ActionRoleGranted, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: userID, JurisdictionCode: jur.Code,
		JurisdictionID: jur.ID,
		Detail:         map[string]any{"role": req.Role, "jurisdiction_code": jur.Code},
	}.FromContext(r.Context()))

	user, err := s.Identity.ByID(r.Context(), userID)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewUser(user))
}

func (s *Server) revokeRole(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())
	userID := r.PathValue("userID")

	var req grantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	jur, err := s.resolveJurisdiction(r, rbac.Role(req.Role), req.JurisdictionLevel, req.JurisdictionCode)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}

	// Ending a posting reaches as far as making one. Otherwise an officer who
	// could not create a Tehsildar could still strip one of their charge,
	// which is the same authority pointed the other way.
	target, err := s.Identity.ByID(r.Context(), userID)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}
	if err := s.mayManage(r.Context(), actor, target); err != nil {
		if writeDelegationError(w, err) {
			return
		}
		s.writeIdentityError(w, err)
		return
	}
	// The last posting cannot be revoked through this route. An officer with
	// no posting can sign in and see nothing, which looks to them exactly
	// like the platform being broken. Suspending the account says what
	// actually happened and ends their sessions.
	if len(target.Grants) <= 1 {
		writeError(w, http.StatusConflict, "LAST_POSTING",
			"that is the account's only posting. Suspend the account instead, "+
				"which ends its sessions and says what happened")
		return
	}

	if err := s.Identity.RevokeRole(r.Context(), userID, rbac.Role(req.Role), jur.ID); err != nil {
		s.writeIdentityError(w, err)
		return
	}
	if err := s.Auth.Sessions.RevokeAllForUser(r.Context(), userID); err != nil {
		s.Log.Warn("revoking sessions after revoke", "user_id", userID, "error", err)
	}

	_ = s.Audit.RecordSync(r.Context(), audit.Event{
		Action: audit.ActionRoleRevoked, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: userID, JurisdictionCode: jur.Code,
		JurisdictionID: jur.ID,
		Detail:         map[string]any{"role": req.Role, "jurisdiction_code": jur.Code},
	}.FromContext(r.Context()))

	user, err := s.Identity.ByID(r.Context(), userID)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewUser(user))
}

type statusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func (s *Server) setUserStatus(w http.ResponseWriter, r *http.Request) {
	actor := rbac.FromContext(r.Context())
	userID := r.PathValue("userID")

	var req statusRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	status := identity.Status(req.Status)
	if !slices.Contains([]identity.Status{
		identity.StatusActive, identity.StatusSuspended, identity.StatusDeactivated,
	}, status) {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS",
			"status must be active, suspended or deactivated")
		return
	}

	// An administrator locking themselves out of the only admin account is a
	// support call that ends in a database console.
	if userID == actor.UserID && status != identity.StatusActive {
		writeError(w, http.StatusBadRequest, "SELF_LOCKOUT",
			"an account cannot suspend itself; ask another administrator")
		return
	}

	// Disabling an account is the sharpest thing this API does to a colleague
	// -- it ends every session the moment it lands -- so it is bounded by the
	// same rank and place as creating one.
	target, err := s.Identity.ByID(r.Context(), userID)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}
	if err := s.mayManage(r.Context(), actor, target); err != nil {
		if writeDelegationError(w, err) {
			return
		}
		s.writeIdentityError(w, err)
		return
	}

	if err := s.Identity.SetStatus(r.Context(), userID, status); err != nil {
		s.writeIdentityError(w, err)
		return
	}
	if status != identity.StatusActive {
		if err := s.Auth.Sessions.RevokeAllForUser(r.Context(), userID); err != nil {
			s.Log.Warn("revoking sessions after suspension", "user_id", userID, "error", err)
		}
	}

	_ = s.Audit.RecordSync(r.Context(), audit.Event{
		Action: audit.ActionUserStatusSet, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: userID,
		Detail: map[string]any{"status": req.Status, "reason": req.Reason},
	}.FromContext(r.Context()))

	user, err := s.Identity.ByID(r.Context(), userID)
	if err != nil {
		s.writeIdentityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewUser(user))
}

// listJurisdictions walks the administrative tree one level at a time.
func (s *Server) listJurisdictions(w http.ResponseWriter, r *http.Request) {
	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	parentID := r.URL.Query().Get("parent_id")
	level := r.URL.Query().Get("level")

	// Two shapes, because two callers want different things. The area picker
	// walks the tree -- "the circles of this taluka" -- and passes a parent.
	// The posting picker searches it -- "the village called Alipur, wherever
	// it is under me" -- and cannot, because the village it wants is two
	// levels below and a child of nothing the officer has selected.
	//
	// The search is scoped to the caller's own postings, which is the same
	// boundary the delegation check applies when the posting is submitted, so
	// the picker cannot offer a place the create would then refuse.
	var res page.Result[*identity.Jurisdiction]
	var err2 error
	actor := rbac.FromContext(r.Context())
	admin := actor.HasRole(rbac.RoleSystemAdmin)
	var scope []string
	empty := func() {
		writeJSON(w, http.StatusOK, map[string]any{
			"jurisdictions": []any{}, "items": []any{}, "total": 0,
			"limit": pg.Limit, "offset": pg.Offset, "has_more": false,
			"query": pg.Query,
		})
	}
	if !admin {
		scope = actor.Scope().JurisdictionIDs
		if len(scope) == 0 {
			empty()
			return
		}
	}
	if parentID == "" && level != "" {
		res, err2 = s.Identity.SearchJurisdictions(r.Context(), level, scope, pg)
	} else {
		// Walking the tree is bounded the same way. It was unscoped: an
		// officer could list the talukas and villages of any district in any
		// state by naming its id, and the roots -- every state -- by naming
		// nothing. The children of a node inside the caller's own area are
		// theirs to walk; nothing else is.
		if !admin {
			if parentID == "" {
				empty()
				return
			}
			within, err := s.Identity.WithinAny(r.Context(), parentID, scope)
			if err != nil || !within {
				empty()
				return
			}
		}
		res, err2 = s.Identity.ListJurisdictions(r.Context(), parentID, level, pg)
	}
	if err2 != nil {
		s.writeIdentityError(w, err2)
		return
	}

	// The envelope every other listing uses, plus the key this endpoint has
	// always answered with, so a client reading `jurisdictions` keeps working
	// while one reading `items` gets the same rows.
	writeJSON(w, http.StatusOK, map[string]any{
		"jurisdictions":        res.Items,
		"items":                res.Items,
		"total":                res.Total,
		"total_is_lower_bound": res.TotalIsLower,
		"limit":                res.Limit,
		"offset":               res.Offset,
		"has_more":             res.HasMore,
		"query":                res.Query,
	})
}

// roles publishes the permission matrix.
//
// The console renders from this rather than hard-coding what a Tehsildar may
// do, so that the matrix has exactly one definition and the front end cannot
// drift from it.
func (s *Server) roles(w http.ResponseWriter, _ *http.Request) {
	out := make([]map[string]any, 0, len(rbac.AllRoles))
	for _, role := range rbac.AllRoles {
		perms := rbac.Permissions(role)
		names := make([]string, 0, len(perms))
		for _, p := range perms {
			names = append(names, string(p))
		}
		slices.Sort(names)

		out = append(out, map[string]any{
			"role":        string(role),
			"permissions": names,
			"assignable":  rbac.Assignable(role),
			"posted_at":   string(rbac.RequiredLevel[role]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// resolveJurisdiction turns a level and code into a node, and checks that the
// role being granted is one that may be posted there.
//
// Two roles are not posted anywhere -- a farmer, whose authority is over
// their own records, and a system administrator, whose authority is over the
// account roll. Every other grant without a jurisdiction would be an officer
// with a title and no area, which the database refuses and which is worth
// refusing here with a sentence rather than a constraint violation.
func (s *Server) resolveJurisdiction(
	r *http.Request, role rbac.Role, level, code string,
) (*identity.Jurisdiction, error) {
	placeless := role == rbac.RoleFarmer || role == rbac.RoleSystemAdmin

	if level == "" && code == "" {
		if !placeless {
			return nil, fmt.Errorf(
				"%w: role %s must be posted to a jurisdiction",
				errBadRequest, role)
		}
		return &identity.Jurisdiction{}, nil
	}
	if placeless {
		return nil, fmt.Errorf(
			"%w: role %s is not posted to a jurisdiction", errBadRequest, role)
	}
	if level == "" || code == "" {
		return nil, fmt.Errorf(
			"%w: jurisdiction_level and jurisdiction_code must be given together",
			errBadRequest)
	}

	jur, err := s.Identity.JurisdictionByCode(r.Context(), rbac.Level(level), code)
	if err != nil {
		return nil, err
	}

	// Acting charge at a level above one's own posting is routine, so a
	// mismatch is logged rather than refused: a system that cannot record
	// what actually happens gets worked around with a shared login.
	if want, ok := rbac.RequiredLevel[role]; ok && want != jur.Level {
		s.Log.Warn("role granted at an unusual level",
			"role", string(role), "expected_level", string(want),
			"granted_level", string(jur.Level), "jurisdiction", jur.Code)
	}

	return jur, nil
}

// errBadRequest marks a caller error inside the identity flow, so that
// writeIdentityError can answer 400 rather than reporting a server fault.
var errBadRequest = errors.New("bad request")

func (s *Server) writeIdentityError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	switch {
	case errors.Is(err, errBadRequest):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, identity.ErrInvalidEmail):
		writeError(w, http.StatusBadRequest, "INVALID_EMAIL", err.Error())
	case errors.Is(err, identity.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, identity.ErrConflict):
		writeError(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, identity.ErrInactive):
		writeError(w, http.StatusForbidden, "ACCOUNT_INACTIVE", err.Error())
	default:
		s.Log.Error("identity operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "IDENTITY_ERROR",
			"the request could not be completed")
	}
}

func joinRoles(roles []rbac.Role) string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, string(r))
	}
	slices.Sort(out)
	joined := ""
	for i, s := range out {
		if i > 0 {
			joined += ", "
		}
		joined += s
	}
	return joined
}
