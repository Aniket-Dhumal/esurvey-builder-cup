"use client";

import { checkEmail } from "@/lib/email";
import { useState } from "react";
import { Loader2, Mail, Trash2 } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import type { OfficerAccount } from "@/lib/types";
import { roleLabel } from "@/lib/format";
import { Button } from "@/components/ui";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Modal } from "@/components/ui/modal";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import {
  POSTED_AT,
  PostingPicker,
  levelNoun,
  type Posting,
} from "@/components/PostingPicker";
import { useSession } from "@/components/SessionProvider";

/**
 * Creating an officer, editing their postings, and disabling their account.
 *
 * All three in one file because they are one job -- staffing -- and because
 * they share the rule that makes them safe: an officer may only reach roles
 * beneath their own, in places inside their own. The server enforces that on
 * every request; what these dialogs do is avoid offering what will be refused.
 */

/** The designations an officer of this role may create, in rank order. */
function assignableBy(roles: string[]): string[] {
  const below: Record<string, string[]> = {
    // The product administrator onboards states and adds its own peers.
    // Mirrors rbac/delegation.go.
    system_admin: ["state_officer", "system_admin"],
    // The state appoints each district's authority, and the district staffs
    // itself beneath. Mirrors rbac/delegation.go.
    state_officer: ["district_magistrate"],
    district_magistrate: [
      "tehsildar",
      "mandal_officer",
      "talathi",
      "krushi_sahayak",
    ],
    tehsildar: ["mandal_officer", "talathi", "krushi_sahayak"],
  };
  const out: string[] = [];
  for (const r of roles) {
    for (const t of below[r] ?? []) if (!out.includes(t)) out.push(t);
  }
  return out;
}

/**
 * Whether the signed-in officer may manage this account at all.
 *
 * Every posting it holds must be a designation the officer could create --
 * the server's own rank rule (api/delegation.go mayManage). Place is left to
 * the server, which scopes the list to the officer's area in the first place.
 * Offering Postings and Disable on an account the server will refuse is a
 * button that only ever produces an error.
 */
export function useCanManage(): (officer: OfficerAccount) => boolean {
  const { user } = useSession();
  const assignable = useAssignableRoles();
  return (officer) =>
    officer.id !== user?.user_id &&
    officer.grants.length > 0 &&
    officer.grants.every((g) => assignable.includes(g.role));
}

/** Every officer designation, highest rank first. */
const DESIGNATIONS = [
  "state_officer",
  "district_magistrate",
  "tehsildar",
  "mandal_officer",
  "talathi",
  "krushi_sahayak",
];

const LEVEL_DEPTH: Record<string, number> = {
  state: 0,
  district: 1,
  sub_district: 2,
  circle: 3,
  village: 4,
};

/**
 * The designations worth offering as a filter on the officer list.
 *
 * The list is the officers posted inside the signed-in officer's own area, so
 * nothing ranked above that area can appear in it -- and a system
 * administrator, who is posted nowhere, never can. Offering those filters to
 * a state officer offered searches that could only ever come back empty.
 * The administrator, whose list is the whole roll, sees every designation.
 */
export function useVisibleDesignations(): string[] {
  const { user } = useSession();
  const roles = (user?.grants ?? []).map((g) => g.role);
  // The product administrator's list is State Officers and administrators.
  if (roles.includes("system_admin")) return ["state_officer", "system_admin"];

  const depths = roles
    .map((r) => LEVEL_DEPTH[POSTED_AT[r] ?? ""])
    .filter((d): d is number => d !== undefined);
  if (depths.length === 0) return [];
  const top = Math.min(...depths);
  return DESIGNATIONS.filter((r) => LEVEL_DEPTH[POSTED_AT[r]] >= top);
}

export function useAssignableRoles(): string[] {
  const { user } = useSession();
  const roles = (user?.grants ?? []).map((g) => g.role);
  return assignableBy(roles);
}

/* ------------------------------------------------------------------ */

