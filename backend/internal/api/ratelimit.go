package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// A ceiling on how fast anyone may call this API.
//
// # Why this is separate from the login throttle
//
// The login throttle counts *failed* authentication and locks an identity
// out. It is about guessing a password. This is about volume: a signed-in
// officer, or a script holding a valid token, walking every report in a
// district one request at a time. Nothing was failing, so the throttle never
// saw it.
//
// # What it is actually protecting
//
// Two things, in order of seriousness. First, bulk extraction: every listing
// here returns identified farmers, their land and their photographs, and the
// pagination added recently made walking them straightforward. A limit does
// not stop a determined insider, but it turns "download the district in a
// minute" into something slow enough to notice in the audit trail.
//
// Second, the database. The search boxes run trigram queries and the rollup
// aggregates a district; a tight loop against either is a self-inflicted
// outage.
//
// # Why per identity rather than per address
//
// Officers in one taluka office share an address, and rate-limiting them as
// one caller would punish the office for its NAT. The identity comes from a
// signed token, so it cannot be spoofed the way an X-Forwarded-For header
// can. Unauthenticated requests fall back to the address, where there is
// nothing else to key on.

const (
	// rateWindow is the period a budget covers.
	rateWindow = time.Minute

	// rateBurst is what one signed-in officer may spend in that window.
	//
	// Generous on purpose. A verification screen loads a report, its
	// captures, its findings and its media in one go, and an officer working
	// briskly through a worklist should never see this. It is a ceiling on
	// automation, not a pace for humans.
	rateBurst = 300

	// rateBurstAnonymous is the budget before anyone has signed in.
	//
	// Lower, because the only legitimate unauthenticated traffic is a
	// handful of login and OTP calls, and those already have their own
	// throttle.
	rateBurstAnonymous = 60

	// rateBurstFarmerAuth is the per-address budget for a farmer signing in
	// and for any caller refreshing a session.
	//
	// # Why it is so much larger
	//
	// Rural mobile carriers put thousands of subscribers behind one public
	// address (carrier-grade NAT). Keyed by address at sixty a minute, the
	// morning after a district-wide event -- every handset under one gateway
	// refreshing within the same hour -- was refused wholesale, and a refused
	// refresh stalls that phone's whole queue (docs/SYNC_SCALE_REVIEW.md, H1).
	//
	// These routes carry their own protection that does not depend on the
	// address: a refresh token is 256 random bits and single-use, and one-time
	// codes are throttled per mobile number with a resend cooldown and a daily
	// cap. The address budget is only a ceiling on a flood.
	rateBurstFarmerAuth = 1200
)

// farmerAuthRoutes are the anonymous routes a farmer's phone calls, which
// share an address with every other phone on the same carrier gateway.
var farmerAuthRoutes = map[string]bool{
	"/api/v1/auth/refresh":       true,
	"/api/v1/auth/farmer/otp":    true,
	"/api/v1/auth/farmer/verify": true,
	"/api/v1/auth/logout":        true,
}

// rateLimit caps requests per caller per window.
//
// Fails open. If Redis is unreachable the request proceeds: the limiter
// exists to slow abuse, and letting a cache outage take down an officer's
// ability to file a panchnama would be a worse failure than the one it
// prevents.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Redis == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Readiness and liveness are polled by the orchestrator every few
		// seconds and must never be throttled -- a limiter that fails a
		// health check restarts the container it was protecting.
		switch r.URL.Path {
		case "/healthz", "/readyz":
			next.ServeHTTP(w, r)
			return
		}

		key, budget := s.rateKey(r)

		count, ttl, err := s.spend(r.Context(), key)
		if err != nil {
			s.Log.Warn("rate limiter unavailable", "error", err)
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("RateLimit-Limit", strconv.Itoa(budget))
		w.Header().Set("RateLimit-Remaining", strconv.Itoa(max(0, budget-count)))
		w.Header().Set("RateLimit-Reset", strconv.Itoa(int(ttl.Seconds())))

		if count > budget {
			// Retry-After in seconds, so a client can wait rather than
			// hammer -- a 429 with no guidance produces a tighter loop than
			// the one it refused.
			w.Header().Set("Retry-After", strconv.Itoa(int(ttl.Seconds())+1))
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED",
				fmt.Sprintf(
					"Too many requests. Try again in %d seconds.",
					int(ttl.Seconds())+1))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// rateKey identifies the caller, and says what they may spend.
func (s *Server) rateKey(r *http.Request) (key string, budget int) {
	// The principal is set by authenticate, which runs closer to the handler
	// than this does -- so parse the token's subject only if it is already
	// there, and otherwise fall back to the address.
	if p := rbac.FromContext(r.Context()); p != nil && p.UserID != "" {
		return "rl:u:" + p.UserID, rateBurst
	}
	if farmerAuthRoutes[r.URL.Path] {
		// A bucket of its own, so a busy gateway's sign-ins and refreshes
		// neither exhaust nor are exhausted by other anonymous traffic.
		return "rl:f:" + clientIP(r, s.TrustedProxyHops), rateBurstFarmerAuth
	}
	return "rl:a:" + clientIP(r, s.TrustedProxyHops), rateBurstAnonymous
}

// spend increments the caller's counter and returns it with the window's
// remaining life.
//
// A fixed window rather than a sliding one: two counters and a Lua script
// would be more precise at the boundary, and the precision buys nothing here.
// The limit is a ceiling on automation, and an automation that doubles its
// rate for one second either side of a minute boundary is still limited.
func (s *Server) spend(ctx context.Context, key string) (int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pipe := s.Redis.TxPipeline()
	incr := pipe.Incr(ctx, key)
	// Set the expiry only when the key is new, so a caller cannot extend
	// their own window by continuing to spend in it.
	pipe.ExpireNX(ctx, key, rateWindow)
	ttl := pipe.TTL(ctx, key)

	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return 0, 0, err
	}

	d := ttl.Val()
	if d <= 0 {
		d = rateWindow
	}
	return int(incr.Val()), d, nil
}
