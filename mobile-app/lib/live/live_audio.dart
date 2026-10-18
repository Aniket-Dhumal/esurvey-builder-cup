import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter_pcm_sound/flutter_pcm_sound.dart';
import 'package:record/record.dart';

import '../core/faults.dart';

/// The microphone and the speaker, for a spoken session.
///
/// # Why raw PCM and not a codec
///
/// Because the relay does the codec. Over WebRTC the platform captures, encodes and
/// sends the microphone itself and none of this runs; over the WebSocket the relay
/// wants 16 kHz PCM and gets it. Compressing here would mean an Opus implementation
/// per platform, and two places for a sample rate to be wrong instead of none.
///
/// # Why the speaker is fed rather than handed a file
///
/// The assistant's voice arrives while it is still being spoken. A player that wants a
/// finished file would have to wait for the sentence to end before starting it, which
/// doubles the latency of every exchange and makes a conversation feel like a radio
/// call. Feeding a buffer plays what has arrived and keeps playing as more does.
class LiveAudio {
  LiveAudio({AudioRecorder? recorder})
    : _recorder = recorder ?? AudioRecorder();

  final AudioRecorder _recorder;

  /// What the Live API works in, on each side.
  ///
  /// Asked for rather than assumed: a handset that will not give 16 kHz resamples, and
  /// the relay is told what it is getting rather than guessing from the byte count.
  static const captureRate = 16000;
  static const playbackRate = 24000;

  StreamSubscription<Uint8List>? _mic;
  bool _speakerReady = false;

  /// What the running capture hands each chunk to, kept so it can be handed to a
  /// replacement stream without the caller knowing one happened.
  void Function(Uint8List pcm)? _onChunk;

  /// When a chunk last arrived, and how many times the microphone has been picked
  /// up again since it last worked.
  DateTime? _lastChunk;
  int _restarts = 0;
  Timer? _heartbeat;
  Timer? _restartAt;
  bool _wanted = false;

  /// Called when the microphone is gone for good, so the screen can stop implying
  /// somebody is being listened to.
  ///
  /// A farmer talking into a session that receives nothing gets silence back and no
  /// way to tell why, and the meter is running the whole time.
  void Function(String why)? onLost;

  /// Whether the microphone is running.
  bool get listening => _mic != null;

  /// How long a silence means the capture has died rather than gone quiet.
  ///
  /// Four seconds. The recorder delivers a buffer every few tens of milliseconds
  /// while it lives, and a field is never silent enough to stop it -- so four
  /// seconds without a chunk is not a quiet farmer, it is a stream Android has
  /// stopped feeding. Opening the camera does it, and so does the audio focus moving
  /// when the speaker starts playing the assistant's reply into the same handset.
  static const _silenceMeansDead = Duration(seconds: 4);

  /// How many times to pick the microphone up before giving up on it.
  ///
  /// Three. A microphone that will not stay open after three attempts is not going
  /// to on the fourth, and by then the farmer has been talking into nothing for long
  /// enough. Better to say so than to keep a session running that cannot hear.
  static const _giveUpAfter = 3;

  Duration get _backoff {
    final seconds = 1 << (_restarts < 3 ? _restarts : 3);
    return Duration(seconds: seconds);
  }

  /// Start capturing, handing each chunk to [onChunk].
  ///
  /// Returns false where the handset refuses -- no permission, no microphone, a
  /// platform channel that failed. A farmer is told the assistant is unavailable, which
  /// is true and is all they can act on; the walk is unaffected either way.
  Future<bool> startCapture(void Function(Uint8List pcm) onChunk) async {
    if (_mic != null) return true;
    _onChunk = onChunk;
    _wanted = true;
    return _open();
  }

  Future<bool> _open() async {
    if (_mic != null) return true;
    final onChunk = _onChunk;
    if (onChunk == null) return false;
    try {
      if (!await _recorder.hasPermission()) return false;

      final stream = await _recorder.startStream(
        const RecordConfig(
          // 16-bit PCM, which is what the relay feeds the model. The platform
          // resamples if it will not give this natively.
          encoder: AudioEncoder.pcm16bits,
          sampleRate: captureRate,
          numChannels: 1,
          // The processing a phone in a field needs. The assistant's voice comes out
          // of the same handset's speaker, so without echo cancellation the model
          // hears itself and answers its own last sentence.
          echoCancel: true,
          noiseSuppress: true,
          autoGain: true,
        ),
      );
      _mic = stream.listen(
        (chunk) {
          _lastChunk = DateTime.now();
          // Working, so the next failure starts its backoff from a second rather
          // than from wherever the last run of bad luck left it.
          _restarts = 0;
          onChunk(chunk);
        },
        // Not fatal. This used to stop the capture and leave it stopped, so one
        // error from the platform meant the farmer talked into nothing for the rest
        // of the session with nothing on screen to say so -- the same fault the
        // position stream had, for the same reason.
        onError: (Object e) {
          Faults.note('live.mic', e);
          _reopen('the microphone reported an error');
        },
        // The other way it goes: the stream simply ends. Android does it when the
        // audio focus moves, which happens when the camera opens and when the
        // speaker starts playing the assistant's reply into the same handset.
        onDone: () => _reopen('the microphone stopped sending'),
        cancelOnError: false,
      );
      _lastChunk = DateTime.now();
      _watchForSilence();
      return true;
    } catch (e) {
      debugPrint('[live audio] the microphone would not start: $e');
      Faults.note('live.mic', e);
      return false;
    }
  }

