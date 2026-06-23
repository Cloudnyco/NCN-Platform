<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api } from '@/api/client'

// PopNode = minimum subset of FleetPublicNode the map needs. Allows callers
// to feed in either an authenticated /admin view or the public /fleet/public
// payload without coupling to one specific type.
interface PopNode { id: string; label?: string; lat: number; lon: number; ok: boolean }
// Back-compat: older callers passed { code, ok } via a `statuses` prop with
// PoP positions baked into the map. Keep accepting both shapes during the
// transition; new callers should use `nodes` exclusively.
interface PopStatus { code: string; ok: boolean }
const props = defineProps<{
  nodes?: PopNode[]
  statuses?: PopStatus[]
}>()

// Normalize PoP codes for lookup: "TYO-01", "ctrl-01", "ctrl01" all match.
function normalizeCode(c: string): string {
  return c.toLowerCase().replace(/[^a-z0-9]/g, '')
}
function statusFor(code: string): boolean | null {
  // Prefer the live node list — fleet API drives both presence and status.
  if (props.nodes) {
    const n = props.nodes.find(n => normalizeCode(n.id) === normalizeCode(code))
    return n ? n.ok : null
  }
  // Fallback path for legacy callers still on the `statuses` prop.
  if (props.statuses) {
    const s = props.statuses.find(s => normalizeCode(s.code) === normalizeCode(code))
    return s ? s.ok : null
  }
  return null
}

/* ---------------------------------------------------------------------------
 * The static layers of this map (grid + continent polygons + ~1000 land
 * dots) are baked to a <canvas>, converted to a single PNG data URL, and
 * inserted into the SVG as ONE <image> element.
 *
 * That collapses ~1200 SVG DOM nodes to 1, which is the difference between
 * "smooth" and "jank" on mid-range mobile GPUs. The animated layers
 * (arcs / particles / PoP markers / ack-ping) stay as native SVG so they
 * keep using SMIL `animateMotion` for the data flow.
 * ------------------------------------------------------------------------- */

const W = 1000
const H = 500

// Equirectangular projection (lat, lon → x, y in viewBox)
function px(lon: number) { return ((lon + 180) / 360) * W }
function py(lat: number) { return ((90 - lat) / 180) * H }

// Convert a closed polygon (lat,lon vertex list) → SVG `d` attribute.
function poly(pts: Array<[number, number]>): string {
  let d = ''
  for (let i = 0; i < pts.length; i++) {
    const [lat, lon] = pts[i]
    d += (i === 0 ? 'M' : 'L') + px(lon).toFixed(1) + ' ' + py(lat).toFixed(1) + ' '
  }
  return d + 'Z'
}

/* ---------------------------------------------------------------------------
 * Continent polygons (lat, lon). Hand-traced from coastline silhouettes —
 * stylized, not GIS-accurate, but recognizable. Order: NW → clockwise.
 * ------------------------------------------------------------------------- */

/* North America — clockwise outer outline, no interior detours.
   Starts at Bering Strait → AK N coast → AK panhandle → BC → US W → Baja →
   Mexico Pacific → C. America Pacific → C. America Caribbean → Yucatán →
   Gulf → Florida → Atlantic seaboard → Maritimes → Labrador → Hudson Bay
   → Nunavut → NWT N coast → back to AK. */
const NORTH_AMERICA: Array<[number, number]> = [
  // AK north coast (W→E)
  [66, -168], [69, -166], [71, -157], [70, -150], [70, -142],
  // AK panhandle / BC coast (smooth diagonal, no political border detour)
  [60, -141], [58, -136], [55, -132], [53, -132], [50, -128], [48, -125],
  // US Pacific coast
  [45, -124], [42, -124], [38, -123], [35, -121], [33, -118], [32, -117],
  // Baja W coast → tip → Gulf of California Baja side
  [30, -116], [28, -114], [25, -112], [23, -110], [23, -109],
  [26, -111], [28, -113], [31, -114],
  // Cross to mainland at top of Gulf, down mainland Gulf side
  [31, -113], [29, -111], [27, -110], [25, -108], [22, -106],
  // Mexico Pacific → Central America Pacific
  [19, -104], [16, -98], [14, -92], [13, -89], [11, -86], [9, -84], [8, -82],
  // Across Panama isthmus (Pacific → Caribbean)
  [8, -80], [9, -78], [9, -77],
  // Up Central America Caribbean coast
  [10, -78], [11, -83], [13, -83], [15, -83], [16, -86], [17, -88],
  // Yucatán (E coast → tip → W coast)
  [19, -87], [21, -87], [21, -90], [19, -91], [18, -94],
  // Gulf of Mexico (Mexico → Texas → Louisiana → Florida Gulf side)
  [21, -97], [25, -97], [29, -94], [29, -89], [30, -87], [29, -83], [26, -82],
  // Florida W → tip → E
  [25, -81], [26, -80], [29, -81],
  // US Atlantic seaboard up to Maritimes
  [32, -80], [34, -78], [37, -76], [39, -74], [41, -72], [43, -70], [45, -67],
  // Nova Scotia / Gulf of St Lawrence
  [45, -64], [47, -60], [49, -65], [51, -57],
  // Labrador / Ungava
  [55, -58], [58, -63], [60, -64], [60, -67],
  // Hudson Bay (E shore → S → W shore)
  [60, -77], [56, -78], [56, -82], [55, -86], [57, -92], [60, -94],
  // Across to Arctic islands
  [63, -94], [67, -96], [70, -95], [72, -91],
  // Arctic island fringe (very simplified)
  [74, -97], [76, -103], [74, -113], [70, -123],
  // NWT mainland N coast back to Alaska
  [69, -130], [70, -138], [70, -146], [71, -156]
]

/* South America — clockwise from Guajira peninsula (Colombia Caribbean):
   Caribbean N coast → Guianas → NE Brazil bulge → Brazil E → Argentina
   Atlantic → Tierra del Fuego (simplified) → Chile Pacific → Peru →
   Ecuador → Colombia Pacific → close. */
const SOUTH_AMERICA: Array<[number, number]> = [
  // Caribbean coast → Guianas
  [12, -72], [12, -70], [11, -64], [10, -61], [8, -60], [6, -57], [4, -52],
  // NE Brazil
  [1, -50], [-1, -47], [-5, -37], [-8, -35],
  // Brazil E coast
  [-13, -39], [-18, -39], [-22, -41], [-23, -43],
  // Argentine Atlantic
  [-26, -48], [-29, -50], [-33, -54], [-35, -57], [-38, -58],
  [-41, -63], [-45, -65], [-49, -67], [-52, -68],
  // Tierra del Fuego (single sweep around tip, no retrace)
  [-54, -67], [-55, -68], [-55, -70], [-54, -73],
  // Chile Pacific coast going N
  [-50, -74], [-46, -74], [-42, -73], [-37, -73], [-33, -71],
  // Peru Pacific
  [-25, -70], [-18, -71], [-12, -77], [-6, -81], [-2, -80],
  // Colombia Pacific
  [2, -79], [5, -78], [7, -77], [9, -78], [11, -75]
]

