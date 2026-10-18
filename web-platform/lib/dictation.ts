"use client";

/**
 * An officer's spoken question, and the answer read back to them.
 *
 * # Why the microphone goes to our own relay
 *
 * Not to the browser's recognition service, and not to a model that is free to
 * answer. Two separate reasons, and both are about where a decision is recorded.
 *
 * The browser's own `SpeechRecognition` is, in every shipping implementation, a
 * connection to the vendor's servers: an officer's question about a named claim in a
 * named village would leave the platform to a third party nobody has an agreement
 * with. Our relay holds the model credential, applies the data boundary, and is the
 * same path the farmer app already speaks down.
 *
 * The relay, in that mode, transcribes and nothing more. The answer comes from the
 * officer agent in the Go service, whose whole worth is what it cannot do — it reads
 * and drafts, it cannot approve, return, reassign or close a claim, and cannot change
 * a figure. A model that heard the question and answered it would be a second
 * assistant with none of those limits, reached by speaking instead of typing. So the
 * spoken half carries words: this hook hands the transcript to the same call the
 * typing box makes.
 *
 * # Why the answer is spoken by the browser
 *
 * Because it costs nothing and sends nothing. `speechSynthesis` reads text that is
 * already on screen, on the device, with no request of any kind — so the officer gets
 * the answer in their ear without a second stream of audio leaving the console. It is
 * absent or voiceless on some browsers, which is why the text is always on screen
 * first and the speech is never the only channel.
 *
 * # What must keep working when this does not
 *
 * Everything. A browser with no microphone API, an officer who refuses access, a
 * relay that is not configured, a socket that never opens: each one leaves the typed
 * chat exactly as it was, and says once why the button did not do what was expected.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import { api } from "@/lib/api";
import { messageForError } from "@/lib/errors";

/**
 * The microphone, collected into chunks worth sending.
 *
 * A worklet rather than a `ScriptProcessorNode` because the latter runs the
 * conversion on the main thread, between renders, and a console that is also
 * fetching a claim list drops audio when it does. Delivered as a Blob URL rather
 * than a file in `public/` because this console is a static export served from
 * several places, and a module that has to be fetched from a path is a module that
 * 404s on one of them.
 *
 * The chunking is the only arithmetic here. The graph hands a worklet 128 samples at
 * a time, which at 16 kHz is 8 ms — sending each one is 125 WebSocket frames a
 * second, nearly all header. 2048 samples is 128 ms and eight frames a second, which
 * is well inside the delay a person reads as immediate.
 */
const PUMP_MODULE = `
const CHUNK = 2048;

class QuestionPump extends AudioWorkletProcessor {
  constructor() {
    super();
    this.pending = new Int16Array(CHUNK);
    this.filled = 0;
  }

  process(inputs) {
    const channel = inputs[0] && inputs[0][0];
    if (!channel) return true;
    for (let i = 0; i < channel.length; i++) {
      // Float -1..1 to signed 16-bit, which is what the relay forwards.
      const s = Math.max(-1, Math.min(1, channel[i]));
      this.pending[this.filled++] = s < 0 ? s * 0x8000 : s * 0x7fff;
      if (this.filled === CHUNK) {
        const full = this.pending;
        this.pending = new Int16Array(CHUNK);
        this.filled = 0;
        this.port.postMessage(full.buffer, [full.buffer]);
      }
    }
    return true;
  }
}

registerProcessor('question-pump', QuestionPump);
`;

/** What the relay takes. The handset resamples too; so does this. */
const INPUT_SAMPLE_RATE = 16000;

/**
 * How long to wait for the last words after the officer stops talking.
 *
 * The model is still finishing the sentence when the button is released, and closing
 * the socket at that moment loses the end of the question — which an officer reads as
 * the feature mishearing them rather than as a race. Three seconds is longer than the
 * tail of a transcript and short enough that a relay which has stopped answering does
 * not hold the button.
 */
const SETTLE_MS = 3000;

/** Where a spoken question has got to. */
export type DictationState = "idle" | "opening" | "listening" | "settling";

export interface Dictation {
  state: DictationState;

  /** What has been heard so far, so the officer can see it being got right. */
  heard: string;

