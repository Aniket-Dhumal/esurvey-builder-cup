import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/foundation.dart';

import 'api.dart';
import 'config.dart';

/// Whether this farm's connection can carry a spoken conversation.
///
/// # Why the handset measures instead of asking Android
///
/// Because Android answers a different question. It reports the radio -- 4G, wifi,
/// five bars -- and a handset showing four bars of 4G in a village at six in the
/// evening can be getting forty kilobits, because the tower is shared with everyone
/// else who just got home. The radio says what is possible. It says nothing about
/// what is available, and a farmer does not care about the difference.
///
/// So this measures what can actually be got to the office, which is the only
/// number that decides anything.
///
/// # What it decides
///
/// One thing: whether to offer live guidance at all. The walk does not depend on
/// the answer. Every instruction a farmer needs to file a claim is scripted, works
/// with no network, and is unaffected by any of this -- the live agent is an
/// addition, and a farm that cannot carry it loses nothing it had.
///
/// # Why not ten megabits
///
/// It was considered, and it would switch the feature off in almost every real
/// field. The whole design targets a quarter of a megabit because that is what
/// rural cellular gives; a ten-megabit floor would mean the careful work on
/// bandwidth never runs and the agent only ever speaks in an office. Ten megabits
/// is instead where frames stop being rationed.
class LinkQuality {
  const LinkQuality({
    required this.downKbps,
    required this.upKbps,
    required this.rttMs,
    required this.at,
    this.minKbps = 100,
    this.maxRttMs = 600,
    this.unthrottledKbps = 10000,
    this.socketMinKbps = 600,
  });

  /// What could not be measured at all.
  const LinkQuality.none()
    : downKbps = 0,
      upKbps = 0,
      rttMs = double.infinity,
      at = null,
      minKbps = 100,
      maxRttMs = 600,
      unthrottledKbps = 10000,
      socketMinKbps = 600;

  final double downKbps;
  final double upKbps;
  final double rttMs;
  final DateTime? at;

  /// The thresholds, as the office stated them.
  ///
  /// Carried from the probe's own headers rather than compiled in, so that what
  /// counts as good enough can change with what the relay can sustain without
  /// every handset needing a new build.
  /// What a conversation over WebRTC needs, per direction.
  ///
  /// A hundred kilobits, and the arithmetic matters because the old figure of three
  /// hundred refused the very link this feature was built for. Opus carries speech at
  /// 24 kbit/s; at a packet every 20 milliseconds the IP, UDP, RTP and SRTP headers
  /// add another 20; call it 48 with the keepalives. Doubled, because a probe measures
  /// a link at its best moment and the spare half is what carries a frame when the
  /// assistant asks without the voice stuttering.
  ///
  /// So a 256 kbit/s farm passes with room, and a link that would only stutter is
  /// still refused.
  final int minKbps;
  final int maxRttMs;
  final int unthrottledKbps;

  /// What the fallback needs, per direction, which is six times as much.
  ///
  /// The socket carries raw PCM: 256 kbit/s of 16 kHz speech going up and 384 of the
  /// model's 24 kHz reply coming down, plus framing. Half again on top, because TCP
  /// does not degrade gracefully when it runs short -- it stalls, and one late packet
  /// holds every packet behind it.
  ///
  /// Judged separately and only once the transport is known. Judging it up front would
  /// refuse WebRTC on a link that carries it perfectly well; not judging it at all
  /// opens a fallback session that cannot work, which is what "always connects" buys
  /// if nobody checks.
  final int socketMinKbps;

  /// Whether a live session is worth opening.
  ///
  /// Both directions, and the round trip. The uplink is the one that matters most
  /// and the one usually forgotten: on cellular it is the asymmetric half, and a
  /// link that downloads at two megabits may upload at a hundred and twenty
  /// kilobits -- which carries the model's voice to the farmer perfectly and the
  /// farmer's voice nowhere.
  bool get canTalk =>
      downKbps >= minKbps && upKbps >= minKbps && rttMs <= maxRttMs;

