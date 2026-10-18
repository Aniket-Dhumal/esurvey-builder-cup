"use client";

import { useState } from "react";
import { Loader2, ShieldCheck, ShieldAlert, Smartphone } from "lucide-react";

import { api } from "@/lib/api";
import { messageForError } from "@/lib/errors";
import { useSession } from "@/components/SessionProvider";
import { PageHeader, Panel } from "@/components/dashboard";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Note } from "@/components/ui";

/**
 * Your own account, which for now means the handset that finishes your
 * sign-in.
 *
 * # Why this page exists
 *
 * An officer signs in with a password and then a code sent to their handset.
 * Five of the eight accounts on this deployment carry no number, so those
 * sign in on a password alone -- and nobody could see that, let alone fix it,
 * because the number could only be set by an administrator writing SQL.
 *
 * The officer sets it themselves on purpose. Whoever sets the number decides
 * which phone can finish this officer's sign-in, and an administrator typing
 * it at a counter is how a second factor ends up pointing at the wrong phone.
 */
export default function AccountPage() {
  const { user, refresh } = useSession();

  const [mobile, setMobile] = useState("");
  const [challenge, setChallenge] = useState<string | null>(null);
  const [hint, setHint] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  if (!user) return null;

  const protectedNow = !!user.mobile;

  async function sendCode(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setDone(null);
    try {
      const res = await api.startMobileChange(mobile.trim());
      setChallenge(res.challenge);
      setHint(res.mobile_hint);
      setCode("");
    } catch (err) {
      setError(messageForError(err));
    } finally {
      setBusy(false);
    }
  }

  async function confirm(e: React.FormEvent) {
    e.preventDefault();
    if (!challenge) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api.confirmMobileChange(challenge, code.trim());
      setChallenge(null);
      setMobile("");
      setDone(res.message);
      // The header and this page both read the account, so it is re-read
      // rather than patched in two places.
      await refresh();
    } catch (err) {
      setError(messageForError(err));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    setError(null);
    try {
      await api.removeMobile();
      setDone("The handset has been taken off this account. Signing in now asks for your password only.");
      await refresh();
    } catch (err) {
      setError(messageForError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Your account"
        subtitle={[user.name, user.email].filter(Boolean).join(" · ")}
      />

      <Panel
        title="Signing in"
        subtitle="How this account is protected">
        <div className="space-y-5 px-4 py-4">
          <div className="flex items-start gap-3">
            {protectedNow ? (
              <ShieldCheck className="mt-0.5 size-5 shrink-0 text-emerald-600" />
            ) : (
              <ShieldAlert className="mt-0.5 size-5 shrink-0 text-amber-600" />
            )}
            <div className="space-y-1">
              <p className="font-medium">
                {protectedNow
                  ? "Password, then a code to your handset"
                  : "Password only"}
              </p>
              <p className="text-sm text-muted-foreground">
                {protectedNow ? (
                  <>
                    Codes go to{" "}
                    <span className="mono">{user.mobile}</span>. Somebody who
                    learns your password still cannot sign in without that
                    phone.
                  </>
                ) : (
                  <>
                    Anyone who learns your password can sign in as you. Add a
                    handset and signing in will ask for a code as well.
                  </>
                )}
              </p>
            </div>
          </div>

          {done && <Note>{done}</Note>}

          {!challenge && (
            <form onSubmit={sendCode} className="space-y-3">
              <div className="grid gap-1.5 sm:max-w-sm">
                <Label htmlFor="mobile">
                  {protectedNow ? "Change the handset" : "Add your handset"}
                </Label>
                <div className="relative">
                  <Smartphone className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-faint" />
                  <Input
                    id="mobile"
                    className="pl-9"
                    inputMode="numeric"
                    autoComplete="tel"
                    placeholder="9876543210"
                    value={mobile}
                    onChange={(e) => setMobile(e.target.value)}
                    required
                  />
                </div>
                <p className="text-xs text-faint">
                  A code is sent to this number and has to come back before it
                  is saved, so a mistyped number cannot lock you out.
                </p>
              </div>

              {error && <p className="text-sm text-destructive">{error}</p>}

              <div className="flex flex-wrap gap-2">
                <Button type="submit" disabled={busy || mobile.trim().length < 10}>
                  {busy && <Loader2 className="size-4 animate-spin" />}
                  Send a code
                </Button>
                {protectedNow && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => void remove()}>
                    Remove this handset
                  </Button>
                )}
              </div>
            </form>
          )}

          {challenge && (
            <form onSubmit={confirm} className="space-y-3">
              <div className="grid gap-1.5 sm:max-w-sm">
                <Label htmlFor="code">
                  Code sent to <span className="mono">{hint}</span>
                </Label>
                <Input
                  id="code"
                  className="tracking-[0.3em]"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  maxLength={6}
                  placeholder="000000"
                  autoFocus
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                  required
                />
              </div>

              {error && <p className="text-sm text-destructive">{error}</p>}

              <div className="flex flex-wrap gap-2">
                <Button type="submit" disabled={busy || code.length < 6}>
                  {busy && <Loader2 className="size-4 animate-spin" />}
                  Save this handset
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => {
                    setChallenge(null);
                    setCode("");
                    setError(null);
                  }}>
                  Cancel
                </Button>
              </div>
            </form>
          )}
        </div>
      </Panel>
    </div>
  );
}