/* Eurasia — clockwise from Gibraltar.
 * Hard rules:
 *  - Strictly monotonic per section (no retracing)
 *  - Capped at ~170°E in NE (skip Chukotka — avoids the antimeridian crossing
 *    which would draw an entire-world-width band in equirectangular projection)
 *  - Kamchatka not detailed (peninsula loop would self-intersect in a simple
 *    closed polygon); E Asia coast smoothed
 *  - Italy boot, Korea, Malay peninsula traced as single loops without back-jumps
 */
const EURASIA: Array<[number, number]> = [
  // Iberia → French Atlantic → English Channel → Low Countries
  [36, -6],  [37, -9],  [40, -10], [43, -9],
  [44, -2],  [48, -5],  [49, -1],  [50, 2],
  [51, 4],   [53, 7],   [55, 9],   [57, 10],
  // Scandinavia (W coast → N Cape → Kola)
  [59, 11],  [63, 11],  [67, 14],  [70, 22],
  [71, 26],  [70, 30],  [69, 35],
  // N Russia arctic coast — latitudes pulled DOWN to realistic values
  [66, 38],  [68, 44],  [69, 54],  [70, 65],
  [73, 78],  [76, 100], [73, 113], [72, 128],
  [70, 140], [70, 153], [68, 163], [62, 170],
  // Russia Far East (E coast S, Kamchatka skipped via straight line)
  [55, 158], [52, 142], [46, 142], [43, 134],
  // Korea peninsula
  [42, 130], [40, 128], [38, 128], [35, 129],
  [34, 127], [36, 126], [38, 125],
  // China east coast (Liaodong → Shanghai → HK)
  [39, 122], [37, 122], [34, 120], [31, 122],
  [27, 121], [24, 118], [22, 114], [21, 110], [19, 108],
  // Indochina E coast → Malay Peninsula loop
  [16, 108], [13, 109], [10, 106], [9, 104],
  [10, 103], [12, 100], [9, 100],
  [4, 103], [1, 104],          // Malay E coast down
  [1, 103],                    // Region D tip
  [3, 101], [5, 100], [7, 98], // Malay W coast up
  [10, 98],
  // Burma → Bay of Bengal → India E
  [14, 98], [16, 96], [21, 92], [22, 89],
  [20, 86], [16, 81], [13, 80], [10, 79], [8, 78],
  // India W → Pakistan → Iran
  [10, 76], [15, 73], [19, 73], [22, 70],
  [24, 67], [25, 61], [25, 58],
  // Persian Gulf opening (skip Qatar/UAE detail)
  [27, 56], [27, 51], [29, 49],
  // Arabian Peninsula outer (skip Qatar peninsula, cut across)
  [22, 52], [17, 55], [13, 48], [12, 45],
  [16, 43], [21, 39], [25, 36], [28, 35],
  // Suez → Levant → Anatolia S coast
  [30, 33], [32, 35], [34, 36], [36, 36],
  [37, 36], [36, 32], [36, 30], [36, 28],
  [37, 27],
  // Greece (Aegean S edge)
  [38, 23], [37, 22], [38, 21],
  // Adriatic E coast
  [40, 19], [42, 19], [44, 15],
  // Italy boot — E coast down → toe → W coast up
  [44, 13], [42, 14], [40, 17], [40, 18],
  [38, 18], [38, 16], [40, 14], [43, 11], [44, 9],
  // Riviera → Spain S
  [43, 6], [42, 3], [39, 0], [37, -2], [36, -5]
]

/* Africa — clockwise: Mediterranean Maghreb → Egypt → Red Sea coast → Horn
   → Indian Ocean → Cape → Atlantic up to Gibraltar. ~65 vertices. */
const AFRICA: Array<[number, number]> = [
  [36, -6], [36, -2], [37, 5], [33, 11], [33, 14], [32, 22], [31, 25],
  [30, 30], [31, 32], [29, 33], [27, 34], [22, 37], [16, 39], [12, 43],
  [11, 44], [12, 51], [9, 49], [4, 48], [2, 46], [-2, 41], [-5, 40],
  [-11, 41], [-15, 40], [-17, 37], [-21, 35], [-24, 35], [-26, 33],
  [-30, 32], [-33, 28], [-34, 25], [-34, 20], [-32, 19], [-30, 18],
  [-28, 16], [-23, 14], [-18, 12], [-15, 12], [-12, 14], [-6, 12],
  [-2, 9], [0, 9], [2, 9], [4, 7], [5, 5], [5, 0], [7, -3], [6, -4],
  [4, -7], [4, -8], [5, -10], [9, -13], [12, -17], [14, -17], [16, -16],
  [19, -16], [21, -17], [23, -16], [27, -13], [29, -10], [32, -9],
  [33, -8], [35, -6], [36, -3], [36, -5]
]

/* Greenland — ~16 vertices including major fjord notches. */
const GREENLAND: Array<[number, number]> = [
  [83, -32], [82, -22], [78, -18], [75, -19], [72, -22], [68, -24],
  [62, -42], [60, -42], [60, -45], [62, -50], [66, -53], [70, -55],
  [73, -57], [76, -65], [78, -68], [80, -68], [82, -55], [83, -45]
]

const BRITISH_ISLES: Array<[number, number]> = [
  [58, -3], [59, -2], [59, 0], [58, 2], [56, 2], [54, 1], [53, 1],
  [51, 1], [51, -1], [50, -3], [50, -5], [51, -6], [53, -5], [55, -5],
  [57, -7], [58, -8], [58, -3]
]

const IRELAND: Array<[number, number]> = [
  [55, -7], [55, -6], [54, -5], [52, -6], [51, -10], [54, -10], [55, -8], [55, -7]
]

const ICELAND: Array<[number, number]> = [
  [66, -24], [66, -22], [66, -16], [65, -14], [64, -14], [63, -16],
  [63, -22], [64, -23], [65, -24], [66, -24]
]

/* Japan — Honshu / Kyushu / Shikoku / Hokkaido as one connected outline. */
const JAPAN: Array<[number, number]> = [
  [45, 141], [45, 145], [43, 145], [42, 144], [41, 141], [40, 142],
  [38, 141], [36, 141], [35, 140], [34, 139], [34, 137], [34, 135],
  [32, 132], [31, 131], [31, 130], [33, 130], [34, 132], [35, 132],
  [36, 133], [37, 136], [38, 139], [40, 140], [41, 140], [42, 140],
  [44, 141], [45, 141]
]

const TAIWAN: Array<[number, number]> = [
  [25, 121], [25, 122], [22, 121], [22, 120], [23, 120], [25, 121]
]

const HAINAN: Array<[number, number]> = [
  [20, 109], [20, 111], [18, 111], [18, 108], [19, 108], [20, 109]
]

const SRI_LANKA: Array<[number, number]> = [
  [10, 80], [9, 81], [7, 82], [6, 81], [6, 80], [8, 80], [10, 80]
]

