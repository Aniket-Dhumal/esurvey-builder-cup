"use client";

import Link from "next/link";
import { useState } from "react";
import {
  KeyRound,
  Loader2,
  Lock,
  Smartphone,
  User,
} from "lucide-react";

import { FieldScene, Wordmark } from "@/components/brand";
import { api, ApiError } from "@/lib/api";
import { messageForError as messageFor } from "@/lib/errors";
import { checkEmail } from "@/lib/email";
import { useSession } from "@/components/SessionProvider";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type Mode = "officer" | "farmer";

/**
 * The sign-in gate.
 *
 * Two doors, because the two kinds of account genuinely authenticate
 * differently: an officer has an email address and a password of their own, a
 * farmer has the handset already recorded in the registry. Neither path is
 * available to the other, and pretending otherwise in the UI would only hide
 * a distinction the backend enforces anyway.
 */
export function SignIn() {
  const { refresh } = useSession();
  const [mode, setMode] = useState<Mode>("officer");

  return (
    <div className="relative min-h-screen overflow-hidden bg-background">
      <Artwork />

      <div className="relative flex min-h-screen items-center justify-center px-4 py-10 lg:justify-end lg:px-[7vw]">
        <div className="w-full max-w-md">
          {/* No lockup out here.

              There used to be one, marked lg:hidden, on the reasoning that a
              phone hides the artwork panel and a login screen with no identity
              on it is a form from nobody. True, but the card below already
              carries the wordmark, so on a phone the page showed it twice —
              once above the card and again inside it. The card's is the one
              that stays: it sits with the heading it belongs to. */}
          <div className="rounded-2xl border border-border/50 bg-card p-7 shadow-[0_4px_32px_rgba(4,60,46,0.10)] sm:p-9">
            <div className="mb-7 text-center">
              {/* Exactly one lockup per screen. The artwork panel carries it
                  from lg up and is hidden below that, so the card carries it
                  below lg — the two are complementary rather than both
                  always-on, which is what put the wordmark on the page twice
                  at desktop width.

                  The wordmark, not the roundel: the roundel belongs on a
                  launcher and a browser tab, where there is room for a glyph
                  and none for a name. */}
              <Wordmark className="mx-auto mb-5 lg:hidden" width={180} />
              <h1 className="text-xl font-semibold tracking-tight text-foreground">
                Sign in
              </h1>
              {/* Not "welcome". Nobody signing in to file a hailstorm claim
                  needs greeting, and the name of the product was already said
                  twice above this line. What they need is to know which of
                  their two credentials this form wants. */}
              <p className="mt-1.5 text-sm text-muted-foreground">
                Use the email address your account was created with.
              </p>
            </div>

            {mode === "officer" ? (
              <OfficerForm onSignedIn={refresh} />
            ) : (
              <FarmerForm onSignedIn={refresh} />
            )}

            <div className="my-6 flex items-center gap-3">
              <span className="h-px flex-1 bg-border" />
              <span className="text-xs font-medium text-muted-foreground">OR</span>
              <span className="h-px flex-1 bg-border" />
            </div>

            {/*
              The other door, as a route rather than a tab.

              An officer signs in with their email address; a farmer has
              only the handset already in the registry. Two equal tabs made the
              console ask everybody which kind of person they were before it
              asked anything useful.
            */}
            <Button size="field"
       type="button"
       variant="outline"
       className="w-full text-base"
       onClick={() => setMode(mode === "officer" ? "farmer" : "officer")}>
              {mode === "officer" ? (
                <>
                  <Smartphone className="size-[18px]" />
                  Sign in with a one-time code
                </>
              ) : (
                <>
                  <KeyRound className="size-[18px]" />
                  Sign in with a password
                </>
              )}
            </Button>

            {/* This was "Contact Administrator" in link green, and it went
                nowhere — a control that looks clickable and is not. Said as a
                fact instead, because that is all it ever was. */}
            <p className="mt-6 text-center text-sm text-muted-foreground">
              Accounts are issued by your district administrator.
            </p>
          </div>

          <p className="mt-6 text-center text-xs leading-relaxed text-faint">
            Access is scoped to the area you are posted to. Every sign-in,
            refusal and record you open is recorded.
          </p>
        </div>
      </div>
    </div>
  );
}

/**
 * The left-hand composition: drawn, not photographed.
 *
 * The supplied artwork is 600x400 with its wordmark and captions burned in.
 * Enlarged across a display that is a 3x upscale, and type does not tolerate
 * that the way a photograph does. Every attempt to keep the picture ran into
 * the same wall: shown whole it duplicated the text, painted out it smeared,
 * cropped it still shipped a soft megabyte.
 *
 * So the scene is vector — see components/brand.tsx. Exact at any resolution,
 * nothing to download, and the captions are real words that can be translated.
 */
