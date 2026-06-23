// Same-origin path in both dev (vite proxy /api → :9000) and prod (nginx proxy /api → :9000).
const BASE = '/api/v1'

export interface Envelope<T = unknown> {
  ok: boolean
  data?: T
  error?: string
  ts: string
}

export interface CmdResult {
  cmd: string
  raw: string
  exit_code: number
  stderr?: string
  duration: string
}

export interface TSSample { t: number; v: number }

export interface PerfSnapshot {
  hostname: string
  iface: string
  boot_epoch: number
  cpu_pct: number
  mem_pct: number
  mem_total: number
  load_1: number
  disk_pct: number
  disk_total: number
  net_rx_bps: number
  net_tx_bps: number
}
export interface PerfData {
  snapshot: PerfSnapshot
  series: {
    cpu: TSSample[]; mem: TSSample[]; load: TSSample[]
    disk: TSSample[]; netRx: TSSample[]; netTx: TSSample[]
  }
}

export interface ConnProbe {
  name: string
  target: string
  type: 'ping4' | 'ping6'
  last_ok: boolean
  last_ms: number
  last_time: number
  series: TSSample[]
}
export interface WGPeer {
  public_key: string
  endpoint: string
  allowed_ips: string
  last_handshake: string
  transfer: string
  keepalive: string
}
export interface WGIface {
  name: string
  public_key: string
  listening_port: string
  peers: WGPeer[]
}
export interface NetTunnel {
  kind: 'gre' | 'gretap' | 'ip6gre' | 'vxlan'
  name: string
  up: boolean
  state?: string
  local?: string
  remote?: string
  ttl?: number
  mtu?: number
  vxlan_id?: number
  dst_port?: number
  underlay_dev?: string
}
export interface ConnectivityData {
  probes: ConnProbe[]
  wg: WGIface[]
}

export interface BirdProtocol {
  name: string
  proto: string
  table: string
  state: string
  since: string
  info: string
  healthy: boolean
}
export interface BirdRouteCount { table: string; count: number }
export interface BirdData {
  ready: boolean
  version: string
  protocols: BirdProtocol[]
  route_counts: BirdRouteCount[]
  last_update: number
  error: string
}

export interface AlertSample {
  at: number      // unix seconds
  message: string
}
export interface AlertEvent {
  id: string
  node_id: string
  rule_id: string
  title: string
  description: string
  threshold?: string
  severity: 'info' | 'warn' | 'crit'
  message: string
  trail?: AlertSample[]
  fired_at: number
  resolved_at?: number
  state: 'firing' | 'resolved'
  acked?: boolean
  acked_by?: string
}
export interface AlertRuleSpec {
  id: string; title: string; description: string; threshold?: string; severity: string
}
export interface AlertsData {
  active: AlertEvent[]
  history: AlertEvent[]
  rules: AlertRuleSpec[]
}

// ─── User-editable alert rules + groups ─────────────────────────────────────
export type AlertOp = 'gt' | 'gte' | 'lt' | 'lte' | 'eq' | 'ne'
export interface MetricMeta { key: string; label: string; unit: string; hint: string }
export interface RuleGroup {
  id: string
  name: string
  description?: string
  enabled: boolean
  node_ids?: string[]
  regions?: number[]
  mute_until?: number   // unix secs; > now = muted
  suppress_tg?: boolean            // group-level TG kill switch
  min_severity?: '' | 'info' | 'warn' | 'crit'  // TG floor; '' = global default (crit)
  default_sustain_secs?: number    // rules with sustain 0 inherit this
  created_by?: string
  created_at: string
  updated_at: string
}
export interface AlertRuleDef {
  id: string
  group_id: string
  name: string
  description?: string
  builtin: boolean      // seeded; tunable/disable-able but not deletable
  metric: string
  op: AlertOp
  threshold: number
  // Anomaly mode: when anomaly_sigma > 0 the rule fires on deviation from the
  // metric's learned rolling baseline (≥ this many σ, in the op's direction),
  // not on a fixed threshold. window = EWMA span in 30s ticks; min_delta = an
  // absolute floor (metric units) below which a deviation is ignored as jitter.
  anomaly_sigma?: number
  anomaly_window?: number
  anomaly_min_delta?: number
  sustain_secs?: number   // hold this long before firing
  resolve_secs?: number   // clear this long before resolving
  escalate_secs?: number  // auto-bump to crit after firing this long
  repeat_secs?: number    // re-send TG every this-many secs while firing
  severity: 'info' | 'warn' | 'crit'
  enabled: boolean
  notify_tg: boolean
  mute_until?: number
  created_by?: string
  created_at: string
  updated_at: string
}
export interface AlertRulesConfig { groups: RuleGroup[]; rules: AlertRuleDef[] }
export interface AnycastState {
  node_id: string
  local: boolean              // the control node — drain refused
  upstreams_up: string[]      // sessions currently announcing
  upstreams_down: string[]    // sessions not announcing
  drained: boolean            // all upstreams down = withdrawn
  drain_script: string        // exactly what drain would run
  undrain_script: string
  confirm: string
}
export interface AlertPreviewResult { node_id: string; value: number; ok: boolean; firing: boolean }

// One bound external login (Google/Microsoft/GitHub/Telegram).
export interface OAuthIdentity {
  provider: string
  email?: string
  bound_at?: string
  last_used_at?: string
}

// One peering application as the admin console renders it. Mirrors the
// PeeringApplication struct on the backend.
export interface PeeringApplication {
  id:            string
  submitted_at:  string
  status:        'pending' | 'approved' | 'rejected'
  ip:            string
  ua?:           string
  asn:           number
  network_name:  string
  as_set?:       string
  irr_source?:   string
  contact_name?: string
  noc_email:     string
  phone?:        string
  prefixes6:     string[]
  prefixes4?:    string[]
  max_prefix6?:  number
  has_rpki:      boolean
  bfd_desired?:  boolean
  locations?:    string[]
  session_types?: string[]
  ix_member?:    string[]
  notes?:        string
  admin_notes?:  string
  decided_by?:   string
  decided_at?:   string
}

// A generated per-peer BIRD config (peergen.go). Keyed by ASN on the backend.
export interface PeerGeneration {
  asn:           number
  network_name?: string
  as_set?:       string
  irr_source?:   string
  neighbor_v6?:  string
  prefix_count:  number
  max_prefix:    number
  config:        string
  warnings?:     string[]
  status:        'generated' | 'applied' | 'drifted'
  generated_at:  string
  applied_at?:   string
  applied_nodes?: string[]
}

// One row in the security audit log. Mirrors AuditEvent on the backend.
// `details` is intentionally loose — different events carry different
// shapes (login.ok has `path`, peering.decide.approved has `id` + `network`,
// etc.). The Audit panel renders details as a JSON dump on expand.
export interface AuditEvent {
  id:        string
  ts:        string   // RFC3339 UTC
  event:     string   // dot-separated, e.g. "login.ok", "peering.decide.approved"
  severity:  'info' | 'warn' | 'critical'
  actor:     string
  peer?:     string
  ua?:       string
  target?:   string
  outcome:   'ok' | 'fail' | 'denied'
  details?:  Record<string, unknown>
}

