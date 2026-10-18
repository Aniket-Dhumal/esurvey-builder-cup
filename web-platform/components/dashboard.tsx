"use client";

import Link from "next/link";
import type { LucideIcon } from "lucide-react";
import { ArrowUpRight } from "lucide-react";
import type { ReactNode } from "react";

import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

/**
 * Page title block: context line, heading, sub-line, right-hand actions.
 *
 * # Why the eyebrow is not a coloured caps label any more
 *
 * It was `text-primary uppercase tracking-[0.12em]` — three separate emphasis
 * devices stacked on the least important line of the page, and the loudest
 * thing above the fold on every screen. Worse, it said something different on
 * each one: the section on /reports, the record type on /events, the reader's
 * own job title on the dashboard. Sized and coloured like that, an officer
 * reads it first and gets a different kind of fact each time.
 *
 * It is context, so it is rendered as context: one muted line, sentence case.
 *
 * # Why the rule underneath
 *
 * A heading, a paragraph and then a card floating in the same white was three
 * things at one depth. The rule says where the page's identity stops and its
 * contents start, which is the job a masthead does on every form an officer
 * has ever signed.
 */
export function PageHeader({
  eyebrow,
  title,
  subtitle,
  actions,
}: {
  eyebrow?: string;
  title: string;
  subtitle?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-5 flex flex-wrap items-end justify-between gap-x-6 gap-y-3 border-b border-border pb-4">
      <div className="min-w-0">
        {eyebrow && (
          <div className="text-[13px] leading-5 text-muted-foreground">
            {eyebrow}
          </div>
        )}
        <h1 className="text-xl font-semibold tracking-tight text-foreground">
          {title}
        </h1>
        {subtitle && (
          <div className="mt-1 text-sm leading-5 text-muted-foreground">
            {subtitle}
          </div>
        )}
      </div>
      {actions && (
        <div className="flex shrink-0 items-center gap-2">{actions}</div>
      )}
    </div>
  );
}

/**
 * The headline counters.
 *
 * # What was wrong with the old ones
 *
 * Each carried a 40px solid-green rounded tile with a white glyph in it and a
 * small ↗ in the opposite corner. Four across the top of the page meant four
 * saturated blocks and four decorative arrows before a single number, and the
 * icons were doing no work — a clock beside "Awaiting action" and a document
 * beside "Reports filed" restate the label they sit next to.
 *
 * A counter's whole job is the figure. So the figure is the largest thing in
 * the card, the label sits above it at reading size, and the icon is a small
 * muted mark rather than a coloured tile.
 *
 * # Why the whole card is the link
 *
 * The arrow was a 24px target in a 200px card that was already, visually, one
 * object. Officers were clicking the number and getting nothing. Now the card
 * is the target, and the arrow only appears under the cursor to say so.
 */
export function KpiCard({
  label,
  value,
  unit,
  icon: Icon,
  href,
  tone = "default",
}: {
  label: string;
  value: ReactNode;
  /** Rendered small beside the figure: "ha", "%". Never inside it. */
  unit?: string;
  icon: LucideIcon;
  href?: string;
  tone?: "default" | "warn";
}) {
  const body = (
    <>
      <div className="flex items-center gap-2">
        <Icon
          className={cn(
            "size-4 shrink-0",
            tone === "warn" ? "text-destructive" : "text-muted-foreground",
          )}
          aria-hidden
        />
        <span className="truncate text-[13px] font-medium text-muted-foreground">
          {label}
        </span>
        {href && (
          <ArrowUpRight
            className="ml-auto size-4 shrink-0 text-faint opacity-0 transition-opacity group-hover/kpi:opacity-100"
            aria-hidden
          />
        )}
      </div>
      <div className="mt-3 flex items-baseline gap-1.5">
        <span
          className={cn(
            "tnum text-2xl font-semibold tracking-tight",
            tone === "warn" ? "text-destructive" : "text-foreground",
          )}
        >
          {value}
        </span>
        {unit && (
          <span className="text-sm font-normal text-muted-foreground">
            {unit}
          </span>
        )}
      </div>
    </>
  );

  if (!href) {
    return <Card className="group/kpi gap-0 p-4">{body}</Card>;
  }

  return (
    <Card className="group/kpi gap-0 p-0 transition-shadow hover:ring-foreground/25">
      <Link
        href={href}
        className="block rounded-xl p-4 outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={`${label}: ${String(value)}`}
      >
        {body}
      </Link>
    </Card>
  );
}

/**
 * A softer metric card: label, figure, and a line of context beneath. Used
 * where the number needs a qualifier to be read correctly.
 *
 * Same restraint as KpiCard — the icon tile is gone for the same reason.
 */
export function MetricCard({
  label,
  value,
  unit,
  footer,
  icon: Icon,
}: {
  label: string;
  value: ReactNode;
  unit?: string;
  footer?: ReactNode;
  icon?: LucideIcon;
}) {
  return (
    <Card className="gap-0 p-4">
      <div className="flex items-center gap-2">
        {Icon && (
          <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        )}
        <span className="truncate text-[13px] font-medium text-muted-foreground">
          {label}
        </span>
      </div>
      <div className="tnum mt-3 flex items-baseline gap-1.5 text-2xl font-semibold tracking-tight">
        {value}
        {unit && (
          <span className="text-sm font-normal text-muted-foreground">
            {unit}
          </span>
        )}
      </div>
      {footer && (
        <div className="mt-2 text-[13px] leading-5 text-muted-foreground">
          {footer}
        </div>
      )}
    </Card>
  );
}

/** "View details ↗" — the standard way out of a metric card. */
export function CardLink({
  href,
  children,
}: {
  href: string;
  children: ReactNode;
}) {
  return (
    <Link
      href={href}
      className="inline-flex items-center gap-1 font-medium text-primary hover:underline"
    >
      {children}
      <ArrowUpRight className="size-3.5" />
    </Link>
  );
}

/** Section wrapper for a chart or list panel. */
export function Panel({
  title,
  subtitle,
  badge,
  children,
  className,
}: {
  title: string;
  subtitle?: string;
  badge?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <Card className={cn("gap-0 overflow-hidden py-0", className)}>
      {/* Matches CardHeader in ui.tsx: same padding, same rule, same type
          sizes. Two panel headers that differ by two pixels of padding is
          exactly the kind of thing that makes a console feel assembled
          rather than designed. */}
      <div className="flex items-start justify-between gap-4 border-b border-border px-4 py-3.5">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold tracking-tight text-foreground">
            {title}
          </h2>
          {subtitle && (
            <p className="mt-0.5 text-xs text-muted-foreground">{subtitle}</p>
          )}
        </div>
        {badge}
      </div>
      <div className="p-4">{children}</div>
    </Card>
  );
}
