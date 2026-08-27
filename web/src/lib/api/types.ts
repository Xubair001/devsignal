/**
 * DTOs mirroring the Go response types, including the awkward parts.
 *
 * `null` is load-bearing throughout. `salary: null` means the employer did not
 * disclose pay — a real state, not a value to `||` away — and `observed: null`
 * on an objective means nothing was measured. Widening either to a default is
 * the invented-field failure the display rules exist to prevent.
 */

/** Money always arrives as minor units. Never format it into the type. */
export type Money = {
  min_minor: number;
  max_minor: number | null;
  currency: string;
  period: 'year' | 'month' | 'week' | 'day' | 'hour';
  is_estimated: boolean;
};

/* ----------------------------------------------------------------- feed --- */

export type FactorView = {
  factor: string;
  points: number;
  max_points: number;
  /**
   * False when there was no observable data. The UI must render these
   * differently from a zero: "we could not read the required skills" and "you
   * match none of them" are opposite statements.
   */
  scored: boolean;
  reason?: string;
};

export type FitView = {
  /** The server decides the band. The client never derives one. */
  band: 'Strong fit' | 'Worth a look' | 'Stretch' | 'Not enough information';
  points: number;
  max_points: number;
  summary: string;
  factors: FactorView[];
  versions: { weights: string; embedding: string; profile: number };
};

export type StateView = {
  saved: boolean;
  applied: boolean;
  /** When the user TOLD US they applied. We cannot see the employer's side. */
  applied_at: string | null;
  dismissed: boolean;
};

/**
 * The posting itself, shared with the browse list rather than duplicated.
 *
 * `liveness` is why this is not optional: the display rules forbid showing a
 * posting in the daily feed whose open state is unknown, so the server drops
 * an item it cannot describe rather than sending a partial one.
 */
export type Posting = {
  id: string;
  title: string;
  company: {
    name: string;
    /**
     * False when the identity came from an ATS board token rather than a real
     * registrable domain. It changes how much company-level detail is worth
     * trusting, so it is shown rather than assumed.
     */
    domain_confirmed: boolean;
  };
  role: {
    family: string | null;
    /** A label, never the internal ordinal. */
    seniority: string | null;
    is_management: boolean;
  };
  location: {
    country: string | null;
    city: string | null;
    work_mode: string | null;
    geo_scope: string[];
  };
  /** null means undisclosed. Never `||` a range into this. */
  salary: Money | null;
  visa_sponsorship: string;
  language: string | null;
  apply_url: string | null;
  liveness: {
    verified_open: boolean;
    checked_at: string | null;
    /** Ours, and the only trustworthy age signal. */
    first_seen_at: string;
    days_open: number;
    /** Theirs. Displayed as a claim, never as an observation. */
    source_claims_posted_at: string | null;
  };
  /**
   * Observable facts only. There is deliberately no competitiveness estimate —
   * we have no applicant counts.
   */
  signals: {
    ghost_risk: 'normal' | 'elevated' | 'high';
    ghost_risk_reasons: string[];
    times_refreshed: number;
    sources_seen_on: number;
    apply_method: string | null;
  };
};

export type FeedItem = {
  opportunity_id: string;
  title: string;
  fit: FitView;
  state: StateView;
  channels: string[];
  posting: Posting;
};

/** Honest about the shape of the result, not only its contents. */
export type FeedDiagnostics = {
  eligible_after_predicates: number;
  retrieved: number;
  passed_eligibility_gate: number;
  excluded_by_gate: number;
  retrieval_truncated: boolean;
  /** Ranked, then closed or merged before the response was written. */
  closed_since_scoring: number;
};

export type FeedResponse = { items: FeedItem[]; diagnostics: FeedDiagnostics };

export type ExcludedItem = {
  opportunity_id: string;
  title: string;
  failed_checks: string[];
  reasons: string[];
};

export type ExcludedResponse = { items: ExcludedItem[] };

export const DISMISS_REASONS = [
  'wrong_stack',
  'wrong_level',
  'wrong_location',
  'comp_too_low',
  'already_applied',
  'not_interested',
] as const;
export type DismissReason = (typeof DISMISS_REASONS)[number];

/* ---------------------------------------------------------------- admin --- */

export type AdminSource = {
  id: string;
  name: string;
  tier: string;
  type: string;
  status: 'active' | 'quarantined' | 'retired';
  last_success_at: string | null;
  last_failure_at: string | null;
  items_discovered: number;
  items_processed: number;
  postings_attributed: number;
  legal_basis: string;
  robots_checked_at: string | null;
  terms_reviewed_at: string | null;
  reviewed_by: string | null;
  etag_supported: boolean;
};

export type SourcesResponse = { sources: AdminSource[] };

export type HealthDay = {
  day: string;
  polls: number;
  poll_failures: number;
  not_modified: number;
  postings_seen: number;
  postings_usable: number;
  with_company: number;
  with_location: number;
  with_apply_url: number;
  with_language: number;
  with_salary: number;
};

export type HealthResponse = { days: HealthDay[] };

/**
 * Five states, not three. `no_data` and `unmeasurable` are the honest ones: a
 * quiet hour is not a missing capability, and neither is a failure.
 */
export type SloStatus = 'met' | 'at_risk' | 'breached' | 'no_data' | 'unmeasurable';

export type SloResult = {
  id: string;
  description: string;
  status: SloStatus;
  detail: string;
  target: number;
  kind: 'ratio' | 'latency' | 'duration' | 'count';
  observed: number | null;
  sample: number;
  budget_remaining: number | null;
  burn_rate: number | null;
  alert_severity: 'none' | 'page' | 'ticket';
};

export type SloResponse = {
  measured_at: string;
  results: SloResult[];
  summary: { breached: number; at_risk: number; unmeasurable: number; healthy: boolean };
  /** Verification recency. Reported beside the objectives, never as one. */
  liveness_verification: {
    shown: number;
    checked_recently: number;
    fraction: number;
    threshold_hours: number;
    oldest_check_hours: number;
    note: string;
  } | null;
  pipeline_states: {
    state: string;
    records: number;
    oldest_entered: string;
    stranded: boolean;
  }[];
};

export type AdminFlag = {
  id: string;
  opportunity_id: string;
  title: string;
  company_name: string | null;
  reason: string;
  detail: string | null;
  status: 'open' | 'upheld' | 'rejected' | 'duplicate';
  created_at: string;
  flags_on_posting: number;
  posting_closed: boolean;
};

export type FlagsResponse = { flags: AdminFlag[] };