export interface AuditQueryResult {
  events:      AuditEvent[]
  next_cursor: string
  count:       number
}

export interface AuditStats {
  now:         string
  total_24h:   number
  by_severity: Record<string, number>
  by_event:    Record<string, number>
  hourly_24h:  Array<{ hour: string; count: number }>
}

// Filter shape sent to /api/v1/auth/audit. All fields optional; the
// view composes only the non-empty ones into URL query params.
export interface AuditFilter {
  event?:    string
  actor?:    string
  severity?: string
  outcome?:  string
  q?:        string
  since?:    string   // RFC3339
  until?:    string   // RFC3339
  limit?:    number
  cursor?:   string
}

// One entry in the webmail forgot-password queue, proxied via the
// operator-bridge into the admin console.
export interface MailForgotEntry {
  id:           string
  mailbox:      string
  requested_at: string  // RFC 3339
  ip:           string
  ua?:          string
}

export interface PeeringDBNet {
  id: number
  asn: number
  name: string
  website: string
  irr_as_set: string
  info_prefixes4: number
  info_prefixes6: number
  info_ipv6: boolean
  info_multicast: boolean
  info_unicast: boolean
  policy_general: string
  policy_locations: string
  policy_ratio: boolean
  policy_contracts: string
  ix_count: number
  fac_count: number
  updated: string
}
export interface PeeringDBIX {
  name: string
  speed: number       // Mbps
  ipaddr4: string
  ipaddr6: string
  is_rs_peer: boolean
  bfd_support: boolean
  operational: boolean
}
export interface PeeringDBSnapshot {
  net: PeeringDBNet | null
  ix: PeeringDBIX[]
  net_url: string
  fetched_at: number
  upstream_updated: string
  error?: string
}

export interface FleetPublicNode {
  id: string
  label: string
  country: string
  lat: number
  lon: number
  ok: boolean
  bgp_sessions: number
  bgp_total: number
  routes_v6: number
  wg_count: number
  tunnel_count: number
  anchor_ms: number
}
// ─── Incidents (status page + admin CRUD) ──────────────────────────────────

export type IncidentStatus = 'investigating' | 'identified' | 'monitoring' | 'resolved'
export type IncidentSeverity = 'minor' | 'major' | 'critical'

export interface IncidentUpdate {
  ts: string
  status?: IncidentStatus | ''
  message: string
  author: string
}
export interface Incident {
  id: string
  title: string
  status: IncidentStatus
  severity: IncidentSeverity
  affected_pops?: string[]
  body: string
  updates?: IncidentUpdate[]
  created_by: string
  created_at: string
  updated_at: string
  resolved_at?: string | null
}

export interface IncidentUpdatePublic {
  ts: string
  status?: IncidentStatus | ''
  message: string
}
export interface IncidentPublic {
  id: string
  title: string
  status: IncidentStatus
  severity: IncidentSeverity
  affected_pops?: string[]
  body: string
  updates?: IncidentUpdatePublic[]
  created_at: string
  updated_at: string
  resolved_at?: string | null
}

export interface IncidentCreateReq {
  title: string
  status?: IncidentStatus
  severity?: IncidentSeverity
  affected_pops?: string[]
  body: string
}
export interface IncidentPatchReq {
  title?: string
  status?: IncidentStatus
  severity?: IncidentSeverity
  affected_pops?: string[]
}
export interface IncidentUpdateReq {
  message: string
  status?: IncidentStatus
}

// ─── VPS billing tracker (internal — track our own outgoing VPS rent) ─────

export type BillingCycle = 'monthly' | 'quarterly' | 'yearly'

export interface BillingPayment {
  paid_at: string
  amount: number
  currency: string
  note?: string
  by: string
}

export interface VPSSubscription {
  id: string
  label: string
  provider: string
  node_id?: string             // optional link to a fleet PoP id
  monthly_cost: number         // amount per billing_cycle (NOT pre-normalised to monthly)
  currency: string             // ISO code: USD, HKD, CNY, JPY, EUR, SGD, ...
  billing_cycle: BillingCycle
  next_due: string             // ISO datetime
  portal_url?: string
  notes?: string
  payments?: BillingPayment[]
  created_by: string
  created_at: string
  updated_at: string
}

export interface BillingCreateReq {
  label: string
  provider: string
  node_id?: string
  monthly_cost: number
  currency: string
  billing_cycle?: BillingCycle
  next_due: string
  portal_url?: string
  notes?: string
}

export interface BillingPatchReq {
  label?: string
  provider?: string
  node_id?: string
  monthly_cost?: number
  currency?: string
  billing_cycle?: BillingCycle
  next_due?: string
  portal_url?: string
  notes?: string
}

export interface BillingPaidReq {
  amount?: number
  currency?: string
  note?: string
}

// FX rates response — rates are "1 <currency> = N CNY", so to convert
// an amount in USD to CNY: amount * rates.USD.
export interface FXRatesResponse {
  rates: Record<string, number>
  fetched_at: string
  stale: boolean
  error?: string
}

export interface FleetPublic {
  nodes: FleetPublicNode[]
  pops_online: number
  pops_total: number
  bgp_sessions: number
  routes_v6: number
  wg_total: number
  tunnels: number
  updated_at: number
}

// ─── Status page availability (heartbeat history) ──────────────────────────

export interface StatusDay {
  day: string   // YYYY-MM-DD (UTC)
  up: number
  down: number
  total: number
}
export interface StatusComponent {
  name: string
  category: string
  type: 'pop' | 'website'
  url?: string
  last_status: number      // 1 up | 0 down | -1 unknown
  last_latency_ms: number
  last_check?: string       // RFC3339 UTC
  uptime: number            // fraction 0..1 over the window
  days: StatusDay[]         // oldest→newest, gaps zero-filled
}
export interface StatusSummary {
  components: StatusComponent[]
  window_days: number
}

// Live inter-PoP RTT matrix (directed edges) for the topology map.
export interface LatencyEdge { from: string; to: string; rtt_ms: number }
export interface LatencyMatrix { edges: LatencyEdge[] }

export interface FleetNode {
  id: string
  label: string
  country: string
  address: string
  lat?: number
  lon?: number
  local: boolean
}
export interface IfaceStat { name: string; rx_bps: number; tx_bps: number }

export interface FleetNodeStatus {
  node: FleetNode
  ok: boolean
  error?: string
  hostname?: string
  iface?: string
  uptime?: string
  load_1: number
  mem_pct: number
  cpu_pct: number
  disk_pct: number
  net_rx_bps: number
  net_tx_bps: number
  ifaces?: IfaceStat[]
  mem_total?: number
  mem_used?: number
  disk_total?: number
  disk_used?: number
  bird_version?: string
  protocols?: BirdProtocol[]
  route_counts?: BirdRouteCount[]
  wg?: WGIface[]
  tunnels?: NetTunnel[]
  probes?: ConnProbe[]
  load_series?: TSSample[]
  mem_series?: TSSample[]
  cpu_series?: TSSample[]
  disk_series?: TSSample[]
  net_rx_series?: TSSample[]
  net_tx_series?: TSSample[]
  fetched_at: number
  scrape_latency?: string
}

