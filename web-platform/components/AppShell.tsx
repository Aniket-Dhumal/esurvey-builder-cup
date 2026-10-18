"use client";

import { useEffect, useState } from "react";
import { usePathname } from "next/navigation";
import { CircleCheck, Search } from "lucide-react";

import { NotificationBell } from "@/components/NotificationBell";
import { Sidebar } from "@/components/Sidebar";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { api } from "@/lib/api";
import Link from "next/link";
import { useSession } from "@/components/SessionProvider";
import { SignIn } from "@/components/SignIn";
import { ChangePassword } from "@/components/ChangePassword";
import { FarmerShell } from "@/components/FarmerShell";

/**
 * Application chrome: fixed sidebar, search topbar, scrolling content.
 *
 * The service indicator is not decoration. Every page here is useless without
 * the API, and "no records yet" and "the server is down" look identical
 * otherwise.
 */
/**
 * Routes that render without a session.
 *
 * Deliberately a closed set rather than a prefix test. "Anything under /auth"
 * is one careless route away from exposing a console screen to a caller with
 * no session -- the server would still refuse the data, and the officer would
 * still see a page that looks like the console.
 */
const PUBLIC_ROUTES = new Set(["/set-password", "/forgot-password"]);

export function AppShell({ children }: { children: React.ReactNode }) {
  const { user, loading, signOut } = useSession();
  const pathname = usePathname();
  const [online, setOnline] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    const check = () =>
      api
        .health()
        .then(() => !cancelled && setOnline(true))
        .catch(() => !cancelled && setOnline(false));

    check();
    const t = setInterval(check, 30_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  // The two screens reached without a session, because the person reading
  // them cannot sign in.
  //
  // Claiming an invited account happens before there is an account to be
  // signed in as; asking for a reset happens when the password is the thing
  // that is missing. Without this the shell replaces both with the sign-in
  // form -- so the link in every invitation email would land on a page asking
  // for the password it exists to create, and "Forgotten your password?"
  // would go straight back to the form it was clicked from.
  //
  // A trailing slash is compared away: the console is exported statically, so
  // a browser that followed a directory link is at "/forgot-password/" and one
  // that typed the path is not.
  if (PUBLIC_ROUTES.has(pathname.replace(/\/$/, "") || "/")) {
    return <>{children}</>;
  }

  // Nothing renders until the session is known. Flashing the console and then
  // replacing it with a sign-in screen looks like a bug and, worse, briefly
  // shows chrome for records the caller may not be entitled to.
  if (loading) {
    return (
      <div className="grid min-h-screen place-items-center bg-muted/40">
        <div className="text-sm text-muted-foreground">Loading…</div>
      </div>
    );
  }

  if (!user) return <SignIn />;

  // An account still carrying an administrator-issued password holds no
  // permissions at all, so there is no console to show it.
  if (user.must_change_password) return <ChangePassword />;

  // A farmer gets their own screen, not the officer console with most of it
  // hidden. They are doing a different job on a different device: one claim a
  // year, on a phone, standing in a field. Sharing a shell would mean serving
  // neither reader well.
  if (user.kind === "farmer") return <FarmerShell>{children}</FarmerShell>;

  return (
    <div className="flex h-screen overflow-hidden bg-background">
      <Sidebar apiOnline={online} />

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-16 shrink-0 items-center gap-4 border-b bg-card px-6">
          <div className="relative w-full max-w-lg">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              placeholder="Search farmers, parcels, assessments…"
              className="bg-muted/60 pl-9"
              aria-label="Search"
            />
          </div>

          <div className="ml-auto flex items-center gap-4">
            <NotificationBell />

            <div className="hidden text-right sm:block">
              <div className="text-sm font-semibold leading-tight">
                {user.name}
              </div>
              <div className="flex items-center justify-end gap-1 text-xs text-muted-foreground">
                <CircleCheck
                  className={
                    "size-3.5 " +
                    (online === false
                      ? "text-sev-catastrophic"
                      : "text-sev-negligible")
                  }
                />
                {online === false ? "API offline" : roleLabel(user.primary_role)}
              </div>
            </div>

            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  className="grid size-9 place-items-center rounded-full bg-primary text-sm font-semibold text-primary-foreground"
                  aria-label="Account"
                >
                  {initials(user.name)}
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-72">
                <DropdownMenuLabel className="font-normal">
                  <div className="text-sm font-medium">{user.name}</div>
                  <div className="mt-0.5 text-xs text-muted-foreground">
                    {roleLabel(user.primary_role)}
                  </div>
                </DropdownMenuLabel>
                <DropdownMenuSeparator />

                {/* Where they are posted, not just what they are called. It is
                    the half of the authorization decision that explains why a
                    record they expected to see is not in the list. */}
                <DropdownMenuLabel className="font-normal">
                  <div className="text-xs font-medium text-muted-foreground">
                    Posted to
                  </div>
                  <ul className="mt-1 space-y-0.5">
                    {user.grants.length === 0 && (
                      <li className="text-xs text-faint">No posting</li>
                    )}
                    {user.grants.map((g, i) => (
                      <li key={i} className="text-xs">
                        {g.jurisdiction_name ? (
                          <>
                            {g.jurisdiction_name}{" "}
                            <span className="mono text-faint">
                              {g.jurisdiction_level} · {g.jurisdiction_code}
                            </span>
                          </>
                        ) : (
                          <span className="text-faint">
                            own records only
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                </DropdownMenuLabel>

                <DropdownMenuSeparator />
                <DropdownMenuItem asChild>
                  <Link href="/account/">Your account</Link>
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => void signOut()}>
                  Sign out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>

        <main className="flex-1 overflow-y-auto">
          <div className="mx-auto max-w-[1400px] px-6 py-6">{children}</div>
        </main>
      </div>
    </div>
  );
}

/** Turns a role slug into the title an officer would recognise. */
function roleLabel(role: string): string {
  const labels: Record<string, string> = {
    system_admin: "System administrator",
    state_officer: "State officer",
    district_magistrate: "District Magistrate",
    tehsildar: "Tehsildar",
    mandal_officer: "Circle Officer",
    talathi: "Talathi",
    krushi_sahayak: "Krushi Sahayak",
    farmer: "Farmer",
  };
  return labels[role] ?? role;
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}
