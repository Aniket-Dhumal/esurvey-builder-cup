"use client";

import { useState } from "react";
import { Pencil, Plus, ShieldAlert, ShieldCheck, ShieldOff } from "lucide-react";

import { api } from "@/lib/api";
import type { OfficerAccount } from "@/lib/types";
import { usePagedResource } from "@/lib/usePagedResource";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
import { formatDateTime, roleLabel } from "@/lib/format";
import {
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui";
import { PageHeader } from "@/components/dashboard";
import { StuckClaims } from "@/components/StuckClaims";
import { useSession } from "@/components/SessionProvider";
import { NativeSelect } from "@/components/ui/native-select";
import {
  AccountStatusDialog,
  CreateOfficerDialog,
  EditPostingsDialog,
  ResendInvite,
  useAssignableRoles,
  useCanManage,
  useVisibleDesignations,
} from "@/components/OfficerDialogs";

/**
 * The account roll.
 *
 * # What an officer sees here
 *
 * Only accounts they could act on: the server scopes the listing to their own
 * jurisdiction, so a Tehsildar sees the officers of their tehsil and a
 * Collector sees the district. That is not a courtesy — it is the same rule
 * that decides what they may change.
 */

/** The three states an account can be in, as the roll shows them. */
const STATUS_STYLES: Record<string, string> = {
  active: "bg-sev-negligible-bg text-sev-negligible",
  invited: "bg-info-bg text-info",
  suspended: "bg-destructive-bg text-on-destructive-bg",
  deactivated: "bg-muted text-muted-foreground",
};

/**
 * An account that has never had a password is *invited*, not active.
 *
 * The distinction matters at exactly one moment: a Tehsildar wondering why the
 * Talathi they added last week has filed nothing. "Active" would say the
 * account is in use; "invited" says the invitation has not been accepted, and
 * puts a re-invite button next to it.
 */
function displayStatus(u: OfficerAccount): string {
  if (u.status === "active" && !u.password_changed_at) return "invited";
  return u.status;
}

export default function AdminPage() {
  const { can } = useSession();
  const productAdmin = can("platform:read") && !can("report:read");
  const assignable = useAssignableRoles();
  const canManage = useCanManage();
  const designations = useVisibleDesignations();

  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<OfficerAccount | null>(null);
  const [changingStatus, setChangingStatus] = useState<OfficerAccount | null>(null);

  const [role, setRole] = useState("");
  const [status, setStatus] = useState("");

  const table = usePagedResource<OfficerAccount>(
    (a) =>
      api.listUsers(a, {
        kind: "officer",
        ...(role ? { role } : {}),
        ...(status ? { status } : {}),
      }),
    { deps: [role, status] },
  );
  const { rows: users, error, reload, loading, query } = table;

  return (
    <div>
      <PageHeader
        eyebrow="Administration"
        title={productAdmin ? "State accounts" : "Officer accounts"}
        subtitle={
          productAdmin
            ? "The State Officers who run each state, and the product administrators. Each state adds its own district and village officers."
            : "Who is posted where. A posting is what decides which records an officer can reach."
        }
        actions={
          can("user:create") && assignable.length > 0 ? (
            <Button onClick={() => setCreating(true)}>
              <Plus className="size-4" />
              Add officer
            </Button>
          ) : undefined
        }
      />

      {/* Above the officer list, because the fix is the officer list. A
          village with claims waiting and nobody posted to it is the one
          failure the claim pipeline cannot resolve on its own. */}
      {/* A state's own operational view; the product administrator holds no
          claim data and does not see it. */}
      {!productAdmin && (
        <div className="mt-6">
          <StuckClaims />
        </div>
      )}

      {error && (
        <div className="mb-5">
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        </div>
      )}

      <Card>
        <CardHeader
          title="Officers in your area"
          subtitle="Newest first"
          action={
            <div className="flex flex-wrap items-center justify-end gap-2">
              <TableSearch
                value={table.query}
                onChange={table.setQuery}
                busy={table.searching}
                placeholder="Name, email or mobile"
              />
              <div className="w-40 shrink-0">
                <NativeSelect
                  selectSize="lg"
                  aria-label="Filter by designation"
                  value={role}
                  onChange={(e) => setRole(e.target.value)}>
                  <option value="">All designations</option>
                  {designations.map((r) => (
                    <option key={r} value={r}>
                      {roleLabel(r)}
                    </option>
                  ))}
                </NativeSelect>
              </div>
              <div className="w-36 shrink-0">
                <NativeSelect
                  selectSize="lg"
                  aria-label="Filter by status"
                  value={status}
                  onChange={(e) => setStatus(e.target.value)}>
                  <option value="">All statuses</option>
                  <option value="active">Active</option>
                  <option value="suspended">Suspended</option>
                  <option value="deactivated">Deactivated</option>
                </NativeSelect>
              </div>
            </div>
          }
        />

        {loading && (
          <div className="space-y-2 p-4">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
        )}

        {!loading && users.length === 0 && query !== "" && (
          <NoMatches query={query} noun="officers" />
        )}

        {!loading && users.length === 0 && query === "" && (
          <EmptyState
            title="No officers"
            body="No accounts inside your reach. Officers you add appear here."
          />
        )}

        {!loading && users.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Officer</TableHead>
                <TableHead>Email</TableHead>
                <TableHead>Designation &amp; posting</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Sign-in</TableHead>
                <TableHead>Last sign-in</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((u) => {
                const shown = displayStatus(u);
                const manageable = canManage(u);
                return (
                  <TableRow key={u.id}>
                    <TableCell className="max-w-[180px] whitespace-normal font-medium">
                      {u.full_name}
                    </TableCell>
                    <TableCell className="mono max-w-[200px] truncate text-xs text-muted-foreground">
                      {u.email ?? u.username}
                    </TableCell>
                    <TableCell className="max-w-[260px] whitespace-normal">
                      {u.grants.length === 0 ? (
                        <span className="text-xs text-faint">no posting</span>
                      ) : (
                        <div className="space-y-0.5">
                          {u.grants.map((g, i) => (
                            <div key={i} className="text-xs">
                              {roleLabel(g.role)}
                              {g.jurisdiction_name && (
                                <span className="text-muted-foreground">
                                  {" · "}
                                  {g.jurisdiction_name}{" "}
                                  <span className="mono text-faint">
                                    {g.jurisdiction_code}
                                  </span>
                                </span>
                              )}
                            </div>
                          ))}
                        </div>
                      )}
                    </TableCell>
                    <TableCell>
                      <span
                        className={
                          "rounded px-1.5 py-0.5 text-xs font-medium " +
                          (STATUS_STYLES[shown] ?? "bg-muted text-muted-foreground")
                        }>
                        {shown}
                      </span>
                    </TableCell>
                    {/* Whether a password is all that stands between this
                        account and whoever learns it. An officer adds their
                        own handset under Your account; nobody can add it for
                        them, which is why this column exists rather than a
                        field on this page. */}
                    <TableCell>
                      {u.mobile ? (
                        <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                          <ShieldCheck className="size-3.5 text-emerald-600" />
                          <span className="mono">{u.mobile}</span>
                        </span>
                      ) : (
                        <span className="inline-flex items-center gap-1.5 text-xs text-amber-700">
                          <ShieldAlert className="size-3.5" />
                          Password only
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-xs text-faint">
                      {u.last_login_at ? formatDateTime(u.last_login_at) : "never"}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex items-center justify-end gap-1.5">
                        {shown === "invited" && manageable && can("user:update") && (
                          <ResendInvite officer={u} onDone={reload} />
                        )}
                        {manageable && can("role:grant") && (
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => setEditing(u)}
                            aria-label={`Edit postings for ${u.full_name}`}>
                            <Pencil className="size-3.5" />
                            Postings
                          </Button>
                        )}
                        {manageable && can("user:update") && (
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => setChangingStatus(u)}
                            aria-label={
                              u.status === "active"
                                ? `Disable ${u.full_name}`
                                : `Re-enable ${u.full_name}`
                            }>
                            {u.status === "active" ? (
                              <>
                                <ShieldOff className="size-3.5" />
                                Disable
                              </>
                            ) : (
                              <>
                                <ShieldCheck className="size-3.5" />
                                Enable
                              </>
                            )}
                          </Button>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}

        <TablePager resource={table} noun="officers" />
      </Card>

      <CreateOfficerDialog
        open={creating}
        onClose={() => setCreating(false)}
        onDone={reload}
      />
      {/* Keyed on the officer: the dialog's own state is about one account,
          and remounting is how it is reset between them. */}
      <EditPostingsDialog
        key={`postings-${editing?.id ?? "none"}`}
        officer={editing}
        open={editing !== null}
        onClose={() => setEditing(null)}
        onDone={reload}
      />
      <AccountStatusDialog
        key={`status-${changingStatus?.id ?? "none"}`}
        officer={changingStatus}
        open={changingStatus !== null}
        onClose={() => setChangingStatus(null)}
        onDone={reload}
      />
    </div>
  );
}
