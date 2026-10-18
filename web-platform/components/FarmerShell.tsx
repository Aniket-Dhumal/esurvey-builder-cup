"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { LogOut, Sprout } from "lucide-react";

import { useSession } from "@/components/SessionProvider";

/**
 * The farmer's chrome.
 *
 * Deliberately almost nothing: a title, who is signed in, a way out. A farmer
 * files one claim a year on a phone; a sidebar of sections would be five
 * places to get lost on the way to the one thing they came to do.
 *
 * Everything they need lives on a single screen at /my, which this redirects
 * to. If that screen ever needs a second, this is where the navigation goes.
 */
export function FarmerShell({ children }: { children: React.ReactNode }) {
  const { user, signOut } = useSession();
  const pathname = usePathname();
  const router = useRouter();

  // The officer console's routes exist and would mostly 403. Rather than let a
  // farmer land on one from a stale bookmark and read an error, send them home.
  useEffect(() => {
    const p = pathname.replace(/\/$/, "");
    if (p !== "/my") router.replace("/my");
  }, [pathname, router]);

  return (
    <div className="min-h-screen bg-background">
      <header className="sticky top-0 z-10 border-b bg-card">
        <div className="mx-auto flex max-w-lg items-center gap-3 px-4 py-3">
          <div className="grid size-9 shrink-0 place-items-center rounded-lg bg-primary text-primary-foreground">
            <Sprout className="size-5" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">eSurvey</div>
            <div className="truncate text-xs text-muted-foreground">
              {user?.name}
            </div>
          </div>
          <button
            onClick={() => void signOut()}
            className="rounded-md p-2 text-muted-foreground hover:bg-muted hover:text-foreground"
            aria-label="Sign out"
          >
            <LogOut className="size-[18px]" />
          </button>
        </div>
      </header>

      <main className="px-4 py-5">{children}</main>
    </div>
  );
}
