/**
 * One page of anything.
 *
 * The shape every listing endpoint returns. `total` is the number of rows
 * that matched, not the number returned — the console previously received the
 * page size under that name and so could never say "of 4,312" or know whether
 * a next page existed.
 */
export interface FarmerSummary {
  farmer_id: string;
  farmer_name: string;
  village_lgd_code: string;
  district_lgd_code: string;
  mobile_no?: string;
  aadhaar_last4?: string;
  parcels: number;
  extent_hectares: number;
  parcels_with_boundary: number;
  crops: number;
}

/** Registry totals for the caller's villages, computed server-side. */
export interface RegistrySummary {
  farmers: number;
  parcels: number;
  parcels_with_boundary: number;
  crops: number;
  extent_hectares: number;
}

/** One parcel with its farmer, joined server-side. */
export interface ParcelRow {
  farm_id: string;
  survey_number: string;
  farmer_id: string;
  farmer_name: string;
  village_lgd_code: string;
  owner_extent_hectares: number;
  has_boundary: boolean;
  crop_name?: string;
  joint_owners: number;
}

/** One appeal against a report's decision. */
export interface Grievance {
  id: string;
  reference: string;
  report_id: string;
  report_reference?: string;
  farmer_id: string;
  farmer_name: string;
  village_lgd_code: string;
  village_name?: string;
  ground: GrievanceGround;
  statement: string;
  claimed_percent?: number;
  /** The figure being appealed, captured when it was filed. */
  disputed_percent?: number;
  disputed_at?: string;
  state: GrievanceState;
  filed_at: string;
  filed_channel: string;
  due_at: string;
  overdue: boolean;
  assigned_to?: string;
  assigned_to_name?: string;
  decided_at?: string;
  decided_by_name?: string;
  decision_reason?: string;
  revised_percent?: number;
}

export type GrievanceGround =
  | "area_wrong"
  | "damage_percent_low"
  | "not_inspected"
  | "evidence_ignored"
  | "other";

export type GrievanceState =
  | "open"
  | "under_review"
  | "upheld"
  | "partly_upheld"
  | "rejected"
  | "withdrawn";

export interface GrievanceGrounds {
  grounds: { code: GrievanceGround; label: string }[];
  sla_days: number;
  sla_message: string;
}

export interface Paged<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
  /**
   * The count stopped at the server's ceiling, so `total` is a floor rather
   * than the answer: there are at least this many matches. Shown as "5,000+",
   * because stating a number the server did not count would be a lie told in
   * small type next to a pager.
   */
  total_is_lower_bound?: boolean;
  has_more: boolean;
  query?: string;
}

/**
 * Mirrors the Go API's JSON. Field names match the wire format exactly so a
 * response can be handed straight to a component without a mapping layer.
 */

export interface Geometry {
  type: "Polygon" | "MultiPolygon";
  /** Nesting depth depends on `type`; narrow before use. */
  coordinates: unknown;
}

export interface JointOwner {
  owner_number: string;
  main_owner_number: string;
  owner_name_ror: string;
  owner_identifier_name: string;
}

export interface Farmer {
  farmer_id: string;
  farmer_name: string;
  aadhaar: string;
  aadhaar_hash: string;
  dob: string;
  gender: string;
  mobile_no: string;
  address: string;
  farmer_category: string;
  caste_category: string;
  identifier_type: string;
  identifier_name: string;
  state_lgd_code: string;
  district_lgd_code: string;
  sub_district_lgd_code: string;
  village_lgd_code: string;
}

export interface Parcel {
  farmer_id: string;
  farm_id: string;
  survey_number: string;
  sub_survey_number: string;
  owner_name_ror: string;
  village_lgd_code: string;
  district_lgd_code: string;
  sub_district_lgd_code: string;
  /** Null on live AgriStack — the registry does not return boundaries today. */
  plot_geometry: Geometry | null;
  ulpin: string | null;
  /** This owner's share, not the polygon area. */
  owner_extent: string;
  area_unit: string;
  khata_number: string | null;
  joint_owners: JointOwner[];
}

export interface CropSown {
  farm_id: string;
  survey_number: string;
  village_lgd_code: string;
  state_lgd_code: string;
  year: string;
  season: string;
  sown_area_integer: number;
  sown_area_decimal: string;
  sown_area_unit: string;
  crop_code: string;
  crop_name: string;
  /** DD/MM/YYYY, as AgriStack returns it. */
  sowing_date: string;
  irrigation_type: string;
  crop_photo: string[];
}

export interface FarmerRecord {
  state_lgd_code: string;
  FarmerData: Farmer;
  land_data: Parcel[];
  crop_sown_data: CropSown[];
}

