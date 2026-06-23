import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, type FleetPublic, type FleetPublicNode, type PeeringDBSnapshot } from '@/api/client'

// Shared public-network data, lifted out of Landing.vue when the single
// long landing page was split into /, /about, /network and /peering. The
// live fleet snapshot drives the home stat band, the Network PoP grid AND
// the Peering facility table; the PeeringDB snapshot drives the IX list.
// Each page that needs it calls usePublicNetwork() and gets its own polling
// instance (fleet/public is a cheap server-cached endpoint).

export interface PoP { code: string; label: string; region: string; asn: string }
export interface IXRow { name: string; speed: string }

// Facility names are proper nouns (carrier + cage), keyed by node id; unknown
// ids fall back to the API label so a brand-new PoP still renders.
const facilityByID: Record<string, string> = {
  'ctrl-01': 'KIX2 / Equinix TY2 ringside',
  'pop-01': 'Cornseed Region A',
  'pop-08': 'MoeDove Region E',
  'pop-04': 'iAdvantage MEGA-i',
  'pop-03': 'DataSphere · Kwai Chung',
  'pop-06': 'Cyberjet SG',
  'pop-05': 'LocIX Region B'
}

// Fallback shown only until the live PeeringDB snapshot lands (or if it fails).
const ixFallback: IXRow[] = [
  { name: 'DataSphere Internet Exchange (DSIX)', speed: '1G' },
  { name: 'Protocol 7 IX (P7IX-HKG)',            speed: '1G' }
]

export function usePublicNetwork(opts: { poll?: boolean } = {}) {
  const fleet = ref<FleetPublic | null>(null)
  const fleetReady = ref(false)
  const pdb = ref<PeeringDBSnapshot | null>(null)
  let pollTimer: ReturnType<typeof setInterval> | null = null

  async function loadFleet() {
    try {
      const env = await api.fleetPublic()
      if (env.ok && env.data) {
        fleet.value = env.data
        fleetReady.value = true
      }
    } catch { /* keep last good snapshot */ }
  }

  async function loadPeeringDB() {
    try {
      const env = await api.peeringDB()
      if (env.ok && env.data) pdb.value = env.data
    } catch { /* keep fallback list */ }
  }

  // Map a landing PoP code (TYO-01, pop04, pop05) to a fleet id (ctrl-01, ...).
  function nodeFor(code: string): FleetPublicNode | undefined {
    const norm = code.toLowerCase().replace(/-/g, '')
    return fleet.value?.nodes.find(n => n.id.toLowerCase().replace(/-/g, '') === norm)
  }

  function fmtRoutes(n: number): string {
    if (!n) return '—'
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
    if (n >= 1_000)     return (n / 1_000).toFixed(n >= 10_000 ? 0 : 1) + 'K'
    return String(n)
  }

  function fmtSpeed(mbps: number): string {
    if (!mbps) return '—'
    return mbps >= 1000 ? `${mbps / 1000}G` : `${mbps}M`
  }

  function popCardBorder(code: string): string {
    const n = nodeFor(code)
    if (!fleetReady.value) return 'border-gray-800'
    if (!n || !n.ok) return 'border-red-500/40'
    return 'border-gray-800 hover:border-emerald-500/40'
  }

  // PoP catalog — DERIVED from the public fleet API so a node added in
  // backend/fleet.go appears automatically. Skeleton row matches the backend
  // nodes order so visitors don't see a reorder once the API responds.
  const pops = computed<PoP[]>(() => {
    if (!fleet.value) {
      return [
        { code: 'pop-03', label: 'Region C, HK', region: facilityByID['pop-03'], asn: 'AS64500' },
        { code: 'pop-04', label: 'Region C, HK', region: facilityByID['pop-04'], asn: 'AS64500' },
        { code: 'ctrl-01', label: 'Region A, JP',     region: facilityByID['ctrl-01'], asn: 'AS64500' },
        { code: 'pop-06', label: 'Region D, SG', region: facilityByID['pop-06'], asn: 'AS64500' },
        { code: 'pop-05', label: 'Region B, DE', region: facilityByID['pop-05'], asn: 'AS64500' }
      ]
    }
    return fleet.value.nodes.map(n => ({
      code:   n.id,
      label:  n.label,
      region: facilityByID[n.id.toLowerCase()] ?? n.label,
      asn:    'AS64500'
    }))
  })

  // IX memberships — single source of truth is PeeringDB (asn/64500).
  // netixlan can list a port twice (v4+v6 / multiple LANs) — dedup by
  // exchange name, keep the fastest.
  const ixMemberships = computed<IXRow[]>(() => {
    const live = pdb.value?.ix ?? []
    if (!live.length) return ixFallback
    const byName = new Map<string, number>()
    for (const x of live) byName.set(x.name, Math.max(byName.get(x.name) ?? 0, x.speed))
    return [...byName.entries()].map(([name, mbps]) => ({ name, speed: fmtSpeed(mbps) }))
  })

  onMounted(() => {
    loadFleet()
    loadPeeringDB()
    if (opts.poll !== false) pollTimer = setInterval(loadFleet, 15000)
  })
  onBeforeUnmount(() => {
    if (pollTimer) clearInterval(pollTimer)
  })

  return { fleet, fleetReady, pdb, nodeFor, fmtRoutes, fmtSpeed, popCardBorder, pops, ixMemberships }
}