// ── Server / PoP management (admin) ────────────────────────────────────────
// The persistent node registry behind /admin/servers. NodeRecord is the
// stored shape; NodeView adds live runtime signals for the table.
export interface NodeRecord {
  id: string
  label: string
  country: string
  address: string
  lat?: number
  lon?: number
  ssh_user?: string
  ssh_identity?: string
  ssh_port?: number
  region?: number
  node_num?: number
  arch?: string
  status: 'active' | 'decommissioned'
  notes?: string
  created_by?: string
  created_at: string
  updated_at: string
}
export interface NodeView extends NodeRecord {
  local: boolean          // the console host itself — can't be removed
  scraped: boolean        // has a cache entry yet
  ok: boolean             // last scrape succeeded
  cert_days_left: number  // agent TLS cert days remaining (0 = unknown)
}
export interface NodeCreateReq {
  id: string
  label: string
  country?: string
  address: string
  lat?: number
  lon?: number
  ssh_user?: string
  ssh_port?: number
  arch?: string
  notes?: string
}
export interface NodePatchReq {
  label?: string
  country?: string
  address?: string
  lat?: number
  lon?: number
  ssh_user?: string
  ssh_port?: number
  arch?: string
  notes?: string
}
export interface NodeProvisionResult {
  exit: number
  output: string
  error: string
}
// Geo autodetect for the add-server form (ipwho.is → Cymru/centroid fallback).
export interface GeoResult {
  country: string
  label: string
  lat: number
  lon: number
  source: string // "ipwho.is" | "cymru" | "none"
}
// Live onboarding job (key bootstrap → provision → verify).
export interface OnboardStep {
  name: string
  status: 'pending' | 'running' | 'ok' | 'fail' | 'skip'
  message?: string
  at?: number       // last update (ms epoch)
  started?: number  // entered "running" (ms epoch)
  ended?: number    // reached terminal status (ms epoch)
}
export interface OnboardState {
  node_id: string
  steps: OnboardStep[]
  log: string[]          // live, streamed provision output (capped)
  running: boolean
  done: boolean
  ok: boolean
  started_at: number
}
// Mesh / BIRD config generator (review-only; nothing is applied server-side).
export interface MeshPeerSnippet {
  node_id: string
  label: string
  transport: 'gre' | 'wg'
  bird: string     // iBGP peer block to add on THIS peer
  tunnel: string   // tunnel bring-up commands on THIS peer
}
export interface MeshConfigBundle {
  node_id: string
  region: number
  node_num: number
  anchor: string
  new_node_bird: string
  filters: string
  bringup: string[]
  peer_snippets: MeshPeerSnippet[]
  warnings?: string[]
}
export interface NodeHealthResult {
  status: number
  body: string
}

export interface OperatorListItem {
  username: string
  role: string                  // "admin" | "operator"
  created_at: string
  recovery_remaining: number
  passkeys_count: number
  has_totp: boolean
  approved: boolean
  invited_by?: string
  invited_at?: string
}
export interface OperatorCreateResult {
  username: string
  role: string
  password: string              // one-time display only
  recovery_codes: string[]      // one-time display only
  created_at: string
}

export interface InvitePreview {
  role: string
  invited_by: string
  expires_at: string
  expires_in: number
}
// One registered SSH public key on the calling operator's record.
// The full PublicKey string isn't returned in lists — only fingerprint /
// type / label / timestamps. Adding requires the authorized_keys-format
// line; the server parses + canonicalizes.
export interface SSHKeyRecord {
  id:            string
  label:         string
  fingerprint:   string  // "SHA256:..."
  type:          string  // "ssh-ed25519", "sk-ssh-ed25519@openssh.com", etc.
  created_at:    number  // unix
  last_used_at:  number  // unix; 0 if never used
}

// Long-lived bearer token for CLI / script auth. Created via UI and
// shown ONCE — UI must surface the plaintext to the user (with "copy
// now" warning) since the server only stores a bcrypt hash afterward.
// List view returns metadata only.
export interface APITokenRecord {
  id:            string
  label:         string
  prefix_hint:   string  // first 10 chars of plaintext + "…", e.g. "ncntok_aBc…"
  created_at:    number
  last_used_at:  number  // 0 if never used
  expires_at:    number  // 0 = no expiry
}
// The one-time response from a successful token create. The `token` field
// is the FULL plaintext — show with a one-time-only banner, never persist
// it to local storage.
export interface APITokenCreateResult {
  id:         string
  label:      string
  token:      string     // ONLY shown once; subsequent list calls hide it
  created_at: number
  expires_at: number
}

export interface InviteRecord {
  token: string                 // short prefix only, never full token after issuance
  role: string
  invited_by: string
  created_at: string
  expires_at: string
  used: boolean
  used_by?: string
  used_at?: string
  // New: invite-by-email metadata. Empty on legacy tokens that predate
  // this field (admin still has those to revoke; resend is blocked
  // server-side for those because there's no destination address).
  invitee_email?: string
  invitee_name?: string
  // mail_status — "sent" on success, "failed: <reason>" on send failure,
  // empty on legacy tokens. Admin UI surfaces this so they know whether
  // they need to copy the URL manually or hit "resend".
  mail_status?: string
}
export interface InviteCreateResult {
  token: string                 // full token (one-time display)
  role: string
  url: string                   // full /invite/<token> URL
  expires_at: string
  expires_in: number
  invitee_email?: string
  invitee_name?: string
  mail_status?: string
}
export interface InviteCompleteResult {
  username: string
  role: string
  approved: false
  invited_by: string
  recovery_codes: string[]
  status: string
}

export interface PasskeyRecord {
  id: string         // base64url of credential ID
  name: string
  created_at: string
  sign_count: number
  transport?: string[]
}

export interface RecoverPasswordRequest {
  username: string
  recovery_code: string
  new_password: string
}

export interface LoginRequest {
  username: string
  password: string
  totp_code?: string        // optional now — server returns totp_required when needed
  turnstile_token?: string  // Cloudflare Turnstile widget output (required in prod)
}

// LoginResponse — when totp_required is true, no session cookie was set
// and the client should call authLoginVerifyTOTP next. Otherwise the
// session is live and the regular dashboard flow applies.
export interface LoginResponse {
  operator: string
  role?: string
  issued_at?: number
  expires_at?: number
  totp_required?: boolean
  intent_expires?: number
}

export interface LoginVerifyTOTPRequest {
  totp_code: string
  trust_device: boolean
}

export interface TrustedDevice {
  id: string
  label: string
  user_agent?: string
  created_at: number
  last_seen_at: number
  last_seen_ip?: string
  current?: boolean
}

import type { MeData } from '@/stores/session'

export interface VisitorInfo {
  ip: string
  ipv6: boolean
  asn?: string         // "AS13335"
  as_org?: string      // "CLOUDFLARENET"
  country?: string     // ISO alpha-2
  prefix?: string
  registry?: string
  allocated_at?: string
  source?: string
}

export type LGTool = 'ping4' | 'ping6' | 'trace4' | 'trace6' | 'bgp_route'

export interface LGRequest {
  tool: LGTool
  target: string
}

