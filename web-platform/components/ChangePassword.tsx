"use client";

import { useState } from "react";
import { Loader2, KeyRound } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import { useSession } from "@/components/SessionProvider";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/**
 * The forced first password change.
 *
 * An account created by an administrator carries a password that
 * administrator chose and has seen. Until it is replaced the backend refuses
 * every permission the account holds, so there is nothing else this screen
 * could usefully offer — showing the console behind it would be showing a
 * console where every action returns 403.
 */
export function ChangePassword() {
  const { user, signOut } = useSession();

  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (next !== confirm) {
      setError("The two new passwords do not match.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.changePassword(current, next);
      // Changing a password ends every session, this one included. That is
      // the point: if the reason for the change is that someone else knew
      // the old password, leaving their session alive undoes it.
      setDone(true);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
      setBusy(false);
    }
  }

  if (done) {
    return (
      <Shell title="Password updated">
        <p className="text-sm leading-relaxed text-muted-foreground">
          Every session has been signed out, including this one. Sign in again
          with your new password.
        </p>
        <Button className="mt-5 w-full" onClick={() => void signOut()}>
          Go to sign in
        </Button>
      </Shell>
    );
  }

  return (
    <Shell title="Set your own password">
      <p className="mb-5 text-sm leading-relaxed text-muted-foreground">
        {user?.name ? `${user.name}, your` : "Your"} account was created with a
        password chosen by an administrator. Replace it before continuing —
        until you do, the account can do nothing else.
      </p>

      <form onSubmit={submit} className="space-y-4">
        <div className="space-y-1.5">
          <Label htmlFor="current">Current password</Label>
          <Input
            id="current"
            type="password"
            autoComplete="current-password"
            autoFocus
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            required
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="next">New password</Label>
          <Input
            id="next"
            type="password"
            autoComplete="new-password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            required
          />
          <p className="text-xs text-faint">
            At least 12 characters. Length matters more than punctuation.
          </p>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="confirm">Confirm new password</Label>
          <Input
            id="confirm"
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            required
          />
        </div>

        {error && (
          <p
            role="alert"
            className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm leading-relaxed text-sev-catastrophic"
          >
            {error}
          </p>
        )}

        <Button type="submit" className="w-full" disabled={busy}>
          {busy && <Loader2 className="size-4 animate-spin" />}
          Update password
        </Button>
      </form>
    </Shell>
  );
}

function Shell({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/40 px-4">
      <div className="w-full max-w-sm">
        <div className="mb-7 text-center">
          <div className="mx-auto mb-3 grid size-11 place-items-center rounded-xl bg-primary text-primary-foreground">
            <KeyRound className="size-5" />
          </div>
          <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
        </div>
        <div className="rounded-xl border bg-card p-6 shadow-sm">{children}</div>
      </div>
    </div>
  );
}