export interface NdviPoint {
  date: string;
  ndvi: number;
}

export interface SatelliteComponent {
  baseline_ndvi: number;
  baseline_date: string;
  incident_ndvi: number;
  incident_date: string;
  delta_ndvi: number;
  rrvl_percent: number;
  observation_count: number;
  largest_gap_days: number;
  /** Null when there were too few observations to say. */
  vegetation_detected: boolean | null;
}

export interface TerrainComponent {
  mean_slope_degrees: number;
  classification: string;
  waterlogging_risk: string;
}

export type Severity =
  | "Negligible"
  | "Moderate"
  | "Severe"
  | "Catastrophic";

export type TriageTier =
  | "GREEN_FAST_TRACK"
  | "AMBER_STANDARD_AUDIT"
  | "RED_ANOMALY_ESCALATE";

/** What a 10 m optical series can establish about a parcel. Damage cannot. */
export type CropPresence = "confirmed" | "absent" | "inconclusive";

export type FraudSeverity = "info" | "warning" | "critical";

export interface FraudIndicator {
  code: string;
  severity: FraudSeverity;
  detail: string;
}

/**
 * The agent's judgement after the backend's own checks.
 *
 * `damage_percent` is null far more often than not: satellite evidence alone
 * cannot measure damage, so a figure only survives when a field photograph
 * backed it. Render the reasoning, not a zero.
 */
export interface VerifiedAssessment {
  damage_percent: number | null;
  severity: Severity | "";
  confidence: number;
  insufficient_evidence: boolean;
  reasoning: string;

  crop_presence: CropPresence;
  crop_presence_reasoning: string;
  fraud_indicators: FraudIndicator[] | null;

  evidence_cited: string[] | null;
  contradictions: string[] | null;

  model: string;
  prompt_version: string;

  /** Kept apart so a divergence between agent and backend stays visible. */
  agent_guardrails: string[] | null;
  backend_guardrails: string[] | null;

  /**
   * What each member of the assessment panel found, including the ones that
   * could not run.
   *
   * An assessment made without the weather check is a different assessment.
   * An officer reading a confidence of 0.4 needs to see whether it was low
   * because the evidence was weak or because half the panel was blind.
   */
  specialists: SpecialistFinding[] | null;
  /** True when a panel member did not report. */
  degraded?: boolean;
  /** Which members did not report, and why. */
  degraded_reason?: string;
}

/** One panel member's run. */
export interface SpecialistFinding {
  agent: string;
  ran: boolean;
  duration_ms: number;
  error?: string;
  /** How this member's evidence bears on the claim. */
  verdict?: "supports" | "contradicts" | "inconclusive" | "not_assessed";
  confidence?: number;
  reasoning?: string;
  /** What this member could not see. Absent limitations are the dangerous kind. */
  limitations?: string[] | null;
}

export interface SatelliteImage {
  date: string;
  /** A `data:image/png;base64,...` URI, ready for an <img src>. */
  base64: string;
}

export interface AssessmentImages {
  ndvi_mid_date?: SatelliteImage | null;
  ndvi_last_visit?: SatelliteImage | null;
  terrain_slope?: string;
  terrain_aspect?: string;
}

export interface Assessment {
  id: string;
  farmer_id: string;
  farm_id: string;
  survey_number: string;
  sowing_date: string;
  incident_date: string;
  crop_name?: string;
  owner_extent_hectares: number;
  assessment: VerifiedAssessment;
  /** The deterministic figures the agent reasoned over. */
  satellite: SatelliteComponent;
  terrain: TerrainComponent;
  triage_tier: TriageTier;
  /** Every cloud-free observation the score was derived from. */
  ndvi_series: NdviPoint[] | null;
  images?: AssessmentImages;
  /**
   * What the figure was measured against, and what could not be obtained.
   *
   * Loosely typed on purpose: the Go service owns this shape and will add to
   * it, and a screen that threw away a source because this file had not been
   * updated would be the wrong way round. Everything rendered from it is
   * checked before use.
   */
  baseline?: EvidencePacket;
  created_at: string;
}

