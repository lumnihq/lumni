// Two kinds of shapes live here.
//
// Contract shapes mirror api/openapi.yaml and pkg/protocol/types.go, so the
// mock can be swapped for the Go API one endpoint at a time.
//
// View shapes are what each console screen reads from /v1/console/*. The
// contract has no list or summary endpoints for these yet; the README lists
// them as proposed.

// ─── contract ──────────────────────────────────────────

export type DecisionType =
  | 'ALLOW'
  | 'WARN'
  | 'WAIT'
  | 'REPLAN'
  | 'DEDUPLICATE'
  | 'REQUIRE_APPROVAL'
  | 'REJECT'
  | 'WOULD_ALLOW'
  | 'WOULD_WAIT'
  | 'WOULD_REPLAN'
  | 'WOULD_DEDUPLICATE'
  | 'WOULD_REQUIRE_APPROVAL'
  | 'WOULD_REJECT';

// The protocol's reason codes, plus the ones the console proposes (marked).
export type ReasonCode =
  | 'NO_CONFLICT'
  | 'EXACT_DUPLICATE_ACTIVE'
  | 'EXACT_EFFECT_ALREADY_COMMITTED'
  | 'IDEMPOTENCY_KEY_REUSED_DIFFERENT_PAYLOAD'
  | 'STALE_OBSERVATION'
  | 'VERSION_UNAVAILABLE'
  | 'VERSION_SOURCE_CHANGED'
  | 'CONFLICTING_ACTIVE_MUTATION'
  | 'WRITE_WRITE_CONFLICT'
  | 'DELETE_WRITE_CONFLICT'
  | 'DOMAIN_OPERATION_CONFLICT'
  | 'APPROVAL_REQUIRED'
  | 'APPROVAL_PENDING'
  | 'APPROVAL_DENIED'
  | 'APPROVAL_EXPIRED'
  | 'RESOURCE_UNRESOLVED'
  | 'RESOURCE_MAPPING_LOW_CONFIDENCE'
  | 'LEASE_BUSY'
  | 'LEASE_EXPIRED'
  | 'FENCING_TOKEN_INVALID'
  | 'POLICY_REJECTED'
  | 'ACTION_RESULT_UNKNOWN'
  | 'COORDINATOR_UNAVAILABLE'
  | 'WORK_SUPERSEDED' // proposed
  | 'ACTOR_PAUSED'
  | 'CONCURRENCY_LIMIT'
  | 'BUDGET_PACING'
  | 'RATE_LIMITED'
  | 'DEPENDENCY_PENDING'; // proposed

export type EnforcementMode = 'SHADOW' | 'WARN' | 'ENFORCE';

export type ActionStatus =
  | 'PROPOSED'
  | 'AWAITING_APPROVAL'
  | 'RESERVED'
  | 'ACTIVE'
  | 'SUCCEEDED'
  | 'FAILED'
  | 'UNKNOWN'
  | 'EXPIRED'
  | 'CANCELLED';

export type OutcomeStatus = 'VERIFIED' | 'MISMATCH' | 'PARTIAL' | 'FAILED' | 'PENDING' | 'UNKNOWN';

export type EvaluationStage = 'FRESHNESS' | 'IDEMPOTENCY' | 'LEASE' | 'POLICY';

export interface DecisionEvaluation {
  stage: EvaluationStage;
  outcome: 'PASS' | 'HIT';
  reason_code?: ReasonCode;
  subject_id?: string;
  duration_us?: number;
  details?: Record<string, unknown>;
}

export interface Decision {
  schema_version: string;
  action_id: string;
  receipt_id: string;
  decision: DecisionType;
  reason_code: ReasonCode;
  enforced: boolean;
  confidence: number;
  kernel_version: string;
  resolver_version: string;
  policy_version: string;
  created_at: string;
  existing_action_id?: string;
  approval_id?: string;
  evaluations: DecisionEvaluation[];
}

export interface Outcome {
  id: string;
  action_id: string;
  status: OutcomeStatus;
  verifier_id?: string;
  checked_at?: string;
  attempts: number;
  evidence: { id: string; status: string; reason?: string; observed?: unknown }[];
}

