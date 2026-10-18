"use client";

import { useState } from "react";
import { Loader2, Plus, Settings2 } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import { AreaPicker } from "@/components/AreaPicker";
import { ConfirmTransition } from "@/components/ConfirmTransition";
import { Modal } from "@/components/ui/modal";
import { usePagedResource } from "@/lib/usePagedResource";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
import type { SurveyEvent } from "@/lib/types";
import {
  calamityLabel,
  calamityLabels,
  eventStateStyles,
  formatDate,
} from "@/lib/format";
import {
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  Field,
  Note,
  Skeleton,
  StateBadge,
} from "@/components/ui";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { ManageEvent } from "@/components/ManageEvent";
import { PageHeader } from "@/components/dashboard";
import { useSession } from "@/components/SessionProvider";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * Survey events — the district's calamity notification.
 *
 * Only a District Magistrate sees the compose form, because only they may
 * declare. Everyone else sees the events covering their own area, because the
 * window and the deadline are the terms they are working to.
 */
/**
 * The crops a Collector can name, by AgriStack code.
 *
 * A fixed list rather than free text, because the code is what is compared
 * against a farmer's crop_sown_data. A typed crop name would match nothing and
 * would fail silently, flagging every farmer in the event as a mismatch.
 */
const CROPS: { code: string; name: string }[] = [
  { code: "0201001", name: "Soybean" },
  { code: "0301002", name: "Cotton" },
  { code: "0101001", name: "Rice" },
  { code: "0102001", name: "Wheat" },
  { code: "0103003", name: "Jowar" },
  { code: "0103005", name: "Bajra" },
  { code: "0104002", name: "Maize" },
  { code: "0401003", name: "Tur" },
  { code: "0402001", name: "Gram" },
  { code: "0501001", name: "Sugarcane" },
  { code: "0601002", name: "Onion" },
  { code: "0701004", name: "Grapes" },
];

export default function EventsPage() {
  const { can } = useSession();
  const [composing, setComposing] = useState(false);

  const table = usePagedResource<SurveyEvent>((a) => api.listEvents(a));
  const { rows: events, error, reload, loading, query } = table;

  return (
    <div>
      <PageHeader
        eyebrow="Claims"
        title="Survey events"
        subtitle="A calamity notified by a district, and the talukas or circles it covers."
        actions={
          can("event:create") ? (
            <Button onClick={() => setComposing((v) => !v)}>
              <Plus className="size-4" />
              {composing ? "Cancel" : "Declare an event"}
            </Button>
          ) : undefined
        }
      />

      {error && (
        <div className="mb-6">
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        </div>
      )}

      {/* A dialog, not a panel wedged above the list. Declaring an event is a
          task with a beginning and an end, and doing it in place pushed the
          list an officer was reading half a screen down. */}
      <DeclareEvent
        open={composing}
        onClose={() => setComposing(false)}
        onDone={() => {
          setComposing(false);
          reload();
        }}
      />

      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          {loading
            ? "Loading events…"
            : `${table.total.toLocaleString("en-IN")}${table.totalIsLowerBound ? "+" : ""} event${table.total === 1 ? "" : "s"} in your area`}
        </p>
        <TableSearch
          value={table.query}
          onChange={table.setQuery}
          busy={table.searching}
          placeholder="Reference or district"
        />
      </div>

      {loading && <Skeleton className="h-40 w-full" />}

      {!loading && events.length === 0 && query !== "" && (
        <Card>
          <NoMatches query={query} noun="events" />
        </Card>
      )}

      {!loading && events.length === 0 && query === "" && (
        <Card>
          <EmptyState
            title="No events"
            body={
              can("event:create")
                ? "Declare one to open surveying in your district. A report can only be filed against an open event, for land inside the talukas or circles it names."
                : "No calamity has been notified for your area."
            }
          />
        </Card>
      )}

      <div className="space-y-4">
        {events.map((e) => (
          <EventCard key={e.id} event={e} onChanged={reload} />
        ))}
      </div>

      {/* Events render as cards rather than rows, but they page like every
          other listing -- a district that has notified forty calamities
          should not be a scroll. */}
      <Card className="mt-4">
        <TablePager resource={table} noun="events" className="border-t-0" />
      </Card>
    </div>
  );
}

