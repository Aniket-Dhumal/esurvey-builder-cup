"use client";

import Link from "next/link";
import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { CheckCircle2, Loader2, Lock } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Wordmark } from "@/components/brand";

/**
 * Claiming an invited account.
 *
 * The one screen in the console reached without signing in, because the person
 * reading it has no credential yet — the invitation link is the credential.
 *
 * # Why the account is named before the form
 *
 * An invitation can be forwarded. Somebody who was sent a colleague's link
 * should see whose account they are about to take over before they set a
 * password on it, not afterwards. So the token is resolved first and the name
 * and address it belongs to are shown above the fields.
 *
 * # Why this does not sign them in
 *
 * Setting a password and signing in are two acts. Doing both here would mean a
 * forwarded link takes over an account *and* lands its holder inside it in one
 * request. They set a password, and then they sign in with it like anyone else.
 */
export default function SetPasswordPage() {
  return (
    <Suspense fallback={<Frame><Loading /></Frame>}>
      <SetPassword />
    </Suspense>
  );
}

function SetPassword() {
  const token = useSearchParams().get("token") ?? "";

  const [invite, setInvite] = useState<{
    full_name: string;
    email: string;
    expires_at: string;
    purpose: "invite" | "reset";
  } | null>(null);
  const [fetchError, setFetchError] = useState<ApiError | null>(null);

  // A link with no token at all is knowable from the URL alone -- there is
  // nothing to ask the server. Derived during render rather than set from an
  // effect, which would render the loading state first and then replace it.
  const linkError: ApiError | null = token
    ? fetchError
    : new ApiError(400, "MISSING_TOKEN", "no token in the link");

  const reset = invite?.purpose === "reset";

  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  useEffect(() => {
    if (!token) return;
    let live = true;
    api
      .checkInvite(token)
      .then((r) => live && setInvite(r))
      .catch((e) => live && setFetchError(e as ApiError));
    return () => {
      live = false;
    };
  }, [token]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    // Checked here as well as by the browser, because the two fields are the
    // only protection against setting a password with a typo in it — and the
    // account cannot be recovered by its owner afterwards.
    if (password !== confirm) {
      setError("The two passwords do not match.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      // The same screen serves both, and the two endpoints are separate on
      // purpose: each refuses the other's token rather than quietly honouring
      // it, because an invitation and a reset have different lifetimes and
      // different rules about who may ask for one.
      if (invite?.purpose === "reset") {
        await api.acceptPasswordReset(token, password);
      } else {
        await api.acceptInvite(token, password);
      }
      setDone(true);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  }

  if (linkError) {
    return (
      <Frame>
        <h1 className="text-lg font-semibold">This link cannot be used</h1>
        <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
          {linkError.message}
        </p>
        <p className="mt-4 text-sm text-muted-foreground">
          A link can be used once and expires. Ask for a new one from the sign
          in page, or ask the officer who created your account to send a fresh
          invitation.
        </p>
        <Button asChild className="mt-5 w-full" size="field">
          <Link href="/">Back to sign in</Link>
        </Button>
      </Frame>
    );
  }

  if (done) {
    return (
      <Frame>
        <div className="text-center">
          <CheckCircle2 className="mx-auto size-9 text-primary" aria-hidden />
          <h1 className="mt-3 text-lg font-semibold">
            {reset ? "Your password has been changed" : "Your password is set"}
          </h1>
          <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
            Sign in with your email address and the password you just chose.
            {reset && " Any other session you had open has been signed out."}
          </p>
          <Button asChild className="mt-5 w-full" size="field">
            <Link href="/">Go to sign in</Link>
          </Button>
        </div>
      </Frame>
    );
  }

  if (!invite) {
    return (
      <Frame>
        <Loading />
      </Frame>
    );
  }

  return (
    <Frame>
      <h1 className="text-lg font-semibold">
        {reset ? "Choose a new password" : "Set your password"}
      </h1>
      <p className="mt-1 text-sm text-muted-foreground">
        for <span className="font-medium text-foreground">{invite.full_name}</span>
      </p>
      <p className="mono mt-0.5 text-xs text-faint">{invite.email}</p>

      <form onSubmit={submit} className="mt-6 space-y-4">
        <div className="space-y-1.5">
          <Label htmlFor="pw">New password</Label>
          <div className="relative">
            <Lock
              className="pointer-events-none absolute left-3.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden
            />
            <Input
              id="pw"
              type="password"
              inputSize="field"
              className="pl-10"
              autoComplete="new-password"
              value={password}
              required
              minLength={12}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          <p className="text-xs text-faint">
            At least 12 characters, and not your name or email address.
          </p>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="pw2">Confirm password</Label>
          <div className="relative">
            <Lock
              className="pointer-events-none absolute left-3.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden
            />
            <Input
              id="pw2"
              type="password"
              inputSize="field"
              className="pl-10"
              autoComplete="new-password"
              value={confirm}
              required
              onChange={(e) => setConfirm(e.target.value)}
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
          Set password
        </Button>
      </form>

      <p className="mt-5 text-center text-xs leading-relaxed text-faint">
        Nobody else has seen this password, including the officer who created
        your account.
      </p>
    </Frame>
  );
}

function Loading() {
  return (
    <p className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
      <Loader2 className="size-4 animate-spin" /> Checking your link…
    </p>
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
