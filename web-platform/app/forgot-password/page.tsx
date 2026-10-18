"use client";

import Link from "next/link";
import { useState } from "react";
import { Loader2, Mail } from "lucide-react";

import { checkEmail } from "@/lib/email";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Wordmark } from "@/components/brand";

/**
 * Asking for a link to set a new password.
 *
 * The second screen in the console reachable without signing in, and for the
 * same reason as the first: the person reading it cannot sign in. Before this
 * existed, an officer who had forgotten their password had one remedy and it
 * was a database edit.
 *
 * # Why the answer never says whether the account exists
 *
 * Because a reset form that says "no such account" is an account enumerator
 * with a friendly face. Point it at a list of addresses and it tells you which
 * officers this platform holds — who works for which district, and by omission
 * who has left.
 *
 * So the server answers identically either way, and this screen shows what it
 * said rather than interpreting it. That is a deliberately less satisfying
 * message than "sent!", and the alternative is worse.
 *
 * # Why re-inviting is not this
 *
 * An invitation is a first credential and a reset is a replacement. Anybody
 * holding user:update can invite; only the holder of the mailbox can reset.
 * Conflating them would make re-invitation a quiet second way into a
 * colleague's claimed account.
 */
export default function ForgotPasswordPage() {
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [answer, setAnswer] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const checked = checkEmail(email);
    if ("error" in checked) {
      setError(checked.error);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await api.requestPasswordReset(checked.email);
      setAnswer(res.message);
    } catch (err) {
      // Only a transport or rate-limit failure reaches here. A missing account
      // is not an error, by design.
      setError(
        err instanceof ApiError
          ? err.message
          : "Could not reach the platform. Try again in a moment.",
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <Frame>
      {answer ? (
        <div>
          <h1 className="text-lg font-semibold">Check your email</h1>
          <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
            {answer}
          </p>
          <p className="mt-4 text-sm leading-relaxed text-muted-foreground">
            Nothing has changed about your account yet. Your current password
            still works until you use the link.
          </p>
          <Button asChild className="mt-5 w-full" size="field">
            <Link href="/">Back to sign in</Link>
          </Button>
        </div>
      ) : (
        <>
          <h1 className="text-lg font-semibold">Forgotten your password?</h1>
          <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
            Give the email address your account was created with. If it belongs
            to an officer account, a link to set a new password is sent to it.
          </p>

          <form onSubmit={submit} className="mt-6 space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="email">Email address</Label>
              <div className="relative">
                <Mail
                  className="pointer-events-none absolute left-3.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
                  aria-hidden
                />
                <Input
                  id="email"
                  type="email"
                  inputSize="field"
                  className="pl-10"
                  autoComplete="username"
                  autoFocus
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
              </div>
            </div>

            {error && (
              <p
                role="alert"
                className="rounded-lg bg-destructive-bg px-3 py-2 text-sm text-on-destructive-bg">
                {error}
              </p>
            )}

            <Button type="submit" size="field" className="w-full" disabled={busy}>
              {busy && <Loader2 className="size-4 animate-spin" />}
              Send me a link
            </Button>
          </form>

          <p className="mt-5 text-center text-sm">
            <Link href="/" className="text-primary hover:underline">
              Back to sign in
            </Link>
          </p>
        </>
      )}
    </Frame>
  );
}

function Frame({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-5 py-10">
      <div className="w-full max-w-md">
        <Wordmark className="mx-auto mb-6" width={170} />
        <div className="rounded-2xl border border-border bg-card p-7 shadow-sm sm:p-8">
          {children}
        </div>
        <p className="mt-5 text-center text-xs leading-relaxed text-faint">
          Post Disaster Crop Loss Assessment Platform
        </p>
      </div>
    </div>
  );
}