const INDONESIA_SUMATRA: Array<[number, number]> = [
  [5, 95], [6, 97], [4, 100], [2, 100], [0, 102], [-2, 103], [-4, 105],
  [-6, 105], [-5, 103], [-3, 101], [-1, 98], [2, 96], [5, 95]
]

const INDONESIA_JAVA: Array<[number, number]> = [
  [-6, 105], [-6, 109], [-7, 113], [-8, 114], [-9, 114], [-8, 110], [-8, 106], [-6, 105]
]

const BORNEO: Array<[number, number]> = [
  [7, 116], [7, 118], [4, 119], [2, 118], [-1, 117], [-3, 114], [-4, 113],
  [-2, 110], [0, 109], [3, 110], [5, 111], [7, 116]
]

const PHILIPPINES: Array<[number, number]> = [
  [19, 121], [19, 122], [16, 123], [14, 122], [13, 124], [12, 125],
  [10, 125], [8, 126], [6, 125], [7, 122], [10, 121], [13, 120],
  [16, 120], [18, 120], [19, 121]
]

const AUSTRALIA: Array<[number, number]> = [
  [-11, 142], [-11, 137], [-15, 137], [-14, 135], [-12, 132], [-15, 130],
  [-12, 128], [-15, 126], [-14, 124], [-17, 122], [-20, 119], [-21, 115],
  [-24, 114], [-28, 114], [-32, 116], [-34, 115], [-35, 117], [-35, 119],
  [-33, 123], [-32, 127], [-32, 132], [-34, 135], [-35, 138], [-38, 141],
  [-39, 143], [-39, 145], [-37, 147], [-37, 150], [-35, 151], [-32, 153],
  [-28, 153], [-25, 153], [-22, 150], [-19, 148], [-15, 146], [-11, 143],
  [-11, 142]
]

const TASMANIA: Array<[number, number]> = [
  [-40, 144], [-40, 148], [-42, 148], [-43, 146], [-43, 144], [-40, 144]
]

const NEW_ZEALAND_N: Array<[number, number]> = [
  [-34, 173], [-35, 174], [-36, 175], [-36, 178], [-39, 178],
  [-39, 176], [-41, 175], [-40, 173], [-38, 174], [-34, 173]
]

const NEW_ZEALAND_S: Array<[number, number]> = [
  [-41, 173], [-42, 174], [-43, 173], [-44, 171], [-46, 169], [-46, 167],
  [-45, 167], [-43, 168], [-42, 171], [-41, 173]
]

const MADAGASCAR: Array<[number, number]> = [
  [-12, 49], [-13, 50], [-15, 50], [-18, 49], [-22, 48], [-25, 45],
  [-25, 44], [-22, 44], [-18, 44], [-15, 46], [-13, 48], [-12, 49]
]

const NEW_GUINEA: Array<[number, number]> = [
  [-1, 131], [-2, 134], [-4, 136], [-4, 138], [-3, 141], [-5, 142],
  [-7, 143], [-9, 144], [-11, 148], [-10, 150], [-8, 149], [-6, 147],
  [-5, 145], [-4, 143], [-2, 140], [-1, 137], [-1, 134], [-1, 131]
]

/* Caribbean major islands. */
const CUBA: Array<[number, number]> = [
  [23, -83], [23, -81], [22, -78], [20, -75], [20, -74], [22, -75],
  [22, -78], [23, -80], [22, -84], [22, -85], [23, -83]
]

const HISPANIOLA: Array<[number, number]> = [
  [20, -73], [19, -69], [18, -68], [17, -71], [18, -73], [19, -74], [20, -73]
]

const JAMAICA: Array<[number, number]> = [
  [18, -78], [18, -76], [17, -76], [17, -78], [18, -78]
]

const PUERTO_RICO: Array<[number, number]> = [
  [18, -67], [18, -66], [18, -65], [17, -66], [18, -67]
]

/* Mediterranean major islands. */
const SICILY: Array<[number, number]> = [
  [38, 13], [38, 15], [37, 15], [37, 13], [38, 13]
]

const SARDINIA: Array<[number, number]> = [
  [41, 9], [41, 10], [39, 9], [39, 8], [41, 9]
]

const CORSICA: Array<[number, number]> = [
  [43, 9], [43, 10], [42, 9], [42, 8], [43, 9]
]

const CRETE: Array<[number, number]> = [
  [35, 24], [36, 26], [35, 26], [35, 23], [35, 24]
]

const CYPRUS: Array<[number, number]> = [
  [35, 32], [35, 34], [34, 34], [34, 32], [35, 32]
]

const FALKLANDS: Array<[number, number]> = [
  [-51, -60], [-51, -57], [-52, -57], [-52, -60], [-51, -60]
]

const NEWFOUNDLAND: Array<[number, number]> = [
  [51, -55], [51, -52], [47, -52], [46, -54], [48, -58], [50, -57], [51, -55]
]

const SVALBARD: Array<[number, number]> = [
  [80, 12], [80, 23], [78, 23], [76, 21], [77, 16], [78, 12], [80, 12]
]

const CONTINENTS: Array<{ name: string; pts: Array<[number, number]> }> = [
  { name: 'na',   pts: NORTH_AMERICA },
  { name: 'sa',   pts: SOUTH_AMERICA },
  { name: 'eu',   pts: EURASIA },
  { name: 'af',   pts: AFRICA },
  { name: 'gl',   pts: GREENLAND },
  { name: 'uk',   pts: BRITISH_ISLES },
  { name: 'ie',   pts: IRELAND },
  { name: 'is',   pts: ICELAND },
  { name: 'sv',   pts: SVALBARD },
  { name: 'jp',   pts: JAPAN },
  { name: 'tw',   pts: TAIWAN },
  { name: 'hi',   pts: HAINAN },
  { name: 'lk',   pts: SRI_LANKA },
  { name: 'sum',  pts: INDONESIA_SUMATRA },
  { name: 'jav',  pts: INDONESIA_JAVA },
  { name: 'bor',  pts: BORNEO },
  { name: 'phl',  pts: PHILIPPINES },
  { name: 'aus',  pts: AUSTRALIA },
  { name: 'tas',  pts: TASMANIA },
  { name: 'nzn',  pts: NEW_ZEALAND_N },
  { name: 'nzs',  pts: NEW_ZEALAND_S },
  { name: 'mdg',  pts: MADAGASCAR },
  { name: 'ng',   pts: NEW_GUINEA },
  { name: 'cu',   pts: CUBA },
  { name: 'hp',   pts: HISPANIOLA },
  { name: 'jm',   pts: JAMAICA },
  { name: 'pr',   pts: PUERTO_RICO },
  { name: 'sic',  pts: SICILY },
  { name: 'srd',  pts: SARDINIA },
  { name: 'cor',  pts: CORSICA },
  { name: 'crt',  pts: CRETE },
  { name: 'cyp',  pts: CYPRUS },
  { name: 'fk',   pts: FALKLANDS },
  { name: 'nf',   pts: NEWFOUNDLAND }
]

