package api

import (
	"crypto/rand"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Deciding whether a farm has the connection for live guidance.
//
// # Why the handset must measure rather than ask Android
//
// Because Android answers the wrong question. It reports the radio -- 4G, 5G, wifi
// -- and a handset showing four bars of 4G in a village at six in the evening can be
// getting 40 kbit/s, because the tower is shared with everybody else who just got
// home. The radio says what is possible; it says nothing about what is available.
//
// So the app measures what it can actually get to this service, which is the only
// number that decides anything.
//
// # What the threshold is, and is not
//
// It depends on the transport, which is the thing the previous single figure of 300
// could not say. A session over WebRTC carries Opus, and a session over the
// WebSocket carries raw PCM, and the two differ by more than a factor of ten -- so
// one number was necessarily wrong for both, and it was: 300 refused the 256 kbit/s
// link this whole feature is designed for, while cheerfully admitting a 400 kbit/s
// one to the socket, which cannot carry a conversation at all.
//
// It is not 10 Mbit/s either. That was considered and would switch the feature off
// in almost every real field. 10 Mbit/s is instead the point above which frames stop
// being rationed.

// LiveMinimumKbps is the sustained throughput a session over WebRTC needs, per
// direction.
//
// # The arithmetic
//
// Opus carries mono speech at 24 kbit/s, which is what the relay's answer asks the
// handset for and what the relay's own encoder is set to -- see OPUS_BITRATE in the
// AI service's live_webrtc.py. The payload is not the bill, though: at 20 ms a
// packet there are fifty packets a second, and each carries an IPv4 header (20
// bytes), UDP (8), RTP (12) and the SRTP tag (10). Fifty bytes of envelope at fifty
// packets a second is 20 kbit/s. So 24 + 20 is 44, and RTCP with the ICE keepalives
// rounds it to 48 kbit/s in each direction.
//
// Doubling that is the floor. A link measured by a 128 KB probe is being measured at
// its best moment, and what it sustains through a five-minute conversation on a
// shared rural tower is less; the spare half is also what carries a frame when the
// model asks for one, without the voice stuttering while it goes.
//
// So 100 kbit/s, per direction, which admits the 256 kbit/s link with room to spare
// and still refuses a link that would only stutter. A farm below this loses nothing
// it had: the scripted walk needs no network at all.
const LiveMinimumKbps = 100

// LiveSocketMinimumKbps is the same question for the fallback, which is a different
// answer.
//
// The WebSocket carries the audio as raw PCM, because that is what the Live API
// speaks and the socket has no codec in it. 16 kHz 16-bit mono is 256 kbit/s going
// up before any framing, and the model's reply at 24 kHz is 384 kbit/s coming down;
// TCP, TLS and WebSocket framing add a few per cent to each. The floor is set by the
// worse direction, which is the downlink at about 400.
//
// Then the half again, for a reason particular to this transport: TCP will not
// deliver the fourth packet until the third arrives, so a link at exactly the
// required rate does not degrade, it stalls. 600 kbit/s per direction is what a
// conversation over the socket actually wants, and stating it separately is the
// point -- a handset that could not negotiate WebRTC is on a link that has to be
// judged again against this, not against the figure above.
const LiveSocketMinimumKbps = 600

// LiveMaxRTTms is the round trip past which a spoken exchange stops working.
//
// Six hundred milliseconds, unchanged: past it the farmer starts talking over the
// answer, and that is a property of conversation rather than of the codec.
//
// It has to be an actual round trip, though, which is worth saying because it is
// easy to measure something else. The throughput probe's own duration is not one: a
// 128 KB body on a 256 kbit/s link takes four seconds to arrive, and a handset that
// read that as its round trip would refuse every link this feature exists for, on the
// one measurement that looks most like an answer. GET /api/v1/net/probe?bytes=1 is
// how to ask this question instead.
const LiveMaxRTTms = 600

// LiveUnthrottledKbps is where frames stop being rationed.
//
// Above this the handset may send a frame every three seconds as the design
// describes; below it, frames go only when the model asks or the farmer presses the
// shutter, which is the largest single saving available.
//
// Bandwidth alone would put this far lower now. A frame every three seconds is 80
// kbit/s, which on top of the 48 above is 128 -- so a link with half a megabit could
// carry it. It stays at 10 Mbit/s because the other cost is the farmer's: frames on a
// metered cellular connection are their data, spent on images nobody asked for, and
// ten megabits is in practice the office rather than the field. That is a decision
// about somebody's money and not one this file should quietly re-derive.
const LiveUnthrottledKbps = 10_000

// probeBytes is what a download probe returns.
//
// 128 KB. Large enough to measure a rural link without the measurement being
// dominated by connection setup, small enough that a farmer on a metered connection
// pays almost nothing to find out the answer -- and the answer is cached on the
// handset, so this is not paid per question.
const probeBytes = 128 << 10

// netProbeDown serves incompressible bytes for a throughput measurement.
//
// Random rather than zeroes, and marked no-store: a compressible body measures the
// gzip encoder, and a cached one measures nothing at all. Both would report a fast
// link on a farm that has none, which is the failure that matters -- the app would
// then open a session it cannot sustain and the farmer would be left with guidance
// that stutters, instead of the offline guidance that works.
func (s *Server) netProbeDown(w http.ResponseWriter, r *http.Request) {
	size := probeBytes
	if n, err := strconv.Atoi(r.URL.Query().Get("bytes")); err == nil {
		if n > 0 && n <= 1<<20 {
			size = n
		}
	}

	body := make([]byte, size)
	if _, err := rand.Read(body); err != nil {
		writeError(w, http.StatusInternalServerError, "PROBE_FAILED",
			"could not generate the probe")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Esurvey-Server-Time", time.Now().UTC().Format(time.RFC3339Nano))
	// The thresholds travel with the probe so a handset never holds a stale copy of
	// them. Changing what counts as good enough is then a server change, which is
	// where it belongs: the decision depends on what the relay can sustain.
	w.Header().Set("X-Esurvey-Live-Min-Kbps", strconv.Itoa(LiveMinimumKbps))
	// And the other floor, because which one applies depends on which transport the
	// handset ends up on and that is not known until it has tried. A handset that
	// negotiated WebRTC is judged against the figure above; one that fell back to the
	// socket has to be judged again against this, and a handset holding only one number
	// cannot make that distinction.
	w.Header().Set("X-Esurvey-Live-Socket-Min-Kbps",
		strconv.Itoa(LiveSocketMinimumKbps))
	w.Header().Set("X-Esurvey-Live-Max-Rtt-Ms", strconv.Itoa(LiveMaxRTTms))
	w.Header().Set("X-Esurvey-Live-Unthrottled-Kbps",
		strconv.Itoa(LiveUnthrottledKbps))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// netProbeUp measures the direction that actually matters.
//
// A farmer's uplink is what carries their voice and their frames, and on cellular
// it is the asymmetric half -- a link that downloads at 2 Mbit/s may upload at 120
// kbit/s. Measuring only the download would open sessions that cannot carry the
// farmer's own speech.
//
// The body is read and discarded. Nothing about it is kept: it is random bytes from
// a handset measuring a road.
func (s *Server) netProbeUp(w http.ResponseWriter, r *http.Request) {
	n, err := discard(r.Body, 1<<20)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PROBE_FAILED",
			"the probe upload could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"received_bytes":   n,
		"min_kbps":         LiveMinimumKbps,
		"socket_min_kbps":  LiveSocketMinimumKbps,
		"max_rtt_ms":       LiveMaxRTTms,
		"unthrottled_kbps": LiveUnthrottledKbps,
	})
}

// discard reads a body to its end without keeping it, and says how much there was.
//
// Bounded, because an unbounded read is a handset able to make this process hold a
// gigabyte by uploading one.
func discard(r io.Reader, limit int64) (int64, error) {
	return io.Copy(io.Discard, io.LimitReader(r, limit))
}