function EventCard({
  event: e,
  onChanged,
}: {
  event: SurveyEvent;
  onChanged: () => void;
}) {
  const { can } = useSession();
  const [confirming, setConfirming] = useState<string | null>(null);
  const [managing, setManaging] = useState(false);

  // Paused sits alongside open in the lifecycle, so the next step out of both
  // is closing. Resuming is not offered here -- it belongs with pausing, in
  // the manage dialog, where the two read as one control.
  const next =
    e.state === "draft" ? { to: "declared", label: "Declare" }
      : e.state === "declared" ? { to: "open", label: "Open for survey" }
        : e.state === "open" || e.state === "paused"
          ? { to: "closed", label: "Close filing" }
          : null;

  const running = e.state === "open" || e.state === "paused";

  const mayMove =
    next &&
    ((next.to === "declared" || next.to === "open")
      ? can("event:declare")
      : can("event:close"));

  return (
    <Card>
      <CardHeader
        title={e.title}
        subtitle={`${e.reference} · ${e.district_name}`}
        action={<StateBadge styles={eventStateStyles} state={e.state} hint />}
      />
      <div className="px-4 py-4">
        <dl className="grid grid-cols-2 gap-x-6 sm:grid-cols-4">
          <Field label="Cause" value={calamityLabel(e.calamity_type)} />
          <Field
            label="Incident window"
            value={`${formatDate(e.incident_start)} → ${formatDate(e.incident_end)}`}
          />
          <Field label="Filing closes" value={formatDate(e.survey_closes_at)} />
          <Field
            label="Notification"
            value={e.notification_ref || "—"}
            mono
          />
        </dl>

        <div className="mt-4">
          <div className="text-xs font-medium text-muted-foreground">
            Areas covered
          </div>
          <div className="mt-1.5 flex flex-wrap gap-2">
            {e.areas.map((a) => (
              <span
                key={a.jurisdiction_id}
                className="rounded-md bg-muted px-2 py-1 text-xs">
                {a.name}
                <span className="ml-1.5 text-faint">
                  {a.level === "circle" ? "circle" : "taluka"} · {a.village_count} villages
                </span>
              </span>
            ))}
          </div>
          <p className="mt-2 text-xs text-faint">
            {e.village_count} villages in total. Only farmers whose land sits in
            one of them may be surveyed against this event.
          </p>
        </div>

        {e.village_count === 0 && (
          <div className="mt-3">
            <Note>
              These areas resolve to no villages, so no farmer could file. That
              usually means the circles named have no villages mapped to them
              yet.
            </Note>
          </div>
        )}

        {(next && mayMove) || running ? (
          <div className="mt-4 flex flex-wrap items-center gap-2">
            {/* Confirmed, never fired on the first click. Closing filing stops
                every farmer in the district filing against this event, and
                the button used to do it with nothing in between. */}
            {next && mayMove && (
              <Button
                size="sm"
                variant={next.to === "closed" ? "destructive" : "default"}
                onClick={() => setConfirming(next.to)}>
                {next.label}
              </Button>
            )}

            {/* Pausing, resuming and changing coverage. Behind one button
                because they are the same job -- adjusting an event that is
                already running -- and because three more buttons on every
                card would bury the one that ends filing. */}
            {running && (can("event:create") || can("event:close")) && (
              <Button size="sm" variant="outline" onClick={() => setManaging(true)}>
                <Settings2 className="size-4" />
                Manage
              </Button>
            )}
          </div>
        ) : null}
      </div>

      {managing && (
        <ManageEvent
          event={e}
          open={managing}
          onClose={() => setManaging(false)}
          onChanged={onChanged}
        />
      )}

      {confirming && (
        <ConfirmTransition
          event={e}
          to={confirming}
          onClose={() => setConfirming(null)}
          onDone={() => {
            setConfirming(null);
            onChanged();
          }}
        />
      )}
    </Card>
  );
}

