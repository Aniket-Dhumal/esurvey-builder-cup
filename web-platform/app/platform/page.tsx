"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import {
  Activity,
  Building2,
  CheckCircle2,
  FileStack,
  MailQuestion,
  Plug,
  ScrollText,
  Satellite,
  UserCheck,
  Users,
} from "lucide-react";

import { api, type ClaimStageCounts, type PlatformOverview } from "@/lib/api";
import { useResource } from "@/lib/useResource";
import { KpiCard, PageHeader } from "@/components/dashboard";
import { Card, CardHeader, ErrorPanel, Skeleton } from "@/components/ui";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * The product administrator's overview.
 *
 * # What it is for
 *
 * The operator of the product answers two questions from here: is each state
 * using the platform, and where is work piling up. Both are answered in
 * numbers -- claims by stage, officers by level, filings per day -- and never
 * in records. No farmer, claim, village or officer name reaches this page:
 * the server does not send one (see api/platform_overview.go), and the
 * administrator has no route that would.
 */

const STAGES: { key: keyof ClaimStageCounts; label: string; tone: string }[] = [
  { key: "received", label: "Received", tone: "bg-chart-moving" },
  { key: "checking", label: "Being checked", tone: "bg-chart-moving" },
  { key: "with_officers", label: "With officers", tone: "bg-chart-waiting" },
  { key: "decided", label: "Decided", tone: "bg-chart-done" },
  { key: "closed", label: "Closed", tone: "bg-chart-done" },
  { key: "withdrawn", label: "Withdrawn", tone: "bg-chart-inert" },
];

const LEVELS: { key: string; label: string }[] = [
  { key: "state", label: "State" },
  { key: "district", label: "District" },
  { key: "sub_district", label: "Taluka" },
  { key: "circle", label: "Circle" },
  { key: "village", label: "Village" },
];

const fmt = (n: number) => n.toLocaleString("en-IN");

export default function PlatformOverviewPage() {
  const { data, error, loading, reload } = useResource(() =>
    api.platformOverview(),
  );

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Platform"
        title="Overview"
        subtitle="How each state is using the platform. Counts only — no client records are shown here."
        actions={
          <div className="flex flex-wrap gap-2 text-sm">
            <ToolChip href="/platform/health/" icon={<Activity className="size-4" />} label="Health & load" />
            <ToolChip href="/platform/integrations/" icon={<Plug className="size-4" />} label="Integrations" />
            <ToolChip href="/audit/" icon={<ScrollText className="size-4" />} label="Audit trail" />
            <ToolChip href="/sources/" icon={<Satellite className="size-4" />} label="Data sources" />
          </div>
        }
      />

      {error ? (
        <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
      ) : loading || !data ? (
        <Skeleton className="h-96" />
      ) : (
        <Overview data={data} />
      )}
    </div>
  );
}

function Overview({ data }: { data: PlatformOverview }) {
  const totals = useMemo(() => {
    const t = { claims: 0, withOfficers: 0, decided: 0, active: 0, pending: 0, live: 0 };
    for (const s of data.states) {
      t.claims += s.total;
      t.withOfficers += s.with_officers;
      t.decided += s.decided + s.closed;
    }
    for (const a of data.accounts) {
      t.active += a.active;
      t.pending += a.pending_invites;
      if (a.active > 0) t.live++;
    }
    return t;
  }, [data]);

  return (
    <>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-6">
        <KpiCard label="States loaded" value={fmt(data.states.length)} icon={Building2} />
        <KpiCard label="States with officers" value={fmt(totals.live)} icon={UserCheck} />
        <KpiCard label="Claims" value={fmt(totals.claims)} icon={FileStack} />
        <KpiCard label="With officers" value={fmt(totals.withOfficers)} icon={Users} />
        <KpiCard label="Decided or closed" value={fmt(totals.decided)} icon={CheckCircle2} />
        <KpiCard
          label="Invitations pending"
          value={fmt(totals.pending)}
          icon={MailQuestion}
          tone={totals.pending > 0 ? "warn" : "default"}
        />
      </div>

      <StateStagesCard data={data} />
      <DistrictStagesCard data={data} />
      <div className="grid gap-6 xl:grid-cols-2">
        <AccountsCard data={data} />
        <TrendCard data={data} />
      </div>
    </>
  );
}