  /// Pick the microphone up again, or say it is gone.
  ///
  /// The silence case is the one that matters and the one neither an error nor an
  /// ending catches: a stream that is still subscribed and delivering nothing. On
  /// this handset it is what the camera opening does.
  void _reopen(String why) {
    if (!_wanted) return;
    final wait = _backoff;
    _restarts++;

    unawaited(_mic?.cancel());
    _mic = null;
    _lastChunk = null;
    _restartAt?.cancel();

    if (_restarts > _giveUpAfter) {
      debugPrint('[live audio] $why, and it will not stay open; giving up');
      _wanted = false;
      _stopWatching();
      unawaited(_release());
      onLost?.call(why);
      return;
    }

    debugPrint(
      '[live audio] $why; picking it up again in ${wait.inSeconds}s '
      '(attempt $_restarts)',
    );
    _restartAt = Timer(wait, () async {
      if (!_wanted) return;
      // The recorder has to be told to stop before it will start again: it holds
      // the platform's capture session, and asking for a second one while it thinks
      // it has the first is how this ends up refusing every attempt.
      await _release();
      if (!_wanted) return;
      if (!await _open()) _reopen('the microphone would not reopen');
    });
  }

  void _watchForSilence() {
    _heartbeat?.cancel();
    _heartbeat = Timer.periodic(const Duration(seconds: 1), (_) {
      if (_mic == null || !_wanted) return;
      final last = _lastChunk;
      if (last == null) return;
      if (DateTime.now().difference(last) > _silenceMeansDead) {
        _reopen('the microphone went quiet');
      }
    });
  }

  void _stopWatching() {
    _heartbeat?.cancel();
    _heartbeat = null;
    _restartAt?.cancel();
    _restartAt = null;
  }

  /// Hand the platform's capture session back, without giving up on wanting it.
  Future<void> _release() async {
    try {
      if (await _recorder.isRecording()) await _recorder.stop();
    } catch (e) {
      // Already torn down by the platform, which is the ordinary case here.
      debugPrint('[live audio] releasing the microphone: $e');
    }
  }

  Future<void> stopCapture() async {
    _wanted = false;
    _stopWatching();
    await _mic?.cancel();
    _mic = null;
    _lastChunk = null;
    try {
      if (await _recorder.isRecording()) await _recorder.stop();
    } catch (e) {
      // A recorder already torn down by the platform. Nothing to recover.
      debugPrint('[live audio] stopping the microphone: $e');
    }
  }

  /// Prepare the speaker.
  ///
  /// Separate from playing so the first sentence is not delayed by setup. Called when a
  /// session opens rather than when audio first arrives.
  Future<void> prepareSpeaker() async {
    if (_speakerReady) return;
    try {
      await FlutterPcmSound.setup(sampleRate: playbackRate, channelCount: 1);
      // No feed threshold and no feed callback, deliberately.
      //
      // Those are for a player that pulls -- it asks for more when its buffer runs
      // low, and the app answers from a source it can seek in. This pushes: the
      // assistant's voice arrives when the assistant speaks, there is nothing to ask
      // for, and a threshold with no callback registered raises an event nobody
      // receives. Whether a turn has finished comes from the relay, which says so.
      _speakerReady = true;
    } catch (e) {
      debugPrint('[live audio] the speaker would not start: $e');
      Faults.note('live.speaker', e);
    }
  }

  /// Play a chunk of the assistant's voice.
  ///
  /// Silently does nothing where the speaker never started. A farmer whose handset
  /// cannot play still sees what was said on screen, which is why the relay sends the
  /// text as well as the audio.
  Future<void> play(Uint8List pcm) async {
    if (!_speakerReady) return;
    try {
      await FlutterPcmSound.feed(PcmArrayInt16.fromList(_toInt16(pcm)));
    } catch (e) {
      debugPrint('[live audio] feeding the speaker: $e');
    }
  }

  /// 16-bit little-endian bytes as the samples the player wants.
  ///
  /// Little-endian because that is what both the Live API and Android produce; reading
  /// it the other way round is not a quiet bug but a loud one, and it sounds like
  /// static rather than speech.
  static List<int> _toInt16(Uint8List bytes) {
    final out = <int>[];
    for (var i = 0; i + 1 < bytes.length; i += 2) {
      final value = bytes[i] | (bytes[i + 1] << 8);
      // Two's complement: anything above 32767 is a negative sample.
      out.add(value >= 0x8000 ? value - 0x10000 : value);
    }
    return out;
  }

  Future<void> dispose() async {
    await stopCapture();
    await _recorder.dispose();
    if (_speakerReady) {
      try {
        await FlutterPcmSound.release();
      } catch (_) {
        // Already gone.
      }
      _speakerReady = false;
    }
  }
}
