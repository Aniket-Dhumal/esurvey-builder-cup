"use client";

/**
 * Spoken guidance, in Marathi.
 *
 * # Why this is not a nicety
 *
 * A farmer standing in a wet field, holding a phone in one hand, is not reading
 * a paragraph. Many will read Marathi slowly and some not at all. Speech is how
 * an instruction actually arrives, and the whole guided capture depends on the
 * instruction arriving before the photograph is taken rather than after it is
 * rejected.
 *
 * # What is honest about it
 *
 * Marathi text-to-speech is not on every handset. Where it is missing this
 * falls back to Hindi, which most Marathi speakers follow, and then to English,
 * and then to nothing. The text is always on screen regardless: speech is a
 * second channel, never the only one. An app whose instructions are inaudible
 * on a cheap phone is an app that does not work on the phones it is for.
 */

import { useCallback, useRef, useState, useSyncExternalStore } from "react";

/** Preference order. Marathi, then the nearest language most speakers follow. */
const LANGS = ["mr-IN", "mr", "hi-IN", "hi", "en-IN", "en-GB", "en-US"];

export interface VoiceState {
  /** Whether the device can speak any of the languages above. */
  available: boolean;
  /** The language actually being used, so the UI can say if it is not Marathi. */
  lang: string | null;
  enabled: boolean;
  speaking: boolean;
}

const STORAGE_KEY = "esurvey.voice";

// ---------------------------------------------------------------------------
// Two external stores
// ---------------------------------------------------------------------------
//
// The installed voices and the saved preference both live outside React, and
// both are read on the client only. Reading them in an effect and calling
// setState means the component renders once with the wrong answer and corrects
// itself, which on the preference is a visible flicker of the voice button.
// useSyncExternalStore is the hook for exactly this: it renders the server
// answer during prerender and the real one as soon as there is a client.

const voiceListeners = new Set<() => void>();
let currentVoice: SpeechSynthesisVoice | null = null;
let watchingVoices = false;

function refreshVoice(): void {
  const next = pickVoice();
  if (next === currentVoice) return;
  currentVoice = next;
  for (const l of voiceListeners) l();
}

function subscribeVoices(onChange: () => void): () => void {
  voiceListeners.add(onChange);
  if (!watchingVoices && typeof window !== "undefined" && "speechSynthesis" in window) {
    watchingVoices = true;
    // Chrome populates the voice list asynchronously, and on a cold load the
    // first read reliably returns nothing.
    window.speechSynthesis.addEventListener("voiceschanged", refreshVoice);
    refreshVoice();
  }
  return () => {
    voiceListeners.delete(onChange);
  };
}

const prefListeners = new Set<() => void>();
let enabledCache: boolean | null = null;

/** The saved preference, defaulting to on. Read once and then cached. */
function readEnabled(): boolean {
  if (enabledCache === null) {
    try {
      const saved = window.localStorage.getItem(STORAGE_KEY);
      enabledCache = saved === null ? true : saved === "on";
    } catch {
      enabledCache = true; // storage disabled; the default stands
    }
  }
  return enabledCache;
}

function writeEnabled(next: boolean): void {
  enabledCache = next;
  try {
    window.localStorage.setItem(STORAGE_KEY, next ? "on" : "off");
  } catch {
    /* nothing to do */
  }
  for (const l of prefListeners) l();
}

function subscribePreference(onChange: () => void): () => void {
  prefListeners.add(onChange);
  return () => {
    prefListeners.delete(onChange);
  };
}

function pickVoice(): SpeechSynthesisVoice | null {
  const voices = window.speechSynthesis.getVoices();
  if (!voices.length) return null;
  for (const want of LANGS) {
    const exact = voices.find((v) => v.lang.toLowerCase() === want.toLowerCase());
    if (exact) return exact;
    const prefix = voices.find((v) =>
      v.lang.toLowerCase().startsWith(want.split("-")[0].toLowerCase()));
    if (prefix) return prefix;
  }
  return null;
}

