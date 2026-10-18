"use client";

import Link from "next/link";
import {
  CircleCheck,
  CircleSlash,
  ScrollText,
  Satellite,
  TriangleAlert,
  XCircle,
} from "lucide-react";

import { api } from "@/lib/api";
import { useResource } from "@/lib/useResource";
import type { Integration } from "@/lib/types";
import { PageHeader } from "@/components/dashboard";
import {
  Card,
  CardHeader,
  ErrorPanel,
  Skeleton,
} from "@/components/ui";

/**
 * The product administrator's console.
 *
 * # Why this page exists
 *
 * Two screens — the audit trail and the data sources — were taken out of the
 * sidebar because they belong to whoever runs the platform rather than to
 * whoever uses it. They stayed reachable at their URLs and had nowhere to be
 * reached from, so an operator got to the audit log by typing a path.
 *
 * # Why the integrations are the top of it and not a footnote
 *
 * Four capabilities are built end to end and delivering nothing, each one
 * credential away. They degrade honestly — the queues hold what they could not
 * send, and an assessment with no threshold yield says the percentage is not a
 * shortfall against a statutory figure — and honest degradation is worth very
 * little if nobody is looking at it. The queue depth was visible only inside
 * /readyz, which is a document meant for a load balancer.
 *
 * So the first thing on the page is what is not connected, what is piling up
 * behind it, and the variable that turns it on.
 */
export default function IntegrationsPage() {
  const { data, error, loading, reload } = useResource(() => api.integrations());

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Platform"
        title="Integrations"
        subtitle="What this deployment is connected to, and what is waiting on something that is not."
      />

      {error ? (
        <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
      ) : loading ? (
        <Skeleton className="h-64" />
      ) : (
        <>
          {/* Waiting on a credential, first. An operator opening this page is
              far more often asking "why has nothing been sent" than reading
              down a list of things that work. */}
          <div className="grid gap-4 lg:grid-cols-2">
            {(data?.integrations ?? [])
              .slice()
              .sort((a, b) => rank(a.state) - rank(b.state))
              .map((i) => (
                <IntegrationCard key={i.key} integration={i} />
              ))}
          </div>

          <CircleCoverageCard />

          <Card>
            <CardHeader
              title="Operator tools"
              subtitle="Not in the sidebar, because a Tehsildar has no use for either."
            />
            <div className="divide-y divide-border">
              <ToolLink
                href="/audit/"
                icon={<ScrollText className="size-4" aria-hidden />}
                title="Audit trail"
                body="Every decision, with the hash chain that shows the record has not been rewritten. Read during an investigation, not during a working day."
              />
              <ToolLink
                href="/sources/"
                icon={<Satellite className="size-4" aria-hidden />}
                title="Data sources"
                body="Which evidence the platform actually has, and which of the specification's signals are still missing."
              />
            </div>
          </Card>

          {data?.environment && (
            <p className="text-xs text-faint">
              Environment: <span className="mono">{data.environment}</span>
            </p>
          )}
        </>
      )}
    </div>
  );
}

/**
 * How much of the state has a Circle Officer above it.
 *
 * The most consequential missing data in the platform, and the least visible:
 * where a village has no circle, the first genuinely independent reviewer in
 * the chain is skipped and the claim reaches the Tehsildar. Nothing fails and
 * nothing is logged, so it is counted here instead.
 *
 * Deliberately no "add a circle" button. The mapping comes from a district
 * revenue record and is loaded from a file that is reviewed; a screen that
 * let somebody type one in is a screen that lets somebody attach a village to
 * a place that does not administer it.
 */