// (Continent paths are no longer emitted as SVG — canvas traces them
// directly from `CONTINENTS[]`. The legacy `poly()` helper is also unused
// but kept around for now in case we want to revert to vector continents.)

/* ---------------------------------------------------------------------------
 * Dot grid — generated densely, then clipped to land outlines.
 * Stable per-cell jitter keeps it organic across re-renders.
 * ------------------------------------------------------------------------- */
// Land dots are now drawn directly to the canvas (see renderStatic below),
// not as SVG circles. That removes ~1000 DOM nodes from the SVG tree.

/* ---------------------------------------------------------------------------
 * Points of Presence
 *
 * Driven by the `nodes` prop (which Looking Glass feeds from the live
 * /fleet/public API). The static `popMeta` table provides a short label
 * + a marketing role string per id — these are visual-only enrichments
 * the API doesn't carry. Unknown ids fall back to safe defaults.
 *
 * Fallback skeleton kicks in only when neither prop is supplied — keeps
 * dev / Storybook / stale-cache loads from rendering an empty map.
 * ------------------------------------------------------------------------- */
interface PoP {
  code: string
  label: string
  lat: number
  lon: number
  role?: string
}

interface PopMeta { label: string; role: string }
// All four PoPs now have working eBGP transit out — previously pop-03 was
// flagged "edge" (announce-only via facility provider) and pop-04 was
// "iBGP" (private peering, no transit). Both upgraded to full transit
// alongside the existing ctrl-01 / pop-05 transit anchors.
const popMeta: Record<string, PopMeta> = {
  'pop-03': { label: 'Region C',  role: 'transit · DataSphere' },
  'pop-04': { label: 'Region C',  role: 'transit · MEGA-i'     },
  'ctrl-01': { label: 'Region A',      role: 'transit · KIX2'       },
  'pop-01': { label: 'Region A',      role: 'transit · Cornseed'   },
  'pop-08': { label: 'Region E',     role: 'transit · MoeDove'    },
  'pop-06': { label: 'Region D',  role: 'transit · Cyberjet'   },
  'pop-05': { label: 'Region B',  role: 'transit · LocIX'      },
}

// Skeleton — only used when no nodes prop is given (initial page paint).
const fallbackPops: PoP[] = [
  { code: 'pop-03', label: 'Region C', lat: 22.37, lon: 114.14, role: popMeta['pop-03'].role },
  { code: 'pop-04', label: 'Region C', lat: 22.30, lon: 114.17, role: popMeta['pop-04'].role },
  { code: 'ctrl-01', label: 'Region A',     lat: 35.68, lon: 139.69, role: popMeta['ctrl-01'].role },
  { code: 'pop-06', label: 'Region D', lat: 1.30,  lon: 103.79, role: popMeta['pop-06'].role },
  { code: 'pop-05', label: 'Region B', lat: 50.11, lon: 8.68,   role: popMeta['pop-05'].role },
]

const pops = computed<PoP[]>(() => {
  const src = props.nodes
  if (!src || src.length === 0) return fallbackPops
  return src.map(n => {
    const meta = popMeta[n.id.toLowerCase()] ?? { label: n.label ?? n.id, role: '' }
    return {
      code:  n.id,
      label: meta.label,
      lat:   n.lat,
      lon:   n.lon,
      role:  meta.role,
    }
  })
})

const popPoints = computed(() =>
  pops.value.map((p) => ({ ...p, x: px(p.lon), y: py(p.lat) }))
)

// Per-marker render state: position + status color + label-flip flag.
// labelLeft draws the callout box on the LEFT side of the dot — used for
// PoPs in the right portion of the map (e.g. TYO-01) to keep the box
// inside the viewBox.
const popMarkers = computed(() => popPoints.value.map(p => {
  const ok = statusFor(p.code)
  const offline = ok === false
  return {
    ...p,
    ok,
    offline,
    color:    offline ? 'rgb(239 68 68)'         : 'rgb(16 185 129)',
    colorDim: offline ? 'rgb(239 68 68 / 0.20)'  : 'rgb(16 185 129 / 0.20)',
    labelLeft: p.x > W * 0.62,
  }
}))

// Jitter markers that share a pixel so they render as visually distinct
// points instead of one stacked dot. Two PoPs in the same metro (e.g.
// pop-03 + pop-04, ~9 km apart → 0.25 px at this projection) used to
// merge into a single clustered marker with a multi-row callout — the
// box got tall and the row alignment looked cramped, so we now render
// each PoP as its own normal marker (own dot, own pulse, own callout)
// with a small vertical offset so they don't fight for the same pixel.
//
// Threshold matches what an unaided eye reads as "same point" (~10px).
// Pairs within that get a symmetric ±8px y-offset; triples get -12 / 0
// / +12; etc. The deterministic sort by code keeps which-goes-up vs
// which-goes-down stable across polls so SMIL animations don't restart.
const POP_JITTER_THRESHOLD = 10
const POP_JITTER_STEP = 16
const popMarkersJittered = computed(() => {
  const list = popMarkers.value
  const groups = new Map<string, typeof list>()
  const groupKey = new Map<string, string>()
  for (const m of list) {
    // Find an existing group whose anchor is within threshold.
    let assigned: string | null = null
    for (const [key, members] of groups) {
      const anchor = members[0]
      if (Math.hypot(m.x - anchor.x, m.y - anchor.y) < POP_JITTER_THRESHOLD) {
        members.push(m)
        groupKey.set(m.code, key)
        assigned = key
        break
      }
    }
    if (!assigned) {
      groups.set(m.code, [m])
      groupKey.set(m.code, m.code)
    }
  }
  // Sort each group's members so jitter ordering is deterministic; flatten.
  for (const members of groups.values()) {
    members.sort((a, b) => a.code.localeCompare(b.code))
  }
  return list.map(m => {
    const members = groups.get(groupKey.get(m.code)!) ?? [m]
    if (members.length === 1) return { ...m, displayY: m.y, labelBelow: false }
    const idx = members.findIndex(x => x.code === m.code)
    const offset = (idx - (members.length - 1) / 2) * POP_JITTER_STEP
    // Upper-half cluster members get their callout above the dot
    // (existing default). Lower-half flip the callout below the dot —
    // otherwise two callouts of 22px each, sitting only 16px apart,
    // still overlap. With the flip, the upper PoP's callout occupies
    // [y-30 .. y-8] and the lower PoP's occupies [y+8 .. y+30], leaving
    // a clean ~16px gap with the dots in between.
    return {
      ...m,
      displayY:   m.y + offset,
      labelBelow: idx >= members.length / 2,
    }
  })
})

