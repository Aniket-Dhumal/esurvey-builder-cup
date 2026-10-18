"use client";

import Image from "next/image";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState, useSyncExternalStore } from "react";
import { ChevronDown, PanelLeftClose, PanelLeftOpen } from "lucide-react";

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { isActive, visibleGroups } from "@/lib/nav";
import { useSession } from "@/components/SessionProvider";

/**
 * Where the console is remembered narrow or wide.
 *
 * Per browser, deliberately: it is a preference about this screen, not about
 * this officer, and a Tehsildar on a 13-inch laptop in the field wants the
 * room while the same person at a desk does not.
 */
const COLLAPSED_KEY = "esurvey.nav.collapsed";

function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem(COLLAPSED_KEY) === "1";
  } catch {
    // Private windows and blocked site data throw here. The nav opens wide,
    // which is the right default and not a failure.
    return false;
  }
}

/**
 * The preference as an external store.
 *
 * The console is exported as static HTML, so the markup is built with no
 * browser to ask -- it has to say "wide" and then correct itself once the
 * preference can be read. useSyncExternalStore is how React is told that:
 * a server snapshot, a client snapshot, and a subscription so a change in one
 * tab reaches the nav in another.
 */
let collapsedNow: boolean | null = null;
const listeners = new Set<() => void>();

function collapsedSnapshot(): boolean {
  if (collapsedNow === null) collapsedNow = readCollapsed();
  return collapsedNow;
}

function setCollapsedNow(value: boolean) {
  collapsedNow = value;
  try {
    window.localStorage.setItem(COLLAPSED_KEY, value ? "1" : "0");
  } catch {
    // Not remembering it is a smaller problem than not collapsing.
  }
  listeners.forEach((l) => l());
}

function subscribeCollapsed(listener: () => void) {
  listeners.add(listener);
  const fromAnotherTab = (e: StorageEvent) => {
    if (e.key === COLLAPSED_KEY) {
      collapsedNow = null;
      listener();
    }
  };
  window.addEventListener("storage", fromAnotherTab);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", fromAnotherTab);
  };
}

