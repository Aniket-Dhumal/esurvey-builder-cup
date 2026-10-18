"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Activity,
  AlertTriangle,
  CircleCheck,
  CircleSlash,
  Clock,
  Cpu,
  Gauge,
  XCircle,
} from "lucide-react";

import { api, ApiError, type GateStats, type HealthComponent, type PlatformHealth } from "@/lib/api";
import { KpiCard, PageHeader } from "@/components/dashboard";
import { Card, CardHeader, ErrorPanel, Skeleton } from "@/components/ui";

/**
 * Health and load, live.
 *
 * Polled every five seconds while the page is open and visible. Every figure
 * is about the platform itself -- services, the database, the queues, this
 * process, the request rate -- and none is about a client record.
 */

const POLL_MS = 5000;

export default function PlatformHealthPage() {
  const [data, setData] = useState<PlatformHealth | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [live, setLive] = useState(true);
  const [lastOk, setLastOk] = useState<number | null>(null);
  const busy = useRef(false);

  const poll = useCallback(async () => {
    if (busy.current) return;
    busy.current = true;
    try {
      setData(await api.platformHealth());
      setError(null);
      setLastOk(Date.now());
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, "NETWORK", "The platform did not answer."));
    } finally {
      busy.current = false;
    }
  }, []);

  useEffect(() => {
    // The first reading on the next tick, then every five seconds while the
    // tab is visible and the page is not paused.
    const first = setTimeout(() => void poll(), 0);
    if (!live) return () => clearTimeout(first);
    const id = setInterval(() => {
      if (document.visibilityState === "visible") void poll();
    }, POLL_MS);
    return () => {
      clearTimeout(first);
      clearInterval(id);
    };
  }, [live, poll]);

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Platform"
        title="Health & load"
        subtitle="Services, database, queues and request load, refreshed every five seconds."
        actions={
          <button
            type="button"
            onClick={() => setLive((v) => !v)}
            className="inline-flex items-center gap-2 rounded-md border border-border px-3 py-1.5 text-sm hover:bg-muted">
            <span
              className={`size-2 rounded-full ${live ? "animate-pulse bg-primary" : "bg-faint"}`}
              aria-hidden
            />
            {live ? "Live" : "Paused"}
          </button>
        }
      />

      {error && !data ? (
        <ErrorPanel code={error.code} message={error.message} onRetry={() => void poll()} />
      ) : !data ? (
        <Skeleton className="h-96" />
      ) : (
        <>
          {error && (
            <p role="alert" className="rounded-md bg-destructive-bg px-3 py-2 text-sm text-on-destructive-bg">
              The last refresh failed; showing the reading from{" "}
              {lastOk ? new Date(lastOk).toLocaleTimeString() : "earlier"}.
            </p>
          )}
          <Health data={data} />
        </>
      )}
    </div>
  );
}

function Health({ data }: { data: PlatformHealth }) {
  const load = data.load;
  const down = data.components.filter((c) => c.status === "down").length;
  const degraded = data.components.filter((c) => c.status === "degraded").length;

  return (
    <>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <KpiCard
          label="Services"
          value={down ? `${down} down` : degraded ? `${degraded} slow` : "All up"}
          icon={down ? XCircle : CircleCheck}
          tone={down || degraded ? "warn" : "default"}
        />
        <KpiCard label="Requests / sec (1 min)" value={load ? load.rps_1m.toFixed(2) : "—"} icon={Activity} />
        <KpiCard
          label="Error rate (5 min)"
          value={load ? `${(load.error_rate_5m * 100).toFixed(1)}` : "—"}
          unit="%"
          icon={AlertTriangle}
          tone={load && load.error_rate_5m > 0.01 ? "warn" : "default"}
        />
        <KpiCard label="Avg response (1 min)" value={load ? load.avg_ms_1m.toFixed(0) : "—"} unit="ms" icon={Gauge} />
        <KpiCard label="Uptime" value={load ? uptime(load.uptime_seconds) : "—"} icon={Clock} />
      </div>

      <Card>
        <CardHeader title="Services" subtitle={`Checked ${new Date(data.checked_at).toLocaleTimeString()} · ${data.env}`} />
        <div className="grid gap-px bg-border sm:grid-cols-2 xl:grid-cols-3">
          {data.components.map((c) => (
            <ComponentTile key={c.key} c={c} />
          ))}
        </div>
      </Card>

      {load && <LoadCard load={load} />}

      <div className="grid gap-6 xl:grid-cols-2">
        <ClaimFlowCard q={data.queues} />
        <GatesCard gates={data.gates ?? []} />
      </div>

      <div className="grid gap-6 xl:grid-cols-3">
        <DatabaseCard db={data.database} />
        <QueuesCard q={data.queues} />
        <RuntimeCard rt={data.runtime} />
      </div>
    </>
  );
}