/* ---------------------------------------------------------------------------
 * Label placement (leader-line solver).
 *
 * The old scheme pinned every callout to a fixed slot (left/right × above/
 * below the dot) chosen by a per-marker heuristic. With 8 PoPs that breaks
 * down in the East-Asia cluster: tyo / pop-01 / pop-08 / pop-03 / pop-04 /
 * pop-06 sit within a ~100px square, so 4 fixed slots can't keep their 96×22
 * boxes from overlapping — pop-08 and pop-03 ended up drawn on top of each
 * other (text on text).
 *
 * Instead we let each callout float to one of several candidate slots
 * arranged around its dot (both sides, a fan of vertical offsets) and pick,
 * greedily, the slot that minimizes a cost = out-of-viewBox penalty
 * (hard) + overlap with already-placed callouts (heavy) + overlap with any
 * dot (light) + a small bias toward the tidy default (preferred side, small
 * offset). A thin leader line connects the dot to the chosen box. Dense
 * markers are placed first so they claim the scarce nearby space; the empty
 * ocean to a cluster's side then naturally absorbs the rest.
 * ------------------------------------------------------------------------- */
const LBL_W = 96
const LBL_H = 22
const LBL_GAP = 14 // px between dot and the near edge of its box

interface Box { l: number; r: number; t: number; b: number }
function labelBox(x: number, dotY: number, side: number, off: number): Box {
  const l = side > 0 ? x + LBL_GAP : x - LBL_GAP - LBL_W
  const t = dotY + off - LBL_H / 2
  return { l, r: l + LBL_W, t, b: t + LBL_H }
}
function rectOverlap(a: Box, b: Box): number {
  const ox = Math.max(0, Math.min(a.r, b.r) - Math.max(a.l, b.l))
  const oy = Math.max(0, Math.min(a.b, b.b) - Math.max(a.t, b.t))
  return ox * oy
}
function outOfBounds(b: Box): number {
  let p = 0
  if (b.l < 2) p += 2 - b.l
  if (b.r > W - 2) p += b.r - (W - 2)
  if (b.t < 2) p += 2 - b.t
  if (b.b > H - 2) p += b.b - (H - 2)
  return p
}

// Candidate vertical offsets (callout mid-line relative to the dot). 0 keeps
// the callout level with the dot; the fan lets a crowded label step up/down
// into clear space.
const LBL_OFFSETS = [-11, 11, -33, 33, -57, 57, -82, 82]

const popLabeled = computed(() => {
  const list = popMarkersJittered.value
  // Dots are obstacles too, so a callout doesn't bury a neighbouring marker.
  const dotObs: Box[] = list.map(m => ({ l: m.x - 6, r: m.x + 6, t: m.displayY - 6, b: m.displayY + 6 }))
  const placed: Box[] = []
  // Place right-most (the dense East-Asia stack) first; tie-break by code so
  // the layout is deterministic across polls and SMIL animations don't reset.
  const order = [...list].sort((a, b) => (b.x - a.x) || a.code.localeCompare(b.code))
  const chosen = new Map<string, { bx: number; by: number; lx2: number }>()
  for (const m of order) {
    const preferSide = m.x > W * 0.5 ? -1 : 1 // right-half PoPs default to a left callout
    let best: Box | null = null
    let bestSide = preferSide
    let bestScore = Infinity
    for (const off of LBL_OFFSETS) {
      for (const side of [preferSide, -preferSide]) {
        const box = labelBox(m.x, m.displayY, side, off)
        let s = outOfBounds(box) * 1000
        for (const p of placed) s += rectOverlap(box, p) * 5
        for (const d of dotObs) s += rectOverlap(box, d)
        s += Math.abs(off) * 0.5          // prefer staying near the dot
        if (side !== preferSide) s += 10   // mild preference for the default side
        if (s < bestScore) { bestScore = s; best = box; bestSide = side }
      }
    }
    const box = best as Box
    placed.push(box)
    chosen.set(m.code, {
      bx: box.l,
      by: box.t,
      lx2: bestSide > 0 ? box.l : box.r, // leader meets the box's near edge
    })
  }
  return list.map(m => {
    const c = chosen.get(m.code) as { bx: number; by: number; lx2: number }
    return { ...m, lblX: c.bx, lblY: c.by, leadX: c.lx2, leadY: c.by + LBL_H / 2 }
  })
})

/* ---------------------------------------------------------------------------
 * Click-to-show latency on a data-flow line.
 *
 * We don't have a public per-edge (PoP↔PoP) RTT matrix — /fleet/public only
 * carries each node's anchor RTT, and inter-PoP probe data is intentionally
 * not exposed. So the figure shown on click is a great-circle ESTIMATE: the
 * fiber-propagation floor between the two cities (speed of light in glass
 * ≈ 200 km/ms, ×1.4 for real route inflation, ×2 for round trip). Labelled
 * "est." on the map; real measured RTT would need a backend latency matrix.
 * ------------------------------------------------------------------------- */
function haversineKm(lat1: number, lon1: number, lat2: number, lon2: number): number {
  const R = 6371
  const r = (d: number) => (d * Math.PI) / 180
  const dLat = r(lat2 - lat1), dLon = r(lon2 - lon1)
  const s = Math.sin(dLat / 2) ** 2 +
    Math.cos(r(lat1)) * Math.cos(r(lat2)) * Math.sin(dLon / 2) ** 2
  return 2 * R * Math.asin(Math.sqrt(s))
}
function estRttMs(km: number): number {
  return Math.max(1, Math.round((km * 1.4 / 200) * 2)) // ×1.4 route, /200 km·ms⁻¹, ×2 RTT
}

const selectedArcId = ref<string | null>(null)
function selectArc(id: string) {
  selectedArcId.value = selectedArcId.value === id ? null : id
}
// Keep the latency label inside the viewBox even for edge-hugging arcs.
function clampX(x: number) { return Math.max(88, Math.min(W - 88, x)) }
function clampY(y: number) { return Math.max(60, Math.min(H - 16, y)) }

/* ---------------------------------------------------------------------------
 * Inter-PoP arcs (quadratic bezier, control point lifted by distance)
 * ------------------------------------------------------------------------- */
// Quadratic-bezier arc with a control point lifted above the midpoint.
// The lift used to be `dist * 0.45` which sent transpacific arcs (TYO ↔ FET)
// outside the viewBox (control-point at y ≈ -180). Cap absolute lift to
// keep arcs visible: max 110 px (≈22% of viewBox height), floor my at 25.
const arcs = computed(() => {
  const pts = popPoints.value
  type Arc = { id: string; d: string; ax: number; ay: number; bx: number; by: number; healthy: boolean; color: string; aColor: string; bColor: string; aCode: string; bCode: string; aLabel: string; bLabel: string; lx: number; ly: number; estMs: number }
  const out: Arc[] = []
  for (let i = 0; i < pts.length; i++) {
    for (let j = i + 1; j < pts.length; j++) {
      const a = pts[i], b = pts[j]
      const mx = (a.x + b.x) / 2
      const dist = Math.hypot(b.x - a.x, b.y - a.y)
      const lift = Math.min(dist * 0.30, 110)
      const my = Math.max(25, (a.y + b.y) / 2 - lift)
      const aOk = statusFor(a.code)
      const bOk = statusFor(b.code)
      const aColor = aOk === false ? 'rgb(239 68 68)' : 'rgb(16 185 129)'
      const bColor = bOk === false ? 'rgb(239 68 68)' : 'rgb(16 185 129)'
      const healthy = aOk !== false && bOk !== false
      // Label sits on the quadratic-bezier apex (t=0.5 → ¼·A + ½·ctrl + ¼·B).
      const lx = 0.25 * a.x + 0.5 * mx + 0.25 * b.x
      const ly = 0.25 * a.y + 0.5 * my + 0.25 * b.y
      out.push({
        id: `arc-${a.code}-${b.code}`.replace(/[^a-zA-Z0-9-]/g, '_'),
        d: `M ${a.x.toFixed(1)} ${a.y.toFixed(1)} Q ${mx.toFixed(1)} ${my.toFixed(1)} ${b.x.toFixed(1)} ${b.y.toFixed(1)}`,
        ax: a.x, ay: a.y, bx: b.x, by: b.y,
        healthy,
        color:  healthy ? 'rgb(16 185 129)' : 'rgb(239 68 68)',
        aColor, bColor,
        aCode: a.code, bCode: b.code,
        aLabel: a.label || a.code,
        bLabel: b.label || b.code,
        lx, ly,
        estMs: estRttMs(haversineKm(a.lat, a.lon, b.lat, b.lon)),
      })
    }
  }
  return out
})