/** The grounding an assessment stood on. */
export interface EvidencePacket {
  denominators?: {
    parcel_history?: {
      verdict?: string;
      this_season_peak?: number;
      mean_prior_peak?: number;
      z_score?: number;
      confidence?: string;
      prior_seasons?: { year: number; peak_ndvi: number }[];
      caveats?: string[];
    };
    neighbourhood?: {
      verdict?: string;
      reason?: string;
      parcel_rrvl_percent?: number;
      neighbourhood_rrvl_percent?: number;
      relative_anomaly_points?: number;
      caveats?: string[];
    };
    threshold_yield?: {
      yield_kg_per_ha?: number;
      basis?: string;
      source?: string;
      unit_name?: string;
    };
  };
  weather?: {
    season?: WeatherWindow;
    sowing?: WeatherWindow;
    incident?: WeatherWindow;
    notes?: string[];
    point?: { resolution_km?: number };
  };
  thermal?: {
    stage?: string;
    stage_en?: string;
    stage_mr?: string;
    accumulated_gdd?: number;
    maturity_gdd?: number;
    percent_of_cycle?: number;
    recoverable?: boolean;
    confidence?: string;
    duration_assumed?: boolean;
    caveats?: string[];
  };
  susceptibility?: {
    level?: string;
    stage?: string;
    recoverable?: boolean;
    reason?: string;
    provenance?: string;
  };
  stress?: {
    heat_days?: number;
    cold_days?: number;
    heat_days_at_flowering?: number;
    hottest_c?: number;
    notes?: string[];
  };
  sowing_check?: {
    declared_date?: string;
    observed_greenup_date?: string;
    difference_days?: number;
    agrees?: boolean;
    confidence?: string;
    note?: string;
  };
  radar?: {
    first_after?: string;
    days_to_first_pass?: number;
    orbits_matched?: boolean;
    delta_vv_db?: number;
    delta_vh_db?: number;
    findings?: {
      code?: string;
      confidence?: string;
      detail?: string;
      caveats?: string[];
    }[];
  };
  radar_passes_in_optical_gap?: number;
  /**
   * Leaf water (Sentinel-2 NDMI) and surface soil moisture (Sentinel-1),
   * each against the field's own prior seasons and the cropland around it.
   * Corroborating only — never a damage figure. See internal/moisture.
   */
  moisture?: MoistureReading;
  claim_history?: {
    signals?: {
      code?: string;
      severity?: string;
      detail?: string;
      innocent_explanation?: string;
    }[];
  };
  field_measurements?: FieldMeasurementSummary;
  absent?: string[];
  elapsed?: string;
}

export interface MoistureReading {
  canopy?: {
    season?: {
      date: string;
      ndmi: number;
      ndvi?: number;
      ring_ndmi?: number;
      clear_fraction: number;
    }[];
    before_ndmi?: number;
    near_incident_ndmi?: number;
    near_incident_passes?: number;
    prior_seasons_ndmi?: number;
    prior_seasons_spread?: number;
    prior_seasons_observed?: number;
    anomaly_vs_prior_seasons?: number;
    gap_vs_neighbours?: number;
    gap_vs_neighbours_before?: number;
  };
  soil?: {
    relative_orbit: number;
    dry_reference_db: number;
    wet_reference_db: number;
    history_passes: number;
    index_near_incident?: number;
    index_date?: string;
    index_before?: number;
    index_before_date?: string;
    canopy_obscured: boolean;
  };
  findings?: {
    code?: string;
    confidence?: string;
    detail?: string;
    caveats?: string[];
  }[];
  caveats?: string[];
  relevance?: string;
  parcel_pixels?: number;
  ring_cropland_pixels?: number;
  missions?: string[];
  synthetic?: boolean;
}

export interface WeatherWindow {
  from?: string;
  to?: string;
  days?: number;
  rain_mm?: number;
  rain_days?: number;
  heavy_rain_days?: number;
  max_daily_rain_mm?: number;
  max_daily_rain_on?: string;
  max_gust_kmh?: number;
  max_gust_on?: string;
  hail_coded_days?: number;
  thunderstorm_days?: number;
  hottest_c?: number;
  coldest_c?: number;
  longest_dry_spell_days?: number;
  longest_dry_spell_from?: string;
  missing_days?: number;
}

export interface FieldMeasurementSummary {
  quadrats?: number;
  total_area_m2?: number;
  mean_stand_loss_percent?: number;
  mean_lodged_percent?: number;
  mean_organ_damage_percent?: number;
  stand_loss_spread_points?: number;
  with_photographs?: number;
  supports_a_figure?: boolean;
  sampling_fraction?: string;
  caveats?: string[];
}

/** One quadrat, as recorded. */
export interface FieldMeasurement {
  id: string;
  line_no?: number;
  recorded_by_name?: string;
  recorded_at: string;
  quadrat_side_m: number;
  plants_expected?: number;
  plants_present?: number;
  plants_lodged?: number;
  organ_sampled?: number;
  organ_damaged?: number;
  soil_type?: string;
  notes?: string;
  capture_id?: string;
  derived: {
    stand_loss_percent?: number;
    lodged_percent?: number;
    organ_damage_percent?: number;
    plants_per_m2?: number;
    working?: string[];
    caveats?: string[];
  };
}

