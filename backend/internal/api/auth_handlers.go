package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// maxBodyBytes bounds request bodies on the auth surface. Generous for JSON
// and small enough that an unauthenticated caller cannot make the process
// allocate on their behalf.
const maxBodyBytes = 64 << 10

// decodeJSON reads a bounded, strict JSON body.
//
// Unknown fields are rejected: a client sending "usrname" should be told it
// was wrong, not have it silently ignored and be shown a failed sign-in.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

type officerLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// sessionResponse is what every successful sign-in returns.
type sessionResponse struct {
	*auth.Tokens
	User userView `json:"user"`
}

func (s *Server) officerLogin(w http.ResponseWriter, r *http.Request) {
	var req officerLoginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"email and password are required")
		return
	}
	// Officers sign in with their email address. Refused on its shape before
	// any lookup: it says nothing about whether an account exists, and an old
	// username such as "admin" is not a login any more.
	email, err := identity.NormalizeEmail(req.Username)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_EMAIL",
			"sign in with your email address")
		return
	}
	req.Username = email

	tokens, user, challenge, err := s.Auth.LoginOfficer(
		r.Context(), req.Username, req.Password, s.caller(r))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	// Half a sign-in: the password was right and a code has gone to the
	// officer's handset. No token is issued until it comes back.
	if challenge != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "code_required",
			"challenge":   challenge.Token,
			"mobile_hint": challenge.MobileHint,
			"expires_at":  challenge.ExpiresAt,
			"message": "A one-time code has been sent to " +
				challenge.MobileHint + ". Enter it to finish signing in.",
		})
		return
	}

	writeJSON(w, http.StatusOK, sessionResponse{Tokens: tokens, User: viewUser(user)})
}

type officerVerifyRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

// officerVerify finishes a sign-in that asked for a second factor.
func (s *Server) officerVerify(w http.ResponseWriter, r *http.Request) {
	var req officerVerifyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	tokens, user, err := s.Auth.VerifyOfficerCode(
		r.Context(), req.Challenge, req.Code, s.caller(r))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, sessionResponse{Tokens: tokens, User: viewUser(user)})
}

type otpRequest struct {
	Mobile string `json:"mobile"`
}

func (s *Server) farmerRequestOTP(w http.ResponseWriter, r *http.Request) {
	var req otpRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	err := s.Auth.RequestFarmerOTP(r.Context(), req.Mobile, s.caller(r))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	// Always the same answer, whether or not the number is registered.
	// Anything else turns this endpoint into a way to ask whether a given
	// farmer is enrolled on the platform.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "sent",
		"message": "If that number is registered, a code has been sent to it.",
	})
}

type otpVerifyRequest struct {
	Mobile string `json:"mobile"`
	Code   string `json:"code"`
}

func (s *Server) farmerVerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req otpVerifyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if req.Mobile == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"mobile and code are required")
		return
	}

	tokens, user, err := s.Auth.VerifyFarmerOTP(r.Context(), req.Mobile, req.Code, s.caller(r))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, sessionResponse{Tokens: tokens, User: viewUser(user)})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "refresh_token is required")
		return
	}

	tokens, err := s.Auth.Refresh(r.Context(), req.RefreshToken, s.caller(r))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	if err := s.Auth.Logout(r.Context(), req.RefreshToken, s.caller(r)); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// changePassword is reachable by any authenticated officer, including one
// still carrying an administrator-issued password. It is the single exception
// to the must-change-password restriction, because it is the way out of it.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())

	var req changePasswordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"current_password and new_password are required")
		return
	}

	err := s.Auth.ChangePassword(r.Context(), p.UserID,
		req.CurrentPassword, req.NewPassword, s.caller(r))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "changed",
		"message": "Password updated. Every session has been signed out, " +
			"including this one; sign in again with the new password.",
	})
}

// me returns the caller's own identity and reach.
//
// The console needs this to decide what to render. Returning permissions
// explicitly means the front end does not reimplement the matrix and drift
// from it.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())

	perms := map[rbac.Permission]bool{}
	for _, g := range p.Grants {
		for _, perm := range rbac.Permissions(g.Role) {
			perms[perm] = true
		}
	}
	list := make([]string, 0, len(perms))
	for perm := range perms {
		list = append(list, string(perm))
	}
	slices.Sort(list)

	// The handset, masked, because this is what the console shows an officer
	// about their own second factor -- and "Password only" has to be
	// distinguishable from "we did not tell you".
	var mobile, email string
	if u, err := s.Identity.ByID(r.Context(), p.UserID); err == nil {
		if u.MobileE164 != "" {
			mobile = maskTail(u.MobileE164)
		}
		email = u.Email
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":              p.UserID,
		"kind":                 p.Kind,
		"name":                 p.Name,
		"email":                email,
		"mobile":               mobile,
		"farmer_id":            p.FarmerID,
		"grants":               p.Grants,
		"primary_role":         p.PrimaryRole(),
		"permissions":          list,
		"must_change_password": p.MustChangePassword,
		"jurisdiction_codes":   p.JurisdictionCodes(),
	})
}

// publicKeys publishes the token verification keys.
//
// The assessment agent and, later, a mobile gateway verify tokens minted
// here. Publishing the public half lets them do so while holding nothing that
// could mint one.
func (s *Server) publicKeys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"alg":        "EdDSA",
		"active_kid": s.Keys.SigningKeyID(),
		"keys":       s.Keys.PublicKeys(),
	})
}