export interface BGPSession {
  name: string
  proto: string
  state: string
  info: string
  status: 'established' | 'connect' | 'passive' | 'down'
  neighbor_addr: string
  neighbor_as: number
  routes_imported: number
  routes_exported: number
}
export interface LGNodeSessions {
  id: string
  label: string
  country: string
  local: boolean
  ready: boolean
  sessions: BGPSession[]
  counts: Record<string, number>
}
export interface LGSessions {
  nodes: LGNodeSessions[]
  default: string
}

async function request<T>(
  path: string,
  init: RequestInit & { method: string }
): Promise<Envelope<T>> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    credentials: 'include', // send session cookie cross-port in dev
    headers: {
      Accept: 'application/json',
      ...(init.body ? { 'Content-Type': 'application/json' } : {}),
      ...(init.headers ?? {})
    }
  })

  // 401 surfaces via the envelope, but we also broadcast a global event so
  // the router guard can drop to /login from anywhere in the app.
  if (res.status === 401) {
    window.dispatchEvent(new CustomEvent('ncn:unauthorized'))
  }

  // Try JSON regardless of status — server always emits envelope JSON.
  return (await res.json()) as Envelope<T>
}

const get  = <T>(path: string, signal?: AbortSignal) =>
  request<T>(path, { method: 'GET', signal })

const post = <T>(path: string, body: unknown, signal?: AbortSignal) =>
  request<T>(path, { method: 'POST', body: JSON.stringify(body), signal })

async function del<T>(path: string): Promise<Envelope<T>> {
  return request<T>(path, { method: 'DELETE' })
}

const patch = <T>(path: string, body: unknown, signal?: AbortSignal) =>
  request<T>(path, { method: 'PATCH', body: JSON.stringify(body), signal })