  /// Whether this link could carry the conversation over the socket instead.
  ///
  /// Asked after a WebRTC negotiation has failed, and the answer is usually no on the
  /// links that make WebRTC fail. Better a farmer who is told the connection will not
  /// carry it than one who talks into a stalling stream for eight minutes.
  bool get canTalkOverSocket =>
      downKbps >= socketMinKbps && upKbps >= socketMinKbps && rttMs <= maxRttMs;

  /// Whether frames may go at the full rate rather than only when asked for.
  bool get framesUnthrottled =>
      canTalk && math.min(downKbps, upKbps) >= unthrottledKbps;

  /// Why live guidance is not on offer, in the farmer's own language.
  ///
  /// Said rather than hidden. A farmer who is offered a talking assistant on one
  /// visit and not the next will assume the app is broken unless told, and "the
  /// connection here is too slow for talking" is something they already
  /// understand about where they are standing.
  ({String mr, String en})? get refusal {
    if (at == null) {
      return (
        mr: 'नेटवर्क तपासता आले नाही. सूचना ऐकू येतील, पण संवाद करता येणार नाही.',
        en:
            'The connection could not be checked. You will hear guidance, but '
            'cannot talk to the assistant.',
      );
    }
    if (canTalk) return null;
    if (rttMs > maxRttMs) {
      return (
        mr:
            'इथले नेटवर्क धिमे आहे. बोलून संवाद करता येणार नाही, पण सूचना ऐकू '
            'येतील.',
        en:
            'The connection here is slow. Talking is not available, but you '
            'will still hear the guidance.',
      );
    }
    return (
      mr:
          'इथल्या नेटवर्कचा वेग पुरेसा नाही. सूचना ऐकू येतील, पण बोलून संवाद '
          'करता येणार नाही.',
      en:
          'The connection here is not fast enough for talking. You will still '
          'hear the guidance.',
    );
  }

  @override
  String toString() =>
      'down ${downKbps.round()} kbps, up ${upKbps.round()} kbps, '
      'rtt ${rttMs.round()} ms, canTalk $canTalk';
}

/// Measures the link, sparingly.
class LinkProbe {
  LinkProbe({required this.api});

  final Api api;

  /// How long a measurement is trusted.
  ///
  /// Three minutes. A farmer walking a field does not change tower in that time,
  /// and measuring before every question would spend more of the connection on
  /// measuring it than on using it.
  static const freshFor = Duration(minutes: 3);

  /// How much is transferred to measure the throughput.
  ///
  /// 32 KB. It was 128, which at the floor this now admits is a ten second wait on
  /// each leg -- a farmer standing in a field watching a screen decide something they
  /// did not ask about. 32 KB still takes long enough at 100 kbit/s to swamp
  /// connection setup, and costs a farmer on a metered connection almost nothing.
  static const probeBytes = 32 * 1024;

  /// How the round trip is measured, separately from the throughput.
  ///
  /// One byte, because a round trip is a round trip. This used to be taken as the
  /// smaller of the two transfer times, which is not a round trip but the time to
  /// move a large body -- so on the 256 kbit/s link the whole feature targets, the
  /// 64 KB upload alone took about two seconds and the measurement reported an RTT of
  /// two thousand milliseconds. Every such farm failed the 600 ms test whatever the
  /// bandwidth floor was set to, and the refusal read as "too slow to talk" on a link
  /// that carries Opus four times over.
  static const rttProbeBytes = 1;

  LinkQuality? _last;

  /// The last measurement, if it is still fresh.
  LinkQuality? get cached {
    final last = _last;
    if (last?.at == null) return null;
    if (DateTime.now().difference(last!.at!) > freshFor) return null;
    return last;
  }

