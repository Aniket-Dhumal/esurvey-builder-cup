package api

import (
	"errors"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// An officer putting a handset on their own account.
//
// # Why an officer does this and not an administrator
//
// The number is the second factor for this account, so whoever sets it
// decides which phone can finish this officer's sign-in. An administrator
// typing it at a counter is how a second factor ends up pointing at somebody
// else's handset -- a typo does it, and so does a helpful colleague. Only the
// person holding the phone can prove they hold it.
//
// # Why the number is verified before it is saved
//
// A caller with a session already has the account; that is not what this
// guards against. It guards against the number being wrong. An unverified
// number is worse than none: sign-in then demands a code that can never
// arrive, and the officer is locked out of a live system by their own typo.

type setMobileRequest struct {
	Mobile string `json:"mobile"`
}

// startMobileChange sends a code to the number an officer wants to use.
func (s *Server) startMobileChange(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())

	var req setMobileRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	challenge, err := s.Auth.StartMobileChange(r.Context(), p.UserID, req.Mobile, s.caller(r))
	if err != nil {
		s.writeMobileError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "code_sent",
		"challenge":   challenge.Token,
		"mobile_hint": challenge.MobileHint,
		"expires_at":  challenge.ExpiresAt,
	})
}

type confirmMobileRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

// confirmMobileChange saves the number once its code comes back.
func (s *Server) confirmMobileChange(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())

	var req confirmMobileRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	user, err := s.Auth.ConfirmMobileChange(
		r.Context(), p.UserID, req.Challenge, req.Code, s.caller(r))
	if err != nil {
		s.writeMobileError(w, err)
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		Action: "user.mobile_set", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: user.ID,
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "saved",
		"user":   viewUser(user),
		"message": "Your handset is on this account. From now on signing in " +
			"asks for your password and then a code sent to it.",
	})
}

// removeMobile takes the handset off an officer's own account.
func (s *Server) removeMobile(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())

	user, err := s.Auth.RemoveMobile(r.Context(), p.UserID, s.caller(r))
	if err != nil {
		s.writeMobileError(w, err)
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		Action: "user.mobile_removed", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: user.ID,
		Detail: map[string]any{
			"note": "this account signs in on its password alone again",
		},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "removed",
		"user":   viewUser(user),
	})
}

func (s *Server) writeMobileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrChallengeInvalid):
		writeError(w, http.StatusUnauthorized, "CHALLENGE_INVALID",
			"that took too long. Enter the number again to get a fresh code.")
	case errors.Is(err, auth.ErrOTPIncorrect), errors.Is(err, auth.ErrOTPNotFound):
		writeError(w, http.StatusUnauthorized, "OTP_INVALID",
			"that code is not correct; ask for a new one")
	case errors.Is(err, identity.ErrConflict):
		writeError(w, http.StatusConflict, "MOBILE_TAKEN",
			"another account already uses that number")
	case errors.Is(err, auth.ErrNotAnOfficer):
		writeError(w, http.StatusForbidden, "NOT_AN_OFFICER",
			"a farmer's handset is set from the registry, not here")
	default:
		s.writeAuthError(w, err)
	}
}