function StageBar({ counts }: { counts: ClaimStageCounts }) {
  if (counts.total === 0) {
    return <div className="h-2 w-full rounded-full bg-muted" />;
  }
  return (
    <div className="flex h-2 w-full overflow-hidden rounded-full bg-muted">
      {STAGES.map((s) =>
        counts[s.key] > 0 ? (
          <div
            key={s.key}
            className={s.tone}
            style={{ width: `${(counts[s.key] / counts.total) * 100}%` }}
            title={`${s.label}: ${counts[s.key]}`}
          />
        ) : null,
      )}
    </div>
  );
}

function StageTable({
  rows,
}: {
  rows: { key: string; name: string; counts: ClaimStageCounts }[];
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[760px] text-sm">
        <thead>
          <tr className="border-b border-border text-left text-xs text-muted-foreground">
            <th className="px-4 py-2 font-medium">Name</th>
            {STAGES.map((s) => (
              <th key={s.key} className="px-3 py-2 text-right font-medium">
                {s.label}
              </th>
            ))}
            <th className="px-3 py-2 text-right font-medium">Total</th>
            <th className="w-40 px-4 py-2 font-medium">
              <span className="sr-only">Distribution</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key} className="border-b border-border last:border-0">
              <td className="px-4 py-2.5 font-medium">{r.name}</td>
              {STAGES.map((s) => (
                <td key={s.key} className="mono px-3 py-2.5 text-right tabular-nums">
                  {r.counts[s.key] ? fmt(r.counts[s.key]) : <span className="text-faint">0</span>}
                </td>
              ))}
              <td className="mono px-3 py-2.5 text-right font-semibold tabular-nums">
                {fmt(r.counts.total)}
              </td>
              <td className="px-4 py-2.5">
                <StageBar counts={r.counts} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function StateStagesCard({ data }: { data: PlatformOverview }) {
  return (
    <Card>
      <CardHeader title="Claims by stage, per state" subtitle="Where every claim on the platform currently is." />
      {data.states.length === 0 ? (
        <p className="px-4 pb-4 text-sm text-muted-foreground">No state is loaded yet.</p>
      ) : (
        <StageTable
          rows={data.states.map((s) => ({
            key: s.state_code,
            name: `${s.state_name} (${s.state_code})`,
            counts: s,
          }))}
        />
      )}
      <Legend />
    </Card>
  );
}

function DistrictStagesCard({ data }: { data: PlatformOverview }) {
  const [state, setState] = useState(data.states[0]?.state_code ?? "");
  const rows = data.districts.filter((d) => !state || d.state_code === state);

  return (
    <Card>
      <CardHeader
        title="Claims by stage, per district"
        subtitle="Only districts with at least one claim are listed."
        action={
          <div className="w-56">
            <NativeSelect
              selectSize="lg"
              aria-label="State"
              value={state}
              onChange={(e) => setState(e.target.value)}>
              <option value="">All states</option>
              {data.states.map((s) => (
                <option key={s.state_code} value={s.state_code}>
                  {s.state_name}
                </option>
              ))}
            </NativeSelect>
          </div>
        }
      />
      {rows.length === 0 ? (
        <p className="px-4 pb-4 text-sm text-muted-foreground">No claims have been filed here yet.</p>
      ) : (
        <StageTable
          rows={rows.map((d) => ({
            key: d.district_code,
            name: d.district_name,
            counts: d,
          }))}
        />
      )}
    </Card>
  );
}

function AccountsCard({ data }: { data: PlatformOverview }) {
  return (
    <Card>
      <CardHeader
        title="Officer accounts"
        subtitle="Active officers per state by the level they are posted at, and invitations not yet accepted."
      />
      <div className="overflow-x-auto">
        <table className="w-full min-w-[560px] text-sm">
          <thead>
            <tr className="border-b border-border text-left text-xs text-muted-foreground">
              <th className="px-4 py-2 font-medium">State</th>
              {LEVELS.map((l) => (
                <th key={l.key} className="px-3 py-2 text-right font-medium">
                  {l.label}
                </th>
              ))}
              <th className="px-3 py-2 text-right font-medium">Pending</th>
            </tr>
          </thead>
          <tbody>
            {data.accounts.map((a) => (
              <tr key={a.state_code} className="border-b border-border last:border-0">
                <td className="px-4 py-2.5 font-medium">{a.state_name}</td>
                {LEVELS.map((l) => (
                  <td key={l.key} className="mono px-3 py-2.5 text-right tabular-nums">
                    {a.by_level[l.key] ? fmt(a.by_level[l.key]) : <span className="text-faint">0</span>}
                  </td>
                ))}
                <td className="mono px-3 py-2.5 text-right tabular-nums">
                  {a.pending_invites > 0 ? (
                    <span className="font-semibold text-destructive">{fmt(a.pending_invites)}</span>
                  ) : (
                    <span className="text-faint">0</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function TrendCard({ data }: { data: PlatformOverview }) {
  // Thirty days ending on the server's today, in India's calendar,
  // zero-filled. Taken from when the figures were generated rather than from
  // the browser clock, so the axis matches the counts.
  const days = useMemo(() => {
    const out: string[] = [];
    const now = new Date(new Date(data.generated_at).getTime() + 5.5 * 3600 * 1000);
    for (let i = 29; i >= 0; i--) {
      const d = new Date(now);
      d.setUTCDate(now.getUTCDate() - i);
      out.push(d.toISOString().slice(0, 10));
    }
    return out;
  }, [data.generated_at]);
  const perDay = useMemo(() => {
    const m: Record<string, number> = {};
    for (const p of data.trend) m[p.day] = (m[p.day] ?? 0) + p.filed;
    return days.map((d) => ({ day: d, filed: m[d] ?? 0 }));
  }, [data, days]);
  const max = Math.max(1, ...perDay.map((p) => p.filed));
  const total = perDay.reduce((a, p) => a + p.filed, 0);

  const byState = useMemo(() => {
    const m: Record<string, number> = {};
    for (const p of data.trend) m[p.state_code] = (m[p.state_code] ?? 0) + p.filed;
    return data.states
      .map((s) => ({ name: s.state_name, filed: m[s.state_code] ?? 0 }))
      .filter((s) => s.filed > 0);
  }, [data]);

  return (
    <Card>
      <CardHeader
        title="Claims filed, last 30 days"
        subtitle={`${fmt(total)} claims across all states.`}
      />
      <div className="px-4 pb-4">
        <div className="flex h-40 items-end gap-[3px]" role="img" aria-label="Claims filed per day">
          {perDay.map((p) => (
            <div key={p.day} className="group relative flex h-full flex-1 items-end">
              <div
                className="w-full rounded-t-sm bg-chart-moving"
                style={{ height: `${(p.filed / max) * 100}%`, minHeight: p.filed ? 2 : 0 }}
              />
              <span className="pointer-events-none absolute -top-6 left-1/2 hidden -translate-x-1/2 whitespace-nowrap rounded bg-foreground px-1.5 py-0.5 text-[11px] text-background group-hover:block">
                {p.day.slice(5)}: {p.filed}
              </span>
            </div>
          ))}
        </div>
        <div className="mt-1 flex justify-between text-[11px] text-faint">
          <span>{days[0].slice(5)}</span>
          <span>today</span>
        </div>
        {byState.length > 0 && (
          <ul className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
            {byState.map((s) => (
              <li key={s.name}>
                {s.name}: <span className="mono text-foreground">{fmt(s.filed)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}

function Legend() {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 border-t border-border px-4 py-2.5 text-xs text-muted-foreground">
      {STAGES.map((s) => (
        <span key={s.key} className="inline-flex items-center gap-1.5">
          <span className={`size-2 rounded-full ${s.tone}`} />
          {s.label}
        </span>
      ))}
    </div>
  );
}

function ToolChip({ href, icon, label }: { href: string; icon: React.ReactNode; label: string }) {
  return (
    <Link
      href={href}
      className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-muted-foreground hover:bg-muted hover:text-foreground">
      {icon}
      {label}
    </Link>
  );
}