export function useVoice(): VoiceState & {
  speak: (mr: string, en?: string) => void;
  stop: () => void;
  toggle: () => void;
} {
  const voice = useSyncExternalStore(subscribeVoices, () => currentVoice, () => null);
  const enabled = useSyncExternalStore(subscribePreference, readEnabled, () => true);
  const [speaking, setSpeaking] = useState(false);
  const lastRef = useRef<string>("");

  const available = voice !== null;
  const lang = voice?.lang ?? null;

  const stop = useCallback(() => {
    if (typeof window === "undefined" || !("speechSynthesis" in window)) return;
    window.speechSynthesis.cancel();
    setSpeaking(false);
  }, []);

  const speak = useCallback(
    (mr: string, en?: string) => {
      if (!enabled || typeof window === "undefined" || !("speechSynthesis" in window)) return;
      if (!voice) return;

      // Saying the same line twice because a component re-rendered is the
      // fastest way to make someone turn the voice off.
      if (lastRef.current === mr) return;
      lastRef.current = mr;

      window.speechSynthesis.cancel();

      // An English voice reading Devanagari produces noise. Where Marathi and
      // Hindi are both absent, speak the English line or stay quiet.
      const isIndic = voice.lang.toLowerCase().startsWith("mr")
        || voice.lang.toLowerCase().startsWith("hi");
      const text = isIndic ? mr : en;
      if (!text) return;

      const u = new SpeechSynthesisUtterance(text);
      u.voice = voice;
      u.lang = voice.lang;
      // A shade slower than default: instructions given over wind, to someone
      // who is also looking at a field.
      u.rate = 0.92;
      u.pitch = 1;
      u.onstart = () => setSpeaking(true);
      u.onend = () => setSpeaking(false);
      u.onerror = () => setSpeaking(false);

      window.speechSynthesis.speak(u);
    },
    [enabled, voice],
  );

  const toggle = useCallback(() => {
    const next = !readEnabled();
    if (!next) {
      window.speechSynthesis?.cancel();
      setSpeaking(false);
    }
    lastRef.current = "";
    writeEnabled(next);
  }, []);

  return { available, lang, enabled, speaking, speak, stop, toggle };
}

/**
 * Every line the app speaks, in one place.
 *
 * Marathi is the line that is spoken and shown; English is what a non-Marathi
 * device says instead, and what an English-reading officer sees in review. They
 * are written as a pair so a change to one is a visible change to the other.
 */
export const LINES = {
  welcome: {
    mr: "नमस्कार. आपल्या शेतातील नुकसानीचे फोटो घेऊया.",
    en: "Let us take photographs of the damage to your field.",
  },
  standInField: {
    mr: "कृपया आपल्या शेतात उभे रहा. फोन आपले ठिकाण शोधत आहे.",
    en: "Please stand in your field. The phone is finding your position.",
  },
  positionGood: {
    mr: "ठिकाण मिळाले. आता पुढे जाऊ शकता.",
    en: "Position found. You can continue.",
  },
  positionPoor: {
    mr: "ठिकाण नीट मिळत नाही. मोकळ्या जागी जा आणि थोडा वेळ थांबा.",
    en: "The position is not clear. Move into the open and wait a moment.",
  },
  outsideField: {
    mr: "आपण शेताच्या बाहेर दिसत आहात. शेतात जाऊन फोटो घ्या.",
    en: "You appear to be outside the field. Step into it before photographing.",
  },
  stageWide: {
    mr: "पहिला फोटो: पूर्ण शेत दिसेल असा फोटो घ्या. मागे जाऊन फोन सरळ धरा.",
    en: "First photograph: the whole field. Step back and hold the phone upright.",
  },
  stageDamage: {
    mr: "दुसरा फोटो: नुकसान झालेल्या पिकाजवळ जा आणि खाली फोन धरून फोटो घ्या.",
    en: "Second photograph: go close to the damaged crop and point the phone down.",
  },
  stageCrop: {
    mr: "तिसरा फोटो: एक रोप स्पष्ट दिसेल असा जवळून फोटो घ्या.",
    en: "Third photograph: one plant, close enough to see it clearly.",
  },
  holdSteady: {
    mr: "फोन स्थिर धरा.",
    en: "Hold the phone steady.",
  },
  tiltDown: {
    mr: "फोन थोडा खाली करा.",
    en: "Tilt the phone down a little.",
  },
  tiltUp: {
    mr: "फोन थोडा वर करा.",
    en: "Lift the phone a little.",
  },
  captured: {
    mr: "फोटो घेतला. ठीक असेल तर पुढे जा.",
    en: "Photograph taken. Continue if it looks right.",
  },
  savedOffline: {
    mr: "नेटवर्क नाही. फोटो फोनमध्ये सुरक्षित आहे आणि नेटवर्क आल्यावर आपोआप पाठवला जाईल.",
    en: "No network. The photograph is saved on your phone and will be sent when the network returns.",
  },
  allDone: {
    mr: "सर्व फोटो झाले. आपला अहवाल पाठवला जात आहे.",
    en: "All photographs taken. Your report is being sent.",
  },
  sentDone: {
    mr: "आपला अहवाल कार्यालयाला मिळाला आहे. तलाठी आणि कृषी सहाय्यक पाहणीसाठी येतील.",
    en: "Your report has reached the office. The Talathi and Krushi Sahayak will come to inspect.",
  },
} as const;

export type LineKey = keyof typeof LINES;