export interface AssessmentSummary {
  id: string;
  farmer_id: string;
  farm_id: string;
  survey_number: string;
  incident_date: string;
  damage_percent: number | null;
  severity: Severity | "";
  confidence_score: number;
  triage_tier: TriageTier;
  crop_presence: CropPresence;
  fraud_count: number;
  critical_fraud: boolean;
  created_at: string;
}

export interface CreateAssessmentInput {
  farmer_id: string;
  farm_id: string;
  incident_date: string;
  sowing_date?: string;

  /**
   * The declared calamity this assessment is for.
   *
   * The form asks for it first, because it is what dates the work. It used to
   * stop there: only the date was sent, and the assessment came back
   * belonging to no event at all.
   */
  survey_event_id?: string;
}

/**
 * A requested assessment, waiting its turn.
 *
 * Producing one is minutes of satellite and model work, and the engine takes a
 * handful at a time. The request records what was asked for and returns; this
 * is what the console polls until `assessment_id` appears.
 */
export interface AssessmentJob {
  id: string;
  farmer_id: string;
  farm_id: string;
  incident_date: string;
  state: "queued" | "running" | "done" | "failed";
  attempts: number;
  assessment_id?: string;
  error?: string;
  created_at: string;
  updated_at: string;
  /** True when an identical request was already queued, and this is it. */
  existing?: boolean;
}

export interface ApiErrorBody {
  error: { code: string; message: string };
}

/* ---------------------------------------------------------------------------
 * Survey events — a district's calamity notification
 * ------------------------------------------------------------------------ */

export type CalamityType =
  | "flood" | "cyclone" | "hailstorm" | "unseasonal_rain" | "drought"
  | "pest_attack" | "landslide" | "cloudburst" | "frost" | "fire";

export type EventState =
  | "draft" | "declared" | "open"
  // Filing suspended without ending it: no new reports, deadline unmoved,
  // and everything already filed keeps moving up the ladder.
  | "paused" | "closed"
  | "assessment_complete" | "cancelled";

export interface EventArea {
  jurisdiction_id: string;
  level: "sub_district" | "circle";
  code: string;
  name: string;
  /** Villages the area resolves to. Zero means the event covers nobody. */
  village_count: number;
}

export interface SurveyEvent {
  id: string;
  reference: string;
  calamity_type: CalamityType;
  title: string;
  description?: string;
  notification_ref?: string;
  district_id: string;
  district_code: string;
  district_name: string;
  incident_start: string;
  incident_end: string;
  survey_opens_at?: string;
  survey_closes_at: string;
  state: EventState;
  declared_by?: string;
  declared_at?: string;
  areas: EventArea[];
  /** Union across areas, not the sum — a circle inside a named taluka is not counted twice. */
  village_count: number;
  created_at: string;
}

/* ---------------------------------------------------------------------------
 * Crop Loss Assessment Report
 * ------------------------------------------------------------------------ */

export type LossCategory = "standing_crop" | "post_harvest";
export type Season = "kharif" | "rabi" | "summer";

export type ReportState =
  | "draft" | "submitted" | "acknowledged" | "assigned"
  | "under_verification" | "verified" | "adjudicated" | "closed" | "withdrawn";

export interface ReportPlace {
  village_lgd_code: string;
  village_name: string;
  /** महसूल मंडळ. Empty where the district's circles are not mapped. */
  circle_code?: string;
  circle_name?: string;
  sub_district_lgd_code: string;
  sub_district_name: string;
  district_lgd_code: string;
  district_name: string;
}

export interface ReportItem {
  line_no: number;
  survey_number: string;
  sub_survey_number?: string;
  farm_id?: string;
  crop_name: string;
  area_sown_ha: number;
  area_affected_ha: number;
  damage_percent: number;
}

export interface CropLossReport {
  id: string;
  reference: string;
  survey_event_id: string;
  survey_event_reference?: string;

  farmer_id: string;
  farmer_name: string;
  farmer_address?: string;
  farmer_mobile?: string;
  farmer_email?: string;

  place: ReportPlace;
  season: Season;
  crop_year: string;

  loss_category: LossCategory;
  calamity_type: CalamityType;
  occurred_at: string;

  reported_at: string;
  reported_by?: string;
  reported_channel: string;

  /** The 48-hour rule is flagged, never enforced. */
  filed_late: boolean;
  late_reason?: string;