export const api = {
  // Auth
  authLogin:  (req: LoginRequest)  => post<LoginResponse>('/auth/login',  req),
  // Step 2 of the password-path login. Reads the short-lived intent
  // cookie set by step 1; submits the TOTP code + the "trust this
  // device" flag. On success the server sets the session cookie and
  // (if trusted) the long-lived device-trust cookie.
  authLoginVerifyTOTP: (req: LoginVerifyTOTPRequest) =>
    post<LoginResponse>('/auth/login/verify-totp', req),
  authLogout: ()                   => post<{status: string}>('/auth/logout', {}),
  authMe:     ()                   => get<MeData>('/auth/me'),

  // Trusted device management (admin Security page)
  devicesList:   () => get<{ devices: TrustedDevice[] }>('/auth/devices'),
  devicesRevoke: (id: string) =>
    del<{ removed: string; was_current: boolean }>(`/auth/devices?id=${encodeURIComponent(id)}`),

  // Forgot password (public)
  authRecover: (req: RecoverPasswordRequest) =>
    post<{ operator: string; remaining_codes: number }>('/auth/recover', req),

  // Recovery codes status (auth)
  authRecoveryStatus: () => get<{ remaining: number }>('/auth/recovery-status'),

  // Change password (auth — proves current pw too)
  authChangePassword: (current_password: string, new_password: string) =>
    post<{ operator: string; changed_at: string }>(
      '/auth/change-password',
      { current_password, new_password }
    ),

  // TOTP enrollment (first-login MFA path)
  totpSetupBegin:   () =>
    post<{ secret: string; otpauth: string }>('/auth/totp/setup-begin', {}),
  totpSetupConfirm: (secret: string, code: string) =>
    post<{ operator: string; status: string }>('/auth/totp/setup-confirm', { secret, code }),

  // Operator account management.
  // GET — visible to any authed user (transparency list).
  // POST / DELETE / PATCH — admin only (backend enforces 403).
  operatorsList:   () =>
    get<OperatorListItem[]>('/auth/operators'),
  operatorsCreate: (req: { username: string; role: string; password?: string }) =>
    post<OperatorCreateResult>('/auth/operators', req),
  operatorsDelete: (username: string) =>
    del<{ deleted: string }>(`/auth/operators?username=${encodeURIComponent(username)}`),
  operatorsUpdate: (req: { username: string; role: string }) =>
    patch<{ operator: string; role: string }>('/auth/operators', req),
  operatorsApprove: (username: string) =>
    post<{ operator: string; approved: true }>('/auth/operators/approve', { username }),

  // Operator → webmail self-invite (HMAC-signed bridge to ncn-mail on pop-03)
  mailSelfInvite: () =>
    post<{
      token: string
      url: string
      expires_at: string
      operator: string
      role: string
    }>('/auth/mail-self-invite', {}),

  // Admin-driven role mailbox recovery: mints a one-shot URL for one of
  // {postmaster, noc, hostmaster, abuse, security}. Admin-only.
  mailRoleRecover: (mailbox: string) =>
    post<{
      mailbox: string      // e.g. "noc@example.com"
      url: string          // the recovery URL to open in a browser
      expires_at: string   // ISO 8601
    }>('/auth/mail-role-recover', { mailbox }),

  // Forgot-password queue mirror (proxied from ncn-mail on pop-03 via the
  // operator-bridge HMAC). Admin-only. List actionable entries (self-
  // recovery entries are filtered out) and dismiss by id.
  mailForgotList: () =>
    get<MailForgotEntry[]>('/auth/mail-forgot'),
  mailForgotDismiss: (id: string) =>
    del<unknown>(`/auth/mail-forgot/${encodeURIComponent(id)}`),
  // Approve & send link — asks ncn-mail to mint a one-shot mailbox-recover
  // URL for the requester and email it directly. Removes the entry from
  // the queue on success.
  mailForgotApprove: (id: string) =>
    post<{ mailbox: string; sent_to: string; expires_at: string }>(
      `/auth/mail-forgot/${encodeURIComponent(id)}/approve`, {}),

  // SSO: mint a 60-second HMAC ticket the webmail will accept to issue
  // a parallel mailbox session. The mapped mailbox is the operator's
  // username (lowercased) at example.com; if it doesn't exist the
  // webmail returns 404 and the user gets a clear "self-register first"
  // message.
  ssoMailTicket: () =>
    post<{ url: string; mailbox: string; expires_at: string }>(
      '/auth/sso/mail-ticket', {}
    ),

  // Peering applications — admin-only review.
  peeringList: () =>
    get<PeeringApplication[]>('/auth/peering/applications'),
  peeringDecide: (id: string, status: 'approved' | 'rejected', admin_notes: string) =>
    post<{ id: string; status: string }>(
      `/auth/peering/applications/${encodeURIComponent(id)}/decide`,
      { status, admin_notes }
    ),
  // Peering/IRR automation (peerApply.go): generate a per-peer BIRD config from
  // an approved application, list generations, and confirm-gated apply.
  peerConfig: (body: { app_id: string; neighbor_v6: string; target_node?: string; max_prefix6?: number }) =>
    post<PeerGeneration>('/auth/peering/peer-config', body),
  peerGens: () => get<PeerGeneration[]>('/auth/peering/peer-gens'),
  peerApply: (body: { asn: number; confirm: string; target_nodes: string[] }) =>
    post<{ applied: string[]; failed: Record<string, string>; log: string }>('/auth/peering/peer-apply', body),

  // Security audit log — admin-only. Server filters + paginates; client
  // assembles query string from the AuditFilter shape.
  auditQuery: (f: AuditFilter = {}) => {
    const qs = new URLSearchParams()
    if (f.event)    qs.set('event', f.event)
    if (f.actor)    qs.set('actor', f.actor)
    if (f.severity) qs.set('severity', f.severity)
    if (f.outcome)  qs.set('outcome', f.outcome)
    if (f.q)        qs.set('q', f.q)
    if (f.since)    qs.set('since', f.since)
    if (f.until)    qs.set('until', f.until)
    if (f.limit)    qs.set('limit', String(f.limit))
    if (f.cursor)   qs.set('cursor', f.cursor)
    const s = qs.toString()
    return get<AuditQueryResult>('/auth/audit' + (s ? '?' + s : ''))
  },
  auditStats: () => get<AuditStats>('/auth/audit/stats'),
  // Export URL is GET-only and meant to be opened directly via window.location
  // so the browser triggers a file download. Returns just the URL string.
  auditExportURL: (f: AuditFilter = {}) => {
    const qs = new URLSearchParams()
    if (f.event)    qs.set('event', f.event)
    if (f.actor)    qs.set('actor', f.actor)
    if (f.severity) qs.set('severity', f.severity)
    if (f.outcome)  qs.set('outcome', f.outcome)
    if (f.q)        qs.set('q', f.q)
    if (f.since)    qs.set('since', f.since)
    if (f.until)    qs.set('until', f.until)
    const s = qs.toString()
    return '/api/v1/auth/audit/export' + (s ? '?' + s : '')
  },

  // Break-glass recovery via signed URL minted by `ncn-api admin mint-recover`
  // on tyo. preview validates the token + returns the resolved username;
  // submit replaces the password and burns the nonce.
  bootstrapRecoverPreview: (token: string) =>
    get<{ user: string }>(`/auth/bootstrap-recover/preview?token=${encodeURIComponent(token)}`),
  bootstrapRecoverSubmit: (token: string, new_password: string) =>
    post<{ user: string; login_url: string }>('/auth/bootstrap-recover', { token, new_password }),

  // Invite system
  invitesList:    () =>
    get<InviteRecord[]>('/auth/invites'),
  // Invite-by-email — admin enters the invitee's address (and an optional
  // display name) and the backend dispatches an email with the registration
  // URL via the operator-mail-bridge to webmail. The `invitee_email` field
  // is required server-side; the call returns mail_status ("sent" / "failed: …").
  invitesCreate:  (role: 'operator', opts: { invitee_email: string; invitee_name?: string }) =>
    post<InviteCreateResult>('/auth/invites', { role, ...opts }),
  invitesResend:  (tokenPrefix: string) =>
    post<{ token: string; mail_status: string }>(
      `/auth/invites/${encodeURIComponent(tokenPrefix)}/resend`, {}),
  invitesRevoke:  (token: string) =>
    del<{ revoked: string }>(`/auth/invites?token=${encodeURIComponent(token)}`),

  invitePreview:  (token: string) =>
    get<InvitePreview>(`/auth/invite/preview?token=${encodeURIComponent(token)}`),
  invitePasskeyBegin: (token: string, username: string) =>
    post<{ challenge_id: string; options: unknown }>('/auth/invite/passkey/begin',
      { token, username }),
  inviteComplete: (req: {
    token: string
    username: string
    password: string
    totp?: { secret: string; code: string }
    passkey?: { name?: string; challenge_id: string; response: unknown }
  }) =>
    post<InviteCompleteResult>('/auth/invite/complete', req),

  // Terminal: mint a single-use ticket after an MFA step-up (TOTP code or
  // WebAuthn passkey assertion). The session cookie proves password — we
  // don't re-ask for it here.
  termTicket: (req: {
    node: string
    totp_code?: string
    passkey?: { challenge_id: string; response: unknown }
  }) =>
    post<{ ticket: string; expires_in: number }>('/term/ticket', req),

  // Terminal: passkey step-up assertion challenge for the current operator.
  termPasskeyBegin: () =>
    post<{ challenge_id: string; options: unknown }>('/term/passkey-begin', {}),

  // Passkey — public login flow
  passkeyLoginBegin:  () =>
    post<{ challenge_id: string; options: any }>('/auth/passkey/login/begin', {}),
  passkeyLoginFinish: (challenge_id: string, response: any) =>
    post<{ operator: string; issued_at: number; expires_at: number }>(
      '/auth/passkey/login/finish',
      { challenge_id, response }
    ),

  // Passkey — auth-protected mgmt
  passkeyList:        () => get<PasskeyRecord[]>('/auth/passkey'),
  passkeyRegBegin:    () =>
    post<{ challenge_id: string; options: any }>('/auth/passkey/register/begin', {}),
  passkeyRegFinish:   (challenge_id: string, name: string, response: any) =>
    post<{ name: string; credential_id: string; created_at: string }>(
      '/auth/passkey/register/finish',
      { challenge_id, name, response }
    ),
  passkeyDelete:      (id: string) =>
    del<{ removed: string }>(`/auth/passkey/delete?id=${encodeURIComponent(id)}`),

  // SSH keys — per-operator self-service. Adding requires the authorized_keys
  // line; the server parses, canonicalizes, and rejects malformed input.
  sshKeysList:    () => get<SSHKeyRecord[]>('/auth/ssh-keys'),
  sshKeyAdd:      (label: string, public_key: string) =>
    post<SSHKeyRecord>('/auth/ssh-keys', { label, public_key }),
  sshKeyDelete:   (id: string) =>
    del<{ removed: string }>(`/auth/ssh-keys/${encodeURIComponent(id)}`),

  // API tokens — bearer-auth credentials for CLI/scripts. List returns
  // metadata only; create returns the plaintext ONCE; delete revokes
  // immediately.
  apiTokensList:    () => get<APITokenRecord[]>('/auth/api-tokens'),
  apiTokenCreate:   (label: string, expires_in?: number) =>
    post<APITokenCreateResult>('/auth/api-tokens', { label, expires_in: expires_in ?? 0 }),
  apiTokenDelete:   (id: string) =>
    del<{ removed: string }>(`/auth/api-tokens/${encodeURIComponent(id)}`),

  // Public: visitor whois
  visitor:    ()                   => get<VisitorInfo>('/visitor'),

  // Public: sanitized fleet snapshot for the landing page
  fleetPublic: (signal?: AbortSignal) => get<FleetPublic>('/fleet/public', signal),

  // Public: status-page incidents (last 30 days, no auth)
  incidentsPublic: (signal?: AbortSignal) => get<IncidentPublic[]>('/incidents/public', signal),

  // Public: status-page availability summary (components + 90-day uptime, no auth)
  statusSummary: (signal?: AbortSignal) => get<StatusSummary>('/status/summary', signal),

  // Public: per-(target,PoP) SLA — availability/loss/latency vs SLO (no auth)
  statusSLA: (signal?: AbortSignal) => get<SLAData>('/status/sla', signal),
  // Admin: read / replace the SLA target list.
  slaTargets: (signal?: AbortSignal) => get<{ targets: SLATarget[] }>('/auth/sla/targets', signal),
  setSLATargets: (targets: SLATarget[]) => post<{ targets: SLATarget[] }>('/auth/sla/targets', { targets }),
  // Netflow/sFlow — top talkers + composition over the current window.
  flowTop: (signal?: AbortSignal) => get<FlowTop>('/auth/flow/top', signal),
  // DDoS mitigation — list / create draft / apply (confirm-gated) / revoke.
  ddosList: (signal?: AbortSignal) => get<{ rules: FlowspecRule[] }>('/auth/ddos', signal),
  ddosCreate: (rule: Partial<FlowspecRule>) => post<{ rule: FlowspecRule; nft: string }>('/auth/ddos/create', rule),
  ddosApply: (id: string, confirm: string, nodes: string[]) =>
    post<{ applied: string[]; failed: Record<string, string>; logs: Record<string, string> }>('/auth/ddos/apply', { id, confirm, nodes }),
  ddosRevoke: (id: string) => post<{ revoked: string }>('/auth/ddos/revoke', { id }),
  // On-call rotation + escalation policy.
  oncall: (signal?: AbortSignal) => get<OncallData>('/auth/oncall', signal),
  setOncall: (config: OncallConfig) => post<{ config: OncallConfig; current: string }>('/auth/oncall', config),
  // Config drift — per-node status, diff (declared vs live), adopt baseline, rollback BIRD.
  drift: (signal?: AbortSignal) => get<{ nodes: DriftState[] }>('/auth/drift', signal),
  configDiff: (node: string) => get<{ declared: ConfigDecl; live: ConfigDecl }>('/auth/config-diff?node=' + encodeURIComponent(node)),
  configAdopt: (node: string) => post<{ captured_at: number }>('/auth/config-adopt?node=' + encodeURIComponent(node), {}),
  configRollback: (node: string, confirm: string) => post<{ log: string }>('/auth/config-rollback?node=' + encodeURIComponent(node), { confirm }),

  // Public: live inter-PoP RTT matrix (topology map latency, no auth)
  statusLatency: (signal?: AbortSignal) => get<LatencyMatrix>('/status/latency', signal),

  // Admin incidents CRUD
  incidentsList:      (signal?: AbortSignal) => get<Incident[]>('/auth/incidents', signal),
  incidentsCreate:    (req: IncidentCreateReq) => post<Incident>('/auth/incidents', req),
  incidentsPatch:     (id: string, req: IncidentPatchReq) =>
    patch<Incident>('/auth/incidents/' + encodeURIComponent(id), req),
  incidentsAddUpdate: (id: string, req: IncidentUpdateReq) =>
    post<Incident>('/auth/incidents/' + encodeURIComponent(id) + '/updates', req),
  incidentsDelete:    (id: string) => del<null>('/auth/incidents/' + encodeURIComponent(id)),

  // VPS billing (our own VPS rent tracker, NOT customer-facing)
  billingList:     (signal?: AbortSignal) => get<VPSSubscription[]>('/auth/billing/subscriptions', signal),
  billingCreate:   (req: BillingCreateReq) => post<VPSSubscription>('/auth/billing/subscriptions', req),
  billingPatch:    (id: string, req: BillingPatchReq) =>
    patch<VPSSubscription>('/auth/billing/subscriptions/' + encodeURIComponent(id), req),
  billingDelete:   (id: string) => del<null>('/auth/billing/subscriptions/' + encodeURIComponent(id)),
  billingMarkPaid: (id: string, req: BillingPaidReq = {}) =>
    post<VPSSubscription>('/auth/billing/subscriptions/' + encodeURIComponent(id) + '/paid', req),

  // FX rates (CNY-base, ie. "1 X = N CNY" — multiply source amount to get CNY)
  fxRates: (signal?: AbortSignal) => get<FXRatesResponse>('/auth/fx/rates', signal),

  // Public: cached PeeringDB record
  peeringDB:   (signal?: AbortSignal) => get<PeeringDBSnapshot>('/peeringdb', signal),

  // Looking Glass (public)
  lgExec:     (req: LGRequest, signal?: AbortSignal) => post<CmdResult>('/lg/exec', req, signal),
  lgSessions: (signal?: AbortSignal)                 => get<LGSessions>('/lg/sessions', signal),

  // Operator-protected: monitoring
  perf:           (signal?: AbortSignal) => get<PerfData>('/perf', signal),
  connectivity:   (signal?: AbortSignal) => get<ConnectivityData>('/connectivity', signal),
  bird:           (signal?: AbortSignal) => get<BirdData>('/bird', signal),
  birdProtocol:   (name: string, node?: string, signal?: AbortSignal) => {
    const q = new URLSearchParams({ name })
    if (node) q.set('node', node)
    return get<{ name: string; node: string; raw: string; exit: number; duration: string }>(
      `/bird/protocol?${q.toString()}`, signal
    )
  },
  alerts:         (signal?: AbortSignal) => get<AlertsData>('/alerts', signal),

  // Fleet (multi-PoP) status
  fleet:          (signal?: AbortSignal) => get<FleetNodeStatus[]>('/fleet', signal),

  // Server / PoP management (admin) — runtime-editable node registry.
  nodesList:        (signal?: AbortSignal) => get<NodeView[]>('/auth/nodes', signal),
  nodeCreate:       (req: NodeCreateReq) => post<NodeRecord>('/auth/nodes', req),
  nodePatch:        (id: string, req: NodePatchReq) =>
    patch<NodeRecord>('/auth/nodes/' + encodeURIComponent(id), req),
  nodeDecommission: (id: string) =>
    post<NodeRecord>('/auth/nodes/' + encodeURIComponent(id) + '/decommission', {}),
  nodeRecommission: (id: string) =>
    post<NodeRecord>('/auth/nodes/' + encodeURIComponent(id) + '/recommission', {}),
  nodeDelete:       (id: string) => del<null>('/auth/nodes/' + encodeURIComponent(id)),
  nodeProvision:    (id: string) =>
    post<NodeProvisionResult>('/auth/nodes/' + encodeURIComponent(id) + '/provision', {}),
  nodeHealth:       (id: string, signal?: AbortSignal) =>
    get<NodeHealthResult>('/auth/nodes/' + encodeURIComponent(id) + '/health', signal),
  nodeGeo:          (address: string, signal?: AbortSignal) =>
    get<GeoResult>('/auth/nodes/geo?address=' + encodeURIComponent(address), signal),
  nodeOnboardStart: (id: string, body: { ssh_user?: string; ssh_password?: string; ssh_private_key?: string; ssh_key_passphrase?: string }) =>
    post<OnboardState>('/auth/nodes/' + encodeURIComponent(id) + '/onboard', body),
  nodeOnboardStatus: (id: string, signal?: AbortSignal) =>
    get<OnboardState | null>('/auth/nodes/' + encodeURIComponent(id) + '/onboard', signal),
  // Anycast drain/undrain: withdraw/restore a PoP's upstream BGP announcements.
  anycastState:   (id: string, signal?: AbortSignal) =>
    get<AnycastState>('/auth/nodes/' + encodeURIComponent(id) + '/anycast', signal),
  anycastDrain:   (id: string) =>
    post<{ output: string; state: AnycastState }>('/auth/nodes/' + encodeURIComponent(id) + '/anycast/drain', { confirm: 'DRAIN ' + id }),
  anycastUndrain: (id: string) =>
    post<{ output: string; state: AnycastState }>('/auth/nodes/' + encodeURIComponent(id) + '/anycast/undrain', {}),
  nodeMeshConfig:   (id: string, body: { transports?: Record<string, string>; region?: number }) =>
    post<MeshConfigBundle>('/auth/nodes/' + encodeURIComponent(id) + '/mesh-config', body),
  nodeMeshApply:    (id: string, body: { targets: string[]; transports?: Record<string, string>; region?: number; confirm: string }) =>
    post<OnboardState>('/auth/nodes/' + encodeURIComponent(id) + '/mesh-apply', body),
  nodeMeshApplyStatus: (id: string, signal?: AbortSignal) =>
    get<OnboardState | null>('/auth/nodes/' + encodeURIComponent(id) + '/mesh-apply', signal),

  // Alert rules / groups (user-editable, admin-gated).
  alertRulesList:  (signal?: AbortSignal) => get<AlertRulesConfig>('/auth/alert-rules', signal),
  alertRuleCreate: (req: Partial<AlertRuleDef>) => post<AlertRuleDef>('/auth/alert-rules', req),
  alertRulePatch:  (id: string, req: Partial<AlertRuleDef>) =>
    patch<AlertRuleDef>('/auth/alert-rules/' + encodeURIComponent(id), req),
  alertRuleDelete: (id: string) => del<null>('/auth/alert-rules/' + encodeURIComponent(id)),
  alertPreview:    (req: { metric: string; op: AlertOp; threshold: number; node_ids?: string[]; regions?: number[] }) =>
    post<{ results: AlertPreviewResult[]; firing_count: number }>('/auth/alert-preview', req),
  alertGroupCreate: (req: Partial<RuleGroup>) => post<RuleGroup>('/auth/alert-groups', req),
  alertGroupPatch:  (id: string, req: Partial<RuleGroup>) =>
    patch<RuleGroup>('/auth/alert-groups/' + encodeURIComponent(id), req),
  alertGroupDelete: (id: string) => del<null>('/auth/alert-groups/' + encodeURIComponent(id)),
  alertMetrics:    (signal?: AbortSignal) => get<MetricMeta[]>('/auth/alert-metrics', signal),
  alertAck:        (id: string) => post<{ id: string; acked_by: string }>('/auth/alerts/ack', { id }),

  // OAuth / external identity binding
  oauthProviders:  (signal?: AbortSignal) =>
    get<{ enabled: string[] }>('/auth/oauth/providers', signal),
  oauthIdentities: (signal?: AbortSignal) =>
    get<{ identities: OAuthIdentity[]; enabled_providers: string[] }>('/auth/oauth-identities', signal),
  oauthBindStart:  (provider: string) =>
    post<{ auth_url: string }>('/auth/oauth-bind/' + encodeURIComponent(provider), {}),
  oauthUnbind:     (provider: string) =>
    del<null>('/auth/oauth-identities/' + encodeURIComponent(provider)),

  // Bot-driven Telegram bind (/bind command → /admin/bind page).
  tgBindPeek:    (token: string, signal?: AbortSignal) =>
    get<{ telegram_id: string; telegram_username: string }>('/auth/telegram/bind-ticket?t=' + encodeURIComponent(token), signal),
  tgBindConfirm: (token: string) =>
    post<{ provider: string; telegram_username: string }>('/auth/telegram/bind-ticket', { token }),

  // DeepSeek-backed console assistant.
  aiChat: (messages: Array<{ role: string; content: string }>, signal?: AbortSignal) =>
    post<{ reply: string }>('/auth/ai/chat', { messages }, signal),

  // DeepSeek ops AGENT — tool-calling loop; writes pause for human approval.
  aiAgent: (messages: AiMsg[]) =>
    post<AgentResult>('/auth/ai/agent', { messages }),
  aiAgentApprove: (messages: AiMsg[], toolCallId: string, decision: 'approve' | 'deny') =>
    post<AgentResult>('/auth/ai/agent/approve', { messages, tool_call_id: toolCallId, decision }),

  // RPKI ROA-validity of our own announced prefixes (rpki.go).
  rpki: (signal?: AbortSignal) => get<RpkiState>('/auth/rpki', signal),
  // Force an immediate RPKI re-poll (auto-poll interval is operator-adjustable).
  rpkiRefresh: () => post<RpkiState>('/auth/rpki/refresh', {}),
  // Set the RPKI auto-poll interval (seconds, clamped server-side to [5m, 7d]).
  rpkiSetInterval: (seconds: number) => post<RpkiState>('/auth/rpki/interval', { seconds }),
  // Capacity planning — per-node daily trends + link-saturation forecast.
  capacity: (signal?: AbortSignal) => get<CapacityData>('/auth/capacity', signal),
  setLinkCapacity: (node: string, mbps: number) => post<{ nodes: CapNodeView[] }>('/auth/capacity/link', { node, mbps }),
  // Prefix-hijack detection — live RIS Live stream (hijack.go).
  hijack: (signal?: AbortSignal) => get<HijackState>('/auth/hijack', signal),

  // Per-purpose AI model selection.
  aiModels: (signal?: AbortSignal) =>
    get<AiModelConfig>('/auth/ai/models', signal),
  aiModelSet: (purpose: string, model: string) =>
    post<AiModelConfig>('/auth/ai/models', { purpose, model }),

  // Conversation history (per operator).
  aiConversations: (signal?: AbortSignal) =>
    get<{ conversations: ConvMeta[] }>('/auth/ai/conversations', signal),
  aiConversationGet: (id: string, signal?: AbortSignal) =>
    get<{ id: string; title: string; messages: AiMsg[] }>('/auth/ai/conversations/' + encodeURIComponent(id), signal),
  aiConversationSave: (id: string, messages: AiMsg[]) =>
    post<{ id: string }>('/auth/ai/conversations', { id, messages }),
  aiConversationDelete: (id: string) =>
    del<null>('/auth/ai/conversations/' + encodeURIComponent(id)),

  // Memory (per operator; the agent also writes via remember/forget tools).
  aiMemory: (signal?: AbortSignal) =>
    get<{ memory: MemoryItem[] }>('/auth/ai/memory', signal),
  aiMemoryAdd: (text: string) =>
    post<{ memory: MemoryItem[] }>('/auth/ai/memory', { text }),
  aiMemoryDelete: (id: string) =>
    del<null>('/auth/ai/memory/' + encodeURIComponent(id)),

  // ── Wiki ──────────────────────────────────────────────────────────────
  // Public tier (anonymous; server forces is_public).
  wikiTree:    (signal?: AbortSignal) => get<WikiMeta[]>('/wiki/tree', signal),
  wikiPage:    (path: string, signal?: AbortSignal) => get<WikiPage>('/wiki/page?path=' + encodeURIComponent(path), signal),
  wikiSearch:  (q: string, signal?: AbortSignal) => get<WikiHit[]>('/wiki/search?q=' + encodeURIComponent(q), signal),
  // Internal read tier (any logged-in operator; all pages).
  wikiTreeAuth:   (signal?: AbortSignal) => get<WikiMeta[]>('/auth/wiki/tree', signal),
  wikiPageAuth:   (path: string, signal?: AbortSignal) => get<WikiPage>('/auth/wiki/page?path=' + encodeURIComponent(path), signal),
  wikiSearchAuth: (q: string, signal?: AbortSignal) => get<WikiHit[]>('/auth/wiki/search?q=' + encodeURIComponent(q), signal),
  // Admin write tier.
  wikiSave:     (req: WikiSaveReq) => post<WikiPage>('/auth/wiki/save', req),
  wikiDelete:   (path: string) => del<{ deleted: string }>('/auth/wiki/delete?path=' + encodeURIComponent(path)),
  wikiVersions: (path: string, signal?: AbortSignal) => get<WikiVersion[]>('/auth/wiki/versions?path=' + encodeURIComponent(path), signal),
  wikiRevert:   (path: string, versionId: number) => post<WikiPage>('/auth/wiki/revert', { path, version_id: versionId }),
}