export interface Action {
  schema_version: string;
  action_id: string;
  work_id: string;
  actor_id: string;
  operation: string;
  tool: string;
  status: ActionStatus;
  created_at: string;
  metadata: Record<string, unknown>;
  latest_decision: Decision;
  outcome: Outcome | null;
}

export type ApprovalStatus = 'PENDING' | 'APPROVED' | 'DENIED' | 'EXPIRED';

export interface Approval {
  id: string;
  action_id: string;
  status: ApprovalStatus;
  reason_code: ReasonCode;
  requested_at: string;
  expires_at?: string;
  decided_at?: string;
  decided_by?: string;
  metadata: Record<string, unknown>;
}

export interface Verifier {
  id: string;
  name: string;
  kind: string;
  version: string;
  config: Record<string, unknown>;
  enabled: boolean;
}

export interface PolicyRule {
  id: string;
  name: string;
  outcome: DecisionType;
  reason_code: ReasonCode;
  mode: EnforcementMode;
  metadata: Record<string, unknown>;
}

export interface Session {
  signed_in: boolean;
  csrf_token?: string;
  user?: { id: string; initials: string; email: string };
  tenant?: { id: string; name: string };
}

export interface ApiErrorBody {
  error: { code: string; message: string; request_id: string; details?: Record<string, unknown> };
}

// ─── shared view parts ─────────────────────────────────

/** How a decision reads in the console. */
export type VerdictKind =
  | 'allow'
  | 'done'
  | 'onit'
  | 'recheck'
  | 'wait'
  | 'turns'
  | 'slow'
  | 'superseded'
  | 'ask'
  | 'stop'
  | 'bad';

export type ProofKind = 'ok' | 'bad' | 'wait' | 'none' | 'busy' | 'stopped';

/** Today's step-in groups, which double as the tallies' legend. */
export type StepKind = 'stop' | 'wait' | 'recheck' | 'ask';

export interface Who {
  id: string;
  name: string;
  initials: string;
  human: boolean;
}

export interface VerdictV {
  kind: VerdictKind;
  would: boolean;
}

export interface ProofV {
  kind: ProofKind;
  text: string;
  /** A fix the reader can take, like connecting the app. */
  link?: { label: string; to: string };
}

export type AppId = string;

/** The white head every page opens with. */
export interface Head {
  title: string;
  sub: string;
}

/** The day, from midnight: how busy each quarter hour was, relative to the busiest. */
export interface DayHill {
  /** Hours since midnight, as a float. */
  now: number;
  quarters: number[];
}

export interface Bead {
  id: string;
  /** Hours since midnight. */
  h: number;
  step: StepKind;
  text: string;
  verdict: VerdictV;
}

// ─── /v1/console/today ─────────────────────────────────

export interface FlowChip {
  id: string;
  /** When it reaches the gate. */
  at: number;
  who: Who;
  text: string;
  caught: boolean;
  verdict: VerdictV | null;
  /** Set once its receipt is written. */
  receipt?: string;
  proof?: ProofKind;
}

export interface StepRow {
  id: string;
  at: number;
  who: Who;
  verdict: VerdictV;
  step: StepKind;
  text: string;
  /** Why, or who: the screen adds when. */
  note: string;
  kept: string;
}

export interface TodayView {
  day: 'today' | 'yesterday';
  date: string;
  head: Head;
  ribbon: DayHill & {
    beads: Bead[];
    /** The newest step-in, pinned to the ribbon with a note. */
    latest: (StepRow & { h: number }) | null;
    deadline: null | { from: number; to: number; at: number; note: string };
    bad: { id: string; h: number }[];
  };
  bad: number;
  perMinute: number;
  refundsKept: number;
  done: number;
  caught: number;
  wouldHave: number;
  tallies: Record<StepKind, number>;
  steps: StepRow[];
  stepsTotal: number;
  flow: FlowChip[];
  /** "About 30 actions a minute", or why the strip is still. */
  flowLabel: string;
  ais: Who[];
  needs: null | {
    id: string;
    kind: NeedItem['kind'];
    who: Who;
    title: string;
    body: string;
    foot?: string;
    cta: string;
    expiresAt?: number;
    count: number;
  };
  /** The side box: this week in a few numbers. */
  week: { n: string; text: string }[];
  paused: boolean;
  pausedAt: number | null;
}

// ─── /v1/console/live ──────────────────────────────────