  /**
   * When the farmer completed the form on the handset, where the handset said
   * so. `reported_at` above is when the claim reached the office, and the gap
   * is the queue doing its job — the form is filled in a field with no signal
   * and delivered when a network appears, days later in a village that has
   * none. The 48-hour window is measured on this (migration 0040).
   *
   * Absent on a counter filing, and on every claim filed before the app
   * recorded it.
   */
  filed_at?: string;

  /**
   * The claim was completed before the event stopped taking claims and
   * reached the office afterwards, so it was admitted on the handset's
   * filing instant rather than refused. The district closed its books on a
   * count that did not include it (migration 0041).
   */
  arrived_after_close: boolean;

  declaration_accepted: boolean;
  documents_promised: string[];
  farmer_signature_kind?: "signature" | "thumb";
  farmer_signed_at?: string;

  acknowledged_by?: string;
  acknowledged_by_name?: string;
  acknowledged_at?: string;

  /**
   * How much evidence the claim rests on: every photograph and clip filed
   * against it, and how many of the four directed points were actually
   * filmed. An officer deciding a claim could not previously tell a whole
   * walk from a single photograph (docs/EVIDENCE_REVALIDATION.md, V8).
   */
  capture_count: number;
  stations_captured: number;

  state: ReportState;
  items: ReportItem[];
  created_at: string;
}

export interface ReportSummary {
  id: string;
  reference: string;
  farmer_id: string;
  farmer_name: string;
  village_name: string;
  village_lgd_code: string;
  circle_name?: string;
  sub_district_name: string;
  loss_category: LossCategory;
  calamity_type: CalamityType;
  occurred_at: string;
  reported_at: string;
  filed_late: boolean;
  state: ReportState;
  lines: number;
  area_sown_ha: number;
  area_affected_ha: number;
  /** Area-weighted, not a mean of the line percentages. */
  damage_percent: number;

  /**
   * What the ladder is actually carrying: the last standing rung's figure, or
   * the village pair's. `damage_percent` above is the farmer's own
   * declaration, and the two are different claims about the same field.
   */
  assessed_percent?: number;

  /** Which rung of the approval ladder this is waiting at. */
  approval_level?: ApprovalLevel;
  /** The two village officers disagreed. Cannot be bulk approved. */
  contested: boolean;
  /** The registry names a crop this event does not cover. Never a refusal. */
  crop_mismatch: boolean;
  registry_crop_name?: string;
  /** How many times this has been sent back down. */
  returned_count: number;

  /**
   * What the claim rests on: every photograph and clip filed against it, and
   * how many of the four directed points were actually filmed. On the row
   * because an officer approving in a batch never opens the dossier, and a
   * claim shot from one corner used to look exactly like a claim walked to
   * all four points (docs/EVIDENCE_REVALIDATION.md, V8).
   */
  capture_count: number;
  stations_captured: number;

  /**
   * Completed before the event closed, delivered after. On the row because a
   * district that has signed off a total needs to see which figures landed
   * after they did.
   */
  arrived_after_close: boolean;
}

/**
 * Where a claim has got to, and what it is waiting for.
 *
 * Computed, never stored: every fact in it is already recorded somewhere, and
 * a stored copy would be the one that goes stale. The server sends keys rather
 * than sentences so the farmer's handset can render them in Marathi with no
 * signal; this console renders the same keys in its own words.
 */
export interface ClaimProgress {
  reference: string;
  stages: ProgressStage[];

  /**
   * Released only once both village officers have recorded. Absent before
   * that — they record blind to each other, and a figure shown early would
   * travel back to the officer who has not yet reached their own.
   */
  assessed_percent?: number;
  remarks?: ProgressRemark[];

  /** The claim went on without all the evidence the handset promised. */
  evidence_short?: boolean;
  /** What is stopping it. Officers only; never sent to a farmer. */
  stuck?: string;
  /** The two village officers disagreed by more than the threshold. */
  contested?: boolean;
}

export type ProgressStageKey =
  | "received"
  | "checked"
  | "field"
  | "decision"
  | "closed";

/**
 * `attention` is current and *not moving on its own* — a village with nobody
 * posted, evidence that stopped arriving. Rendered differently, so a claim
 * that is stuck does not look like one in progress.
 */
export type ProgressStageState = "done" | "current" | "pending" | "attention";

export interface ProgressStage {
  key: ProgressStageKey;
  state: ProgressStageState;
  at?: string;
  /** Why it is where it is, as a key this console renders. */
  why?: string;
}

export interface ProgressRemark {
  role: string;
  /** The officer's name. Present for officers, omitted for the farmer. */
  by?: string;
  at: string;
  text: string;
  percent?: number;
}