  /// Measure, or return a fresh enough previous answer.
  Future<LinkQuality> measure({bool force = false}) async {
    if (!force) {
      final fresh = cached;
      if (fresh != null) return fresh;
    }

    try {
      // The round trip first, and on its own.
      //
      // It used to be inferred from the transfer times, which is not a round trip but
      // the time to move a body -- so on a 256 kbit/s link the measurement reported
      // two thousand milliseconds and every such farm failed the 600 ms test whatever
      // the bandwidth floor said. One byte answers the question the test is actually
      // asking: how long before the other end replies at all.
      var rttMs = double.infinity;
      final pinged = DateTime.now();
      final ping = await api.get('/api/v1/net/probe?bytes=$rttProbeBytes');
      if (ping.ok) {
        rttMs = DateTime.now().difference(pinged).inMilliseconds.toDouble();
      }

      final started = DateTime.now();
      final res = await api.get('/api/v1/net/probe?bytes=$probeBytes');
      final elapsed = DateTime.now().difference(started);
      if (!res.ok) return const LinkQuality.none();

      final bytes = res.byteCount;
      if (bytes < probeBytes ~/ 2) {
        // A short body measures a failure rather than a link.
        return const LinkQuality.none();
      }
      final downKbps = _kbps(bytes, elapsed);

      // Up, which is the direction that carries the farmer's voice and is the
      // asymmetric half of a cellular link.
      final upStarted = DateTime.now();
      final upRes = await api.putBytes(
        '/api/v1/net/probe',
        List<int>.filled(probeBytes ~/ 2, 7),
      );
      final upElapsed = DateTime.now().difference(upStarted);
      final upKbps = upRes.ok ? _kbps(probeBytes ~/ 2, upElapsed) : 0.0;

      final quality = LinkQuality(
        downKbps: downKbps,
        upKbps: upKbps,
        rttMs: rttMs,
        at: DateTime.now(),
        minKbps: _header(res, 'x-esurvey-live-min-kbps', fallbackMinKbps),
        maxRttMs: _header(res, 'x-esurvey-live-max-rtt-ms', fallbackMaxRttMs),
        unthrottledKbps: _header(res, 'x-esurvey-live-unthrottled-kbps', 10000),
        socketMinKbps: _header(
          res,
          'x-esurvey-live-socket-min-kbps',
          fallbackSocketMinKbps,
        ),
      );
      _last = quality;
      debugPrint('[link] $quality');
      return quality;
    } catch (e) {
      // Offline, refused, timed out. None of them are errors a farmer should see:
      // the answer is simply that talking is not available here.
      debugPrint('[link] could not measure: $e');
      return const LinkQuality.none();
    }
  }

  static double _kbps(int bytes, Duration took) {
    final ms = took.inMilliseconds;
    if (ms <= 0) return 0;
    return (bytes * 8) / ms; // bits per millisecond is kbit per second
  }

  static int _header(Result res, String name, int fallback) {
    final raw = res.headers[name];
    if (raw == null) return fallback;
    return int.tryParse(raw) ?? fallback;
  }
}

/// The thresholds a build falls back to when the office did not say.
///
/// Matched to the server's own constants. Compiled in only as a fallback: the
/// server's headers win, because the decision depends on what the relay can
/// sustain and that is not something a handset can know.
const fallbackMinKbps = 100;

/// What the socket fallback needs, where the office did not say.
///
/// Six times the WebRTC figure, because raw PCM is six times the traffic. See
/// LinkQuality.socketMinKbps for the arithmetic.
const fallbackSocketMinKbps = 600;
const fallbackMaxRttMs = 600;

/// Whether this build is allowed to talk at all.
///
/// A separate question from whether the link can carry it. Live guidance is off
/// unless the build was made with it on, so a trial can be run without it and a
/// handset in the field cannot be surprised by a feature nobody tested there.
const liveGuidanceEnabled = bool.fromEnvironment(
  'ESURVEY_LIVE_GUIDANCE',
  defaultValue: false,
);

/// Where the relay is, for a build that has it enabled.
/// Where the relay is, for a build that has it enabled.
///
/// The platform's own address, not the AI service's: a session is proxied through the
/// platform so the process holding the model credential is never published. Used only
/// as a fallback -- the grant returns the URL to open, with the signed ticket already
/// on it, and that is what a session should use.
String get liveSessionUrl {
  final base = Config.apiBase.replaceFirst(RegExp(r'^http'), 'ws');
  return '$base/api/v1/live/relay';
}
