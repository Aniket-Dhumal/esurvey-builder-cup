package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/auth"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

type ctxKeyRequestID struct{}

// RequestID returns the correlation id assigned to this request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID{}).(string)
	return id
}

// requestID assigns a correlation id and echoes it.
//
// A client-supplied id is accepted but bounded and sanitised: it ends up in
// the audit log and in structured log fields, and an unbounded caller-
// controlled string in either is a way to make both unreadable.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitiseID(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID{}, id)))
	})
}

func sanitiseID(raw string) string {
	if len(raw) > 64 {
		raw = raw[:64]
	}
	var b strings.Builder
	for _, r := range raw {
		if r == '-' || r == '_' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newRequestID() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// recovering turns a panic into a 500 rather than a dropped connection.
//
// A panic in one handler must not take the process down: the other officers
// mid-request lose their work for a bug that affected one of them.
func recovering(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// http.ErrAbortHandler is the documented way to abandon a
					// response; it is not a bug and must not be logged as one.
					if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(rec)
					}
					log.Error("panic in handler",
						"panic", rec,
						"method", r.Method,
						"path", r.URL.Path,
						"request_id", RequestID(r.Context()),
						"stack", string(debug.Stack()))
					writeError(w, http.StatusInternalServerError, "INTERNAL",
						"the request could not be completed")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders sets the defensive headers every response carries.
//
// This started as "a JSON API is not a document, so a content security policy
// would be cargo-culted noise". That was true when it was written and is not
// any more: GET /reports/{id}/print returns text/html, and the assessment
// dossier renders HTML too. Both are pages, served from the API's own origin,
// carrying a named farmer's land and losses.
//
// The values are escaped at render time, so a policy is defence in depth
// rather than the only thing standing in the way. It is worth having anyway --
// the cost is one header, and the failure it covers is the one where somebody
// adds a field to that template without thinking about where its contents came
// from.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Tokens and farmer records must not sit in a shared cache.
		h.Set("Cache-Control", "no-store")

		// script-src is absent, so 'none' from default-src applies: no script
		// runs on these pages under any circumstances, inline or otherwise.
		// Styles are inline by necessity -- the report is one self-contained
		// file that has to print from a browser with no network -- and images
		// arrive as data: URIs for the same reason.
		h.Set("Content-Security-Policy",
			"default-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"font-src 'self' data:; base-uri 'none'; form-action 'none'; "+
				"frame-ancestors 'none'")

		// Only over TLS, and only when the connection really is TLS. Sent on a
		// plaintext response it would be ignored by browsers and misleading to
		// anyone reading the headers to find out how the service is deployed.
		if s.isTLS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

// isTLS reports whether the client's connection is encrypted.
//
// r.TLS covers termination in this process. X-Forwarded-Proto covers the usual
// deployment, where a proxy terminates -- and is only believed when a proxy is
// actually configured, because otherwise the header is just something the
// caller sent.
func (s *Server) isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.TrustedProxyHops > 0 &&
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clientIP resolves the caller's address.
//
// X-Forwarded-For is attacker-controlled except for the entries appended by
// proxies we run. Reading the value `hops` from the right is the only correct
// way to use it, and a deployment with no proxy in front reads the socket and
// ignores the header entirely.
func clientIP(r *http.Request, hops int) string {
	if hops > 0 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			idx := len(parts) - hops
			if idx >= 0 && idx < len(parts) {
				if ip := strings.TrimSpace(parts[idx]); ip != "" {
					return ip
				}
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// caller assembles the audit and throttling context for a request.
func (s *Server) caller(r *http.Request) auth.Caller {
	return auth.Caller{
		IP:        clientIP(r, s.TrustedProxyHops),
		UserAgent: r.UserAgent(),
		RequestID: RequestID(r.Context()),
	}
}

// authenticate reads a bearer token and attaches the principal.
//
// It does not reject anonymous requests: some routes are public and the
// decision belongs to the route, not here. What it does reject is a token
// that is present and bad, because carrying on anonymously in that case would
// silently downgrade a caller who believes they are signed in.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			next.ServeHTTP(w, r)
			return
		}

		scheme, token, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			writeError(w, http.StatusUnauthorized, "BAD_AUTH_SCHEME",
				"expected an Authorization header of the form: Bearer <token>")
			return
		}

		principal, err := s.Keys.Parse(strings.TrimSpace(token))
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrTokenExpired):
				// Distinguished because the client's next move differs: this
				// one is answered by a refresh, the others by a sign-in.
				writeError(w, http.StatusUnauthorized, "TOKEN_EXPIRED",
					"the access token has expired; refresh it")
			default:
				s.Log.Warn("rejected access token",
					"error", err, "request_id", RequestID(r.Context()),
					"ip", clientIP(r, s.TrustedProxyHops))
				writeError(w, http.StatusUnauthorized, "TOKEN_INVALID",
					"the access token is not valid")
			}
			return
		}

		next.ServeHTTP(w, r.WithContext(rbac.WithPrincipal(r.Context(), principal)))
	})
}