function Artwork() {
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-0 hidden lg:block">
      {/* The curve is the artwork's own gesture, kept as a shape. An ellipse
          wider than the panel puts only its flank on screen. */}
      <div
        className="absolute inset-y-0 left-0 w-[58%] overflow-hidden"
        style={{ clipPath: "ellipse(96% 132% at 20% 50%)" }}>
        <FieldScene className="size-full" />
      </div>

      <Leaves className="absolute -right-4 top-0 h-[46%] w-auto text-primary/12" />
      <Leaves className="absolute -right-8 bottom-0 h-[52%] w-auto rotate-180 text-primary/10" />

      {/* What used to sit here: four icons captioned "Digital Survey", "Crop
          Health Assessment", "Geo Tagged Evidence" and "Accurate Insights,
          Better Decisions", floating on the sky at three different baselines
          because the labels wrapped to different numbers of lines.

          It was a feature grid — copy written to persuade somebody to adopt
          the system, on the screen used every morning by the people who
          already have. It sold the console to its own operators and told them
          nothing. One line saying what this system is replaces all four. */}
      <div className="absolute left-[6%] top-[12%] max-w-[46%]">
        <Wordmark width={320} />
        <p className="mt-5 text-base font-medium text-foreground/85">
          Post Disaster Crop Loss Assessment Platform
        </p>
        <p className="mt-2 max-w-[22rem] text-[15px] leading-relaxed text-foreground/70">
          Evidence is collected in the field and verified up the revenue chain.
        </p>
      </div>
    </div>
  );
}

/**
 * The leaf motif from the mark, as paths.
 *
 * currentColor throughout, so a caller sets the weight with a text colour and
 * the shapes follow the theme into dark mode instead of staying a fixed green
 * that only works on one ground.
 */
function Leaves({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 160 320" fill="none" className={className}>
      <g fill="currentColor">
        <path d="M96 18c26 6 46 26 52 52-26-6-46-26-52-52Z" />
        <path d="M118 74c28 2 50 20 58 46-28-2-50-20-58-46Z" />
        <path d="M88 96c24 10 40 32 42 58-24-10-40-32-42-58Z" />
        <path d="M124 140c28 6 48 28 52 54-28-6-48-28-52-54Z" />
        <path d="M100 186c22 12 36 34 36 60-22-12-36-34-36-60Z" />
        <path d="M132 214c26 10 42 34 44 60-26-10-42-34-44-60Z" />
      </g>
      <g stroke="currentColor" strokeWidth="2" strokeLinecap="round" opacity="0.7">
        <path d="M96 18c22 12 38 32 44 56" />
        <path d="M118 74c24 10 42 28 50 48" />
        <path d="M88 96c18 16 30 38 34 60" />
        <path d="M124 140c22 14 38 34 44 56" />
        <path d="M100 186c16 18 26 40 30 62" />
        <path d="M132 214c20 16 34 38 38 62" />
      </g>
    </svg>
  );
}