export function CreateOfficerDialog({
  open,
  onClose,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const options = useAssignableRoles();
  const [fullName, setFullName] = useState("");
  const [email, setEmail] = useState("");
  const [mobile, setMobile] = useState("");
  const [chosenRole, setRole] = useState("");
  // What the select shows is what is submitted. A default fixed at mount could
  // be a designation the officer cannot assign -- the session's roles may not
  // have arrived yet -- and the select would display its first option while
  // the picker and the request used the other.
  const role =
    chosenRole && options.includes(chosenRole) ? chosenRole : (options[0] ?? "");
  const [postings, setPostings] = useState<Posting[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [issued, setIssued] = useState<{ email: string; mailed: boolean } | null>(
    null,
  );

  const level = POSTED_AT[role] ?? "";

  // A posting is only meaningful at the level its designation is posted at, so
  // changing the designation clears what was chosen. Done here, in the event
  // that causes it, rather than in an effect watching `role`: an effect would
  // also fire on mount and on every unrelated re-render that happened to
  // change the value, and React now flags exactly that shape.
  //
  // Carried over instead, a village left selected under "Tehsildar" would be
  // submitted and refused, naming a level the officer never chose.
  function chooseRole(next: string) {
    setRole(next);
    setPostings([]);
  }

  function reset() {
    setFullName("");
    setEmail("");
    setMobile("");
    setRole("");
    setPostings([]);
    setError(null);
    setIssued(null);
    setBusy(false);
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    // Their email is their login, so it is checked before anything is sent:
    // an address that cannot receive mail is an officer who never gets the
    // invitation and cannot sign in.
    const checked = checkEmail(email);
    if ("error" in checked) {
      setError(checked.error);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await api.createOfficer({
        full_name: fullName.trim(),
        email: checked.email,
        mobile_number: mobile.trim() || undefined,
        role,
        postings: postings.map((p) => ({
          jurisdiction_level: p.jurisdiction_level,
          jurisdiction_code: p.jurisdiction_code,
        })),
      });
      setIssued({ email: r.user.email ?? email.trim(), mailed: r.mail_configured });
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  }

  // What happened, after it happened. An administrator setting the platform up
  // before a mail server exists has to be told the invitation is queued rather
  // than sent, or they will tell the new officer to check an inbox nothing is
  // going to arrive in.
  if (issued) {
    return (
      <Modal
        open={open}
        onClose={() => {
          reset();
          onClose();
        }}
        title="Account created"
        description={`An invitation was addressed to ${issued.email}.`}
        footer={
          <Button
            onClick={() => {
              reset();
              onClose();
            }}>
            Done
          </Button>
        }>
        <div className="space-y-3 text-sm">
          {issued.mailed ? (
            <p className="text-muted-foreground">
              They will receive a link to set their own password. It can be used
              once and stops working after three days. Until they use it, the
              account cannot be signed in to.
            </p>
          ) : (
            <div className="rounded-lg bg-destructive-bg px-3 py-2.5 text-on-destructive-bg">
              <p className="font-medium">No mail server is configured.</p>
              <p className="mt-1 leading-relaxed">
                The invitation is queued and will be sent as soon as one is.
                Nothing is lost — but do not tell this officer to expect an
                email yet.
              </p>
            </div>
          )}
          <p className="text-muted-foreground">
            Their username is their email address:{" "}
            <span className="mono text-foreground">{issued.email}</span>
          </p>
        </div>
      </Modal>
    );
  }

  return (
    <Modal
      open={open}
      onClose={() => {
        reset();
        onClose();
      }}
      title="Add an officer"
      description="They will be emailed a link to set their own password. You never choose it."
      size="lg"
      footer={
        <>
          <Button
            variant="outline"
            onClick={() => {
              reset();
              onClose();
            }}>
            Cancel
          </Button>
          <Button type="submit" form="officer-form" disabled={busy}>
            {busy && <Loader2 className="size-4 animate-spin" />}
            Create and invite
          </Button>
        </>
      }>
      <form id="officer-form" onSubmit={submit} className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="of-name">Name</Label>
            <Input
              id="of-name"
              value={fullName}
              required
              autoComplete="off"
              onChange={(e) => setFullName(e.target.value)}
              placeholder="R. B. Deshmukh"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="of-email">Email</Label>
            <Input
              id="of-email"
              type="email"
              value={email}
              required
              autoComplete="off"
              onChange={(e) => setEmail(e.target.value)}
              placeholder="r.deshmukh@nic.in"
            />
            <p className="text-xs text-faint">
              This is also their username, and where the invitation goes.
            </p>
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="of-mobile">Mobile number</Label>
            <Input
              id="of-mobile"
              value={mobile}
              autoComplete="off"
              onChange={(e) => setMobile(e.target.value)}
              placeholder="98765 43210"
            />
            <p className="text-xs text-faint">
              Optional, for reaching them. Officers sign in with a password, not
              a one-time code.
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="of-role">Designation</Label>
            <NativeSelect
              selectSize="lg"
              id="of-role"
              value={role}
              onChange={(e) => chooseRole(e.target.value)}>
              {options.map((r) => (
                <option key={r} value={r}>
                  {roleLabel(r)}
                </option>
              ))}
            </NativeSelect>
            <p className="text-xs text-faint">
              Only designations below your own are offered.
            </p>
          </div>
        </div>

        <div className="space-y-1.5">
          <Label>
            {level ? `Posted to ${levelNoun(level)}s` : "Posting"}
          </Label>
          <PostingPicker level={level} value={postings} onChange={setPostings} />
        </div>

        {error && (
          <p
            role="alert"
            className="rounded-lg bg-destructive-bg px-3 py-2 text-sm text-on-destructive-bg">
            {error}
          </p>
        )}
      </form>
    </Modal>
  );
}

/* ------------------------------------------------------------------ */

/**
 * Editing an officer's postings, and their designation.
 *
 * A transfer is the ordinary case: a Talathi picks up a fourth village, or
 * hands two back. Both are recorded as grants rather than edits -- the old
 * posting is revoked with a date on it and the new one is granted -- so "who
 * could see what, when" survives the change. That is why this adds and removes
 * rather than replacing a list.
 */
/**
 * Note for callers: mount this with `key={officer.id}`.
 *
 * Its state -- what is being added, under which designation -- belongs to one
 * officer. Remounting is how that state is reset, rather than an effect
 * watching the id: an effect runs after the render that already showed the
 * previous officer's half-filled form against the new officer's name.
 */
export function EditPostingsDialog({
  officer,
  open,
  onClose,
  onDone,
}: {
  officer: OfficerAccount | null;
  open: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const options = useAssignableRoles();
  const [adding, setAdding] = useState<Posting[]>([]);
  const [role, setRole] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const current = officer?.grants ?? [];
  // The designation to add under. Defaults to what they already hold, because
  // adding a village to a Talathi is far more common than changing what they
  // are.
  //
  // Only when it is one the signed-in officer may assign. A state officer
  // opening a Circle Officer's postings had the select showing "District
  // Magistrate" -- its first option -- while the picker searched circles,
  // because the held role, which the select cannot display, was still the one
  // in effect.
  const heldRole = current[0]?.role;
  const held =
    heldRole && options.includes(heldRole) ? heldRole : (options[0] ?? "");
  const effective = role && options.includes(role) ? role : held;
  const level = POSTED_AT[effective] ?? "";

  async function add() {
    if (!officer || adding.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      for (const p of adding) {
        await api.grantPosting(officer.id, {
          role: effective,
          jurisdiction_level: p.jurisdiction_level,
          jurisdiction_code: p.jurisdiction_code,
        });
      }
      setAdding([]);
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  }

  async function remove(g: OfficerAccount["grants"][number]) {
    if (!officer) return;
    setBusy(true);
    setError(null);
    try {
      await api.revokePosting(officer.id, {
        role: g.role,
        jurisdiction_level: g.jurisdiction_level ?? "",
        jurisdiction_code: g.jurisdiction_code ?? "",
      });
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  }

  if (!officer) return null;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Postings — ${officer.full_name}`}
      description="Where this officer works. Ending a posting keeps it on the record with the date it ended."
      size="lg"
      footer={<Button variant="outline" onClick={onClose}>Close</Button>}>
      <div className="space-y-5">
        <div>
          <div className="mb-2 text-[13px] font-medium text-muted-foreground">
            Current postings
          </div>
          {current.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              None. This account can sign in and see nothing.
            </p>
          ) : (
            <ul className="divide-y divide-border rounded-lg border border-border">
              {current.map((g, i) => (
                <li
                  key={i}
                  className="flex items-center justify-between gap-3 px-3 py-2.5">
                  <span className="min-w-0 text-sm">
                    <span className="font-medium">{roleLabel(g.role)}</span>
                    {g.jurisdiction_name && (
                      <>
                        {" · "}
                        {g.jurisdiction_name}{" "}
                        <span className="mono text-xs text-faint">
                          {g.jurisdiction_code}
                        </span>
                      </>
                    )}
                  </span>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy || current.length <= 1}
                    title={
                      current.length <= 1
                        ? "The only posting. Suspend the account instead."
                        : undefined
                    }
                    onClick={() => void remove(g)}>
                    <Trash2 className="size-3.5" />
                    End
                  </Button>
                </li>
              ))}
            </ul>
          )}
          {current.length === 1 && (
            <p className="mt-1.5 text-xs text-faint">
              An officer with no posting can sign in and see nothing, which
              looks to them like the platform being broken. To stop this
              account, suspend it instead.
            </p>
          )}
        </div>

        <div className="space-y-3 border-t border-border pt-4">
          <div className="text-[13px] font-medium text-muted-foreground">
            Add a posting
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="ep-role">Designation</Label>
              <NativeSelect
                selectSize="lg"
                id="ep-role"
                value={effective}
                onChange={(e) => {
                  setRole(e.target.value);
                  setAdding([]);
                }}>
                {options.map((r) => (
                  <option key={r} value={r}>
                    {roleLabel(r)}
                  </option>
                ))}
              </NativeSelect>
            </div>
          </div>
          <PostingPicker level={level} value={adding} onChange={setAdding} />
          <Button disabled={busy || adding.length === 0} onClick={() => void add()}>
            {busy && <Loader2 className="size-4 animate-spin" />}
            Add {adding.length > 0 ? adding.length : ""}{" "}
            {adding.length === 1 ? "posting" : "postings"}
          </Button>
        </div>

        {error && (
          <p
            role="alert"
            className="rounded-lg bg-destructive-bg px-3 py-2 text-sm text-on-destructive-bg">
            {error}
          </p>
        )}
      </div>
    </Modal>
  );
}

/* ------------------------------------------------------------------ */

/**
 * Disabling an account, and re-enabling it.
 *
 * Takes a reason and asks for confirmation, because it ends every session the
 * officer has the moment it lands -- mid-form, on a phone in a field. A
 * mistaken click here interrupts somebody's work with no explanation on their
 * screen.
 */
/** Mount with `key={officer.id}`; see EditPostingsDialog. */
export function AccountStatusDialog({
  officer,
  open,
  onClose,
  onDone,
}: {
  officer: OfficerAccount | null;
  open: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!officer) return null;
  const disabling = officer.status === "active";

  async function apply() {
    if (!officer) return;
    setBusy(true);
    setError(null);
    try {
      await api.setUserStatus(
        officer.id,
        disabling ? "suspended" : "active",
        reason.trim(),
      );
      onDone();
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={disabling ? "Disable this account" : "Re-enable this account"}
      description={officer.full_name}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant={disabling ? "destructive" : "default"}
            disabled={busy || reason.trim().length < 3}
            onClick={() => void apply()}>
            {busy && <Loader2 className="size-4 animate-spin" />}
            {disabling ? "Disable account" : "Re-enable account"}
          </Button>
        </>
      }>
      <div className="space-y-4">
        <p className="text-sm leading-relaxed text-muted-foreground">
          {disabling ? (
            <>
              This signs them out everywhere immediately, including on a phone
              mid-survey. Their postings and everything they have already
              recorded are kept.
            </>
          ) : (
            <>
              They will be able to sign in again with the password they already
              have. Their postings are unchanged.
            </>
          )}
        </p>
        <div className="space-y-1.5">
          <Label htmlFor="st-reason">Reason</Label>
          <Textarea
            id="st-reason"
            rows={3}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={
              disabling
                ? "Transferred out of the district on 30 August"
                : "Returned from deputation"
            }
          />
          <p className="text-xs text-faint">
            Recorded against your name in the audit trail.
          </p>
        </div>
        {error && (
          <p
            role="alert"
            className="rounded-lg bg-destructive-bg px-3 py-2 text-sm text-on-destructive-bg">
            {error}
          </p>
        )}
      </div>
    </Modal>
  );
}

/* ------------------------------------------------------------------ */

/** Re-send an invitation that went to the wrong address, or expired. */
export function ResendInvite({ officer, onDone }: { officer: OfficerAccount; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState<boolean | null>(null);

  async function resend() {
    setBusy(true);
    try {
      const r = await api.resendInvite(officer.id);
      setSent(r.mail_configured);
      onDone();
    } catch {
      setSent(false);
    } finally {
      setBusy(false);
    }
  }

  if (sent !== null) {
    return (
      <span className="text-xs text-muted-foreground">
        {sent ? "Invitation sent" : "Queued — no mail server"}
      </span>
    );
  }

  return (
    <Button size="sm" variant="outline" disabled={busy} onClick={() => void resend()}>
      {busy ? <Loader2 className="size-3.5 animate-spin" /> : <Mail className="size-3.5" />}
      Re-invite
    </Button>
  );
}