/** One village whose claims the pipeline cannot advance. */
export interface StuckVillage {
  village_lgd_code: string;
  village_name: string;
  claims: number;
  since: string;
  reason: string;
}

/** A rung of the approval ladder, in order. */
export type ApprovalLevel = "village_pair" | "circle" | "tehsil" | "district";

/**
 * One rung's decision.
 *
 * Superseded rows are returned too. A report that went up at 40%, came back
 * and went up again at 65% has two Circle approvals, and hiding the first
 * would leave the history reading as though the Circle always thought 65%.
 */
export interface ApprovalRecord {
  id: string;
  report_id: string;
  level: ApprovalLevel;
  officer_id: string;
  officer_name?: string;
  role: string;
  decision: "approved" | "returned";
  percent_before?: number;
  percent_after?: number;
  area_before_ha?: number;
  area_after_ha?: number;
  reason?: string;
  decided_at: string;
  superseded_at?: string;
}

/** What happened to one report in a batch. */
export interface BulkOutcome {
  report_id: string;
  approved: boolean;
  /** Why it was refused, in words the officer can act on. */
  reason?: string;
}

export interface BulkApprovalResult {
  selected: number;
  approved: number;
  refused: number;
  results: BulkOutcome[];
}

/* ---------------------------------------------------------------------------
 * Verification by the village's allocated authorities
 * ------------------------------------------------------------------------ */

export type Tier = "village_pair" | "arbitration" | "escalation";
export type Outcome =
  | "confirmed" | "partly_confirmed" | "rejected" | "not_traceable";
export type Route = "consensus" | "arbitration" | "escalation";

export interface Authority {
  user_id: string;
  role: string;
  full_name: string;
  jurisdiction_level: string;
}

export interface VillageAuthorities {
  village_lgd_code: string;
  authorities: Authority[];
  missing_roles: string[] | null;
  can_verify: boolean;
}

export interface Assignment {
  id: string;
  report_id: string;
  officer_id: string;
  officer_name?: string;
  role: string;
  tier: Tier;
  assigned_at: string;
  due_at: string;
  completed_at?: string;
  overdue: boolean;
}

export interface WorklistEntry extends Assignment {
  reference: string;
  farmer_name: string;
  village_name: string;
  calamity_type: CalamityType;
  occurred_at: string;
  report_state: ReportState;
  /** That the peer has been, never what they concluded. */
  peer_recorded: boolean;
}

export interface VerifiedItem {
  line_no: number;
  crop_name?: string;
  area_sown_ha?: number | null;
  area_affected_ha?: number | null;
  damage_percent?: number | null;
}

export interface Finding {
  id: string;
  officer_id: string;
  officer_name?: string;
  role: string;
  tier: Tier;
  outcome: Outcome;
  damage_percent: number | null;
  area_affected_ha: number | null;
  findings: string;
  visited_at?: string;
  submitted_at: string;
  saw_peer_finding: boolean;
  saw_model_assessment: boolean;
  /**
   * The officer had a finding here and replaced it after reading their
   * colleague's. Not the same as saw_peer_finding, which only means they filed
   * second — an officer who moved towards their colleague and an officer who
   * simply went later are different things to a reader.
   */
  revised: boolean;
  items?: VerifiedItem[];
}

export interface Consensus {
  complete: boolean;
  first_percent: number | null;
  second_percent: number | null;
  divergence: number | null;
  mean: number | null;
  route: Route;
  adjudicated_by: string;
  explanation: string;
}

export interface Adjudication {
  id: string;
  officer_id: string;
  officer_name?: string;
  role: string;
  shown_first_percent: number | null;
  shown_second_percent: number | null;
  shown_model_percent: number | null;
  divergence: number | null;
  route: Route;
  adjudicated_percent: number;
  adjudicated_area_ha: number;
  reason: string;
  decided_at: string;
}

/** What an assigned officer sees before the visit. Never the peer's figure. */
export interface VerificationBrief {
  report: CropLossReport;
  assigned: boolean;
  tier: Tier | "";
  already_recorded: boolean;
  peer_recorded: boolean;
  peer_finding_withheld: boolean;
  /**
   * Whether a revised finding may be submitted.
   *
   * True once both authorities have filed and while the report is still
   * theirs. The pair record blind, then read each other, and closing a gap of
   * a few points between themselves is better than sending it to the Circle
   * Officer as a disagreement to arbitrate.
   */
  may_amend: boolean;
  findings?: Finding[];
  consensus?: Consensus;