export interface Progress {
  /** progress(t) = p0 + (1 - p0) * clamp((t - t0) / (t1 - t0)); frozen when t1 is null. */
  p0: number;
  t0: number;
  t1: number | null;
  /** Count-style progress: "312 of 480". */
  of?: number;
}

export interface Worker {
  who: Who;
  doing: string;
  here?: boolean;
  progress?: Progress;
  done?: boolean;
}

export interface LaneV {
  id: string;
  thing: string;
  sub: string;
  now: Worker | null;
  next: Worker[];
  verdict: VerdictV | null;
  /** "{n}" is replaced with the live count on screen: "312 of 480 sent." */
  say: string;
  small: string;
  link?: { label: string; to: string };
  /** The ribbon: who held it before, since when the current worker has, and where Lumni stepped in. */
  past: Stretch[];
  since: number | null;
  marks: LaneMark[];
  /** What's coming: the rest of the current work, then each worker in line. Only what can be timed. */
  soon: Stretch[];
  /** An AI waiting on a person's decision, from when it asked until Lumni decides for you. */
  ask?: { from: number; until: number; who: Who };
}

export interface Stretch {
  /** Null when a block joins up work by more than one AI. */
  who: Who | null;
  t0: number;
  t1: number;
  /** What the block says on the ribbon: "Release AI", "3 AIs", "Ops AI next". Empty for none. */
  label: string;
  /** What was done, for the tooltip. */
  doing: string;
}

export interface LaneMark {
  at: number;
  kind: StepKind;
  rec?: string;
}

export interface HandOff {
  at: number;
  thing: string;
  text: string;
}

export interface LiveView {
  head: { title: string; sub: string };
  lanes: LaneV[];
  ais: number;
  people: string[];
  things: number;
  waiting: number;
  working: number;
  /** Actions Lumni made take turns today. */
  held: number;
  paused: boolean;
  pausedAt: number | null;
  /** The last hour's hand-offs, newest first. */
  log: HandOff[];
}

// ─── /v1/console/map ───────────────────────────────────

/** A safe fix the inspector offers for a node. */
export type MapFix =
  | { kind: 'retry'; post: string; label: string; done: string }
  | { kind: 'connect'; app: AppId; label: string; done: string };

/** One stretch of a node's row on the ribbon, from when the AI acted to when it matched. */
export interface MapSpan {
  kind: 'busy' | 'bad' | 'wait' | 'none';
  t0: number;
  /** Still going. */
  t1: number | null;
  /** What to call it on the ribbon, when the kind alone doesn’t say enough. */
  label?: string;
}

export interface MapTrack {
  spans: MapSpan[];
  okAt?: number;
  beads: { at: number; kind: StepKind; label: string; rec?: string }[];
}

export interface MapNode {
  id: string;
  layer: number;
  row: number;
  actor?: Who;
  app?: { id: AppId; name: string };
  label: string;
  title: string;
  verdict?: VerdictV & { word?: string };
  proof: ProofV;
  /** What would have happened if Lumni hadn’t stopped it. Drawn dashed. */
  would?: boolean;
  receipt?: string;
  /** The step-in's story, when Lumni stepped in here. */
  story?: string;
  timeline: { at: number; text: string }[];
  fix?: MapFix;
  /** App nodes get a row on the ribbon. */
  track?: MapTrack;
}

export interface MapEdge {
  from: string;
  to: string;
  label: string;
  tone?: 'wait' | 'would';
  /** Lumni is working along this edge right now. */
  pulse?: boolean;
}

export interface MapPickItem {
  id: string;
  title: string;
  sub: string;
}

export interface MapView {
  id: string;
  scope: 'action' | 'customer';
  /** The customer, when the action has one, so the page can offer everything about them. */
  customer?: string;
  /** What the scope switch calls this one action: "This refund". */
  one: string;
  title: string;
  sub: string;
  /** When the AI acted, and the stretch of time the ribbon covers. */
  at: number;
  from: number;
  to: number;
  nodes: MapNode[];
  edges: MapEdge[];
  tallies: { checked: number; bad: number; waiting: number; none: number; busy: number };
  picks: MapPickItem[];
}

// ─── /v1/console/receipts ──────────────────────────────

