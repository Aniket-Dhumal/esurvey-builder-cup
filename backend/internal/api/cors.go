package api

import (
	"net/http"
	"slices"
	"strings"
)

// cors permits the browser console to call the API cross-origin.
//
// The allowed origins are an explicit list rather than "*": credentials will
// be attached once officer authentication exists, and a wildcard cannot carry
// them. Configure via ESURVEY_CORS_ORIGINS.
func cors(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(allowed, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			// Every method the console actually uses. PUT was missing, and
			// the one route that needs it is an officer amending their own
			// verification after reading their colleague's -- so the browser
			// refused that at the preflight and nobody could revise a finding
			// cross-origin. Handler tests could not see it: they call the
			// handler, and the browser never got that far.
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			h.Set("Access-Control-Allow-Headers",
				"Content-Type, Authorization, Idempotency-Key, X-App-Version, "+
					"X-Farmer-Grant")
			h.Set("Access-Control-Max-Age", "600")
			// The response varies by Origin, so caches must not serve one
			// origin's response to another.
			h.Add("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ParseOrigins splits a comma-separated origin list, trimming blanks.
func ParseOrigins(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
