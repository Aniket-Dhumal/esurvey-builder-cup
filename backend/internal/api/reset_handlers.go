package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/mail"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Letting an officer who has forgotten their password back in.
//
// Unauthenticated by necessity: somebody who cannot sign in is asking. The
// token that comes back is the credential, and it is single-use, expires in
// an hour, and is stored only as a SHA-256 hash.
//
// # Why this answers the same way whether or not the account exists
//
// A reset form that says "no such account" is an account enumerator with a
// friendly face. Point it at a list of addresses and it tells you which
// officers this platform holds -- who works for which district, and by
// omission who has left. So the answer is identical either way, and the
// difference is only whether an email is queued.
//
// It also costs the same time either way, near enough: the work is one
// indexed lookup and, when there is an account, a queued row. Neither is slow
// enough for the gap to be a signal.

type resetRequest struct {
	Email string `json:"email"`
}

type resetAcceptRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// requestReset queues a reset link for an officer, if that officer exists.
func (s *Server) requestReset(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	email := normaliseEmail(req.Email)
	ip := clientIP(r, s.TrustedProxyHops)

	// The same body, always. Written once, before any lookup, so that no
	// later branch can accidentally answer differently.
	answer := func() {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status": "accepted",
			"message": "If that address belongs to an officer account, a link " +
				"to set a new password is on its way. It can be used once and " +
				"expires in an hour.",
		})
	}

	if email == "" || !looksLikeEmail(email) {
		answer()
		return
	}

	// One link per address per cooldown.
	//
	// The global limiter caps a caller at sixty requests a window, which is
	// sixty emails to sixty different officers -- a mail cannon aimed at a
	// department, and the messages are genuine so nothing marks them spam.
	// Keyed on the address rather than the caller, because the person being
	// mailbombed is the one this protects and the sender can change IP.
	//
	// Refusing looks exactly like succeeding, for the same reason the whole
	// endpoint does: a different answer here would say "this address has an
	// account and has already been asked about".
	if !s.resetAllowed(r.Context(), email) {
		s.Log.Info("password reset within its cooldown", "cooldown", resetCooldown)
		answer()
		return
	}

	user, err := s.Identity.OfficerByEmail(r.Context(), email)
	if err != nil {
		// Not found, or a database fault. Both answer the same to the caller;
		// only the second is worth a log line.
		if !errors.Is(err, identity.ErrNotFound) {
			s.Log.Error("looking up an account for reset",
				"request_id", RequestID(r.Context()), "error", err)
		}
		answer()
		return
	}

	// An account that has never been claimed gets no reset: it has no
	// password to replace, and issuing one here would be a second, quieter
	// path to claiming an account that was invited to somebody else.
	if user.PasswordChangedAt == nil || !user.Active() {
		s.Log.Info("reset refused for an unclaimed or inactive account",
			"user_id", user.ID)
		answer()
		return
	}

	if err := s.Identity.ResetOfficer(r.Context(), user, ip,
		func(ctx context.Context, q db.Querier, token string, expires time.Time) error {
			return s.Mail.Queue(ctx, q, mail.Message{
				To:           user.Email,
				Subject:      "Set a new eSurvey password",
				Body:         resetBody(user.FullName, s.ConsoleBaseURL, token, expires),
				Purpose:      mail.PurposePasswordReset,
				ResourceType: "user",
				ResourceID:   user.ID,
			})
		}); err != nil {
		s.Log.Error("issuing a password reset",
			"request_id", RequestID(r.Context()), "user_id", user.ID, "error", err)
		answer()
		return
	}

	_ = s.Audit.RecordSync(r.Context(), audit.Event{
		Action: "user.reset_requested", Outcome: audit.OutcomeAllow,
		ActorIP: ip, RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: user.ID,
		Detail: map[string]any{"email": user.Email},
	}.FromContext(r.Context()))

	answer()
}

// acceptReset sets the new password and consumes the token.
func (s *Server) acceptReset(w http.ResponseWriter, r *http.Request) {
	var req resetAcceptRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if req.Token == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"a link and a password are both needed")
		return
	}

	inv, err := s.Identity.InviteByToken(r.Context(), req.Token)
	if err != nil {
		writeInviteError(w, err)
		return
	}
	// A token is for one thing. An invitation presented here, or a reset
	// presented at the invitation endpoint, is refused rather than quietly
	// honoured -- the two have different lifetimes and different rules about
	// who may ask for them, and treating them as interchangeable throws both
	// away.
	if inv.Purpose != identity.PurposeReset {
		writeError(w, http.StatusBadRequest, "WRONG_TOKEN",
			"that link is for setting up a new account, not for changing a password")
		return
	}

	if err := auth.CheckPasswordPolicy(req.Password, inv.Email, inv.FullName); err != nil {
		writeError(w, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.Log.Error("hashing a reset password",
			"request_id", RequestID(r.Context()), "error", err)
		writeError(w, http.StatusInternalServerError, "HASH_FAILED", "")
		return
	}

	userID, err := s.Identity.AcceptInvite(r.Context(), req.Token, hash)
	if err != nil {
		writeInviteError(w, err)
		return
	}

	// Every session ends. This is the half that makes a reset a remedy: if
	// the reason for it is that somebody else had the password, leaving their
	// session alive hands them the account they were being locked out of.
	if err := s.Auth.Sessions.RevokeAllForUser(r.Context(), userID); err != nil {
		s.Log.Warn("revoking sessions after a password reset",
			"user_id", userID, "error", err)
	}

	_ = s.Audit.RecordSync(r.Context(), audit.Event{
		Action: audit.ActionPasswordChanged, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(r.Context()),
		ResourceType: "user", ResourceID: userID,
		Detail: map[string]any{"via": "reset"},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"email":  inv.Email,
	})
}

// resetBody is the email an officer receives.
func resetBody(name, consoleURL, token string, expires time.Time) string {
	return fmt.Sprintf(`%s,

Somebody asked to set a new password for your eSurvey account.

%s/set-password?token=%s

This link can be used once and stops working at %s. Using it signs you out
everywhere else.

If it was not you, you can ignore this -- your password has not changed, and
nobody can use this link without your mailbox. Tell your district
administrator if you were not expecting it.

-- eSurvey
`, name, strings.TrimRight(consoleURL, "/"), token,
		expires.In(time.FixedZone("IST", 5*3600+1800)).Format("3:04 pm on 2 January"))
}

// resetCooldown is how long one address waits between reset links.
//
// Long enough that a request cannot be turned into a stream of mail, short
// enough that an officer who deleted the first message by accident is not
// locked out for an afternoon.
const resetCooldown = 5 * time.Minute

// resetAllowed reports whether a reset may be issued for this address now.
//
// Fails open when Redis is unreachable, like the rate limiter above it and
// for the same reason: a cache outage must not become an officer who cannot
// get back into the platform during an event.
func (s *Server) resetAllowed(ctx context.Context, email string) bool {
	if s.Redis == nil {
		return true
	}
	// The address is hashed, not stored. Redis holds this key for five
	// minutes and it would otherwise be a list of officers who have recently
	// forgotten a password, sitting in a cache nobody thinks of as a
	// database.
	key := "reset:cooldown:" + sha256hex(email)
	ok, err := s.Redis.SetNX(ctx, key, 1, resetCooldown).Result()
	if err != nil {
		s.Log.Warn("reset cooldown unavailable", "error", err)
		return true
	}
	return ok
}

func sha256hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