export type ReceiptFilter = 'checked' | 'none' | 'stopped' | 'waiting' | 'bad' | 'stepins';

export interface ReceiptRow {
  id: string;
  at: number;
  who: Who;
  did: string;
  app: { id: AppId; name: string };
  verdict: VerdictV;
  proof: ProofV;
  /** Didn’t happen, and nobody has sorted it out yet. */
  flagged: boolean;
}

export interface ReceiptAction {
  kind: 'retry' | 'handled' | 'connect' | 'story' | 'map' | 'need';
  label: string;
  /** A write, or a page to open. */
  post?: string;
  body?: Record<string, unknown>;
  to?: string;
  /** What the toast says once the write lands. */
  done?: string;
  busy?: boolean;
  /** Shown but can’t be used yet, and why. */
  unavailable?: string;
}

/** What the AI said it did, next to what the app shows. */
export interface ReceiptDetail {
  id: string;
  said: { who: Who; head: string; text: string; note: string };
  shows: {
    /** Whose side this card tells: the app’s, or Lumni’s when it stepped in first. */
    by: 'app' | 'lumni';
    app: { id: AppId; name: string };
    head: string;
    title: string;
    note: string;
    tone: 'ok' | 'off' | 'none' | 'busy' | 'wait' | 'lumni';
  };
  actions: ReceiptAction[];
}

export interface ReceiptsView {
  day: 'today' | 'yesterday';
  date: string;
  head: Head;
  /** Who and where, for the pickers: every AI and app seen that day. */
  ais: Who[];
  apps: { id: AppId; name: string }[];
  /** Set when the day couldn’t be read in full: what’s missing, in a sentence. */
  partial: string | null;
  /** Every receipt that day, before the window, search or filter. */
  total: number;
  ribbon: DayHill & {
    /** Can’t-check receipts per quarter, against their own busiest quarter. */
    hatch: number[];
    stops: { id: string; h: number }[];
    bad: { id: string; h: number }[];
  };
  /** The stretch of the day shown, in hours since midnight; `to` is null for "now". */
  window: { from: number; to: number | null };
  /** Counts inside the window and the search, before the filter. */
  shown: number;
  tallies: Record<ReceiptFilter, number>;
  tip: null | { id: string; title: string; note: string; retrying: boolean; h: number; post: string };
  items: ReceiptRow[];
  /** How many match the window, search and filter; `items` is the first page of them. */
  matched: number;
  /** An opened receipt that the window, search or filter hides. */
  focusHidden: boolean;
}

// ─── /v1/console/story/:id ─────────────────────────────

/** One of the four things Lumni checks before an action reaches an app. */
export interface StoryCheck {
  stage: EvaluationStage;
  question: string;
  answer: string;
  /** This is the check that stopped it, held it or sent it back. */
  hit: boolean;
}

/** How a moment on the story’s timeline is marked. */
export type StoryNode = 'plain' | 'lumni' | 'busy' | 'bad';

export interface StoryEvent {
  /** Stable while the moment means the same thing; it changes when a pending result lands. */
  id: string;
  at: number;
  /** "2:02:07.412 PM", or "Now" while it’s still happening. */
  precise: string;
  who?: Who;
  title: string;
  verdict?: { v: VerdictV; word?: string };
  proof?: ProofV;
  note?: string;
  checks?: StoryCheck[];
  node: StoryNode;
  /** What Lumni did about it, said after the checks. */
  after?: string;
  link?: { label: string; to: string };
}

export interface StoryLine {
  label: string;
  value: string;
  verdict?: VerdictV;
}

export interface StoryView {
  id: string;
  step: StepKind;
  head: { before: string; after: string; sub: string };
  /** Where this one sits among today’s step-ins, earliest first. */
  index: number;
  total: number;
  prev: string | null;
  next: string | null;
  rule: { id: string; text: string } | null;
  ribbon: DayHill & { beads: Bead[]; current: { id: string; h: number; label: string } };
  tallies: Record<StepKind, number>;
  evaluations: DecisionEvaluation[];
  events: StoryEvent[];
  /** False while the app hasn’t confirmed the result yet. */
  settled: boolean;
  receipt: { when: string; lines: StoryLine[]; totalLabel: string; total: string; money: boolean };
}

// ─── /v1/console/needs-you ─────────────────────────────