const selectedArc = computed(() => arcs.value.find(a => a.id === selectedArcId.value) ?? null)

// Live inter-PoP RTT matrix (GET /api/v1/status/latency), keyed by the
// sorted normalized pair "a|b" → averaged ms (A→B and B→A both pings, so
// we mean them for a stable figure). Falls back to the great-circle estimate
// when a pair has no live sample (node down / not yet scraped).
const liveLat = ref<Map<string, number>>(new Map())
let latTimer: ReturnType<typeof setInterval> | null = null
function pairKey(a: string, b: string) {
  return [normalizeCode(a), normalizeCode(b)].sort().join('|')
}
async function fetchLatency() {
  try {
    const r = await api.statusLatency()
    if (!r.ok || !r.data) return
    const acc = new Map<string, { sum: number; n: number }>()
    for (const e of r.data.edges) {
      const k = pairKey(e.from, e.to)
      const a = acc.get(k) ?? { sum: 0, n: 0 }
      a.sum += e.rtt_ms; a.n++; acc.set(k, a)
    }
    const m = new Map<string, number>()
    for (const [k, v] of acc) m.set(k, v.sum / v.n)
    liveLat.value = m
  } catch { /* keep last good / fall back to estimate */ }
}
interface ArcLatency { ms: number; live: boolean }
function arcLatency(arc: { aCode: string; bCode: string; estMs: number }): ArcLatency {
  const live = liveLat.value.get(pairKey(arc.aCode, arc.bCode))
  if (live != null && live > 0) return { ms: Math.round(live * 10) / 10, live: true }
  return { ms: arc.estMs, live: false }
}
const selectedLatency = computed<ArcLatency | null>(() =>
  selectedArc.value ? arcLatency(selectedArc.value) : null,
)

const meridians = Array.from({ length: 23 }, (_, i) => i * 15 - 180)
const parallels = Array.from({ length: 11 }, (_, i) => i * 15 - 75)

/* ---------------------------------------------------------------------------
 * Static layer baking
 *
 * On mount (and on theme / breakpoint change) we draw grid + continents +
 * land dots onto an off-screen canvas, convert it to a data-URL, and bind
 * it into the SVG as a single <image> element. ~1200 DOM nodes → 1.
 * ------------------------------------------------------------------------- */

const mapImage = ref('')

function readVar(name: string): string {
  if (typeof document === 'undefined') return ''
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}
function rgbVar(name: string, alpha = 1): string {
  const v = readVar(name).split(/\s+/).filter(Boolean)
  if (v.length >= 3) return `rgba(${v[0]}, ${v[1]}, ${v[2]}, ${alpha})`
  return `rgba(100,116,139,${alpha})`
}
function numVar(name: string, fallback: number): number {
  const n = parseFloat(readVar(name))
  return isFinite(n) ? n : fallback
}

/* Each edge of a continent polygon is subdivided into N segments. Each
   intermediate point is offset perpendicular to the edge by a deterministic
   noise value, turning straight chords into organic coastlines.
   This is what makes hand-traced polygons look like real shorelines. */
function tracePolygon(
  ctx: CanvasRenderingContext2D,
  pts: Array<[number, number]>,
  opts: { segments?: number; amp?: number; appendOnly?: boolean } = {}
) {
  const segments = opts.segments ?? 5
  const amp      = opts.amp ?? 1.0
  if (!opts.appendOnly) ctx.beginPath()
  for (let i = 0; i < pts.length; i++) {
    const [aLat, aLon] = pts[i]
    const [bLat, bLon] = pts[(i + 1) % pts.length]
    const ax = px(aLon), ay = py(aLat)
    const bx = px(bLon), by = py(bLat)

    if (i === 0) ctx.moveTo(ax, ay)

    const dx = bx - ax, dy = by - ay
    const len = Math.hypot(dx, dy)
    if (len < 3) { ctx.lineTo(bx, by); continue }

    // Unit perpendicular (rotate edge vector 90° CCW)
    const nx = -dy / len
    const ny =  dx / len

    for (let s = 1; s < segments; s++) {
      const t  = s / segments
      const mx = ax + dx * t
      const my = ay + dy * t
      // Two octaves of sin-based pseudo-noise, stable per position.
      const n = (
        Math.sin(mx * 0.31 + my * 0.47) * 0.6 +
        Math.sin(mx * 0.13 - my * 0.21 + 1.7) * 0.4
      ) * amp
      ctx.lineTo(mx + nx * n, my + ny * n)
    }
    ctx.lineTo(bx, by)
  }
  ctx.closePath()
}

