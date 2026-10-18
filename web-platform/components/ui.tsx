"use client";

/**
 * Domain UI built on shadcn primitives.
 *
 * The shadcn components in components/ui/* stay untouched so they can be
 * updated from the registry. This module adds the vocabulary this application
 * actually speaks — severity, triage, provenance — and keeps a stable API for
 * the pages.
 */

import Link from "next/link";
import type { ReactNode } from "react";
import {
  ChevronLeft,
  Inbox,
  TriangleAlert,
} from "lucide-react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import {
  Card as ShadCard,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader as ShadCardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton as ShadSkeleton } from "@/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { useNow } from "@/lib/useResource";
import {
  cropPresenceStyles,
  fraudLabel,
  fraudStyles,
  guardrailLabel,
  severityStyles,
  triageStyles,
} from "@/lib/format";
import type {
  CropPresence,
  FraudIndicator,
  Severity,
  TriageTier,
} from "@/lib/types";
import { hintFor } from "@/lib/errors";

export { Button } from "@/components/ui/button";
export { CardContent, CardFooter } from "@/components/ui/card";
export {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

export function Card({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <ShadCard className={cn("gap-0 py-0 overflow-hidden", className)}>
      {children}
    </ShadCard>
  );
}

/** Convenience header: title, optional subtitle, optional right-hand action. */
export function CardHeader({
  title,
  subtitle,
  action,
}: {
  title: string;
  subtitle?: string;
  action?: ReactNode;
}) {
  return (
    <ShadCardHeader className="border-b [.border-b]:pb-3.5 gap-0.5 pt-3.5">
      <CardTitle className="text-sm font-semibold tracking-tight">
        {title}
      </CardTitle>
      {subtitle && (
        <CardDescription className="text-xs">{subtitle}</CardDescription>
      )}
      {action && <CardAction>{action}</CardAction>}
    </ShadCardHeader>
  );
}

export function SeverityBadge({ severity }: { severity: Severity | "" }) {
  // An empty severity is the ordinary case: no damage figure survived
  // verification, so there is no tier to name.
  if (!severity) {
    return (
      <Badge variant="outline" className="font-medium text-muted-foreground">
        No damage figure
      </Badge>
    );
  }
  const s = severityStyles[severity];
  return (
    <Badge
      variant="secondary"
      className={cn("border-transparent font-medium", s.bg, s.text)}>
      {s.label}
    </Badge>
  );
}

export function TriageBadge({
  tier,
  showHint = false,
}: {
  tier: TriageTier;
  showHint?: boolean;
}) {
  const t = triageStyles[tier];
  const badge = (
    <Badge
      variant="secondary"
      className={cn("gap-1.5 border-transparent font-medium", t.bg, t.text)}>
      <span aria-hidden className="size-1.5 rounded-full bg-current opacity-70" />
      {t.label}
    </Badge>
  );

  if (!showHint) return badge;

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex">{badge}</span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{t.hint}</TooltipContent>
    </Tooltip>
  );
}

export function CropPresenceBadge({
  presence,
  showHint = false,
}: {
  presence: CropPresence;
  showHint?: boolean;
}) {
  const p = cropPresenceStyles[presence];
  const badge = (
    <Badge
      variant="secondary"
      className={cn("border-transparent font-medium", p.bg, p.text)}>
      {p.label}
    </Badge>
  );
  if (!showHint) return badge;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex">{badge}</span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{p.hint}</TooltipContent>
    </Tooltip>
  );
}

/**
 * Fraud indicators, ordered by severity.
 *
 * These route a claim to closer human scrutiny. None of them determines
 * anything on its own, and the wording keeps that distinction: a discrepancy
 * between the claim and the observed record is a question for an officer, not
 * a verdict on a farmer.
 */