export interface VersionSide {
  who: Who;
  /** "Finance, changed it 2 min ago" */
  note: string;
  value: string;
  why: string;
}

export interface NeedItem {
  id: string;
  /** Two versions of one thing; an action that didn’t happen; or an AI asking before it acts. */
  kind: 'versions' | 'mismatch' | 'approval';
  status: ApprovalStatus | 'HANDLED' | 'RETRIED';
  requestedAt: number;
  expiresAt?: number;
  decidedAt?: number;
  decidedBy?: 'you' | 'lumni';
  title: string;
  paragraph: string;
  /** What it’s about, mid-sentence: "Acme’s billing email", "Priya’s $40 refund". */
  subject: string;
  /** The receipt it’s about. */
  rec: string;
  /** Where to read the whole thing: its story, or its receipt. */
  more: { label: string; to: string };
  field?: string;
  person?: VersionSide;
  ai?: VersionSide;
  /** Which version the app keeps, once someone chose. */
  kept?: 'person' | 'ai';
  /** What happens if nobody answers: "Lumni keeps Jess’s version." */
  fallback?: string;
  mismatch?: {
    said: { who: Who; text: string; note: string };
    shows: { app: { id: AppId; name: string }; title: string; note: string };
    missed: string;
    retry: string;
    /** Where the retry goes. */
    post: string;
    retrying: boolean;
    checks: number;
  };
  /** An AI asking before it acts, when all there is to show is what it wants to do. */
  ask?: {
    who: Who;
    /** What it wants to do: “Refund Dana Lee $250”. */
    did: string;
    app?: { id: AppId; name: string };
    /** Why it waits: “A rule says refunds over $100 need a person”. */
    note: string;
    /** What’s worth knowing before deciding. */
    facts: { label: string; value: string }[];
  };
  /** Once it’s settled: what the page says about it. */
  resolved?: { title: string; note: string };
  why: { text: string; rule?: { id: string; text: string } };
}

export interface DecidedRow {
  id: string;
  at: number;
  text: string;
  by: 'you' | 'lumni';
  /** How long it waited for an answer, when it waited on you. */
  waited?: number;
  more?: { label: string; to: string };
}

/** One decision on the ribbon: when it was asked, and when it was settled. */
export interface NeedPin {
  id: string;
  at: number;
  h0: number | null;
  h: number;
  by: 'you' | 'lumni';
  label: string;
  short: string;
  text: string;
}

export interface NeedsView {
  head: { title: string; sub: string };
  /** What’s waiting on you, and what was settled in the last few minutes, in the order they were asked. */
  items: NeedItem[];
  decided: DecidedRow[];
  notify: 'both' | 'here' | 'slack';
  ribbon: {
    now: number;
    pins: NeedPin[];
    open: { id: string; h0: number; h1: number | null; kind: NeedItem['kind']; label: string; note: string }[];
  };
  tallies: { waiting: number; you: number; lumni: number };
  /** Minutes you took to answer, on average today. */
  answerIn: number | null;
}

// ─── /v1/console/rules ─────────────────────────────────

export interface RuleToken {
  id: string;
  value: string;
  /** Each choice, with what last week would have come to under it, when Lumni can replay it. */
  options: { value: string; label: string; lastWeek: number | null }[];
}

export type RulePart = string | { token: string };

export interface RuleV {
  id: string;
  area: string;
  parts: RulePart[];
  tokens: RuleToken[];
  text: string;
  verdict: VerdictV;
  mode: EnforcementMode;
  verb: 'stopped' | 'asked' | 'waited';
  lastWeek: number;
  /** Last week, one count per day. */
  days: number[];
  /** A word you’re trying: last week replayed with it, before anything changes. Null counts when it can’t be replayed. */
  draft?: { token: string; value: string; label: string; lastWeek: number | null; delta: number | null };
  ais: string[];
  /** Times it stepped in today, and times it would have. */
  today: { did: number; would: number };
  isNew?: boolean;
}

export interface RuleTemplate {
  id: string;
  text: string;
  verdict: VerdictV;
  area: string;
  added: boolean;
}

export interface RuleBead {
  id: string;
  h: number;
  k: StepKind;
  would: boolean;
  label: string;
  to: string;
}