function renderStatic(): string {
  if (typeof document === 'undefined') return ''
  const dpr = Math.min(2, window.devicePixelRatio || 1)
  const cv  = document.createElement('canvas')
  cv.width  = W * dpr
  cv.height = H * dpr
  const ctx = cv.getContext('2d')
  if (!ctx) return ''
  ctx.scale(dpr, dpr)

  // 1) deep wash
  ctx.fillStyle = rgbVar('--map-grid', 0.04)
  ctx.fillRect(0, 0, W, H)

  // 2) thin lat/lon grid
  ctx.strokeStyle = rgbVar('--map-grid', 0.18)
  ctx.lineWidth = 0.35
  ctx.beginPath()
  for (const m of meridians) { ctx.moveTo(px(m), 0); ctx.lineTo(px(m), H) }
  for (const p of parallels) { ctx.moveTo(0, py(p)); ctx.lineTo(W, py(p)) }
  ctx.stroke()

  // 3) bolder equator + prime meridian
  ctx.strokeStyle = rgbVar('--map-grid', 0.42)
  ctx.lineWidth = 0.6
  ctx.beginPath()
  ctx.moveTo(0, py(0)); ctx.lineTo(W, py(0))
  ctx.moveTo(px(0), 0); ctx.lineTo(px(0), H)
  ctx.stroke()

  // 4) continent fill (noised coastline)
  ctx.fillStyle = 'rgba(16, 185, 129, 0.10)'
  for (const c of CONTINENTS) {
    tracePolygon(ctx, c.pts)
    ctx.fill()
  }

  // 5) dots, clipped to continents — clip path uses the SAME noised polygons
  ctx.save()
  ctx.beginPath()
  for (const c of CONTINENTS) {
    tracePolygon(ctx, c.pts, { appendOnly: true })
  }
  ctx.clip()

  ctx.fillStyle = rgbVar('--map-dot', numVar('--map-dot-alpha', 0.4))
  const step = (window.innerWidth < 768) ? 3.6 : 2.2
  for (let lat = -56; lat <= 80; lat += step) {
    for (let lon = -180; lon <= 180; lon += step) {
      const x = px(lon), y = py(lat)
      const s = Math.sin(lat * 13.21 + lon * 7.91) * 1234.567
      const t = Math.cos(lat * 5.31 + lon * 17.13) * 4321.987
      const jx = (s - Math.floor(s) - 0.5) * 1.2
      const jy = (t - Math.floor(t) - 0.5) * 1.2
      const r  = (Math.abs(s + t) % 1) < 0.18 ? 1.6 : 1.1
      ctx.beginPath()
      ctx.arc(x + jx, y + jy, r, 0, Math.PI * 2)
      ctx.fill()
    }
  }
  ctx.restore()

  // 6) continent outline on top
  ctx.strokeStyle = 'rgba(16, 185, 129, 0.45)'
  ctx.lineWidth = 0.5
  for (const c of CONTINENTS) {
    tracePolygon(ctx, c.pts)
    ctx.stroke()
  }

  return cv.toDataURL('image/png')
}

/* Schedule + lifecycle */
let rerenderHandle: ReturnType<typeof setTimeout> | null = null
function scheduleRerender() {
  if (rerenderHandle) clearTimeout(rerenderHandle)
  rerenderHandle = setTimeout(() => { mapImage.value = renderStatic() }, 80)
}

let lastBreak = (typeof window !== 'undefined') ? window.innerWidth < 768 : false
function onResize() {
  const m = window.innerWidth < 768
  if (m !== lastBreak) { lastBreak = m; scheduleRerender() }
}

let themeObs: MutationObserver | null = null

onMounted(() => {
  // Defer one frame so the page paints UI before we spend ~30ms on the canvas.
  requestAnimationFrame(() => { mapImage.value = renderStatic() })

  // Re-bake when the user toggles theme (html.class flips).
  themeObs = new MutationObserver(scheduleRerender)
  themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })

  window.addEventListener('resize', onResize, { passive: true })

  // Live RTT matrix for click-to-show-latency. Poll on the fleet cadence.
  fetchLatency()
  latTimer = setInterval(fetchLatency, 30_000)
})

onBeforeUnmount(() => {
  themeObs?.disconnect()
  window.removeEventListener('resize', onResize)
  if (rerenderHandle) clearTimeout(rerenderHandle)
  if (latTimer) clearInterval(latTimer)
})
</script>