export function Sidebar({ apiOnline }: { apiOnline: boolean | null }) {
  // Only routes this officer can use. The server checks every request
  // regardless; what this buys is a sidebar they can trust.
  const { can } = useSession();
  const groups = visibleGroups(can);

  const pathname = usePathname();
  const [open, setOpen] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(groups.map((g) => [g.label, true])),
  );

  const collapsed = useSyncExternalStore(
    subscribeCollapsed,
    collapsedSnapshot,
    () => false,
  );
  const toggle = () => setCollapsedNow(!collapsed);

  return (
    <aside
      className={cn(
        "flex h-full shrink-0 flex-col border-r bg-card transition-[width] duration-200",
        collapsed ? "w-[68px]" : "w-64",
      )}
      data-collapsed={collapsed}
    >
      {/* The masthead is the supplied artwork rather than a generic glyph and
          a typed name: this is the one mark on the screen that has to be
          itself. Narrow, it becomes the roundel alone, which is the same mark
          and is what the browser tab already shows. */}
      <div
        className={cn(
          "flex h-16 shrink-0 items-center border-b",
          collapsed ? "flex-col justify-center gap-1 px-2" : "gap-2 pl-4 pr-2",
        )}
      >
        <Link
          href="/"
          aria-label="eSurvey — home"
          className="flex min-w-0 items-center outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
        >
          {collapsed ? (
            <Image
              src="/brand/icon-esurvey.png"
              alt=""
              width={127}
              height={97}
              className="h-7 w-auto"
              priority
            />
          ) : (
            <Image
              src="/brand/logo-esurvey@3x.png"
              alt="eSurvey"
              width={1131}
              height={426}
              className="h-9 w-auto"
              priority
            />
          )}
        </Link>

        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              onClick={toggle}
              aria-label={collapsed ? "Expand the menu" : "Collapse the menu"}
              aria-expanded={!collapsed}
              className={cn(
                "grid size-8 shrink-0 place-items-center rounded-md text-muted-foreground",
                "transition-colors hover:bg-muted hover:text-foreground",
                "focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
                !collapsed && "ml-auto",
              )}
            >
              {collapsed ? (
                <PanelLeftOpen className="size-[18px]" />
              ) : (
                <PanelLeftClose className="size-[18px]" />
              )}
            </button>
          </TooltipTrigger>
          <TooltipContent side="right">
            {collapsed ? "Expand the menu" : "Collapse the menu"}
          </TooltipContent>
        </Tooltip>
      </div>

      <ScrollArea className="flex-1">
        <nav className={cn("py-3", collapsed ? "px-2" : "px-2.5")}>
          {groups.map((group, groupIndex) => {
            const items = group.items.map((item) => {
              const active = isActive(item, pathname);
              const Icon = item.icon;

              const link = (
                <Link
                  key={item.href}
                  href={item.href}
                  aria-label={collapsed ? item.label : undefined}
                  className={cn(
                    "relative flex items-center rounded-md text-sm transition-colors",
                    collapsed
                      ? "h-9 w-full justify-center"
                      : "gap-2.5 px-2.5 py-[7px]",
                    active
                      ? // Solid green. A grey wash on a grey sidebar barely
                        // reads as selected -- it looks like the row is
                        // hovered, and on a page you did not just click it
                        // gives no answer to "where am I".
                        "bg-primary font-medium text-primary-foreground"
                      : "text-muted-foreground hover:bg-muted hover:text-foreground",
                  )}
                >
                  <Icon className="size-4 shrink-0" />
                  {!collapsed && item.label}
                </Link>
              );

              // Narrow, the label has nowhere to go, so it becomes the
              // tooltip. Without it the nav is a column of glyphs and the
              // officer has to click to find out what they are.
              return collapsed ? (
                <Tooltip key={item.href}>
                  <TooltipTrigger asChild>{link}</TooltipTrigger>
                  <TooltipContent side="right">{item.label}</TooltipContent>
                </Tooltip>
              ) : (
                link
              );
            });

            if (collapsed) {
              // No group headings: they are words, and there is no room for
              // words. A rule keeps the grouping visible without them.
              return (
                <div
                  key={group.label}
                  className={cn(
                    "space-y-0.5",
                    groupIndex > 0 && "mt-2 border-t pt-2",
                  )}
                >
                  {items}
                </div>
              );
            }

            return (
              <Collapsible
                key={group.label}
                open={open[group.label]}
                onOpenChange={(v) => setOpen((s) => ({ ...s, [group.label]: v }))}
                className="mb-1"
              >
                <CollapsibleTrigger className="flex w-full items-center justify-between rounded-md px-2 py-1.5 text-[11px] font-semibold tracking-[0.08em] text-faint uppercase transition-colors hover:text-muted-foreground">
                  {group.label}
                  <ChevronDown
                    className={cn(
                      "size-4 transition-transform",
                      !open[group.label] && "-rotate-90",
                    )}
                  />
                </CollapsibleTrigger>
                <CollapsibleContent className="mt-0.5 mb-3 space-y-0.5">
                  {items}
                </CollapsibleContent>
              </Collapsible>
            );
          })}
        </nav>
      </ScrollArea>

      <div className={cn("border-t py-2.5", collapsed ? "px-0" : "px-4")}>
        <span
          className={cn(
            "flex items-center text-xs text-muted-foreground",
            collapsed ? "justify-center" : "gap-2",
          )}
          title={
            apiOnline === null
              ? "Checking connection…"
              : apiOnline
                ? "Connected"
                : "No connection to the server"
          }
        >
          <span
            className={cn(
              "size-1.5 rounded-full",
              apiOnline === null
                ? "bg-faint"
                : apiOnline
                  ? "bg-sev-negligible"
                  : "bg-sev-catastrophic",
            )}
            aria-hidden
          />
          {!collapsed &&
            (apiOnline === null
              ? "Checking connection…"
              : apiOnline
                ? "Connected"
                : "No connection to the server")}
        </span>
      </div>
    </aside>
  );
}