  /**
   * Why speaking is not available, in one sentence, or null.
   *
   * Set at the moment it becomes true rather than on page load: the microphone is
   * asked for when the officer presses the button, so whether they will allow it is
   * not knowable before then.
   */
  unavailable: string | null;

  start: () => void;
  stop: () => void;
}

/**
 * One event the relay sends down the socket.
 *
 * `said` is the transcript, in pieces as the model produces them. `ended` and
 * `unavailable` are the session finishing for a reason worth telling somebody.
 */
interface RelayEvent {
  type?: string;
  text?: string;
  reason?: string;
}

/**
 * Everything one spoken question holds open, so that ending it is one call.
 *
 * A half-released session is the failure that matters here: a microphone left live is
 * an indicator light on an officer's laptop that nobody asked for, and an audio
 * context left running is a tab that keeps the device awake.
 */
interface OpenSession {
  socket: WebSocket | null;
  audio: AudioContext | null;
  pump: AudioWorkletNode | null;
  microphone: MediaStream | null;
  timers: ReturnType<typeof setTimeout>[];
}

function release(session: OpenSession): void {
  for (const t of session.timers) clearTimeout(t);
  session.timers = [];

  session.pump?.disconnect();
  session.pump = null;

  for (const track of session.microphone?.getTracks() ?? []) track.stop();
  session.microphone = null;

  // Closed rather than suspended. A suspended context is still a context, and a
  // browser that has been given twenty of them over an afternoon refuses the
  // twenty-first.
  void session.audio?.close().catch(() => undefined);
  session.audio = null;

  if (session.socket) {
    const socket = session.socket;
    session.socket = null;
    // Told, where the socket is still open, so the relay ends the session rather
    // than waiting ninety seconds for a caller it thinks went quiet.
    try {
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "close" }));
      }
      socket.close();
    } catch {
      // Already gone is the ordinary ending, not a failure.
    }
  }
}

/**
 * Whether this browser can carry a spoken question at all.
 *
 * Answered on the click rather than at render: a static export is prerendered with
 * no browser behind it, and a button whose label depends on `navigator` renders one
 * way on the server and another in the tab.
 */
function whatIsMissing(): string | null {
  if (typeof window === "undefined") return null;
  if (!navigator.mediaDevices?.getUserMedia) {
    return "This browser will not give a page access to a microphone. The typed box below works as it always has.";
  }
  if (typeof window.AudioContext === "undefined") {
    return "This browser cannot process audio in a page. The typed box below works as it always has.";
  }
  if (typeof window.WebSocket === "undefined") {
    return "This browser cannot hold the connection speaking needs. The typed box below works as it always has.";
  }
  return null;
}

/**
 * Dictate one question at a time.
 *
 * `onQuestion` is called with the transcript once, when the officer stops and the
 * last words have arrived. Nothing is asked of the assistant from in here: this hook
 * produces text, and the page decides what to do with it, which is what keeps the
 * spoken path and the typed path the same path.
 */