export interface ConvMeta { id: string; title: string; updated_at: number }
export interface MemoryItem { id: string; text: string; created_at: number }

export interface RpkiPrefix { prefix: string; validity: 'valid' | 'invalid' | 'unknown'; roas: number }
export interface RpkiRov { node: string; established: boolean; vrps: number; valid: number; invalid: number; unknown: number }
export interface RpkiState {
  asn: string; checked_at: number
  prefixes: RpkiPrefix[]
  valid: number; invalid: number; unknown: number
  rov?: RpkiRov
  interval_secs: number
  error?: string
}

export interface CapDay { day: string; max: number; mean: number; p95: number; samples: number }
export interface CapNodeView {
  node: string
  capacity_mbps: number
  eta_days: number // -1 = unknown
  series: Record<string, CapDay[]>
}
export interface CapacityData { nodes: CapNodeView[]; metrics: string[] }

export interface SLATarget { name: string; target: string; type: 'ping4' | 'ping6'; slo_pct: number; rtt_budget_ms: number }
export interface SLAPopStat {
  pop: string; sent: number; ok: number
  avail_pct: number; loss_pct: number; mean_rtt_ms: number; max_rtt_ms: number; meets_slo: boolean
}
export interface SLATargetView extends SLATarget { pops: SLAPopStat[] }
export interface SLAData { targets: SLATargetView[]; window_days: number }

