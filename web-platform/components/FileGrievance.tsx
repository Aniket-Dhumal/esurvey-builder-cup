"use client";

import { useEffect, useState } from "react";

import { api, ApiError } from "@/lib/api";
import type { Grievance, GrievanceGrounds } from "@/lib/types";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";

/**
 * Where a farmer says the decision was wrong.
 *
 * This is the one screen in the system a farmer reaches while already
 * unhappy, and it is reached after a decision they disagree with. Two things
 * follow from that.
 *
 * Everything is in Marathi, including the errors. A refusal in English at
 * this point is a second refusal.
 *
 * And it asks for their words before it asks for a number. The category is
 * what lets a Collector see that thirty appeals in one taluka are about the
 * same thing; the sentence is what an officer actually acts on, and demanding
 * a percentage first would filter out the farmers who know they were treated
 * unfairly without having a figure in mind.
 */
export function FileGrievance({
  reportId,
  reportReference,
  onFiled,
  onCancel,
}: {
  reportId: string;
  reportReference: string;
  onFiled: (g: Grievance) => void;
  onCancel: () => void;
}) {
  const [grounds, setGrounds] = useState<GrievanceGrounds | null>(null);
  const [ground, setGround] = useState("");
  const [statement, setStatement] = useState("");
  const [claimed, setClaimed] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    api
      .grievanceGrounds()
      .then((g) => {
        if (!live) return;
        setGrounds(g);
        if (g.grounds.length) setGround(g.grounds[0].code);
      })
      .catch(() => {
        // The list is served by the same API that will accept the filing, so
        // if it cannot be fetched the filing will fail too. Say so once,
        // rather than showing an empty picker.
        if (live) setError("सध्या हरकत नोंदवता येत नाही. थोड्या वेळाने पहा.");
      });
    return () => {
      live = false;
    };
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);

    if (statement.trim().length < 10) {
      setError("काय चुकले ते एका-दोन वाक्यात लिहा.");
      return;
    }

    setBusy(true);
    try {
      const g = await api.fileGrievance({
        report_id: reportId,
        ground,
        statement: statement.trim(),
        claimed_percent: claimed ? Number(claimed) : undefined,
      });
      onFiled(g);
    } catch (err) {
      const e = err as ApiError;
      setError(
        e.code === "GRIEVANCE_ALREADY_OPEN"
          ? "या सूचनेवर आधीच एक हरकत प्रलंबित आहे. तिचे उत्तर आल्यावर नवीन नोंदवता येईल."
          : e.code === "NOT_APPEALABLE"
            ? "या सूचनेवर अद्याप निर्णय झालेला नाही, त्यामुळे हरकत नोंदवता येणार नाही."
            : e.message,
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={submit}
      className="space-y-4 rounded-xl border border-border p-4">
      <div>
        <h2 className="text-base font-semibold text-foreground">हरकत नोंदवा</h2>
        <p className="mt-0.5 text-sm text-muted-foreground">
          सूचना <span className="mono text-xs">{reportReference}</span> वरील
          निर्णयाबद्दल
        </p>
      </div>

      <div className="space-y-1.5">
        <label htmlFor="ground" className="text-sm font-medium text-foreground">
          काय चुकले?
        </label>
        <NativeSelect selectSize="field" id="ground" value={ground} onChange={(e) => setGround(e.target.value)}
          className="w-full">
          {(grounds?.grounds ?? []).map((g) => (
            <option key={g.code} value={g.code}>
              {g.label}
            </option>
          ))}
        </NativeSelect>
      </div>

      <div className="space-y-1.5">
        <label htmlFor="statement" className="text-sm font-medium text-foreground">
          तुमच्या शब्दांत सांगा
        </label>
        <Textarea textareaSize="field" id="statement" rows={4} value={statement} onChange={(e) => setStatement(e.target.value)}
          placeholder="उदा. माझ्या ४५३ गट क्रमांकाची पाहणी झालीच नाही."
          className="w-full"
        />
        <p className="text-xs text-muted-foreground">
          अधिकारी हेच वाचून निर्णय घेतील.
        </p>
      </div>

      <div className="space-y-1.5">
        <label htmlFor="claimed" className="text-sm font-medium text-foreground">
          तुमच्या मते नुकसान किती? <span className="text-faint">(ऐच्छिक)</span>
        </label>
        <Input inputSize="field" id="claimed"
          type="number"
          inputMode="decimal"
          min={0}
          max={100}
          value={claimed}
          onChange={(e) => setClaimed(e.target.value)}
 
          placeholder="%" />
        {/* Optional on purpose: a farmer may know they were treated unfairly
            without having a number in mind, and requiring one would filter
            out the honest ones. */}
        <p className="text-xs text-muted-foreground">
          माहीत नसल्यास रिकामे ठेवा.
        </p>
      </div>

      {grounds && (
        <p className="rounded-md bg-muted px-3 py-2 text-sm text-muted-foreground">
          {grounds.sla_days} दिवसांत अधिकारी उत्तर देतील.
        </p>
      )}

      {error && (
        <p
          role="alert"
          className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic">
          {error}
        </p>
      )}

      <div className="flex gap-2">
        <button
          type="submit"
          disabled={busy || !grounds}
          className="h-12 flex-1 rounded-md bg-primary text-base font-medium text-primary-foreground disabled:opacity-50">
          {busy ? "नोंदवत आहे…" : "हरकत नोंदवा"}
        </button>
        <button
          type="button"
          onClick={onCancel}
          className="h-12 rounded-md border border-border px-4 text-base text-muted-foreground">
          रद्द करा
        </button>
      </div>
    </form>
  );
}