export function useDictation(onQuestion: (question: string) => void): Dictation {
  const [state, setState] = useState<DictationState>("idle");
  const [heard, setHeard] = useState("");
  const [unavailable, setUnavailable] = useState<string | null>(null);

  const session = useRef<OpenSession>({
    socket: null,
    audio: null,
    pump: null,
    microphone: null,
    timers: [],
  });

  // Held in refs because the socket's handlers outlive the render that made them,
  // and a transcript read out of a stale closure is an empty question.
  const transcript = useRef("");

  // The caller's own handler, kept current after each render rather than captured
  // once. The page rebuilds it on every render because it closes over the
  // conversation so far, and a socket handler holding the first one would ask the
  // assistant a question with an empty history.
  const deliver = useRef(onQuestion);
  useEffect(() => {
    deliver.current = onQuestion;
  }, [onQuestion]);

  // A session already finishing must not finish twice: `turn_complete` and the
  // settling timer routinely arrive together, and the second one would ask the
  // assistant the same question again.
  const finished = useRef(false);

  // Whether the officer has said they are done.
  //
  // This is what makes `turn_complete` mean something. The model completes a turn
  // whenever it decides the speaker has paused, which happens in the middle of a
  // question said slowly — so a session that ended on the first `turn_complete`
  // would cut officers off mid-sentence and look like mishearing. Before the button
  // is released a completed turn is just a paragraph break; after it, it is the
  // signal that the last words have arrived and the settling wait can be cut short.
  const stopping = useRef(false);

  const finish = useCallback(() => {
    if (finished.current) return;
    finished.current = true;

    release(session.current);
    setState("idle");

    const asked = transcript.current.trim();
    if (!asked) {
      setUnavailable(
        "Nothing was heard. Check the microphone your browser is using, or type the question instead.",
      );
      return;
    }
    deliver.current(asked);
  }, []);

  // Let go of the microphone when the page does. An officer who navigates away
  // mid-question has finished asking it, whatever the socket still thinks.
  useEffect(() => () => release(session.current), []);

  const start = useCallback(() => {
    const missing = whatIsMissing();
    if (missing) {
      setUnavailable(missing);
      return;
    }

    // Any previous attempt, whatever state it reached.
    release(session.current);
    finished.current = false;
    stopping.current = false;
    transcript.current = "";
    setHeard("");
    setUnavailable(null);
    setState("opening");

    void (async () => {
      const open = session.current;

      let microphone: MediaStream;
      try {
        // Asked for here, on the press, and nowhere else. A console that asks on
        // load is a console every officer refuses once and then cannot use.
        microphone = await navigator.mediaDevices.getUserMedia({
          audio: {
            channelCount: 1,
            echoCancellation: true,
            noiseSuppression: true,
          },
        });
      } catch {
        // Refused, or there is no input device. The same sentence either way,
        // because the officer's next move is the same.
        setState("idle");
        setUnavailable(
          "This page was not given access to the microphone. Allow it in the browser's address bar, or type the question instead.",
        );
        return;
      }
      open.microphone = microphone;

      let url: string;
      let allowedSeconds: number;
      try {
        // Asked for after the microphone, so a refused permission does not spend a
        // ticket that would then expire unused.
        const grant = await api.startOfficerVoice();
        url = api.officerVoiceSocketUrl(grant);
        allowedSeconds = grant.max_seconds;
      } catch (e) {
        release(open);
        setState("idle");
        setUnavailable(
          `Speaking is not available: ${messageForError(e)} The typed box below is unaffected.`,
        );
        return;
      }

      const socket = new WebSocket(url);
      socket.binaryType = "arraybuffer";
      open.socket = socket;

      socket.onerror = () => {
        release(open);
        setState("idle");
        setUnavailable(
          "The connection for speaking could not be opened. The typed box below is unaffected.",
        );
      };

      socket.onclose = () => {
        // A close before anything was heard is a refused ticket or a relay that is
        // not there; after, it is the ordinary ending and `finish` has the words.
        if (!finished.current && transcript.current.trim() === "") {
          release(open);
          setState("idle");
          setUnavailable(
            "The connection for speaking closed before anything was heard. Try again, or type the question.",
          );
          finished.current = true;
          return;
        }
        finish();
      };

      socket.onmessage = (message: MessageEvent<unknown>) => {
        // Binary on this socket would be the relay speaking, which a transcribing
        // session does not do. Ignored rather than played: the answer is read out by
        // this browser, from text, and audio arriving here is a mode gone wrong.
        if (typeof message.data !== "string") return;

        let event: RelayEvent;
        try {
          event = JSON.parse(message.data) as RelayEvent;
        } catch {
          return;
        }

        switch (event.type) {
          case "ready":
            setState("listening");
            break;
          case "said":
            // Appended without a separator. The relay preserves each piece's own
            // spacing precisely so that joining them here reproduces the sentence.
            transcript.current += event.text ?? "";
            setHeard(transcript.current);
            break;
          case "turn_complete":
            // Only once the officer has stopped. See `stopping`: a completed turn
            // while they are still talking is a pause, not an ending.
            if (stopping.current) finish();
            break;
          case "ended":
          case "unavailable":
            finish();
            break;
        }
      };

      socket.onopen = () => {
        void (async () => {
          try {
            // 16 kHz asked of the context rather than resampled by hand: the browser
            // does it in the audio thread, correctly, for free.
            const audio = new AudioContext({ sampleRate: INPUT_SAMPLE_RATE });
            open.audio = audio;

            const pumpUrl = URL.createObjectURL(
              new Blob([PUMP_MODULE], { type: "text/javascript" }),
            );
            try {
              await audio.audioWorklet.addModule(pumpUrl);
            } finally {
              URL.revokeObjectURL(pumpUrl);
            }

            const pump = new AudioWorkletNode(audio, "question-pump");
            open.pump = pump;
            pump.port.onmessage = (chunk: MessageEvent<ArrayBuffer>) => {
              if (socket.readyState === WebSocket.OPEN) socket.send(chunk.data);
            };
            audio.createMediaStreamSource(microphone).connect(pump);

            // The relay ends the session at its own ceiling, which is the one the
            // platform put on the ticket. This is the same deadline held here, so
            // that the microphone light goes out at the moment the session does
            // rather than when the officer next looks at the tab.
            open.timers.push(setTimeout(finish, allowedSeconds * 1000));
          } catch {
            release(open);
            setState("idle");
            setUnavailable(
              "This browser could not open the microphone for speaking. The typed box below is unaffected.",
            );
          }
        })();
      };
    })();
  }, [finish]);

  const stop = useCallback(() => {
    const open = session.current;
    stopping.current = true;
    if (!open.socket) {
      finish();
      return;
    }

    setState("settling");
    try {
      // The officer has finished their sentence. Said explicitly rather than left to
      // silence detection, because a person who has stopped talking on purpose
      // should not wait for a timer to agree with them.
      if (open.socket.readyState === WebSocket.OPEN) {
        open.socket.send(JSON.stringify({ type: "done_speaking" }));
      }
    } catch {
      // Nothing to do; the settling timer below ends it either way.
    }
    open.timers.push(setTimeout(finish, SETTLE_MS));
  }, [finish]);

  return { state, heard, unavailable, start, stop };
}