export interface FlowspecRule {
  id: string; family?: string; src?: string; dst?: string; proto?: string
  src_port?: number; dst_port?: number; action: 'drop' | 'rate'; rate_pps?: number
  ttl_secs: number; note?: string; created_by: string; created_at: number
  expires_at?: number; applied_pops?: string[]; status: string
}

export interface FlowEntry { key: string; bytes: number; packets: number }
export interface FlowTop {
  window_secs: number; flows: number
  src_ip: FlowEntry[]; dst_ip: FlowEntry[]; port: FlowEntry[]; proto: FlowEntry[]
  src_as: FlowEntry[]; dst_as: FlowEntry[]
  in_bytes: number; out_bytes: number; transit_bytes: number
}

export interface EscalationTier { after_min: number; target: 'oncall' | 'admins' | 'group' }
export interface OncallConfig { rotation: string[]; start_date: string; period_days: number; tiers: EscalationTier[] }
export interface OncallData { config: OncallConfig; current: string; operators: string[] }

export interface DriftState {
  node_id: string; has_baseline: boolean
  bird_drift: boolean; filters_drift: boolean; nft_drift: boolean; drift: boolean
  checked_at: number; captured_at?: number; captured_by?: string; error?: string
}
export interface ConfigDecl {
  node_id: string; bird_conf: string; filters: string; nft: string
  bird_hash: string; filters_hash: string; nft_hash: string; captured_at: number; captured_by: string
}