// userView is the account shape the API returns. It exists so that a field
// added to the stored record is not published by accident.
type userView struct {
	ID                 string       `json:"id"`
	Kind               rbac.Kind    `json:"kind"`
	Status             string       `json:"status"`
	FullName           string       `json:"full_name"`
	Username           string       `json:"username,omitempty"`
	Mobile             string       `json:"mobile,omitempty"`
	FarmerID           string       `json:"farmer_id,omitempty"`
	AadhaarLast4       string       `json:"aadhaar_last4,omitempty"`
	Email              string       `json:"email,omitempty"`
	Grants             []rbac.Grant `json:"grants"`
	MustChangePassword bool         `json:"must_change_password"`
	LastLoginAt        *string      `json:"last_login_at,omitempty"`
	// Absent on an account whose invitation has not been accepted. That is
	// what separates "invited" from "active" in the account roll, and it is
	// the difference between an officer who has not started and one who has.
	PasswordChangedAt *string `json:"password_changed_at,omitempty"`
}

func viewUser(u *identity.User) userView {
	v := userView{
		ID:                 u.ID,
		Kind:               u.Kind,
		Status:             string(u.Status),
		FullName:           u.FullName,
		Username:           u.Username,
		Email:              u.Email,
		FarmerID:           u.AgriStackFarmerID,
		AadhaarLast4:       u.AadhaarLast4,
		Grants:             u.Grants,
		MustChangePassword: u.MustChangePassword,
	}
	// The handset is shown masked. An officer listing farmers has no reason
	// to be handed a directory of mobile numbers.
	if u.MobileE164 != "" {
		v.Mobile = maskTail(u.MobileE164)
	}
	if u.LastLoginAt != nil {
		s := u.LastLoginAt.UTC().Format("2006-01-02T15:04:05Z")
		v.LastLoginAt = &s
	}
	if u.PasswordChangedAt != nil {
		s := u.PasswordChangedAt.UTC().Format("2006-01-02T15:04:05Z")
		v.PasswordChangedAt = &s
	}
	return v
}

func maskTail(s string) string {
	if len(s) < 4 {
		return "****"
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}

// writeAuthError maps the auth package's errors onto status codes.
//
// Every credential failure returns the same body. The distinctions that do
// surface -- expiry, lockout, a required password change -- are ones the
// caller must act on differently and that tell an attacker nothing they could
// not learn by waiting.
func (s *Server) writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS",
			"the credentials were not accepted")

	case errors.Is(err, auth.ErrLockedOut):
		writeError(w, http.StatusTooManyRequests, "LOCKED_OUT", err.Error())

	case errors.Is(err, auth.ErrOTPCooldown):
		writeError(w, http.StatusTooManyRequests, "OTP_COOLDOWN",
			"a code was sent recently; wait before requesting another")
	case errors.Is(err, auth.ErrOTPDailyCap):
		writeError(w, http.StatusTooManyRequests, "OTP_DAILY_CAP",
			"too many codes have been requested for this number today")
	case errors.Is(err, auth.ErrOTPAttempts):
		writeError(w, http.StatusTooManyRequests, "OTP_ATTEMPTS",
			"too many incorrect attempts; request a new code")
	case errors.Is(err, auth.ErrOTPIncorrect), errors.Is(err, auth.ErrOTPNotFound):
		writeError(w, http.StatusUnauthorized, "OTP_INVALID",
			"that code is not valid; request a new one")

	case errors.Is(err, auth.ErrChallengeInvalid):
		writeError(w, http.StatusUnauthorized, "CHALLENGE_INVALID",
			"that sign-in took too long or was already used; "+
				"enter your email and password again")

	case errors.Is(err, auth.ErrOfficerHandset):
		// Only reached where the deployment allows it to be said; see
		// auth.ErrOfficerHandset.
		//
		// 403 and not 409. A 409 in this platform means a concurrency
		// conflict -- the console renders every unrecognised one as "someone
		// else changed this while you were working", which is what an officer
		// was shown here until it was corrected.
		writeError(w, http.StatusForbidden, "OFFICER_HANDSET",
			"This number belongs to an officer account. Officers sign in on "+
				"the eSurvey console with their email and password, not in "+
				"the farmer app.")

	// The code could not be sent. Said plainly, and differently for the two
	// cases, because a farmer standing in a field decides what to do next
	// from this sentence: wait and press again, or stop and go and ask.
	case errors.Is(err, auth.ErrChannelDown):
		s.Log.Error("one-time codes cannot be sent", "error", err)
		w.Header().Set("Retry-After", "120")
		writeError(w, http.StatusServiceUnavailable, "SMS_UNAVAILABLE",
			"The code could not be sent just now. Wait a couple of minutes "+
				"and ask for it again.")
	case errors.Is(err, auth.ErrUndeliverable):
		// Logged at error because it is ours to fix and nobody else can:
		// an unregistered template or header stops every farmer in the
		// state, silently, until an operator reads this line.
		s.Log.Error("one-time codes cannot be delivered at all; "+
			"check the sender header and the DLT template", "error", err)
		writeError(w, http.StatusServiceUnavailable, "SMS_UNDELIVERABLE",
			"Codes cannot be sent at the moment. This is a fault at our end, "+
				"not with your number. Ask at the village office, or try "+
				"again later.")

	case errors.Is(err, auth.ErrRefreshReused):
		writeError(w, http.StatusUnauthorized, "SESSION_REVOKED",
			"this session has been ended because its refresh token was replayed; sign in again")
	case errors.Is(err, auth.ErrRefreshUnknown):
		writeError(w, http.StatusUnauthorized, "REFRESH_INVALID",
			"the refresh token is not recognised; sign in again")

	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())

	case errors.Is(err, identity.ErrInactive):
		writeError(w, http.StatusForbidden, "ACCOUNT_INACTIVE",
			"this account is not active")
	case errors.Is(err, identity.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such account")

	default:
		s.Log.Error("authentication failure", "error", err)
		writeError(w, http.StatusInternalServerError, "AUTH_ERROR",
			"the request could not be completed")
	}
}
