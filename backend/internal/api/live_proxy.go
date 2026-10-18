package api

import (
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Passing a handset's live session through to the relay.
//
// # Why the platform proxies instead of the handset connecting directly
//
// Because the alternative is publishing the AI service. That process holds the model
// credential and the Gemini quota, and the moment it is reachable from a phone it is
// reachable from everywhere -- so every protection on it has to be its own, in a
// service whose job is audio.
//
// Proxying keeps one public surface. The handset talks to the platform, which it is
// already authenticated to, and the relay stays on the internal network where it
// started. Caddy needs no new route, the AI service needs no new port, and the
// authentication stays in the place that already does authentication.
//
// The signed ticket survives all of that and is not made redundant by it. It is what
// stops a request that reaches the relay by some other path -- a misconfigured
// firewall, a future second caller, somebody inside the network -- from being free
// audio. Two checks in series, each cheap, neither relied on alone.
//
// # Why the app's route names differ from the relay's
//
// They do not, deliberately. The app calls /api/v1/live/webrtc/offer and so does the
// relay; this forwards the path unchanged. A proxy that renames things is a proxy
// somebody has to read before they can follow a request through it, and the farmer app
// contract test compares strings.

// liveProxyTimeout bounds the offer exchange.
//
// Only the offer: a WebSocket is meant to stay open, and a timeout on the proxy would
// end a conversation mid-sentence. Fifteen seconds is generous for an SDP answer, which
// is arithmetic and no network round trips of its own.
const liveProxyTimeout = 15 * time.Second

// proxyWebRTCOffer forwards a handset's offer to the relay and returns the answer.
//
// The internal token is added here, because the handset must never hold it: that token
// authenticates the platform to its own analysis services, and a copy on ten thousand
// phones is a copy that has leaked. The handset's own credential is the ticket, which
// travels in the body and which the relay checks.
func (s *Server) proxyWebRTCOffer(w http.ResponseWriter, r *http.Request) {
	if s.AIServiceURL == "" {
		writeError(w, http.StatusServiceUnavailable, "LIVE_UNAVAILABLE",
			"live guidance is not configured")
		return
	}

	// Bounded: an SDP offer is a few kilobytes and anything larger is not one.
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST",
			"the offer could not be read")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		s.AIServiceURL+"/api/v1/live/webrtc/offer", strings.NewReader(string(body)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIVE_FAILED",
			"the offer could not be forwarded")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: liveProxyTimeout}
	res, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "LIVE_UNAVAILABLE",
			"the assistant could not be reached")
		return
	}
	defer res.Body.Close()

	// The relay's own status is passed through rather than flattened. A refused ticket
	// is a 401 and a farmer's app reads it as "start again"; collapsing everything to
	// 503 would have it wait for a service that is running.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, 256<<10))
}

// proxyLiveSocket forwards the WebSocket to the relay.
//
// ReverseProxy handles the upgrade, which is why this is not written by hand: an
// upgrade proxied by hand means copying bytes in both directions and getting the close
// handshake right, and getting it wrong means sockets that leak rather than sockets
// that fail.
//
// No timeout on the transport. A conversation is meant to stay open, and the session's
// own limits -- the relay's ceiling and the farmer's daily allowance -- are what end
// it.
func (s *Server) proxyLiveSocket(w http.ResponseWriter, r *http.Request) {
	// The query untouched: the ticket and the claim's context are both in it, and the
	// relay reads them from there.
	s.proxyRelaySocket(w, r, r.URL.RawQuery)
}

// proxyOfficerLiveSocket forwards an officer's dictation, and only their dictation.
//
// The one difference from the farmer's socket above is the query string, and it is the
// difference that matters: the platform states the mode rather than letting the browser
// ask for one, and nothing else travels. See officerRelayQuery.
func (s *Server) proxyOfficerLiveSocket(w http.ResponseWriter, r *http.Request) {
	s.proxyRelaySocket(w, r, officerRelayQuery(r.URL.Query()).Encode())
}

// proxyRelaySocket is the upgrade itself, with the query the caller settled on.
func (s *Server) proxyRelaySocket(
	w http.ResponseWriter, r *http.Request, rawQuery string,
) {
	if s.AIServiceURL == "" {
		writeError(w, http.StatusServiceUnavailable, "LIVE_UNAVAILABLE",
			"live guidance is not configured")
		return
	}

	target, err := url.Parse(s.AIServiceURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIVE_FAILED",
			"the relay address is not usable")
		return
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			// The relay's own path, and the query its caller settled on.
			pr.Out.URL.Path = "/api/v1/live/session"
			pr.Out.URL.RawQuery = rawQuery
			pr.Out.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// An upgrade that fails cannot be answered with JSON once bytes have
			// moved, so this only reaches the farmer before the handshake. Logged
			// either way, because a relay that refuses upgrades is a fault nobody
			// else will notice.
			s.Log.Warn("proxying a live socket failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "LIVE_UNAVAILABLE",
				"the assistant could not be reached")
		},
	}
	proxy.ServeHTTP(w, r)
}