export interface HijackEvent { prefix: string; origin: string; peer: string; as_path: string; seen_at: number }
export interface HijackState {
  connected: boolean
  watching: string[]
  events: HijackEvent[]
  checked_at: number
  error?: string
}

export interface AgentStreamHandlers {
  onTool?: (name: string, summary: string) => void
  onText?: (delta: string) => void
  onDone?: (res: AgentResult) => void
  onError?: (msg: string) => void
}

// streamAgent POSTs to an SSE endpoint and dispatches `tool`/`text`/`done`/
// `error` events. Returns when the stream ends. Uses fetch + ReadableStream
// (EventSource can't POST a body / send cookies the same way).
async function streamAgent(path: string, body: unknown, h: AgentStreamHandlers, signal?: AbortSignal): Promise<void> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST', credentials: 'include', signal,
    headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
    body: JSON.stringify(body),
  })
  if (!res.ok || !res.body) {
    h.onError?.('stream failed: ' + res.status)
    return
  }
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buf += dec.decode(value, { stream: true })
    // SSE events are separated by a blank line.
    let sep
    while ((sep = buf.indexOf('\n\n')) >= 0) {
      const raw = buf.slice(0, sep)
      buf = buf.slice(sep + 2)
      let event = 'message', data = ''
      for (const line of raw.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) data += line.slice(5).trim()
      }
      if (!data) continue
      let parsed: unknown
      try { parsed = JSON.parse(data) } catch { continue }
      const p = parsed as Record<string, unknown>
      if (event === 'tool') h.onTool?.(String(p.name ?? ''), String(p.summary ?? ''))
      else if (event === 'text') h.onText?.(String(p.delta ?? ''))
      else if (event === 'done') h.onDone?.(parsed as AgentResult)
      else if (event === 'error') h.onError?.(String(p.error ?? 'error'))
    }
  }
}

export const aiStream = {
  agent: (messages: AiMsg[], h: AgentStreamHandlers, signal?: AbortSignal) =>
    streamAgent('/auth/ai/agent/stream', { messages }, h, signal),
  approve: (messages: AiMsg[], toolCallId: string, decision: 'approve' | 'deny', h: AgentStreamHandlers, signal?: AbortSignal) =>
    streamAgent('/auth/ai/agent/approve/stream', { messages, tool_call_id: toolCallId, decision }, h, signal),
}

export interface WikiMeta { path: string; title: string; is_public: boolean; sort: number; updated_by: string; updated_at: string }
export interface WikiPage extends WikiMeta { content: string; locale: string; version: number }
export interface WikiVersion { id: number; path: string; title: string; version: number; edited_by: string; edited_at: string }
export interface WikiHit { path: string; title: string; snippet: string }
export interface WikiSaveReq { path: string; title: string; content: string; locale?: string; is_public: boolean; sort?: number }

export interface AiModelConfig { available: string[]; purposes: Record<string, string>; order: string[] }

export interface AiToolCall { id: string; type: string; function: { name: string; arguments: string } }
export interface AiMsg {
  role: string; content?: string
  tool_calls?: AiToolCall[]; tool_call_id?: string; name?: string
}
export interface AgentPending { tool_call_id: string; name: string; args: Record<string, unknown>; summary: string }
export interface AgentResult { messages: AiMsg[]; pending?: AgentPending; final?: string }