export function FraudList({ indicators }: { indicators: FraudIndicator[] }) {
  if (indicators.length === 0) {
    return (
      <div className="px-4 py-4">
        <p className="text-sm text-muted-foreground">
          No discrepancy found between the claim and the observed record.
        </p>
      </div>
    );
  }

  const rank = { critical: 0, warning: 1, info: 2 } as const;
  const sorted = [...indicators].sort(
    (a, b) => rank[a.severity] - rank[b.severity],
  );

  return (
    <ul className="divide-y divide-border">
      {sorted.map((f, i) => {
        const style = fraudStyles[f.severity];
        return (
          <li key={`${f.code}-${i}`} className="flex gap-3 px-4 py-3">
            <span
              className={cn(
                "mt-0.5 shrink-0 rounded px-1.5 py-0.5 text-xs font-semibold uppercase tracking-wide",
                style.bg,
                style.text,
              )}>
              {style.label}
            </span>
            <div className="min-w-0">
              <p className="text-sm font-medium text-foreground">
                {fraudLabel(f.code)}
              </p>
              <p className="mt-0.5 text-sm leading-relaxed text-muted-foreground">
                {f.detail}
              </p>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

/** Guardrail codes, rendered as the plain statements an officer can read. */
export function GuardrailList({
  agent,
  backend,
}: {
  agent: string[] | null;
  backend: string[] | null;
}) {
  // Deduplicated: the agent and the backend apply the same rules, and an
  // officer does not need to know a rule fired in two places. Where they
  // diverge, the union is what actually shaped the answer.
  const codes = Array.from(new Set([...(agent ?? []), ...(backend ?? [])]));

  if (codes.length === 0) {
    return (
      <div className="px-4 py-4">
        <p className="text-sm text-muted-foreground">
          No guardrail altered this assessment.
        </p>
      </div>
    );
  }

  return (
    <ul className="divide-y divide-border">
      {codes.map((c) => (
        <li key={c} className="flex gap-3 px-4 py-3">
          <svg
            className="mt-0.5 size-3.5 shrink-0 text-sev-moderate"
            viewBox="0 0 16 16"
            fill="currentColor"
            aria-hidden>
            <path d="M8 1.5 15 14H1L8 1.5Zm0 4.5a.75.75 0 0 0-.75.75v3a.75.75 0 0 0 1.5 0v-3A.75.75 0 0 0 8 6Zm0 6.5a.9.9 0 1 0 0-1.8.9.9 0 0 0 0 1.8Z" />
          </svg>
          <div className="min-w-0">
            <p className="text-sm leading-relaxed text-muted-foreground">
              {guardrailLabel(c)}
            </p>
            <p className="mono mt-0.5 text-xs text-faint">{c}</p>
          </div>
        </li>
      ))}
    </ul>
  );
}

/** A labelled figure. `hint` explains provenance where a number could mislead. */
export function Stat({
  label,
  value,
  unit,
  hint,
  tone = "default",
}: {
  label: string;
  value: string | number;
  unit?: string;
  hint?: string;
  tone?: "default" | "muted";
}) {
  return (
    <div className="min-w-0">
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          "mt-1 flex items-baseline gap-1",
          tone === "muted" ? "text-muted-foreground" : "text-foreground",
        )}>
        <span className="tnum text-xl font-semibold tracking-tight">
          {value}
        </span>
        {unit && <span className="text-xs text-muted-foreground">{unit}</span>}
      </dd>
      {hint && <p className="mt-1 text-xs leading-snug text-faint">{hint}</p>}
    </div>
  );
}

export function Field({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0 py-2">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd
        className={cn("mt-0.5 truncate text-sm", mono && "mono")}
        title={typeof value === "string" ? value : undefined}>
        {value || <span className="text-faint">—</span>}
      </dd>
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return (
    <svg
      className={cn("size-4 animate-spin", className)}
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden>
      <circle
        cx="12"
        cy="12"
        r="10"
        stroke="currentColor"
        strokeWidth="3"
        className="opacity-25"
      />
      <path
        d="M22 12a10 10 0 0 1-10 10"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
      />
    </svg>
  );
}

export function EmptyState({
  title,
  body,
  action,
}: {
  title: string;
  body: string;
  action?: ReactNode;
}) {
  return (
    <CardContent className="flex flex-col items-center justify-center px-6 py-12 text-center">
      <span
        aria-hidden
        className="mb-3 grid size-9 place-items-center rounded-full bg-muted text-muted-foreground">
        <Inbox className="size-[18px]" />
      </span>
      <h3 className="text-sm font-semibold text-foreground">{title}</h3>
      <p className="mt-1 max-w-sm text-sm leading-relaxed text-muted-foreground">
        {body}
      </p>
      {action && <div className="mt-4">{action}</div>}
    </CardContent>
  );
}

/**
 * Treats the API's stable codes as explanations rather than failures.
 * NO_GEOMETRY and NO_OBSERVATIONS are expected states with specific causes,
 * and saying so is more useful than "something went wrong".
 */

export function ErrorPanel({
  code,
  message,
  onRetry,
}: {
  code?: string;
  message: string;
  onRetry?: () => void;
}) {
  const explanation = hintFor(code);
  return (
    <Alert variant="destructive">
      <TriangleAlert />
      <AlertTitle>{message}</AlertTitle>
      <AlertDescription>
        {explanation && <p className="leading-relaxed">{explanation}</p>}
        <div className="flex items-center gap-3">
          {code && <span className="mono text-xs opacity-70">{code}</span>}
          {onRetry && (
            <button
              onClick={onRetry}
              className="text-xs font-medium underline underline-offset-2">
              Try again
            </button>
          )}
        </div>
      </AlertDescription>
    </Alert>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <ShadSkeleton className={className} />;
}

export function BackLink({
  href,
  children,
}: {
  href: string;
  children: ReactNode;
}) {
  return (
    <Link
      href={href}
      className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground">
      <ChevronLeft className="size-4" />
      {children}
    </Link>
  );
}

/* ---------------------------------------------------------------------------
 * Survey events, reports and verification
 * ------------------------------------------------------------------------ */

export function StateBadge({
  styles,
  state,
  hint,
}: {
  styles: Record<string, { text: string; bg: string; label: string; hint?: string }>;
  state: string;
  hint?: boolean;
}) {
  const s = styles[state] ?? {
    text: "text-muted-foreground", bg: "bg-muted", label: state,
  };
  const badge = (
    <Badge variant="secondary" className={cn("border-transparent font-medium", s.bg, s.text)}>
      {s.label}
    </Badge>
  );
  if (!hint || !s.hint) return badge;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex">{badge}</span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{s.hint}</TooltipContent>
    </Tooltip>
  );
}

/**
 * A deadline, coloured only once it is close or past.
 *
 * Five tiers is five places for a report to stall, and the deadline is the
 * thing that stops the hierarchy reproducing its own delays with better
 * logging. It should be visible without being noise on everything that is fine.
 */
export function DueBadge({ dueAt, overdue }: { dueAt: string; overdue: boolean }) {
  // A clock that ticks, not a reading taken once. Date.now() during render is
  // impure -- and a worklist left open on a desk would keep saying "2d left"
  // for as long as the tab stayed open, which is exactly the badge that must
  // not be wrong.
  const now = useNow();
  const days = Math.round((new Date(dueAt).getTime() - now) / 86_400_000);
  const tone = overdue
    ? "bg-sev-catastrophic-bg text-sev-catastrophic"
    : days <= 2
      ? "bg-sev-moderate-bg text-sev-moderate"
      : "bg-muted text-muted-foreground";
  return (
    <span className={cn("rounded px-1.5 py-0.5 text-xs font-medium", tone)}>
      {overdue ? `${Math.abs(days)}d overdue` : days === 0 ? "due today" : `${days}d left`}
    </span>
  );
}

/** A labelled figure with its unit, for a verdict row. */
export function Figure({
  label,
  value,
  unit,
  hint,
  tone,
}: {
  label: string;
  value: string;
  unit?: string;
  hint?: string;
  tone?: "good" | "warn" | "bad";
}) {
  const toneCls =
    tone === "good" ? "text-sev-negligible"
      : tone === "warn" ? "text-sev-moderate"
        : tone === "bad" ? "text-sev-catastrophic"
          : "text-foreground";
  return (
    <div className="min-w-0">
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd className={cn("mt-1 flex items-baseline gap-1", toneCls)}>
        <span className="tnum text-xl font-semibold tracking-tight">{value}</span>
        {unit && <span className="text-xs text-muted-foreground">{unit}</span>}
      </dd>
      {hint && <p className="mt-1 text-xs leading-snug text-faint">{hint}</p>}
    </div>
  );
}

/** A short note explaining a rule, where the rule is not obvious from the UI. */
export function Note({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs leading-relaxed text-muted-foreground">
      {children}
    </p>
  );
}
