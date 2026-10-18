package api

import (
	"errors"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
)

// Claiming an invited account.
//
// Both routes are unauthenticated, because the whole point is that the person
// holding the link has no credential yet. The token is the credential, and it
// is single-use, time-limited and stored only as a hash -- see
// identity/invite.go.

type inviteAcceptRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// checkInvite tells the set-password screen whose account it is about.
//
// Returns the name and the email, and never the username as a separate fact,
// because they are the same string. The screen shows it so that somebody who
// was forwarded a link meant for a colleague sees whose account they are about
// to take over before they set a password on it.
func (s *Server) checkInvite(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "MISSING_TOKEN",
			"that link is incomplete")
		return
	}

	inv, err := s.Identity.InviteByToken(r.Context(), token)
	if err != nil {
		writeInviteError(w, err)
		return
	}
	// The purpose is returned so the screen can word itself correctly. The
	// same link resolves both, and "set your password" over a reset reads as
	// though the account is new -- which is exactly the confusion somebody
	// who has just been phished would fail to notice.
	writeJSON(w, http.StatusOK, map[string]any{
		"full_name":  inv.FullName,
		"email":      inv.Email,
		"expires_at": inv.ExpiresAt,
		"purpose":    inv.Purpose,
	})
}

// acceptInvite sets the password and consumes the invitation.
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req inviteAcceptRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if req.Token == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"a link and a password are both needed")
		return
	}

	// Resolved before the password is checked, so the policy can be told the
	// name and address it must not let the password contain.
	inv, err := s.Identity.InviteByToken(r.Context(), req.Token)
	if err != nil {
		writeInviteError(w, err)
		return
	}
	// The mirror of the check on the reset endpoint. A token is for one thing:
	// the two have different lifetimes and different rules about who may ask
	// for one, and honouring either here would throw both away.
	if inv.Purpose != identity.PurposeInvite {
		writeError(w, http.StatusBadRequest, "WRONG_TOKEN",
			"that link is for changing a password on an existing account, "+
				"not for setting up a new one")
		return
	}
	if err := auth.CheckPasswordPolicy(req.Password, inv.Email, inv.FullName); err != nil {
		writeError(w, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.Log.Error("hashing an accepted invitation",
			"request_id", RequestID(r.Context()), "error", err)
		writeError(w, http.StatusInternalServerError, "HASH_FAILED", "")
		return
	}

	userID, err := s.Identity.AcceptInvite(r.Context(), req.Token, hash)
	if err != nil {
		writeInviteError(w, err)
		return
	}

	// Any session that somehow exists for this account ends here. There
	// should be none -- the account could not be signed in to -- but an
	// account being claimed is exactly the moment to be sure of it.
	if err := s.Auth.Sessions.RevokeAllForUser(r.Context(), userID); err != nil {
		s.Log.Warn("revoking sessions after an invitation was accepted",
			"user_id", userID, "error", err)
	}

	_ = s.Audit.RecordSync(r.Context(), audit.Event{
		Action: audit.ActionPasswordChanged, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: userID,
		Detail: map[string]any{"via": "invitation"},
	}.FromContext(r.Context()))

	// No session is issued. Setting a password and signing in are two acts,
	// and doing both here would mean a forwarded link both takes over an
	// account and lands the holder inside it in one request.
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"email":  inv.Email,
	})
}

// writeInviteError answers the three ways a link can fail.
//
// Distinguished on purpose, unlike most auth failures: the holder of an
// expired link needs to be told to ask for a new one, and a used link is how
// somebody discovers their account was already claimed. Neither reveals
// anything about an account the caller does not already have a token for.
func writeInviteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrInviteExpired):
		writeError(w, http.StatusGone, "INVITE_EXPIRED",
			"that link has expired. Ask the officer who created your account to send another.")
	case errors.Is(err, identity.ErrInviteUsed):
		writeError(w, http.StatusConflict, "INVITE_USED",
			"that link has already been used. Sign in with the password you set, or ask for a new link.")
	case errors.Is(err, identity.ErrInviteNotFound):
		writeError(w, http.StatusNotFound, "INVITE_NOT_FOUND",
			"that link is not valid. Check it was copied in full.")
	default:
		writeError(w, http.StatusInternalServerError, "INVITE_FAILED", "")
	}
}