function OfficerForm({ onSignedIn }: { onSignedIn: () => Promise<void> }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The second factor, once the password has been accepted. An officer with
  // no mobile number on their account never sees this step; see
  // auth/officer_second_factor.go for why that is allowed rather than fixed
  // by locking them out.
  const [challenge, setChallenge] = useState<string | null>(null);
  const [mobileHint, setMobileHint] = useState("");
  const [code, setCode] = useState("");

  async function verifyCode(e: React.FormEvent) {
    e.preventDefault();
    if (!challenge) return;
    setBusy(true);
    setError(null);
    try {
      await api.verifyOfficerCode(challenge, code.trim());
      await onSignedIn();
    } catch (err) {
      // A challenge that has expired or been spent sends them back to the
      // password: there is nothing to retry a code against.
      if (err instanceof ApiError && err.code === "CHALLENGE_INVALID") {
        setChallenge(null);
        setCode("");
        setPassword("");
      }
      setError(messageFor(err));
      setBusy(false);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    // The login is the email address. A visible mistake is said here, before
    // the request; the server holds the same rule.
    const checked = checkEmail(username);
    if ("error" in checked) {
      setError(checked.error);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const result = await api.loginOfficer(checked.email, password);
      if (result.kind === "code-required") {
        setChallenge(result.challenge);
        setMobileHint(result.mobileHint);
        setCode("");
        setBusy(false);
        return;
      }
      await onSignedIn();
    } catch (err) {
      setError(messageFor(err));
      setBusy(false);
    }
  }

  if (challenge) {
    return (
      <form onSubmit={verifyCode} className="space-y-3.5">
        <p className="text-sm text-muted-foreground">
          Your password was accepted. A one-time code has been sent to{" "}
          <span className="font-medium text-foreground">{mobileHint}</span>.
          Enter it to finish signing in.
        </p>

        <Field htmlFor="officer-code" label="One-time code" icon={Lock}>
          <Input
            inputSize="field"
            className="pl-11 tracking-[0.3em]"
            id="officer-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            placeholder="000000"
            maxLength={6}
            autoFocus
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
            required
          />
        </Field>

        {error && <FormError message={error} />}

        <Button
          size="field"
          type="submit"
          className="w-full text-base"
          disabled={busy || code.length < 6}>
          {busy && <Loader2 className="size-4 animate-spin" />}
          Finish signing in
        </Button>

        <p className="pt-1 text-center text-sm">
          <button
            type="button"
            className="text-muted-foreground hover:text-primary hover:underline"
            onClick={() => {
              setChallenge(null);
              setCode("");
              setPassword("");
              setError(null);
            }}>
            Start again
          </button>
        </p>
      </form>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-3.5">
      {/* Every officer signs in with their email address; there are no
          usernames. */}
      <Field htmlFor="username" label="Email address" icon={User}>
        <Input
          inputSize="field"
          className="pl-11"
          id="username"
          type="email"
          inputMode="email"
          placeholder="you@department.gov.in"
          autoComplete="username"
          autoFocus
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          required
        />
      </Field>

      <Field htmlFor="password" label="Password" icon={Lock}>
        <Input
          inputSize="field"
          className="pl-11"
          id="password"
          type="password"
 
          placeholder="Password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
      </Field>

      {error && <FormError message={error} />}

      <Button size="field" type="submit" className="w-full text-base" disabled={busy}>
        {busy && <Loader2 className="size-4 animate-spin" />}
        Sign in
      </Button>

      {/* Under the button, not beside the password field: an officer who is
          typing a password they remember should not be offered a way to
          replace it mid-thought. */}
      <p className="pt-1 text-center text-sm">
        <Link
          href="/forgot-password/"
          className="text-muted-foreground hover:text-primary hover:underline">
          Forgotten your password?
        </Link>
      </p>
    </form>
  );
}

function FarmerForm({ onSignedIn }: { onSignedIn: () => Promise<void> }) {
  const [mobile, setMobile] = useState("");
  const [code, setCode] = useState("");
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);

  async function request(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await api.requestFarmerOtp(mobile.trim());
      // Deliberately non-committal: the API answers the same way whether or
      // not the number is registered, so that this form cannot be used to
      // find out who is enrolled.
      setNote(res.message);
      setSent(true);
    } catch (err) {
      setError(messageFor(err));
    } finally {
      setBusy(false);
    }
  }

  async function verify(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.verifyFarmerOtp(mobile.trim(), code.trim());
      await onSignedIn();
    } catch (err) {
      setError(messageFor(err));
      setBusy(false);
    }
  }

  return (
    <form onSubmit={sent ? verify : request} className="space-y-4">
      <Field htmlFor="mobile" label="Mobile number" icon={Smartphone}>
        <Input
          inputSize="field"
          className="pl-11"
          id="mobile"
          inputMode="tel"
          autoComplete="tel"
          placeholder="Mobile number"
          autoFocus
          value={mobile}
          onChange={(e) => setMobile(e.target.value)}
          disabled={sent}
          required
        />
      </Field>

      {sent && (
        <Field htmlFor="code" label="Code" icon={Lock}>
          <Input
            inputSize="field"
            className="pl-11 tracking-[0.3em]"
            id="code"
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={8}
          placeholder="Code"
          autoFocus
          value={code}
          onChange={(e) => setCode(e.target.value)}
          required
          />
        </Field>
      )}

      {note && !error && (
        <p className="text-xs leading-relaxed text-muted-foreground">{note}</p>
      )}
      {error && <FormError message={error} />}

      <Button size="field" type="submit" className="w-full text-base" disabled={busy}>
        {busy && <Loader2 className="size-4 animate-spin" />}
        {sent ? "Verify and sign in" : "Send code"}
      </Button>

      {sent && (
        <button
          type="button"
          onClick={() => {
            setSent(false);
            setCode("");
            setError(null);
            setNote(null);
          }}
          className="w-full text-center text-xs text-muted-foreground underline underline-offset-2">
          Use a different number
        </button>
      )}
    </form>
  );
}

/**
 * A field with its icon inside it.
 *
 * The label is present and correctly associated, and visually hidden: the
 * design puts the field's name in the placeholder, which vanishes the moment
 * anybody types. Dropping the label to match would leave a screen reader
 * announcing "edit text, blank" on the two inputs that gate the whole console.
 */
function Field({
  htmlFor,
  label,
  icon: Icon,
  children,
}: {
  htmlFor: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
}) {
  return (
    <div>
      <Label htmlFor={htmlFor} className="sr-only">
        {label}
      </Label>
      <div className="relative">
        <Icon className="pointer-events-none absolute left-3.5 top-1/2 size-[18px] -translate-y-1/2 text-muted-foreground" />
        {children}
      </div>
    </div>
  );
}

function FormError({ message }: { message: string }) {
  return (
    <p
      role="alert"
      className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm leading-relaxed text-sev-catastrophic">
      {message}
    </p>
  );
}
