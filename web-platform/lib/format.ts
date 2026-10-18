import type {
  CropPresence,
  FraudSeverity,
  Severity,
  TriageTier,
} from "./types";

export function formatDate(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-IN", {
    day: "2-digit",
    month: "short",
    year: "numeric",
  });
}

export function formatDateTime(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("en-IN", {
    day: "2-digit",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Days between two YYYY-MM-DD dates. */
export function daysBetween(a: string, b: string): number | null {
  const ta = Date.parse(a);
  const tb = Date.parse(b);
  if (Number.isNaN(ta) || Number.isNaN(tb)) return null;
  return Math.round((tb - ta) / 86_400_000);
}

export function pct(v: number, digits = 1): string {
  return `${v.toFixed(digits)}%`;
}

/**
 * A damage figure, or a dash.
 *
 * Null is the common case, not an error: satellite evidence alone cannot
 * measure damage, so a figure only exists once a field photograph backed one.
 * Never substitute a zero — "no measurement" and "no loss" are opposite
 * findings.
 */
export function damageFigure(v: number | null, digits = 1): string {
  return v === null ? "—" : `${v.toFixed(digits)}%`;
}

export const severityStyles: Record<
  Severity,
  { text: string; bg: string; label: string }
> = {
  Negligible: {
    text: "text-sev-negligible",
    bg: "bg-sev-negligible-bg",
    label: "Negligible",
  },
  Moderate: {
    text: "text-sev-moderate",
    bg: "bg-sev-moderate-bg",
    label: "Moderate",
  },
  Severe: {
    text: "text-sev-severe",
    bg: "bg-sev-severe-bg",
    label: "Severe",
  },
  Catastrophic: {
    text: "text-sev-catastrophic",
    bg: "bg-sev-catastrophic-bg",
    label: "Catastrophic",
  },
};

export const triageStyles: Record<
  TriageTier,
  { text: string; bg: string; label: string; hint: string }
> = {
  // Nothing routes here today. Retained because the tier is part of the
  // specification's audit framework, and a claim with a field survey behind it
  // could earn it once the survey app exists.
  GREEN_FAST_TRACK: {
    text: "text-sev-negligible",
    bg: "bg-sev-negligible-bg",
    label: "Fast track",
    hint: "Ground-verified and internally consistent.",
  },
  AMBER_STANDARD_AUDIT: {
    text: "text-sev-moderate",
    bg: "bg-sev-moderate-bg",
    label: "Standard audit",
    hint: "Routine blind dual-officer review. Every claim reaches at least this tier — without a field visit, none is safe to approve unreviewed.",
  },
  RED_ANOMALY_ESCALATE: {
    text: "text-sev-catastrophic",
    bg: "bg-sev-catastrophic-bg",
    label: "Escalate",
    hint: "A fraud indicator fired, or the evidence contradicts the claim. Senior review required.",
  },
};

/**
 * Where a confidence figure sits against the specification's thresholds.
 * Deliberately blunt: an officer should not have to remember that 0.85 is the
 * fast-track line.
 */
export function confidenceLabel(c: number): string {
  if (c >= 0.85) return "High";
  if (c >= 0.65) return "Moderate";
  if (c >= 0.45) return "Low";
  return "Very low";
}

/** Aadhaar is only ever shown as its last four digits. */
export function maskedAadhaar(last4: string): string {
  return `XXXX XXXX ${last4 || "····"}`;
}

export const cropPresenceStyles: Record<
  CropPresence,
  { text: string; bg: string; label: string; hint: string }
> = {
  confirmed: {
    text: "text-sev-negligible",
    bg: "bg-sev-negligible-bg",
    label: "Crop confirmed",
    hint: "The vegetation curve is consistent with a crop having grown on this parcel.",
  },
  absent: {
    text: "text-sev-catastrophic",
    bg: "bg-sev-catastrophic-bg",
    label: "No crop found",
    hint: "The parcel stayed at bare soil through the declared season. A declared crop that never grew is a fraud signal, not a loss.",
  },
  inconclusive: {
    text: "text-sev-moderate",
    bg: "bg-sev-moderate-bg",
    label: "Inconclusive",
    hint: "Too few cloud-free passes, or the readings contradict each other.",
  },
};

export const fraudStyles: Record<
  FraudSeverity,
  { text: string; bg: string; label: string }
> = {
  info: { text: "text-muted-foreground", bg: "bg-muted", label: "Note" },
  warning: {
    text: "text-sev-moderate",
    bg: "bg-sev-moderate-bg",
    label: "Warning",
  },
  critical: {
    text: "text-sev-catastrophic",
    bg: "bg-sev-catastrophic-bg",
    label: "Critical",
  },
};

/** Plain-language names for the agent's fraud codes. */
const FRAUD_LABELS: Record<string, string> = {
  no_vegetation_detected: "No vegetation ever detected",
  crop_type_mismatch: "Curve does not match the declared crop",
  sowing_date_mismatch: "Growth does not match the declared sowing date",
  healthy_after_incident: "Crop looks healthy after the incident date",
  area_inconsistent: "Declared area disagrees with the boundary",
};

export function fraudLabel(code: string): string {
  return FRAUD_LABELS[code] ?? code.replace(/_/g, " ");
}

/**
 * Guardrail codes, as the officer needs to read them. A guardrail firing is
 * the system working, so these are worded as facts rather than faults.
 */
const GUARDRAIL_LABELS: Record<string, string> = {
  damage_requires_ground_imagery:
    "A damage figure was withheld: satellite evidence alone cannot measure crop damage, and no field photograph was available.",
  cleared_figure_on_refusal:
    "A figure accompanying a refusal was removed — a refusal must not carry a number out with it.",
  damage_cleared_no_crop_present:
    "A damage figure was withheld because no crop was found on this parcel.",
  no_figure_treated_as_refusal:
    "No figure was produced, so the assessment is recorded as a refusal rather than as zero loss.",
  damage_percent_clamped: "The damage figure was clamped to the 0–100% range.",
  severity_derived_from_damage_percent:
    "Severity was re-derived from the damage figure rather than taken as stated.",
  confidence_capped_no_ground_truth:
    "Confidence was capped at 0.60: nothing without a field visit may present as a confident assessment.",
  confidence_reduced_material_cloud_gap:
    "Confidence was reduced: a long stretch of cloud left the record blind around the incident.",
  presence_downgraded_series_at_bare_soil:
    "A crop-confirmed finding was downgraded — the vegetation series never rose above bare soil.",
  presence_downgraded_series_shows_vegetation:
    "A no-crop finding was downgraded — the vegetation series clearly shows growth.",
};

export function guardrailLabel(code: string): string {
  return GUARDRAIL_LABELS[code] ?? code.replace(/_/g, " ");
}

/* ---------------------------------------------------------------------------
 * Survey events, reports and verification
 *
 * Marathi beside English wherever an officer or a farmer reads the value. The
 * printed form is Marathi-first; the console is bilingual because the same
 * screen is used by a Talathi in a village office and by a district desk that
 * aggregates across talukas.
 * ------------------------------------------------------------------------ */

export const calamityLabels: Record<string, { mr: string; en: string }> = {
  flood: { mr: "पुराचे पाणी शेतात शिरून", en: "Inundation" },
  hailstorm: { mr: "गारपीट", en: "Hailstorm" },
  landslide: { mr: "भूस्खलन", en: "Landslide" },
  cyclone: { mr: "चक्रीवादळ", en: "Cyclone" },
  unseasonal_rain: { mr: "अवकाळी पाऊस", en: "Unseasonal rain" },
  cloudburst: { mr: "ढगफुटी", en: "Cloudburst" },
  drought: { mr: "दुष्काळ", en: "Drought" },
  pest_attack: { mr: "किड रोग प्रादुर्भाव", en: "Pest attack" },
  frost: { mr: "थंडी / हिम", en: "Frost" },
  fire: { mr: "आग", en: "Fire" },
};

export function calamityLabel(code: string): string {
  return calamityLabels[code]?.en ?? code.replace(/_/g, " ");
}

export const lossCategoryLabels: Record<string, { mr: string; en: string }> = {
  standing_crop: { mr: "स्थानिक नैसर्गिक आपत्ती", en: "Standing crop" },
  post_harvest: { mr: "काढणी पश्चात", en: "Post-harvest" },
};

export const seasonLabels: Record<string, string> = {
  kharif: "Kharif",
  rabi: "Rabi",
  summer: "Summer",
};

/** Role slugs as the officers themselves would say them. */
export const roleLabels: Record<string, string> = {
  system_admin: "System administrator",
  state_officer: "State officer",
  district_magistrate: "District Magistrate",
  tehsildar: "Tehsildar",
  mandal_officer: "Circle Officer",
  talathi: "Talathi",
  krushi_sahayak: "Krushi Sahayak",
  farmer: "Farmer",
};

export function roleLabel(role: string): string {
  return roleLabels[role] ?? role.replace(/_/g, " ");
}

export const eventStateStyles: Record<
  string,
  { text: string; bg: string; label: string; hint: string }
> = {
  draft: {
    text: "text-muted-foreground", bg: "bg-muted", label: "Draft",
    hint: "Not yet notified. Areas can still be changed.",
  },
  declared: {
    text: "text-info", bg: "bg-info-bg", label: "Declared",
    hint: "Notified by the Collector. Surveying has not opened.",
  },
  open: {
    text: "text-sev-negligible", bg: "bg-sev-negligible-bg", label: "Open",
    hint: "Farmers in the named areas may file until the deadline.",
  },
  paused: {
    // Yellow, like the other "waiting on somebody" states: paused is a hold,
    // not an ending, and colouring it like Closed would say the opposite.
    text: "text-sev-moderate", bg: "bg-sev-moderate-bg", label: "Paused",
    hint: "Filing is suspended by the district. The deadline has not moved.",
  },
  closed: {
    text: "text-sev-severe", bg: "bg-sev-severe-bg", label: "Closed",
    hint: "The filing deadline has passed. Assessment continues.",
  },
  assessment_complete: {
    text: "text-muted-foreground", bg: "bg-muted", label: "Complete", hint: "",
  },
  cancelled: {
    text: "text-muted-foreground", bg: "bg-muted", label: "Cancelled", hint: "",
  },
};

export const reportStateStyles: Record<
  string,
  { text: string; bg: string; label: string }
> = {
  draft: { text: "text-muted-foreground", bg: "bg-muted", label: "Draft" },

  // Blue, because these four mean the report is moving, not that anything is
  // wrong with it. They were all yellow, so a queue of ordinary work in hand
  // looked like a wall of things needing attention, and the one report that
  // genuinely did was the same colour as the ninety that did not.
  submitted: { text: "text-info", bg: "bg-info-bg", label: "Filed" },
  acknowledged: { text: "text-info", bg: "bg-info-bg", label: "Acknowledged" },
  assigned: { text: "text-info", bg: "bg-info-bg", label: "Assigned" },
  under_verification: { text: "text-info", bg: "bg-info-bg", label: "Being verified" },

  // Yellow here is right: this one is waiting on a person to decide.
  verified: { text: "text-sev-moderate", bg: "bg-sev-moderate-bg", label: "Awaiting decision" },
  adjudicated: { text: "text-sev-negligible", bg: "bg-sev-negligible-bg", label: "Decided" },
  closed: { text: "text-muted-foreground", bg: "bg-muted", label: "Closed" },
  withdrawn: { text: "text-muted-foreground", bg: "bg-muted", label: "Withdrawn" },
};

export const outcomeLabels: Record<string, { label: string; text: string; bg: string }> = {
  confirmed: {
    label: "Confirmed", text: "text-sev-negligible", bg: "bg-sev-negligible-bg",
  },
  partly_confirmed: {
    label: "Partly confirmed", text: "text-sev-moderate", bg: "bg-sev-moderate-bg",
  },
  rejected: {
    label: "No loss found", text: "text-sev-catastrophic", bg: "bg-sev-catastrophic-bg",
  },
  not_traceable: {
    label: "Not traceable", text: "text-sev-severe", bg: "bg-sev-severe-bg",
  },
};

export const routeStyles: Record<
  string,
  { label: string; text: string; bg: string }
> = {
  consensus: {
    label: "Consensus", text: "text-sev-negligible", bg: "bg-sev-negligible-bg",
  },
  arbitration: {
    label: "Arbitration", text: "text-sev-moderate", bg: "bg-sev-moderate-bg",
  },
  escalation: {
    label: "Escalated", text: "text-sev-catastrophic", bg: "bg-sev-catastrophic-bg",
  },
};

/** Hectares without trailing zeros — 1.6, not 1.6000, on a form people read. */
export function ha(v: number | null | undefined): string {
  if (v === null || v === undefined) return "—";
  return `${Number(v.toFixed(4))}`;
}

/** A date and time in IST, which is the clock the incident happened on. */
export function istDateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("en-IN", {
    day: "2-digit", month: "short", year: "numeric",
    hour: "2-digit", minute: "2-digit", timeZone: "Asia/Kolkata",
  });
}

/** How long until a deadline, or how long past it. */
export function dueLabel(iso: string): string {
  const due = new Date(iso).getTime();
  const days = Math.round((due - Date.now()) / 86_400_000);
  if (days < 0) return `${Math.abs(days)}d overdue`;
  if (days === 0) return "due today";
  return `${days}d left`;
}
