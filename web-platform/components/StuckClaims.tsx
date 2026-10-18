"use client";

import { useCallback, useEffect, useState } from "react";
import { TriangleAlert } from "lucide-react";

import { api } from "@/lib/api";
import type { StuckVillage } from "@/lib/types";
import { istDateTime } from "@/lib/format";
import { Card, CardHeader } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { useSession } from "@/components/SessionProvider";

/**
 * Villages whose claims have stopped, and why.
 *
 * # The failure this exists to surface
 *
 * A claim now advances itself: received, assessed, then allocated to the
 * village's Talathi and Krushi Sahayak. Allocation is the one step that can
 * fail for a reason nobody has looked at — a village with nobody posted to it.
 *
 * When it does, the claim stops silently. The farmer's app says it is with the
 * office. No officer's queue shows it, because it never reached a queue. The
 * only record is a sentence on a row nobody is reading. That is precisely the
 * failure the pipeline was built to end, reappearing one step further along.
 *
 * # Why it lives on the officer accounts page
 *
 * Because the fix is on that page. A District reading "Barshi Kh. — 412 claims
 * waiting, no Krushi Sahayak posted" can post one in the form below it. On a
 * dashboard it would be a number somebody notes and does not act on.
 *
 * Nothing is drawn when nothing is stuck.
 */
export function StuckClaims() {
  // Releasing a village's claims is report:review; reading the list is
  // report:read. A State Officer has the second and not the first.
  const { can } = useSession();
  const canRelease = can("report:review");

  const [villages, setVillages] = useState<StuckVillage[]>([]);
  const [releasing, setReleasing] = useState<string | null>(null);

  const load = useCallback(() => {
    api
      .stuckClaims()
      .then((r) => setVillages(r.villages ?? []))
      // Silent: this is a warning panel, and a warning panel that fails loudly
      // pushes the page it is warning about off the screen.
      .catch(() => undefined);
  }, []);

  useEffect(load, [load]);

  /// Having just posted the officer these claims were waiting for.
  async function release(code: string) {
    setReleasing(code);
    try {
      await api.retryVillage(code);
      load();
    } catch {
      // Nothing to say: the pipeline reaches them on its own within the half
      // hour, so a failure here costs time and not work.
    } finally {
      setReleasing(null);
    }
  }

  if (villages.length === 0) return null;

  const claims = villages.reduce((n, v) => n + v.claims, 0);

  return (
    <Card className="border-sev-severe/40">
      <CardHeader
        title="Claims that have stopped"
        subtitle={
          `${claims} claim${claims === 1 ? "" : "s"} in ` +
          `${villages.length} village${villages.length === 1 ? "" : "s"} ` +
          "cannot go to an officer. Posting one is enough to release them."
        }
      />

      <ul className="divide-y divide-border">
        {villages.map((v) => (
          <li
            key={v.village_lgd_code}
            className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-4 py-3"
          >
            <TriangleAlert className="size-4 shrink-0 self-center text-sev-severe" />
            <span className="font-medium">{v.village_name}</span>
            <span className="mono text-xs text-faint">
              {v.village_lgd_code}
            </span>
            <span className="text-sm tabular-nums text-sev-severe">
              {v.claims} waiting
            </span>
            <span className="text-xs text-muted-foreground">
              oldest {istDateTime(v.since)}
            </span>
            <span className="w-full text-xs leading-relaxed text-muted-foreground">
              {v.reason}
            </span>
            {/* The pipeline gets there on its own within half an hour. This is
                the difference between posting an officer and watching it
                work.

                Only for somebody who may release a claim: reading the list is
                report:read, which a State Officer holds as oversight, and
                retrying is report:review, which they do not. The list is worth
                showing them -- it is exactly the staffing gap a state cares
                about -- and the button was an invitation to a refusal. */}
            {canRelease && (
              <Button
                size="sm"
                variant="outline"
                className="mt-1"
                disabled={releasing === v.village_lgd_code}
                onClick={() => release(v.village_lgd_code)}
              >
                {releasing === v.village_lgd_code
                  ? "Releasing…"
                  : "I have posted one — try these now"}
              </Button>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}
