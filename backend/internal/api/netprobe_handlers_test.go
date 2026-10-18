package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// What the probe promises the handset, and the arithmetic behind it.
//
// These are assertions about numbers rather than about code, which is unusual and is
// the point: the numbers are the whole behaviour. A single floor of 300 kbit/s passed
// every test in this package while refusing the 256 kbit/s link the live agent was
// designed for, because nothing here had ever been asked to compare the threshold with
// the target.

func probeHeaders(t *testing.T, query string) http.Header {
	t.Helper()
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/net/probe"+query, nil)
	w := httptest.NewRecorder()
	s.netProbeDown(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("the probe answered %d", w.Code)
	}
	return w.Header()
}

func TestTheWebRTCFloorAdmitsTheLinkTheAgentIsDesignedFor(t *testing.T) {
	// The failure this replaces: 300 kbit/s is above the 256 the whole design targets,
	// so the gate refused every farm it was built for and the feature only ever worked
	// in an office.
	const designTarget = 256
	if LiveMinimumKbps > designTarget {
		t.Errorf("the floor is %d kbit/s on a link the design assumes has %d",
			LiveMinimumKbps, designTarget)
	}

	// And not so low that it admits a link that would only stutter. A session over
	// WebRTC costs 48 kbit/s each way -- 24 of Opus payload and 20 of IP, UDP, RTP and
	// SRTP envelope at fifty packets a second, plus RTCP -- and the floor is twice that.
	if LiveMinimumKbps < 2*48 {
		t.Errorf("%d kbit/s leaves a conversation no room at all", LiveMinimumKbps)
	}
}

func TestTheSocketFloorIsHigherBecauseRawPCMIsHigher(t *testing.T) {
	// The fallback has no codec in it: 16 kHz 16-bit mono is 256 kbit/s going up and the
	// model's 24 kHz reply is 384 coming down. A floor that admitted a link to the
	// socket on the strength of what WebRTC costs would admit it to a conversation that
	// cannot happen.
	if LiveSocketMinimumKbps <= LiveMinimumKbps {
		t.Errorf("the socket floor (%d) is not above the WebRTC floor (%d)",
			LiveSocketMinimumKbps, LiveMinimumKbps)
	}
	if LiveSocketMinimumKbps < 400 {
		t.Errorf("%d kbit/s will not carry 24 kHz PCM downstream",
			LiveSocketMinimumKbps)
	}
}

func TestTheProbeStatesBothFloorsSoAHandsetNeedsNoBuild(t *testing.T) {
	// The thresholds travel with the probe so a handset never holds a stale copy: what
	// counts as good enough depends on what the relay can sustain, which is not
	// something a phone can know.
	header := probeHeaders(t, "")

	for name, want := range map[string]int{
		"X-Esurvey-Live-Min-Kbps":         LiveMinimumKbps,
		"X-Esurvey-Live-Socket-Min-Kbps":  LiveSocketMinimumKbps,
		"X-Esurvey-Live-Max-Rtt-Ms":       LiveMaxRTTms,
		"X-Esurvey-Live-Unthrottled-Kbps": LiveUnthrottledKbps,
	} {
		if got := header.Get(name); got != strconv.Itoa(want) {
			t.Errorf("%s said %q, wanted %d", name, got, want)
		}
	}
}

func TestATinyProbeIsServedSoARoundTripCanBeMeasured(t *testing.T) {
	// Because the throughput probe's own duration is not a round trip. A 128 KB body on
	// a 256 kbit/s link takes four seconds to arrive, and a handset reading that as its
	// round trip would refuse every rural link against a 600 ms ceiling. One byte
	// measures the road rather than the load.
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/net/probe?bytes=1", nil)
	w := httptest.NewRecorder()
	s.netProbeDown(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("the probe answered %d", w.Code)
	}
	if n := w.Body.Len(); n != 1 {
		t.Errorf("a one-byte probe returned %d bytes", n)
	}
}