/**
 * Reading an answer aloud, and stopping the moment it is not wanted.
 *
 * Output only: this sends nothing anywhere. It reads text that is already on the
 * screen, which is why the answer is rendered first and spoken second — a browser
 * with no voices installed loses the speech and keeps the answer.
 */
export function useReadAloud(): {
  speaking: boolean;
  /** Why an answer was not read out, said once, or null. */
  silent: string | null;
  speak: (text: string) => void;
  stop: () => void;
} {
  const [speaking, setSpeaking] = useState(false);
  const [silent, setSilent] = useState<string | null>(null);

  const stop = useCallback(() => {
    if (typeof window === "undefined" || !("speechSynthesis" in window)) return;
    window.speechSynthesis.cancel();
    setSpeaking(false);
  }, []);

  // A page left while it is talking keeps talking: speechSynthesis belongs to the
  // browser, not to this component, and nothing else will stop it.
  useEffect(() => stop, [stop]);

  const speak = useCallback(
    (text: string) => {
      if (typeof window === "undefined") return;
      if (!("speechSynthesis" in window)) {
        setSilent(
          "This browser cannot read answers aloud. Every answer is on screen as usual.",
        );
        return;
      }

      const said = text.trim();
      if (!said) return;

      // Cancelled first, always. Two answers read over each other is unintelligible,
      // and an officer who has moved on to their next question does not want the
      // previous answer finishing underneath it.
      window.speechSynthesis.cancel();

      const utterance = new SpeechSynthesisUtterance(said);
      // The assistant answers in the officer's own terms, which in practice means
      // English with Marathi place and crop names in it. A Latin-script voice reading
      // Devanagari produces noise, so where the answer is mostly Devanagari it is
      // offered to a Marathi voice instead.
      utterance.lang = /[ऀ-ॿ]/.test(said) ? "mr-IN" : "en-IN";
      utterance.rate = 1;
      utterance.onstart = () => setSpeaking(true);
      utterance.onend = () => setSpeaking(false);
      utterance.onerror = () => setSpeaking(false);

      window.speechSynthesis.speak(utterance);
    },
    [],
  );

  return { speaking, silent, speak, stop };
}
