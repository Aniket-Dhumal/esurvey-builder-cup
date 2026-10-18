"use client";

import Link from "next/link";
import { EventPicker } from "@/components/EventPicker";
import { FarmerPicker } from "@/components/FarmerPicker";
import { ParcelPicker } from "@/components/ParcelPicker";
import { useEffect, useState } from "react";
import { Loader2, Plus } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import type { ReportSummary } from "@/lib/types";
import { usePagedResource } from "@/lib/usePagedResource";
import { CloseReport } from "@/components/CloseReport";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
import type { SurveyEvent } from "@/lib/types";
import {
  calamityLabel,
  formatDate,
  ha,
  reportStateStyles,
} from "@/lib/format";
import {
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  Skeleton,
  StateBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/dashboard";
import { PrintReport } from "@/components/PrintReport";
import { useSession } from "@/components/SessionProvider";
import { NativeSelect } from "@/components/ui/native-select";

export default function ReportsPage() {
  const { can } = useSession();
  const [state, setState] = useState("");
  const [composing, setComposing] = useState(false);

  const table = usePagedResource<ReportSummary>(
    (a) => api.listReports(a, state ? { state } : {}),
    { deps: [state] },
  );
  const { rows: items, error, reload, loading, query } = table;

  return (
    <div>
      <PageHeader
        eyebrow="Claims"
        title="Crop loss reports"
        subtitle="Filed against a district's survey event, verified by the village's allocated authorities."
        actions={
          can("report:create") ? (
            <Button onClick={() => setComposing((v) => !v)}>
              <Plus className="size-4" />
              {composing ? "Cancel" : "File a report"}
            </Button>
          ) : undefined
        }
      />

      {error && (
        <div className="mb-6">
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        </div>
      )}

      {composing && (
        <div className="mb-6">
          <FileReport
            onDone={() => {
              setComposing(false);
              reload();
            }}
          />
        </div>
      )}

      <Card>
        <CardHeader
          title="Reports in your area"
          subtitle="Newest first"
          action={
            <div className="flex flex-wrap items-center justify-end gap-2">
              <TableSearch
                value={table.query}
                onChange={table.setQuery}
                busy={table.searching}
                placeholder="Reference, farmer, village or mobile"
              />
              <div className="w-40 shrink-0">
                <NativeSelect
                  selectSize="lg"
                  aria-label="Filter by state"
                  value={state}
                  onChange={(e) => setState(e.target.value)}>
                  <option value="">All states</option>
                  {Object.entries(reportStateStyles).map(([k, v]) => (
                    <option key={k} value={k}>{v.label}</option>
                  ))}
                </NativeSelect>
              </div>
            </div>
          }
        />

        {loading && (
          <div className="space-y-2 p-5">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
        )}

        {/* An empty table and an empty search result are different facts,
            and only the second one has an obvious next action. */}
        {!loading && items.length === 0 && query !== "" && (
          <NoMatches query={query} noun="reports" />
        )}

        {!loading && items.length === 0 && query === "" && (
          <EmptyState
            title="No reports"
            body="Nothing has been filed in the villages you are posted to."
          />
        )}

        {!loading && items.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                {/* Units in the header, not repeated down four hundred rows.
                    "Sown 1.6" on its own does not say hectares, and a reader
                    who assumes acres is out by a factor of two and a half on
                    every compensation figure that follows from it. */}
                <TableHead>Reference</TableHead>
                <TableHead>Farmer</TableHead>
                <TableHead>Village</TableHead>
                <TableHead>Cause</TableHead>
                <TableHead numeric>Sown (ha)</TableHead>
                {/* The farmer's declaration, not an assessment. */}
                <TableHead numeric>Affected (ha)</TableHead>
                <TableHead numeric>Damage, declared</TableHead>
                <TableHead>State</TableHead>
                <TableHead>
                  <span className="sr-only">Print</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((r) => (
                <TableRow key={r.id}>
                  <TableCell>
                    <Link href={`/verify/?id=${r.id}`} className="mono font-medium hover:text-primary">
                      {r.reference}
                    </Link>
                    {/* A badge, not loose grey text under the reference. It
                        changes how the report is adjudicated, so it has to
                        look like a fact about the row rather than a caption
                        that slipped out of the cell above. */}
                    {r.filed_late && (
                      <span className="mt-0.5 block w-fit rounded bg-sev-moderate-bg px-1.5 text-[11px] font-medium text-sev-moderate">
                        Late
                      </span>
                    )}
                  </TableCell>
                  {/* Wraps, rather than truncating. A name that runs onto a
                      second line costs a few pixels of row height; a name cut
                      to "Harichndra Yeda…" costs the reader the record. */}
                  <TableCell className="max-w-[200px] whitespace-normal font-medium">
                    {r.farmer_name}
                  </TableCell>
                  <TableCell className="max-w-[130px] whitespace-normal text-muted-foreground">
                    {r.village_name}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {calamityLabel(r.calamity_type)}
                  </TableCell>
                  <TableCell numeric>{ha(r.area_sown_ha)}</TableCell>
                  <TableCell numeric>{ha(r.area_affected_ha)}</TableCell>
                  <TableCell numeric className="font-medium">
                    {r.damage_percent.toFixed(1)}%
                  </TableCell>
                  <TableCell>
                    <StateBadge styles={reportStateStyles} state={r.state} />
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex items-center justify-end gap-2">
                      {/* Closing a decided claim. The state machine has always
                          allowed verified and adjudicated to close, and
                          nothing anywhere ever did it -- so every claim this
                          platform has decided is still sitting open, and the
                          farmer's progress screen shows a last step that
                          cannot be reached. */}
                      <CloseReport
                        report={r}
                        onDone={() => void table.reload()}
                      />
                      <PrintReport reportId={r.id} reference={r.reference} />
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        <TablePager resource={table} noun="reports" />
      </Card>

      <p className="mt-3 text-xs leading-relaxed text-faint">
        The damage column is weighted by affected area, not a mean of the crop
        lines — a total loss on a tenth of a hectare and a scratch on four
        hectares do not average to half.
      </p>
    </div>
  );
}

/* --------------------------------------------------------------------- */

function FileReport({ onDone }: { onDone: () => void }) {
  const [event, setEvent] = useState<SurveyEvent | null>(null);
  const [farmerId, setFarmerId] = useState("");
  const [category, setCategory] = useState("standing_crop");
  const [calamity, setCalamity] = useState("hailstorm");
  const [occurred, setOccurred] = useState("");
  const [season, setSeason] = useState("kharif");
  const [year, setYear] = useState(String(new Date().getFullYear()));
  const [surveyNumber, setSurveyNumber] = useState("");
  const [farmId, setFarmId] = useState("");
  const [crop, setCrop] = useState("");
  const [sown, setSown] = useState("");
  const [affected, setAffected] = useState("");
  const [damage, setDamage] = useState("");
  const [signature, setSignature] = useState("thumb");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [calamities, setCalamities] = useState<Record<string, string[]>>({});

  useEffect(() => {
    api.reportForm().then((f) => {
      const m: Record<string, string[]> = {};
      for (const c of f.loss_categories) m[c.code] = c.calamities;
      setCalamities(m);
    }).catch(() => {});
  }, []);

  // The form's own rule: a cyclone is only in scope once the crop is cut.
  //
  // Resolved during render rather than corrected by an effect. An effect
  // leaves one frame in which the selected calamity is not in scope for the
  // chosen category, and a farmer who submits in that frame files a report
  // the form's own rule says cannot exist.
  const allowed = calamities[category] ?? [];
  const chosenCalamity =
    allowed.length && !allowed.includes(calamity) ? allowed[0] : calamity;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createReport({
        survey_event_id: event?.id ?? "",
        farmer_id: farmerId,
        season,
        crop_year: year,
        loss_category: category,
        calamity_type: chosenCalamity,
        occurred_at: new Date(occurred).toISOString(),
        declaration_accepted: true,
        farmer_signature_kind: signature,
        documents_promised: ["7_12"],
        items: [
          {
            survey_number: surveyNumber,
            farm_id: farmId || undefined,
            crop_name: crop,
            area_sown_ha: Number(sown),
            area_affected_ha: Number(affected),
            damage_percent: Number(damage),
          },
        ],
      });
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader
        title="File a crop loss report"
        subtitle="Recorded at the counter, on the farmer's declaration."
      />
      <form onSubmit={submit} className="space-y-5 px-4 py-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="event">Survey event</Label>
            {/* The same picker the assessment form asks first. Both are work
                against a declared calamity, and both used to ask for it their
                own way. */}
            <EventPicker
              id="event"
              value={event?.id ?? ""}
              onChange={(e) => {
                setEvent(e);
                // The window moved; a time chosen under the old one is no
                // longer a time inside this calamity.
                setOccurred("");
              }}
              emptyNote="No survey event is open in your area. A report can only be filed against one — that is what stops retrospective claiming."
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="farmer">Farmer</Label>
            {/* The same picker the assessment form uses. An eleven-digit id
                typed by hand is a claim filed against whatever land that id
                turns out to name, and the officer has no way to notice. */}
            <FarmerPicker
              id="farmer"
              value={farmerId}
              onChange={(id) => {
                setFarmerId(id);
                setFarmId("");
                setSurveyNumber("");
                setCrop("");
                setSown("");
              }}
            />
            <p className="text-xs text-faint">
              Name and handset are taken from the registry, not typed here.
            </p>
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-4">
          <div className="space-y-1.5">
            <Label htmlFor="cat">Loss category</Label>
            <NativeSelect selectSize="lg" id="cat" value={category} onChange={(e) => setCategory(e.target.value)}
              className="w-full">
              <option value="standing_crop">Standing crop</option>
              <option value="post_harvest">Post-harvest</option>
            </NativeSelect>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="cal">Cause</Label>
            <NativeSelect selectSize="lg" id="cal" value={chosenCalamity} onChange={(e) => setCalamity(e.target.value)}
              className="w-full">
              {allowed.map((c) => (
                <option key={c} value={c}>{calamityLabel(c)}</option>
              ))}
            </NativeSelect>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="occ">Occurred</Label>
            {/* Inside the declared window, because a loss outside it belongs
                to a different event -- or to none, which is the claim this
                whole gate exists to refuse. */}
            <Input id="occ" type="datetime-local" value={occurred} required
              min={event ? `${event.incident_start.slice(0, 10)}T00:00` : undefined}
              max={event ? `${event.incident_end.slice(0, 10)}T23:59` : undefined}
              disabled={!event}
              onChange={(e) => setOccurred(e.target.value)} />
            <p className="text-xs text-faint">
              {event
                ? `Inside ${formatDate(event.incident_start)} → ${formatDate(event.incident_end)}.`
                : "Choose the survey event first."}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="season">Season / year</Label>
            <div className="flex gap-2">
              <NativeSelect selectSize="lg" id="season" value={season} onChange={(e) => setSeason(e.target.value)}
                className="w-full">
                <option value="kharif">Kharif</option>
                <option value="rabi">Rabi</option>
                <option value="summer">Summer</option>
              </NativeSelect>
              <Input value={year} onChange={(e) => setYear(e.target.value)}
                className="w-20" required />
            </div>
          </div>
        </div>

        <div className="rounded-md border border-border p-4">
          <div className="mb-3 text-xs font-medium text-muted-foreground">
            Affected crop
          </div>
          <div className="mb-4 space-y-1.5">
            <Label htmlFor="parcel">Parcel</Label>
            {/* Picked, not typed, and it fills what the registry already
                knows: the survey number, the crop and the sown area. A claim
                may name a parcel with no boundary -- the farmer walks it later
                -- so unmapped parcels are choosable here, unlike on an
                assessment, which has nothing to run against without one. */}
            <ParcelPicker
              id="parcel"
              farmerId={farmerId}
              value={farmId}
              allowUnmapped
              onChange={(c) => {
                setFarmId(c?.parcel.farm_id ?? "");
                setSurveyNumber(c?.parcel.survey_number ?? "");
                setCrop(c?.crop?.crop_name ?? "");
                const ha = c?.parcel.owner_extent ?? "";
                setSown(ha);
              }}
            />
            <p className="text-xs text-faint">
              The survey number, crop and sown area below come from the
              registry. Correct them where the farmer says otherwise.
            </p>
          </div>

          <div className="grid gap-4 sm:grid-cols-3 lg:grid-cols-6">
            <div className="space-y-1.5">
              <Label htmlFor="sn">Survey no.</Label>
              <Input id="sn" value={surveyNumber} required
                onChange={(e) => setSurveyNumber(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="crop">Crop</Label>
              <Input id="crop" value={crop} required
                onChange={(e) => setCrop(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="sown">Sown (ha)</Label>
              <Input id="sown" type="number" step="0.0001" min="0" value={sown}
                required onChange={(e) => setSown(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="aff">Affected (ha)</Label>
              <Input id="aff" type="number" step="0.0001" min="0" value={affected}
                required onChange={(e) => setAffected(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="dmg">Damage (%)</Label>
              <Input id="dmg" type="number" step="0.01" min="0" max="100"
                value={damage} required onChange={(e) => setDamage(e.target.value)} />
            </div>
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="sig">Farmer&rsquo;s mark</Label>
          <NativeSelect selectSize="lg" id="sig" value={signature} onChange={(e) => setSignature(e.target.value)}>
            <option value="thumb">Thumb impression</option>
            <option value="signature">Signature</option>
          </NativeSelect>
          <p className="text-xs text-faint">
            Recording a mark asserts the farmer accepted the declaration: that
            they will not alter the damaged crop before the inspection.
          </p>
        </div>

        {error && (
          <p role="alert" className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic">
            {error}
          </p>
        )}

        <Button type="submit" disabled={busy || !event}>
          {busy && <Loader2 className="size-4 animate-spin" />}
          File report
        </Button>
      </form>
    </Card>
  );
}
