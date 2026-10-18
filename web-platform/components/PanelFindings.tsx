"use client";

import type { SpecialistFinding } from "@/lib/types";
import { Card, CardHeader } from "@/components/ui";

/**
 * What the assessment panel actually examined.
 *
 * The conclusion at the top of this page is a synthesis of four narrow
 * opinions. An officer who has to defend a decision needs the opinions, not
 * just the synthesis — and above all needs to see which of them is *missing*,
 * because a confident-looking assessment made without ground photographs or
 * without the weather record is a different thing from one made with them.
 *
 * So a member that did not run is rendered as prominently as one that did.
 * It is the more useful row.
 */

const LABELS: Record<string, { name: string; asks: string }> = {
  weather_specialist: {
    name: "Weather",
    asks: "Did the area actually experience the reported calamity?",
  },
  phenology_specialist: {
    name: "Growth stage",
    asks: "Would damage at this stage look like the claim?",
  },
  vision_specialist: {
    name: "Field photographs",
    asks: "What do the images show? The only member that may state a figure.",
  },
  satellite_specialist: {
    name: "Vegetation history",
    asks: "Did a crop grow here, and does the curve match the declaration?",
  },
};

// "Supports" is not scored as good news. A member agreeing with the claim is
// a finding, not an approval, and colouring it green would quietly turn this
// panel into a recommendation. Only "contradicts" is marked, because that is
// the one an officer must not miss.
const VERDICTS: Record<string, { label: string; tone: string }> = {
  supports: { label: "Supports", tone: "text-foreground" },
  contradicts: { label: "Contradicts", tone: "text-sev-severe" },
  inconclusive: { label: "Inconclusive", tone: "text-muted-foreground" },
  not_assessed: { label: "Not assessed", tone: "text-faint" },
};

function seconds(ms: number): string {
  if (!ms) return "—";
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

function Member({ f }: { f: SpecialistFinding }) {
  const meta = LABELS[f.agent] ?? { name: f.agent, asks: "" };
  const verdict = f.verdict ? VERDICTS[f.verdict] : undefined;
  const limitations = f.limitations ?? [];

  return (
    <li className="px-4 py-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h4 className="text-sm font-medium text-foreground">{meta.name}</h4>
        {f.ran ? (
          <span className="flex items-baseline gap-2 text-xs">
            {verdict && (
              <span className={`font-medium ${verdict.tone}`}>
                {verdict.label}
              </span>
            )}
            {typeof f.confidence === "number" && (
              <span className="mono tabular-nums text-faint">
                {f.confidence.toFixed(2)}
              </span>
            )}
            <span className="mono text-faint">{seconds(f.duration_ms)}</span>
          </span>
        ) : (
          <span className="text-xs font-medium text-sev-moderate">
            Did not run
          </span>
        )}
      </div>

      {meta.asks && (
        <p className="mt-0.5 text-xs text-faint">{meta.asks}</p>
      )}

      <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
        {f.ran ? f.reasoning || "—" : f.error || "No reason was recorded."}
      </p>

      {limitations.length > 0 && (
        <ul className="mt-2 space-y-1">
          {limitations.map((l, i) => (
            <li key={i} className="text-xs leading-relaxed text-faint">
              · {l}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

export function PanelFindings({
  specialists,
  degraded,
  degradedReason,
}: {
  specialists: SpecialistFinding[] | null;
  degraded?: boolean;
  degradedReason?: string;
}) {
  const members = specialists ?? [];
  if (members.length === 0) return null;

  // Missing members first: the gap is what changes how the conclusion above
  // should be read, so it should not be scrolled to.
  const ordered = [...members].sort(
    (a, b) => Number(a.ran) - Number(b.ran),
  );
  const ran = members.filter((m) => m.ran).length;

  return (
    <Card>
      <CardHeader
        title="Panel findings"
        subtitle={`${ran} of ${members.length} members reported`}
      />

      {degraded && degradedReason && (
        <p className="border-b bg-sev-moderate/5 px-4 py-3 text-sm leading-relaxed text-muted-foreground">
          This assessment was made short-handed — {degradedReason}. Weigh the
          conclusion accordingly.
        </p>
      )}

      <ul className="divide-y divide-border">
        {ordered.map((f) => (
          <Member key={f.agent} f={f} />
        ))}
      </ul>
    </Card>
  );
}