export interface RulesView {
  head: { title: string; sub: string };
  rules: RuleV[];
  protecting: number;
  watching: number;
  stoppedLastWeek: number;
  templates: RuleTemplate[];
  ais: Who[];
  ribbon: { now: number; lanes: { id: string; beads: RuleBead[] }[] };
  /** Today, across every rule, and what Lumni’s own checks caught besides. */
  today: { did: number; would: number; lumni: number };
}

// ─── /v1/console/spend ─────────────────────────────────

/** A stretch of the day in dollars: [hour, spent by then]. */
export type SpendCurve = [number, number][];

export interface SpendLimitOption {
  value: number | null;
  label: string;
  /** What it would do today: "Slows at 7:05 PM", "$333 by midnight". */
  effect: string;
  /** What the confirmation says once it’s set. */
  said: string;
}

export interface WastedItem {
  id: string;
  at: number;
  h: number;
  who: Who;
  text: string;
  verdict: VerdictV;
  cost: number;
  to: string;
}

export interface SpendByAi {
  who: Who;
  spent: number;
  actions: number;
  perAction: number;
}

export interface SpendView {
  head: { title: string; sub: string };
  /** Hours since midnight: the clock, and where spend stands (they differ while AIs are paused). */
  now: number;
  at: number;
  spent: number;
  /** By midnight at this pace, with nothing slowing it. */
  forecast: number;
  perAction: number;
  actions: number;
  paused: boolean;
  limit: number | null;
  /** How the limit bends the day, when it has to. */
  held: { slowAt: number; slowAtLabel: string; end: number; slowing: boolean } | null;
  curve: SpendCurve;
  /** From now to midnight: at this pace, and under the limit. */
  pace: SpendCurve;
  heldCurve: SpendCurve | null;
  options: SpendLimitOption[];
  aside: string;
  wasted: { total: number; count: number; items: WastedItem[] };
  byAi: SpendByAi[];
}

// ─── /v1/console/apps ──────────────────────────────────

/** Whether Lumni can check an app: connected, on its way, or not yet. */
export type AppState = 'connected' | 'connecting' | 'off';

/** One app’s row on the ribbon: its day in quarter hours, midnight to now. */
export interface AppLane {
  id: AppId;
  name: string;
  state: AppState;
  /** Actions today. */
  count: number;
  /** Per quarter hour: actions Lumni checked, or is checking, and actions it can’t check. */
  quarters: { seen: number; blind: number }[];
  /** What didn’t match in this app today, where it sits on the day. */
  bad: { id: string; h: number; text: string; at: number }[];
}

export interface AppRow {
  id: AppId;
  verifier: string;
  name: string;
  desc: string;
  state: AppState;
  /** Added from the catalog today; disconnecting removes it. */
  added: boolean;
  /** Whether Connect can do anything here. */
  canConnect: boolean;
  usedBy: Who[];
  count: number;
  /** "601 checked", "65 emails", "Nothing yet today". */
  total: { n: number | null; text: string };
  /** "All match", "1 didn’t happen", "Can’t check yet". */
  note: { kind: 'ok' | 'bad' | 'none' | 'busy' | 'quiet'; text: string; to?: string };
  /** Set once the app is connected and Lumni checks what it did before. */
  backfill?: {
    startedAt: number;
    duration: number;
    checked: number;
    /** Hour of the first action it checked: the sweep runs from here to now. */
    fromH: number;
  };
  receiptsTo: string;
}

export interface AppsView {
  head: { title: string; sub: string };
  now: number;
  lanes: AppLane[];
  /** The busiest quarter hour of any lane, so bars compare across apps. */
  peak: number;
  apps: AppRow[];
  catalog: { id: AppId; name: string; desc: string }[];
  tally: { checked: number; off: number; bad: number };
}

// ─── /v1/console/playground ────────────────────────────

export type PlayMode = 'yours' | 'defaults';

/** Whether a scenario is handled: stepped in for real, only watched, or let through. */
export type PlayStatus = 'handled' | 'watching' | 'not';

/** Something on a lane, at a share of the run from 0 to 1. */
export interface PlayMark {
  at: number;
  kind: 'verdict' | 'would' | 'proof' | 'human' | 'ghost' | 'note';
  text: string;
  verdict?: VerdictKind;
  proof?: ProofKind;
  initials?: string;
}