  /**
   * What the panel concluded, for the officer to confirm or overrule.
   *
   * Absent where no assessment has run yet -- a claim can reach an officer
   * before its evidence has been through the panel -- and the form then asks
   * for the officer's own figure instead of offering one to agree with.
   *
   * The brief withheld this until now, so that an officer's figure could be
   * used to judge the model's. That is still what it costs: every finding
   * made with the figure in view is marked saw_model_assessment, and the
   * calibration report keeps informed and blind findings apart.
   */
  assessment?: {
    /** Needed to fetch the report itself, which the officer reads before
     *  recording a finding. See components/AssessmentReport. */
    id: string;
    damage_percent: number | null;
    crop_presence?: string;
    confidence?: number;
    triage_tier?: string;
  };
}

/**
 * One report that has sat at a rung longer than that rung allows.
 *
 * Distinct from an overdue assignment, which is a named officer late on a
 * field visit. Here a *rung* is late: above the village pair a report is not
 * allocated to a person, and whoever holds the posting owes the answer.
 */
export interface WaitingEntry {
  report_id: string;
  reference: string;
  farmer_name: string;
  village_name: string;
  village_lgd_code: string;
  level: ApprovalLevel;
  role: string;
  waiting_since: string;
  due_at: string;
  days_waiting: number;
  days_overdue: number;
  contested: boolean;
}

/**
 * One external dependency and what it is doing.
 *
 * `enable` names the environment variables that turn it on and never their
 * values: a status page that echoes an API key has published it to everyone
 * who can read the page.
 */
export interface Integration {
  key: string;
  name: string;
  state: "live" | "degraded" | "unconfigured" | "failing";
  summary: string;
  /** What the platform is doing meanwhile. Absent when it is live. */
  consequence?: string;
  enable?: string[];
  metrics?: { label: string; value: string; warn?: boolean }[];
}

/**
 * How much of the circle tier exists, per taluka.
 *
 * A circle is a revenue unit that LGD assigns no code to, so the
 * village-to-circle mapping is in no national directory. Where it is missing
 * the Circle Officer rung is skipped and claims reach the Tehsildar with one
 * fewer independent review behind them — quietly, which is why it is counted.
 */
export interface CircleCoverage {
  talukas: {
    district_name: string;
    district_code: string;
    taluka_name: string;
    taluka_code: string;
    circles: number;
    villages_under_circle: number;
    villages_direct: number;
  }[];
  summary: {
    talukas: number;
    talukas_with_circle: number;
    villages: number;
    villages_no_circle: number;
    note: string;
  };
}

/**
 * Oversight above a district: the districts side by side, the same claims cut
 * by crop and by event, and the filing trend.
 *
 * Read from the rollup tables rather than computed live, so a figure here is
 * as old as the last sweep. `refreshed_at` and `pending_refresh` say how old,
 * because an aggregate that claims to be current and is not is worse than one
 * that dates itself.
 */
export interface Statewide {
  districts: {
    code: string;
    name: string;
    reports: number;
    unresolved: number;
    adjudicated: number;
    contested: number;
    filed_late: number;
    area_affected_ha: number;
    area_sown_ha: number;
    declared_percent: number;
    adjudicated_percent: number;
    villages: number;
  }[];
  crops: {
    crop: string;
    reports: number;
    area_affected_ha: number;
    declared_percent: number;
  }[];
  events: {
    id: string;
    reference: string;
    title: string;
    calamity_type: string;
    state: string;
    district_name: string;
    incident_from: string;
    reports: number;
    unresolved: number;
    area_affected_ha: number;
  }[];
  filed: { day: string; reports: number }[];
  totals: {
    reports: number;
    unresolved: number;
    adjudicated: number;
    contested: number;
    area_affected_ha: number;
    area_sown_ha: number;
    farmers: number;
    declared_percent: number;
    adjudicated_percent: number;
  };
  refreshed_at?: string;
  pending_refresh: number;
  generated_at: string;
}

/* ---------------------------------------------------------------------------
 * Administration
 * ------------------------------------------------------------------------ */

export interface Grant {
  role: string;
  jurisdiction_id?: string;
  jurisdiction_code?: string;
  jurisdiction_level?: string;
  jurisdiction_name?: string;
}

export interface OfficerAccount {
  id: string;
  kind: "officer" | "farmer";
  status: string;
  full_name: string;
  /** The email address, which is also the username for accounts created
   *  since invitations replaced administrator-chosen passwords. */
  username?: string;
  email?: string;
  mobile?: string;
  farmer_id?: string;
  grants: Grant[];
  must_change_password: boolean;
  last_login_at?: string;
  /** Null on an account whose invitation has not been accepted yet. That is
   *  the difference between "invited" and "active" in the account roll. */
  password_changed_at?: string;
  created_at?: string;
}

export interface Jurisdiction {
  id: string;
  level: string;
  code: string;
  name: string;
  parent_id?: string;
}