function ComponentTile({ c }: { c: HealthComponent }) {
  const icon = {
    up: <CircleCheck className="size-4 text-primary" />,
    degraded: <AlertTriangle className="size-4 text-chart-waiting" />,
    down: <XCircle className="size-4 text-destructive" />,
    unconfigured: <CircleSlash className="size-4 text-faint" />,
  }[c.status];
  return (
    <div className="bg-card px-4 py-3">
      <div className="flex items-center gap-2">
        {icon}
        <span className="text-sm font-medium">{c.name}</span>
        <span className="mono ml-auto text-xs text-muted-foreground">
          {c.status === "unconfigured" ? "not configured" : `${c.latency_ms.toFixed(0)} ms`}
        </span>
      </div>
      <div className="mt-0.5 pl-6 text-xs capitalize text-muted-foreground">
        {c.status}
        {c.detail ? <span className="normal-case"> — {c.detail}</span> : null}
      </div>
    </div>
  );
}

function LoadCard({ load }: { load: NonNullable<PlatformHealth["load"]> }) {
  const series = load.series;
  const maxReq = Math.max(1, ...series.map((p) => p.requests));
  const maxMs = Math.max(1, ...series.map((p) => p.avg_ms));
  const w = 600;
  const h = 120;
  const x = (i: number) => (series.length <= 1 ? 0 : (i / (series.length - 1)) * w);
  const latency = series
    .map((p, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${(h - (p.avg_ms / maxMs) * h).toFixed(1)}`)
    .join(" ");

  return (
    <Card>
      <CardHeader
        title="Request load, last 5 minutes"
        subtitle={`${load.requests_5m.toLocaleString("en-IN")} requests · peak ${load.max_ms_5m.toFixed(0)} ms · ${load.rps_5m.toFixed(2)} req/s average`}
      />
      <div className="px-4 pb-4">
        <svg viewBox={`0 0 ${w} ${h}`} className="h-40 w-full" preserveAspectRatio="none" role="img" aria-label="Requests and latency over five minutes">
          {series.map((p, i) => {
            const bw = w / series.length - 2;
            const bh = (p.requests / maxReq) * h;
            const eh = (p.errors / maxReq) * h;
            return (
              <g key={p.at}>
                <rect x={(i * w) / series.length + 1} y={h - bh} width={bw} height={bh} className="fill-chart-moving/60" />
                {p.errors > 0 && (
                  <rect x={(i * w) / series.length + 1} y={h - eh} width={bw} height={eh} className="fill-destructive" />
                )}
              </g>
            );
          })}
          <path d={latency} fill="none" strokeWidth={2} className="stroke-chart-waiting" vectorEffect="non-scaling-stroke" />
        </svg>
        <div className="mt-2 flex flex-wrap gap-x-4 text-xs text-muted-foreground">
          <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-sm bg-chart-moving/60" />Requests per 10 s (max {maxReq})</span>
          <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-sm bg-destructive" />Server errors</span>
          <span className="inline-flex items-center gap-1.5"><span className="h-0.5 w-3 bg-chart-waiting" />Average response time (max {maxMs.toFixed(0)} ms)</span>
        </div>
      </div>
    </Card>
  );
}

function Row({ label, value, warn }: { label: string; value: React.ReactNode; warn?: boolean }) {
  return (
    <div className="flex items-center justify-between px-4 py-2 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className={`mono tabular-nums ${warn ? "font-semibold text-destructive" : ""}`}>{value}</span>
    </div>
  );
}

function DatabaseCard({ db }: { db: PlatformHealth["database"] }) {
  const used = db.pool_max ? db.pool_in_use / db.pool_max : 0;
  return (
    <Card>
      <CardHeader title="Database" subtitle="Connection pool and server" />
      <div className="px-4 pb-2">
        <div className="mb-1 flex justify-between text-xs text-muted-foreground">
          <span>Pool in use</span>
          <span className="mono">{db.pool_in_use} / {db.pool_max}</span>
        </div>
        <div className="h-2 overflow-hidden rounded-full bg-muted">
          <div className={used > 0.8 ? "h-full bg-destructive" : "h-full bg-primary"} style={{ width: `${used * 100}%` }} />
        </div>
      </div>
      <div className="divide-y divide-border">
        <Row label="Open connections" value={db.pool_total} />
        <Row label="Idle" value={db.pool_idle} />
        <Row label="Waited for a connection" value={db.pool_waits} warn={db.pool_waits > 0} />
        {db.server_active !== undefined && <Row label="Active queries" value={db.server_active} />}
        {db.size_bytes !== undefined && <Row label="Size" value={bytes(db.size_bytes)} />}
        {db.replica && (
          <Row
            label="Dashboard reads from"
            value={
              db.replica.replica_configured
                ? `${db.replica.reading_from} · lag ${(db.replica.lag_ms / 1000).toFixed(1)} s`
                : "primary (no replica)"
            }
            warn={db.replica.replica_configured && db.replica.reading_from === "primary"}
          />
        )}
        {db.replica?.replica_configured && db.replica.reading_from === "primary" && db.replica.reason && (
          <p className="px-4 py-2 text-xs text-destructive">{db.replica.reason}</p>
        )}
      </div>
      {db.pools && db.pools.length > 0 && (
        <div className="border-t border-border px-4 py-3">
          <div className="mb-2 text-xs font-medium text-muted-foreground">Connection pools on this instance</div>
          <div className="space-y-2">
            {db.pools.map((p) =>
              !p.configured ? (
                <div key={p.name} className="flex justify-between text-xs text-muted-foreground">
                  <span className="capitalize">{p.name}</span>
                  <span>not configured</span>
                </div>
              ) : (
                <div key={p.name}>
                  <div className="flex justify-between text-xs">
                    <span className="capitalize">{p.name}</span>
                    <span className={`mono tabular-nums ${(p.waits ?? 0) > 0 ? "text-destructive" : "text-muted-foreground"}`}>
                      {p.in_use} / {p.max}
                      {(p.waits ?? 0) > 0 && ` · waited ${p.waits}×`}
                    </span>
                  </div>
                  <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted">
                    <div
                      className={(p.in_use ?? 0) / (p.max || 1) > 0.8 ? "h-full bg-destructive" : "h-full bg-primary"}
                      style={{ width: `${((p.in_use ?? 0) / (p.max || 1)) * 100}%` }}
                    />
                  </div>
                </div>
              ),
            )}
          </div>
        </div>
      )}
    </Card>
  );
}

function QueuesCard({ q }: { q: PlatformHealth["queues"] }) {
  return (
    <Card>
      <CardHeader title="Queues" subtitle="Work waiting to be done" />
      <div className="divide-y divide-border">
        {q.assessments && (
          <>
            <Row label="Assessments queued" value={q.assessments.queued} warn={q.assessments.queued > 20} />
            <Row label="Assessments running" value={q.assessments.running} />
            <Row label="Assessments failed (24 h)" value={q.assessments.failed} warn={q.assessments.failed > 0} />
          </>
        )}
        {q.sms && (
          <Row label={`SMS waiting (${q.sms.provider})`} value={q.sms.queued} warn={q.sms.failed > 0} />
        )}
        {q.email_pending !== undefined && <Row label="Emails waiting" value={q.email_pending} />}
        {q.claims_stuck_in_pipeline !== undefined && (
          <Row label="Claims stuck in pipeline" value={q.claims_stuck_in_pipeline} warn={q.claims_stuck_in_pipeline > 0} />
        )}
      </div>
    </Card>
  );
}

const GATE_NAMES: Record<string, string> = {
  satellite: "Satellite analysis",
  radar: "Radar and moisture",
  ai: "Assessment panel (model)",
  vision: "Ground video",
};

function GatesCard({ gates }: { gates: GateStats[] }) {
  return (
    <Card>
      <CardHeader
        title="Upstream capacity"
        subtitle="Analyses running on each service, across every API instance, against its limit"
      />
      {gates.length === 0 ? (
        <p className="px-4 pb-4 text-sm text-muted-foreground">No limits are configured on this deployment.</p>
      ) : (
        <div className="divide-y divide-border">
          {gates.map((g) => {
            const used = g.in_use < 0 ? g.held_here : g.in_use;
            const full = used >= g.limit;
            return (
              <div key={g.name} className="px-4 py-3">
                <div className="mb-1 flex items-center justify-between text-sm">
                  <span>{GATE_NAMES[g.name] ?? g.name}</span>
                  <span className="mono tabular-nums text-muted-foreground">
                    {used} / {g.limit}
                    {g.waiting_here > 0 && <span className="text-chart-waiting"> · {g.waiting_here} waiting here</span>}
                  </span>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-muted">
                  <div
                    className={full ? "h-full bg-chart-waiting" : "h-full bg-primary"}
                    style={{ width: `${Math.min(1, used / g.limit) * 100}%` }}
                  />
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  Average wait for a slot {g.avg_wait_ms.toFixed(0)} ms
                  {!g.shared && <span className="text-destructive"> · counting this instance only (cache unavailable)</span>}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </Card>
  );
}

function ClaimFlowCard({ q }: { q: PlatformHealth["queues"] }) {
  const t = q.assessment_throughput;
  const owed = q.oldest_receipt_owed_seconds;
  return (
    <Card>
      <CardHeader title="Claim flow" subtitle="Receipts owed, assessments waiting, and the rate they are finishing" />
      <div className="divide-y divide-border">
        <Row
          label="Oldest receipt owed"
          value={owed === undefined ? "none" : duration(owed)}
          warn={owed !== undefined && owed > 300}
        />
        {q.claims_waiting_on_assessment !== undefined && (
          <Row label="Claims waiting on an assessment" value={q.claims_waiting_on_assessment.toLocaleString("en-IN")} />
        )}
        {t && (
          <>
            <Row label="Assessments finished (last hour)" value={(t.done_last_hour + t.failed_last_hour).toLocaleString("en-IN")} />
            <Row label="Of which failed" value={t.failed_last_hour} warn={t.failed_last_hour > 0} />
            <Row label="Run time, median / 95th" value={`${duration(t.run_p50_ms / 1000)} / ${duration(t.run_p95_ms / 1000)}`} />
            <Row label="Per day at this rate" value={t.per_day_at_this_rate.toLocaleString("en-IN")} />
          </>
        )}
      </div>
    </Card>
  );
}

function duration(sec: number): string {
  if (sec < 60) return `${sec.toFixed(0)} s`;
  if (sec < 3600) return `${Math.floor(sec / 60)} m ${Math.floor(sec % 60)} s`;
  return `${Math.floor(sec / 3600)} h ${Math.floor((sec % 3600) / 60)} m`;
}

function RuntimeCard({ rt }: { rt: PlatformHealth["runtime"] }) {
  return (
    <Card>
      <CardHeader title="API process" subtitle={rt.go_version} />
      <div className="divide-y divide-border">
        <Row label="Memory in use" value={bytes(rt.heap_in_use)} />
        <Row label="Memory from OS" value={bytes(rt.memory_from_os)} />
        <Row label="Goroutines" value={rt.goroutines} />
        <Row label="CPUs" value={<span className="inline-flex items-center gap-1"><Cpu className="size-3.5" />{rt.cpus}</span>} />
        <Row label="GC cycles" value={rt.gc_cycles.toLocaleString("en-IN")} />
      </div>
    </Card>
  );
}

function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const u = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${u[i]}`;
}

function uptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}