// requireAuth rejects anonymous requests.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := rbac.FromContext(r.Context())
		if p == nil {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED",
				"this endpoint requires an access token")
			return
		}

		// The same gate require() applies.
		//
		// It was missing here, so an officer still holding the password an
		// administrator issued them could reach every requireAuth route while
		// being refused every permissioned one. An account on a credential
		// somebody else chose -- and may still know, or have written down --
		// is exactly the account that should be able to do nothing but
		// replace it.
		if p.RestrictedToPasswordChange() {
			writeError(w, http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED",
				"set a password of your own before using the platform")
			return
		}

		next(w, r)
	}
}

// requireAuthDuringPasswordChange authenticates without applying the
// must-change-password gate.
//
// For the two routes that gate would deadlock: setting the new password, and
// reading who you are -- which is how a client discovers it must ask for one.
// Nothing else may use this.
func (s *Server) requireAuthDuringPasswordChange(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rbac.FromContext(r.Context()) == nil {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED",
				"this endpoint requires an access token")
			return
		}
		next(w, r)
	}
}

// authenticated requires a signed-in officer and nothing more.
//
// For resources whose scope is the caller themselves -- their own
// notifications -- where the only authorization question is who is asking. A
// permission would be the wrong shape: there is no role that should be able to
// read another officer's bell, so there is nothing to name.
//
// The must-change-password gate still applies. An account that has not been
// claimed has no work waiting on it to read about.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := rbac.FromContext(r.Context())
		if p == nil {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED",
				"this endpoint requires an access token")
			return
		}
		if p.RestrictedToPasswordChange() {
			writeError(w, http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED",
				"set a password of your own before using the platform")
			return
		}
		next(w, r)
	}
}

// require enforces a permission, and records every denial.
//
// Denials are audited rather than merely counted. "Who tried to approve a
// claim outside their taluka" is a question that gets asked, and it cannot be
// answered later from a metric.
func (s *Server) require(perm rbac.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		p := rbac.FromContext(ctx)

		if p == nil {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED",
				"this endpoint requires an access token")
			return
		}

		// A first sign-in with an administrator-issued password can do
		// exactly one thing: replace it.
		if p.RestrictedToPasswordChange() {
			writeError(w, http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED",
				"set a password of your own before using the platform")
			return
		}

		if !p.Can(perm) {
			s.Audit.Record(ctx, audit.Event{
				Action:       audit.ActionAuthzDenied,
				Outcome:      audit.OutcomeDeny,
				ActorIP:      clientIP(r, s.TrustedProxyHops),
				ResourceType: "endpoint",
				ResourceID:   r.Method + " " + r.URL.Path,
				RequestID:    RequestID(ctx),
				Detail: map[string]any{
					"required_permission": string(perm),
					"roles":               p.Roles(),
				},
			}.FromContext(ctx))

			writeError(w, http.StatusForbidden, "FORBIDDEN",
				"your role does not carry "+string(perm))
			return
		}

		next(w, r)
	}
}

// logging records one line per request.
func logging(log *slog.Logger, trustedHops int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"request_id", RequestID(r.Context()),
			"ip", clientIP(r, trustedHops),
		}
		if p := rbac.FromContext(r.Context()); p != nil {
			attrs = append(attrs, "user_id", p.UserID, "role", string(p.PrimaryRole()))
		}
		// The reason, for anything refused. These messages are the ones the
		// caller was already shown -- writeError replaces anything that looks
		// internal before it gets this far -- so they carry nothing the
		// client did not see.
		if rec.errCode != "" {
			attrs = append(attrs, "error_code", rec.errCode,
				"error", truncate(rec.errMessage, 200))
		}

		// Server errors are the operator's problem; client errors are the
		// caller's. Logging both at the same level makes neither visible.
		if rec.status >= 500 {
			log.Error("request", attrs...)
		} else {
			log.Info("request", attrs...)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int

	// Why a request was refused: the code and message writeError sent.
	//
	// The request line used to carry only the status, and the farmer's app
	// keeps only a friendly translation -- so the real reason for a refusal
	// was recorded nowhere. Found on a handset: a claim walked and filmed at
	// all four points came back 400, and "documents_promised is not
	// satisfied" had to be recovered by replaying the request by hand.
	errCode, errMessage string
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets writeError reach every recorder in the chain.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) noteError(code, message string) {
	r.errCode, r.errMessage = code, message
}

// noteError hands a refusal's reason to every recorder wrapping w, so the
// request log can say why and not only that.
func noteError(w http.ResponseWriter, code, message string) {
	for cur := w; cur != nil; {
		if n, ok := cur.(interface{ noteError(code, message string) }); ok {
			n.noteError(code, message)
		}
		u, ok := cur.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		cur = u.Unwrap()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