export interface PlayLane {
  who: { kind: 'ai' | 'human' | 'app'; name: string; initials?: string };
  bars: { from: number; to: number; text: string }[];
  marks: PlayMark[];
}

export type PlayBy =
  | { kind: 'rule'; ruleId: string; text: string; watching: boolean }
  | { kind: 'default'; text: string }
  | { kind: 'check'; text: string; fix?: { label: string; to: string } }
  | { kind: 'limit'; text: string; fix: { label: string; to: string } }
  | { kind: 'none'; text: string; yours: boolean; fix?: { label: string; to: string } };

/** A scenario under one set of rules: what Lumni says, and how the run goes. */
export interface PlayResult {
  status: PlayStatus;
  verdict: VerdictV & { word?: string };
  /** The fix, when it isn’t handled and there is one: "Set a limit". */
  fix?: { label: string; to: string };
  headline: string;
  sub: string;
  /** Where Lumni steps in, or where it goes wrong. */
  act: number;
  lanes: PlayLane[];
  outcomes: ProofV[];
  by: PlayBy;
}

export interface ScenarioV {
  id: string;
  title: string;
  /** Its short name on the ribbon. */
  lane: string;
  runTitle: string;
  yours: PlayResult;
  defaults: PlayResult;
  lastRun?: { at: number; status: PlayStatus; rules: PlayMode };
}

/** A 20-minute slot of real step-ins on one lane of the ribbon. */
export interface PlayBead {
  id: string;
  h: number;
  n: number;
  k: StepKind | 'bad';
  would: boolean;
  label: string;
  to: string;
}

export interface PlaygroundView {
  heads: Record<PlayMode, { title: string; sub: string }>;
  scenarios: ScenarioV[];
  ribbon: { now: number; lanes: { id: string; n: number; beads: PlayBead[] }[] };
}

// ─── /v1/console/watch-report ──────────────────────────

/** One watching rule, and what it would have done last week. */
export interface WatchArea {
  ruleId: string;
  area: string;
  /** "4 AIs kept working on a cancelled plan" */
  title: string;
  sub: string;
  count: number;
  /** Last week, Monday to Sunday. */
  days: number[];
  verb: RuleV['verb'];
  mode: EnforcementMode;
  /** When it switched to protecting, if it has. */
  protectedAt: number | null;
  text: string;
  today: { did: number; would: number };
}

export interface WatchView {
  head: { title: string; sub: string };
  /** Last week’s Monday, and its days as "Mon 14". */
  week: { from: number; to: number; days: string[] };
  areas: WatchArea[];
  /** Everything else last week that went through with nothing to step in on. */
  fine: number;
  would: { verb: RuleV['verb']; n: number }[];
  watching: number;
  /** What watching added to each action last week, on average, when Lumni measured it. */
  overheadMs: number | null;
  ribbon: { now: number; lanes: { id: string; beads: RuleBead[] }[] };
}

// ─── the top bar ───────────────────────────────────────

/** What this workspace can do. A missing one shows its control disabled, with a note. */
export interface Caps {
  pause: boolean;
  spendLimit: boolean;
  notify: boolean;
  handled: boolean;
  rulePreview: boolean;
  playground: boolean;
  customerMap: boolean;
  /** Add an app Lumni hasn’t seen. */
  connect: boolean;
  /** Retry one app on the Map. */
  appRetry: boolean;
}

export interface FrameView {
  /** The seeded demo, with its clock and reset. */
  demo: boolean;
  caps: Caps;
  needs: number;
  protecting: number;
  watching: number;
  paused: boolean;
  clockPaused: boolean;
  org: string;
  me: string;
  email: string;
  now: number;
  /** Things that happened on their own in the last few seconds, for toasts. */
  notes: { id: string; at: number; text: string; to?: string }[];
  tabs: FrameTabs;
}

/** What each tab says under its name, so a page's status shows before it's opened. */
export interface FrameTabs {
  today: { stepins: number };
  live: { working: number; paused: boolean };
  map: { fixing: number };
  receipts: { bad: number; checked: number };
  needs: { count: number; expiresAt: number | null };
  rules: { protecting: number; watching: number };
  spend: { spent: number; limit: number | null };
  apps: { checked: number; used: number };
  playground: { handled: number; total: number };
}