export interface AuditEntry {
  id: number;
  at: string;
  actor_user_id?: string;
  actor_label?: string;
  actor_role?: string;
  actor_ip?: string;
  action: string;
  resource_type?: string;
  resource_id?: string;
  jurisdiction_code?: string;
  outcome: "allow" | "deny" | "error";
  detail: Record<string, unknown>;
  request_id?: string;
}

/* ---------------------------------------------------------------------------
 * Self-survey evidence
 * ------------------------------------------------------------------------ */

export type CaptureKind =
  | "wide_shot" | "damage_closeup" | "crop_closeup" | "boundary_point";

export interface Capture {
  id: string;
  report_id: string;
  line_no?: number;
  kind: CaptureKind;
  caption?: string;
  content_type: string;
  byte_size: number;
  sha256: string;
  captured_at: string;
  lat: number;
  lon: number;
  accuracy_m?: number;
  device_label?: string;
  uploaded_at: string;
  /** Null when the parcel has no recorded boundary to check against. */
  inside_parcel: boolean | null;
  distance_from_parcel_m: number | null;
  gates_passed: string[];
  /**
   * A failed gate is a note for the officer, not a rejection. Only two of them
   * stop a photograph reaching the model: taken outside the plot, or dated
   * before the loss.
   */
  gates_failed: string[];

  /**
   * Which of the four directed points this was taken at, how far from it the
   * farmer actually stood, and — for a clip — how long it runs. Absent on a
   * capture from the plain photograph sequence, and on everything filed
   * before the walk existed.
   */
  station_no?: number;
  station_offset_m?: number;
  duration_ms?: number;

  captured_by?: string;
  created_at: string;
}

export interface ParcelBoundary {
  farm_id: string;
  farmer_id: string;
  survey_number: string;
  village_lgd_code: string;
  polygon_area_ha: number;
  source: string;
  geojson?: unknown;
}

/**
 * The jurisdiction-scoped rollup behind every role's landing page.
 *
 * One shape for every officer. What a State Officer and a Talathi see differs
 * because their jurisdiction differs, not because the console decided what
 * their rank should be shown.
 */
export interface Overview {
  /** Report counts keyed by workflow state. */
  reports: Record<string, number>;
  reports_total: number;
  /** Hectares claimed as affected, summed from the crop lines. */
  area_hectares: number;
  /** Households, not reports: one farmer with four parcels is one household. */
  farmers: number;
  open_events: number;
  awaiting_acknowledgement: number;
  awaiting_inspection: number;
  filed_late: number;
  /** One level down: talukas for a district, villages for a taluka. */
  areas: AreaRollup[];
  generated_at: string;
}

export interface AreaRollup {
  code: string;
  name: string;
  level: string;
  reports: number;
  farmers: number;
  area_hectares: number;
  /** Not yet closed or withdrawn. The column an oversight officer sorts by. */
  unresolved: number;
}

/**
 * One report waiting on an adjudicator.
 *
 * The queue exists because the platform could already adjudicate a report and
 * show one by reference, but could not answer "which are waiting on me" —
 * so reports sat in verified and the farmer heard nothing.
 */
export interface AdjudicationEntry {
  report_id: string;
  reference: string;
  farmer_name: string;
  village_name: string;
  village_lgd_code: string;
  calamity_type: string;
  occurred_at: string;
  state: string;

  /** Null while only one officer has filed: waiting on a verifier, not on you. */
  first_percent: number | null;
  second_percent: number | null;
  divergence: number | null;
  findings_filed: number;

  /** What Reconcile would decide on these numbers. */
  route: Route;
  /** Findings that rejected the claim outright. Never averaged away. */
  rejected: number;

  waiting_since: string;
  waiting_days: number;
}

/** How a boundary came to be, and what it is worth. */
export interface BoundaryTrace {
  source: string;
  traced_at?: string;
  traced_by_name?: string;
  worst_accuracy_m?: number;
  traced_points?: { lat: number; lon: number; accuracy_m: number; at: string }[];
  confirmed_at?: string;
  confirmed_by_name?: string;
  registry_extent_ha?: number;
  /** The walk against the registry's extent. Reported, never resolved. */
  area_disagreement_percent?: number;
  notes?: string[];
}

/** One thing that has arrived at this officer's rung and not yet been seen. */
export interface Notification {
  id: string;
  kind: string;
  title: string;
  body?: string;
  /** Where to go to act on it. A notification with nowhere to go is noise. */
  link?: string;
  read: boolean;
  created_at: string;
  read_at?: string;
  resource_type?: string;
  resource_id?: string;
}
