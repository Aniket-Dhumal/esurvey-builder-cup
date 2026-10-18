"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { Bell, CheckCheck, Loader2 } from "lucide-react";

import { api } from "@/lib/api";
import type { Notification } from "@/lib/types";
import { cn } from "@/lib/utils";

/**
 * The console's bell.
 *
 * # What it replaces
 *
 * A bell icon that did nothing. It has been in the topbar since the console
 * was built, and clicking it has never opened anything — which is worse than
 * no bell, because an officer who has learned that the icon is decoration
 * will not look at it on the day it starts meaning something.
 *
 * # Why it polls rather than holds a socket
 *
 * The thing being counted changes when a *different officer* acts — a report
 * approved a rung below, a report returned from above — so there is no local
 * event to react to. A socket would be the right answer for a chat window; for
 * a queue that moves a few times an hour, one small request a minute costs
 * less than a connection per signed-in officer held open all day across a
 * district's desks.
 *
 * Polling stops while the tab is hidden. An officer with the console open on a
 * second monitor overnight should not be issuing a request a minute until
 * morning.
 */

const POLL_MS = 60_000;

export function NotificationBell() {
  const [items, setItems] = useState<Notification[] | null>(null);
  const [unread, setUnread] = useState(0);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const panel = useRef<HTMLDivElement>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.listNotifications(false, 20);
      setItems(r.items);
      setUnread(r.unread);
    } catch {
      // A bell that cannot reach the API is not worth an error banner over
      // the page an officer is working on. The service indicator in the
      // sidebar already says the API is unreachable, and says it once.
    }
  }, []);

  useEffect(() => {
    let live = true;
    // The first fetch is written out here rather than as `void load()`,
    // because setState reached through an awaited helper reads to React's
    // lint rule as a synchronous set inside the effect body. Inside a .then
    // callback it is what it actually is: a response arriving from outside.
    api
      .listNotifications(false, 20)
      .then((r) => {
        if (!live) return;
        setItems(r.items);
        setUnread(r.unread);
      })
      .catch(() => {
        // Silent on purpose: the sidebar's service indicator already says the
        // API is unreachable, and says it once.
      });

    let timer: ReturnType<typeof setInterval> | null = null;

    const start = () => {
      if (timer === null) timer = setInterval(() => void load(), POLL_MS);
    };
    const stop = () => {
      if (timer !== null) {
        clearInterval(timer);
        timer = null;
      }
    };
    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        void load();
        start();
      } else {
        stop();
      }
    };

    if (document.visibilityState === "visible") start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      live = false;
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [load]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (panel.current && !panel.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  async function openPanel() {
    setOpen((v) => !v);
    if (!open) await load();
  }

  async function markRead(n: Notification) {
    if (n.read) return;
    // Marked locally first. The officer is about to navigate away, and a
    // notification that stays bold until a round trip completes looks like
    // the click did not register.
    setItems((cur) =>
      (cur ?? []).map((x) => (x.id === n.id ? { ...x, read: true } : x)),
    );
    setUnread((u) => Math.max(0, u - 1));
    try {
      const r = await api.markNotificationRead(n.id);
      setUnread(r.unread);
    } catch {
      void load(); // put back whatever the server actually thinks
    }
  }

  async function clearAll() {
    setBusy(true);
    try {
      await api.markAllNotificationsRead();
      await load();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div ref={panel} className="relative">
      <button
        onClick={() => void openPanel()}
        aria-label={
          unread > 0 ? `Notifications, ${unread} unread` : "Notifications"
        }
        aria-expanded={open}
        className="relative rounded-md p-2 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground">
        <Bell className="size-[18px]" />
        {unread > 0 && (
          <span className="absolute -right-0.5 -top-0.5 grid min-w-4 place-items-center rounded-full bg-primary px-1 text-[10px] font-semibold leading-4 text-primary-foreground">
            {unread > 99 ? "99+" : unread}
          </span>
        )}
      </button>

      {open && (
        <div className="absolute right-0 z-50 mt-2 w-96 max-w-[calc(100vw-2rem)] overflow-hidden rounded-xl border border-border bg-card shadow-lg">
          <div className="flex items-center justify-between border-b border-border px-4 py-3">
            <h2 className="text-sm font-semibold">Notifications</h2>
            {unread > 0 && (
              <button
                onClick={() => void clearAll()}
                disabled={busy}
                className="inline-flex items-center gap-1.5 text-xs font-medium text-primary hover:underline disabled:opacity-50">
                {busy ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <CheckCheck className="size-3.5" />
                )}
                Mark all read
              </button>
            )}
          </div>

          <div className="max-h-96 overflow-y-auto">
            {items === null && (
              <p className="flex items-center gap-2 px-4 py-6 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" /> Loading…
              </p>
            )}

            {items !== null && items.length === 0 && (
              <div className="px-4 py-8 text-center">
                <p className="text-sm font-medium">Nothing new</p>
                <p className="mt-1 text-sm text-muted-foreground">
                  Work arriving at your level will appear here.
                </p>
              </div>
            )}

            {(items ?? []).map((n) => {
              const body = (
                <>
                  <div className="flex items-start gap-2">
                    {/* The unread marker is a dot, not a colour on the text:
                        a read and an unread row must differ by something
                        other than weight for anyone who cannot see it. */}
                    <span
                      aria-hidden
                      className={cn(
                        "mt-1.5 size-1.5 shrink-0 rounded-full",
                        n.read ? "bg-transparent" : "bg-primary",
                      )}
                    />
                    <div className="min-w-0 flex-1">
                      <p
                        className={cn(
                          "text-sm leading-snug",
                          n.read ? "text-muted-foreground" : "font-medium text-foreground",
                        )}>
                        {n.title}
                      </p>
                      {n.body && (
                        <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">
                          {n.body}
                        </p>
                      )}
                      <p className="mt-1 text-xs text-faint">{ago(n.created_at)}</p>
                    </div>
                  </div>
                </>
              );

              const className =
                "block w-full border-b border-border/60 px-4 py-3 text-left last:border-0 hover:bg-muted/60";

              return n.link ? (
                <Link
                  key={n.id}
                  href={n.link}
                  onClick={() => {
                    void markRead(n);
                    setOpen(false);
                  }}
                  className={className}>
                  {body}
                </Link>
              ) : (
                <button
                  key={n.id}
                  onClick={() => void markRead(n)}
                  className={className}>
                  {body}
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

/**
 * "4 minutes ago", and a date once it stops being useful.
 *
 * Relative time is what a queue wants — how stale is this — and it stops being
 * an answer after a day, where "3 September" is what an officer would say.
 */
function ago(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000));

  if (secs < 60) return "just now";
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins} minute${mins === 1 ? "" : "s"} ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;

  return new Date(then).toLocaleDateString("en-IN", {
    day: "numeric",
    month: "short",
  });
}