<template>
  <div class="relative w-full font-mono">
    <svg
      :viewBox="`0 0 ${W} ${H}`"
      preserveAspectRatio="xMidYMid meet"
      class="w-full h-auto block"
      aria-hidden="true"
      @click="selectedArcId = null"
    >
      <defs>
        <!-- Arc paths used by animateMotion + visible stroke -->
        <path
          v-for="arc in arcs"
          :key="arc.id"
          :id="arc.id"
          :d="arc.d"
          fill="none"
        />
      </defs>

      <!-- One rasterized image replaces ~1200 static SVG nodes
           (grid + continent fills/outlines + ~1000 land dots).
           Re-baked on mount, theme switch, or breakpoint flip. -->
      <image
        v-if="mapImage"
        :href="mapImage"
        x="0" y="0"
        :width="W" :height="H"
        preserveAspectRatio="none"
      />

      <!-- ============== ARC + DATA FLOW ============== -->
      <g v-for="arc in arcs" :key="`fx-${arc.id}`">
        <!-- 1) Soft underglow halo on the arc -->
        <use :href="`#${arc.id}`"
             :stroke="arc.color" stroke-opacity="0.22" stroke-width="9" fill="none" />

        <!-- 2) Solid base line (subtle) -->
        <use :href="`#${arc.id}`"
             :stroke="arc.color" stroke-opacity="0.30" stroke-width="1" fill="none" />

        <!-- 3) "Flow" dashed overlay — only animated when both endpoints healthy. -->
        <use :href="`#${arc.id}`"
             :stroke="arc.color" :stroke-opacity="arc.healthy ? 1 : 0.55" stroke-width="2"
             stroke-dasharray="14 10" fill="none">
          <animate v-if="arc.healthy" attributeName="stroke-dashoffset"
                   from="0" to="-48" dur="1.3s" repeatCount="indefinite" />
        </use>

        <!-- 4) Forward comet — only on healthy arcs. -->
        <g v-if="arc.healthy">
          <circle r="9" :fill="arc.color" fill-opacity="0.18">
            <animateMotion :href="`#${arc.id}`" dur="3.5s" repeatCount="indefinite" begin="0s" />
          </circle>
          <circle r="5" :fill="arc.color" fill-opacity="0.45">
            <animateMotion :href="`#${arc.id}`" dur="3.5s" repeatCount="indefinite" begin="0s" />
          </circle>
          <circle r="3" :fill="arc.color">
            <animateMotion :href="`#${arc.id}`" dur="3.5s" repeatCount="indefinite" begin="0s" />
          </circle>
          <circle r="2.4" :fill="arc.color" opacity="0.6">
            <animateMotion :href="`#${arc.id}`" dur="3.5s" repeatCount="indefinite" begin="-0.18s" />
          </circle>
          <circle r="1.8" :fill="arc.color" opacity="0.35">
            <animateMotion :href="`#${arc.id}`" dur="3.5s" repeatCount="indefinite" begin="-0.32s" />
          </circle>
        </g>

        <!-- 5) Reverse pink comet — bi-directional traffic indicator. -->
        <g v-if="arc.healthy">
          <circle r="7" fill="rgb(236 72 153)" fill-opacity="0.20">
            <animateMotion :href="`#${arc.id}`" dur="4.5s" repeatCount="indefinite" begin="1.1s"
                           keyPoints="1;0" keyTimes="0;1" calcMode="linear" />
          </circle>
          <circle r="2.6" fill="rgb(236 72 153)">
            <animateMotion :href="`#${arc.id}`" dur="4.5s" repeatCount="indefinite" begin="1.1s"
                           keyPoints="1;0" keyTimes="0;1" calcMode="linear" />
          </circle>
          <circle r="2" fill="rgb(236 72 153)" opacity="0.5">
            <animateMotion :href="`#${arc.id}`" dur="4.5s" repeatCount="indefinite" begin="0.95s"
                           keyPoints="1;0" keyTimes="0;1" calcMode="linear" />
          </circle>
        </g>

        <!-- 6) Endpoint ack-ping — colored per endpoint status. Capped to r=16
                so we don't draw outside the viewBox for edge-hugging PoPs. -->
        <circle :cx="arc.bx" :cy="arc.by" r="3" fill="none"
                :stroke="arc.bColor" stroke-width="1.2">
          <animate attributeName="r" from="3" to="16" dur="3.5s"
                   repeatCount="indefinite" begin="0s" />
          <animate attributeName="opacity" from="0.9" to="0" dur="3.5s"
                   repeatCount="indefinite" begin="0s" />
        </circle>
        <circle :cx="arc.ax" :cy="arc.ay" r="3" fill="none"
                stroke="rgb(236 72 153)" stroke-width="1.2">
          <animate attributeName="r" from="3" to="16" dur="4.5s"
                   repeatCount="indefinite" begin="1.1s" />
          <animate attributeName="opacity" from="0.9" to="0" dur="4.5s"
                   repeatCount="indefinite" begin="1.1s" />
        </circle>
      </g>

      <!-- ============== POP MARKERS (jittered when co-located) ==============
           Each PoP renders as its own marker (dot, pulse, crosshair,
           halo, callout) — even when two PoPs share a pixel (HK metro
           pairs). The popMarkersJittered computed offsets the y-coords
           of co-located markers by ±8 px so they appear as two distinct
           dots stacked vertically instead of one merged dot. -->
      <g v-for="p in popLabeled" :key="p.code">
        <!-- Outer pulse ring — animated when online. Cap max radius at
             18 to stay inside the viewBox for edge-hugging PoPs. -->
        <circle v-if="!p.offline" :cx="p.x" :cy="p.displayY" r="6" fill="none"
                :stroke="p.color" stroke-width="1.2">
          <animate attributeName="r" values="6;18;6" dur="2.6s" repeatCount="indefinite" />
          <animate attributeName="opacity" values="0.8;0;0.8" dur="2.6s" repeatCount="indefinite" />
        </circle>
        <!-- Crosshair -->
        <g :stroke="p.color" stroke-width="0.7" opacity="0.85">
          <line :x1="p.x - 12" :y1="p.displayY" :x2="p.x - 7" :y2="p.displayY" />
          <line :x1="p.x + 7"  :y1="p.displayY" :x2="p.x + 12" :y2="p.displayY" />
          <line :x1="p.x" :y1="p.displayY - 12" :x2="p.x" :y2="p.displayY - 7" />
          <line :x1="p.x" :y1="p.displayY + 7"  :x2="p.x" :y2="p.displayY + 12" />
        </g>
        <!-- Halo + core -->
        <circle :cx="p.x" :cy="p.displayY" r="6" :fill="p.colorDim" />
        <circle :cx="p.x" :cy="p.displayY" r="4" :fill="p.color"
                stroke="rgb(var(--g-950))" stroke-width="1" />

        <!-- Label block — position chosen by the leader-line solver
             (popLabeled). Box floats to clear space around the dot; a thin
             leader connects the dot to the box's near edge. Two co-located
             PoPs and near-neighbours (pop-08 / hkg) no longer collide. -->
        <line :x1="p.x" :y1="p.displayY" :x2="p.leadX" :y2="p.leadY"
              :stroke="p.color" stroke-width="0.6" stroke-opacity="0.7" />
        <rect :x="p.lblX" :y="p.lblY" width="96" height="22"
              fill="rgb(var(--g-950) / 0.88)"
              :stroke="p.color" stroke-opacity="0.6" stroke-width="0.6" />
        <text :x="p.lblX + 5" :y="p.lblY + 9" font-size="8" letter-spacing="1.2"
              :fill="p.color" font-family="JetBrains Mono, monospace">{{ p.code }}</text>
        <text :x="p.lblX + 5" :y="p.lblY + 18" font-size="6" letter-spacing="0.5"
              fill="rgb(var(--map-grid))" font-family="JetBrains Mono, monospace">{{ p.role }}</text>
      </g>

      <!-- Telemetry corner readouts (decorative) -->
      <g font-family="JetBrains Mono, monospace" font-size="7" letter-spacing="0.5"
         fill="rgb(var(--map-grid))" opacity="0.7">
        <text x="8"   y="14">[ AS64500 · MAP ]</text>
        <text x="8"   y="26">PROJ : equirectangular / WGS-84</text>
        <text x="8"   y="38">VIEW : -180° / +180° · -85° / +85°</text>
        <text :x="W - 8" y="14" text-anchor="end">{{ pops.length }} PoP · {{ arcs.length }} arc</text>
        <!-- ↑ pops here is the computed ref's auto-unwrap in template context. -->

        <text :x="W - 8" :y="H - 8" text-anchor="end" fill="rgb(16 185 129)" opacity="1">● LIVE</text>
      </g>

      <!-- ============== INTERACTIVE: click a flow line → est. latency ==============
           Transparent wide hit-ribbons over each arc, drawn last so they win
           clicks against the animated comets beneath. Tap toggles a label with
           the great-circle RTT estimate between the two cities. -->
      <use v-for="arc in arcs" :key="`hit-${arc.id}`"
           :href="`#${arc.id}`" fill="none" stroke="transparent" stroke-width="16"
           pointer-events="stroke" style="cursor: pointer"
           @click.stop="selectArc(arc.id)" />

      <g v-if="selectedArc" pointer-events="none" font-family="JetBrains Mono, monospace">
        <line :x1="clampX(selectedArc.lx)" :y1="clampY(selectedArc.ly) - 10"
              :x2="selectedArc.lx" :y2="selectedArc.ly"
              :stroke="selectedArc.color" stroke-opacity="0.5" stroke-width="1" />
        <rect :x="clampX(selectedArc.lx) - 86" :y="clampY(selectedArc.ly) - 58"
              width="172" height="48" rx="7"
              fill="rgb(var(--g-950))" fill-opacity="0.94"
              :stroke="selectedArc.color" stroke-opacity="0.75" stroke-width="1" />
        <text :x="clampX(selectedArc.lx)" :y="clampY(selectedArc.ly) - 40" text-anchor="middle"
              font-size="11" fill="rgb(var(--g-200))">{{ selectedArc.aLabel }} ↔ {{ selectedArc.bLabel }}</text>
        <text :x="clampX(selectedArc.lx)" :y="clampY(selectedArc.ly) - 23" text-anchor="middle"
              font-size="15" font-weight="700" :fill="selectedArc.color">{{ selectedLatency?.live ? '' : '≈ ' }}{{ selectedLatency?.ms }} ms</text>
        <text :x="clampX(selectedArc.lx)" :y="clampY(selectedArc.ly) - 13" text-anchor="middle"
              font-size="7" letter-spacing="0.8" fill="rgb(var(--g-500))">{{ selectedLatency?.live ? 'MEASURED · RTT' : 'GREAT-CIRCLE EST' }}</text>
      </g>
    </svg>
  </div>
</template>