function CircleCoverageCard() {
  const { data, loading } = useResource(() => api.circleCoverage());

  if (loading) return <Skeleton className="h-32" />;
  if (!data) return null;

  const s = data.summary;
  const talukasMissing = s.talukas - s.talukas_with_circle;
  const complete = talukasMissing === 0;

  return (
    <Card>
      <CardHeader
        title="Circle coverage"
        subtitle={
          complete
            ? "Every taluka has its circles recorded."
            : `${talukasMissing} of ${s.talukas} talukas have no circle recorded.`
        }
      />
      <div className="px-4 py-4">
        <dl className="flex flex-wrap gap-x-8 gap-y-3">
          <div>
            <dt className="text-xs text-faint">Talukas with circles</dt>
            <dd className="tnum text-sm font-medium">
              {s.talukas_with_circle} / {s.talukas}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-faint">Villages with a Circle Officer</dt>
            <dd className="tnum text-sm font-medium">
              {(s.villages - s.villages_no_circle).toLocaleString("en-IN")} /{" "}
              {s.villages.toLocaleString("en-IN")}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-faint">Villages without one</dt>
            <dd
              className={
                "tnum text-sm font-medium " +
                (s.villages_no_circle > 0 ? "text-sev-severe" : "")
              }>
              {s.villages_no_circle.toLocaleString("en-IN")}
            </dd>
          </div>
        </dl>
        <p className="mt-3 rounded-md bg-muted px-3 py-2 text-xs leading-relaxed text-muted-foreground">
          {s.note}
        </p>
      </div>
    </Card>
  );
}

/** Broken first, then unconfigured, then degraded, then what works. */
function rank(state: Integration["state"]): number {
  return { failing: 0, unconfigured: 1, degraded: 2, live: 3 }[state] ?? 4;
}

const TONE: Record<
  Integration["state"],
  { label: string; text: string; bg: string; icon: React.ReactNode }
> = {
  live: {
    label: "Connected",
    text: "text-sev-negligible",
    bg: "bg-sev-negligible-bg",
    icon: <CircleCheck className="size-4" aria-hidden />,
  },
  degraded: {
    label: "Degraded",
    text: "text-sev-severe",
    bg: "bg-sev-severe-bg",
    icon: <TriangleAlert className="size-4" aria-hidden />,
  },
  unconfigured: {
    label: "Not configured",
    text: "text-muted-foreground",
    bg: "bg-muted",
    icon: <CircleSlash className="size-4" aria-hidden />,
  },
  failing: {
    label: "Not answering",
    text: "text-sev-catastrophic",
    bg: "bg-sev-catastrophic-bg",
    icon: <XCircle className="size-4" aria-hidden />,
  },
};

function IntegrationCard({ integration: i }: { integration: Integration }) {
  const tone = TONE[i.state];
  return (
    <Card className="px-4 py-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-foreground">{i.name}</h2>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
            {i.summary}
          </p>
        </div>
        <span
          className={`inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium ${tone.bg} ${tone.text}`}>
          {tone.icon}
          {tone.label}
        </span>
      </div>

      {i.consequence && (
        <p className="mt-3 rounded-md bg-muted px-3 py-2 text-xs leading-relaxed text-muted-foreground">
          {i.consequence}
        </p>
      )}

      {i.metrics && i.metrics.length > 0 && (
        <dl className="mt-3 flex flex-wrap gap-x-6 gap-y-2">
          {i.metrics.map((m) => (
            <div key={m.label}>
              <dt className="text-xs text-faint">{m.label}</dt>
              <dd
                className={
                  "tnum text-sm font-medium " +
                  (m.warn ? "text-sev-severe" : "text-foreground")
                }>
                {m.value}
              </dd>
            </div>
          ))}
        </dl>
      )}

      {/* Named, never valued. What an operator needs is which variable to
          set; what they must never be handed is the one that is already
          set. */}
      {i.state !== "live" && i.enable && i.enable.length > 0 && (
        <div className="mt-3">
          <p className="text-xs text-faint">Turned on by</p>
          <ul className="mono mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
            {i.enable.map((v) => (
              <li key={v}>{v}</li>
            ))}
          </ul>
        </div>
      )}
    </Card>
  );
}

function ToolLink({
  href,
  icon,
  title,
  body,
}: {
  href: string;
  icon: React.ReactNode;
  title: string;
  body: string;
}) {
  return (
    <Link
      href={href}
      className="flex gap-3 px-4 py-3.5 transition-colors hover:bg-muted">
      <span className="mt-0.5 text-muted-foreground">{icon}</span>
      <span>
        <span className="block text-sm font-medium text-foreground">{title}</span>
        <span className="mt-0.5 block text-sm leading-relaxed text-muted-foreground">
          {body}
        </span>
      </span>
    </Link>
  );
}