function DeclareEvent({
  open,
  onClose,
  onDone,
}: {
  open: boolean;
  onClose: () => void;
  onDone: () => void;
}) {
  const [calamity, setCalamity] = useState("hailstorm");
  const [title, setTitle] = useState("");
  const [notificationRef, setNotificationRef] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [closes, setCloses] = useState("");
  const [areas, setAreas] = useState<string[]>([]);
  const [description, setDescription] = useState("");
  const [crops, setCrops] = useState<string[]>([]);

  // The district is the Collector's own posting -- the form never asks for it,
  // because an event is declared for the district you hold and no other.
  const { user } = useSession();
  const districtId =
    user?.grants?.find((g) => g.jurisdiction_level === "district")?.jurisdiction_id ?? "";
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(ev: React.FormEvent) {
    ev.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createEvent({
        calamity_type: calamity,
        title,
        description: description || undefined,
        notification_ref: notificationRef || undefined,
        incident_start: start,
        incident_end: end,
        survey_closes_at: new Date(closes).toISOString(),
        area_codes: areas,
        crops: crops.map((code) => ({
          crop_code: code,
          crop_name: CROPS.find((c) => c.code === code)?.name ?? code,
        })),
      });
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title="Declare a survey event"
      description="It is created as a draft. Declaring is a separate act.">
      <form id="declare-event" onSubmit={submit} className="space-y-5">
        <Note>
          The district is taken from your own posting — you declare for your
          district and no other. Areas are talukas or circles inside it, by
          code. A village cannot be an area: a hailstorm across a taluka is not
          a hundred-row form.
        </Note>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="title">Title</Label>
            <Input
              inputSize="lg"
              id="title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Unseasonal hailstorm, Barshi and Solapur North"
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="calamity">Cause</Label>
            <NativeSelect selectSize="lg" id="calamity" value={calamity} onChange={(e) => setCalamity(e.target.value)}
              className="w-full">
              {Object.entries(calamityLabels).map(([code, l]) => (
                <option key={code} value={code}>
                  {l.en} — {l.mr}
                </option>
              ))}
            </NativeSelect>
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-3">
          <div className="space-y-1.5">
            <Label htmlFor="start">Incident from</Label>
            <Input inputSize="lg" id="start" type="date" value={start}
              onChange={(e) => setStart(e.target.value)} required />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="end">Incident to</Label>
            <Input inputSize="lg" id="end" type="date" value={end}
              onChange={(e) => setEnd(e.target.value)} required />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="closes">Filing closes</Label>
            <Input inputSize="lg" id="closes" type="datetime-local" value={closes}
              onChange={(e) => setCloses(e.target.value)} required />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label>Talukas and circles covered</Label>
          {/*
            Replaces a box that asked for "4246, 4247" from memory. A mistyped
            LGD code is almost always another real place, so the old field
            could declare a calamity over the wrong taluka with nothing looking
            wrong until a farmer there could not file.

            Ticking a taluka covers everything beneath it, because coverage
            resolves through the tree. Opening one and unticking a circle is
            the case that cannot be said that way, and there the selection
            becomes the remaining circles by name.
          */}
          <AreaPicker districtId={districtId} value={areas} onChange={setAreas} />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="notif">District order / GR reference</Label>
            <Input
              inputSize="lg"
              id="notif"
              value={notificationRef}
              onChange={(e) => setNotificationRef(e.target.value)}
              placeholder="SOL/DM/2026/HAIL/17"
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="desc">Description</Label>
          <Textarea id="desc" rows={2} value={description}
            onChange={(e) => setDescription(e.target.value)} />
        </div>

        {/*
          Which crops this event covers.
          
          Selecting none covers all of them, and that is stated rather than
          left to be inferred: a Collector declaring a cyclone across four
          talukas has no reason to enumerate what was planted, and a form that
          silently required a choice would make them guess.

          A mismatch against a farmer's registry crop never refuses the
          survey. AgriStack returns an empty crop record for most farmers, so
          refusing would lock out the genuine claimant far more often than it
          would catch anybody; it is flagged for the village pair instead.
        */}
        <div className="space-y-1.5">
          <Label>Crops covered</Label>
          <div className="flex flex-wrap gap-2">
            {CROPS.map((c) => {
              const on = crops.includes(c.code);
              return (
                <button
                  key={c.code}
                  type="button"
                  aria-pressed={on}
                  onClick={() =>
                    setCrops((prev) =>
                      prev.includes(c.code)
                        ? prev.filter((x) => x !== c.code)
                        : [...prev, c.code],
                    )
                  }
                  className={
                    "inline-flex h-9 items-center rounded-full border px-3.5 " +
                    "text-xs font-medium transition-colors " +
                    (on
                      ? "border-primary bg-primary text-primary-foreground"
                      : "border-border text-muted-foreground hover:border-primary/50")
                  }>
                  {c.name}
                </button>
              );
            })}
          </div>
          <p className="text-xs text-muted-foreground">
            {crops.length === 0
              ? "Nothing selected, so the event covers every crop."
              : `Covers ${crops.length} crop${crops.length === 1 ? "" : "s"}. A farmer whose registry crop is not among them can still file; the village officers are shown the discrepancy.`}
          </p>
        </div>


        {error && (
          <p role="alert" className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic">
            {error}
          </p>
        )}

      </form>

      {/* The actions live on the dialog's footer rather than at the end of a
          scrolling form, so they stay reachable however long the crop list
          gets. The submit is bound back to the form by id.

          The old codes box carried `required`, so the browser refused an empty
          submit. A picker has no such affordance, and a server refusal after a
          round trip is a worse way to learn it. */}
      <div className="mt-6 flex items-center justify-end gap-3 border-t border-border pt-4">
        <Button size="lg" type="button" variant="ghost" onClick={onClose} disabled={busy}>
          Cancel
        </Button>
        <Button size="lg"
     type="submit"
     form="declare-event"
     disabled={busy || areas.length === 0}>
          {busy && <Loader2 className="size-4 animate-spin" />}
          Create draft
        </Button>
      </div>
    </Modal>
  );
}
