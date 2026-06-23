<script setup lang="ts">
// PeeringApply.vue — public peering application form. Replaces the old
// mailto: CTA on Landing.vue. Submits to POST /api/v1/peering/apply,
// which persists + emails postmaster@ + emails the applicant. Operator
// reviews at /admin/peering and approves/rejects from there.
//
// The form is intentionally a single long page (no multi-step wizard) so
// applicants can see everything they're committing to up-front. Required
// fields are marked; optional ones live below required ones.
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

const { t } = useI18n()
const router = useRouter()

// --- Form state ---
const asn          = ref<string>('')
const networkName  = ref('')
const asSet        = ref('')
const irrSource    = ref('')
const contactName  = ref('')
const nocEmail     = ref('')
const phone        = ref('')
const prefixes6Raw = ref('')   // newline / comma separated
const prefixes4Raw = ref('')
const maxPrefix6   = ref<string>('200000')
const hasRPKI      = ref(true)
const bfdDesired   = ref(false)
const locations    = ref<string[]>([])
const sessionTypes = ref<string[]>([])
const ixMember     = ref<string[]>([])
const notes        = ref('')

const submitting = ref(false)
const submittedId = ref<string | null>(null)
const errMsg     = ref<string | null>(null)

const LOCATIONS = [
  { id: 'hkg', label: 'Region C (pop-03 / pop-04)' },
  { id: 'tyo', label: 'Region A (ctrl-01 / pop-01)' },
  { id: 'tpe', label: 'Region E (pop-08)' },
  { id: 'sin', label: 'Region D (pop-06)' },
  { id: 'fra', label: 'Region B (pop-05)' },
] as const

const SESSION_TYPES = [
  { id: 'tunnel', label: 'Tunnel (WireGuard / GRE)' },
  { id: 'ix',     label: 'IX route-server peering' },
] as const

const IX_OPTIONS = [
  { id: 'dsix',     label: 'DSIX (DataSphere IX Region C)' },
  { id: 'p7ix',     label: 'P7IX (Protocol 7 IX Region C)' },
  { id: 'p7ix-tyo', label: 'P7IX (Protocol 7 IX Region A)' },
  { id: 'tyix',     label: 'TYIX (Region A)' },
  { id: 'locix',    label: 'LocIX (Region B)' },
  { id: 'stuix',    label: 'STUIX' },
] as const

// Light client-side validation just for fast feedback; the backend
// re-validates everything authoritatively.
const asnNum = computed(() => Number(asn.value.replace(/^AS/i, '').trim()))
const valid = computed(() => {
  if (!Number.isFinite(asnNum.value) || asnNum.value < 1 || asnNum.value > 4294967295) return false
  if (!networkName.value.trim()) return false
  if (!nocEmail.value.match(/^[^\s@]+@[^\s@]+\.[^\s@]+$/)) return false
  if (splitLines(prefixes6Raw.value).length === 0) return false
  return true
})

function splitLines(s: string): string[] {
  return s.split(/[,\n]+/).map(x => x.trim()).filter(Boolean)
}

async function submit() {
  if (!valid.value || submitting.value) return
  submitting.value = true
  errMsg.value = null
  try {
    const body = {
      asn:           asnNum.value,
      network_name:  networkName.value.trim(),
      as_set:        asSet.value.trim(),
      irr_source:    irrSource.value.trim(),
      contact_name:  contactName.value.trim(),
      noc_email:     nocEmail.value.trim(),
      phone:         phone.value.trim(),
      prefixes6:     splitLines(prefixes6Raw.value),
      prefixes4:     splitLines(prefixes4Raw.value),
      max_prefix6:   Number(maxPrefix6.value) || 0,
      has_rpki:      hasRPKI.value,
      bfd_desired:   bfdDesired.value,
      locations:     locations.value,
      session_types: sessionTypes.value,
      ix_member:     ixMember.value,
      notes:         notes.value.trim(),
    }
    const r = await fetch('/api/v1/peering/apply', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    const env = await r.json()
    if (!env.ok) throw new Error(env.error || 'submit failed')
    submittedId.value = env.data?.id || '(saved)'
  } catch (e: unknown) {
    errMsg.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <main class="min-h-screen bg-gray-950 text-gray-200 font-mono py-10 px-3 sm:px-6">
    <div class="max-w-3xl mx-auto">
      <header class="border-b border-gray-800 pb-4 mb-6">
        <div class="text-[10px] tracking-widest text-emerald-500 uppercase mb-1">// {{ t('peering_apply.eyebrow') }}</div>
        <h1 class="font-display text-2xl sm:text-3xl text-gray-100 font-bold">{{ t('peering_apply.title') }}</h1>
        <p class="mt-3 text-sm text-gray-400 leading-relaxed normal-case tracking-normal">
          {{ t('peering_apply.intro') }}
        </p>
      </header>

      <!-- Success state -->
      <div v-if="submittedId" class="border border-emerald-500/60 bg-emerald-950/20 p-6 space-y-3">
        <div class="text-emerald-400 text-sm tracking-widest uppercase">✓ {{ t('peering_apply.success.title') }}</div>
        <p class="text-sm text-gray-300 normal-case tracking-normal leading-relaxed">
          {{ t('peering_apply.success.body', { id: submittedId }) }}
        </p>
        <div class="flex gap-2 pt-2">
          <button @click="router.push('/')" class="px-4 py-2 border border-gray-700 hover:border-emerald-500 text-[10px] tracking-widest uppercase text-gray-300 hover:text-emerald-400">
            {{ t('peering_apply.success.back_home') }}
          </button>
        </div>
      </div>

      <!-- Form -->
      <form v-else @submit.prevent="submit" class="space-y-6">
        <!-- ===== Identity ===== -->
        <section class="space-y-3">
          <h2 class="text-[10px] tracking-widest text-emerald-500 uppercase border-b border-gray-800 pb-1">
            {{ t('peering_apply.sec.identity') }}
          </h2>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <label class="block">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.asn') }} <span class="text-red-500">*</span></span>
              <input v-model="asn" type="text" required placeholder="AS65534"
                class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-sm focus:outline-none" />
            </label>
            <label class="block">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.network_name') }} <span class="text-red-500">*</span></span>
              <input v-model="networkName" type="text" required placeholder="Example Networks"
                class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-sm focus:outline-none" />
            </label>
            <label class="block">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.as_set') }}</span>
              <input v-model="asSet" type="text" placeholder="AS-EXAMPLE"
                class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-sm focus:outline-none" />
            </label>
            <label class="block">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.irr_source') }}</span>
              <select v-model="irrSource" class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-sm focus:outline-none">
                <option value="">—</option>
                <option value="RIPE">RIPE</option>
                <option value="ARIN">ARIN</option>
                <option value="APNIC">APNIC</option>
                <option value="LACNIC">LACNIC</option>
                <option value="AFRINIC">AFRINIC</option>
                <option value="RADB">RADB</option>
                <option value="ALTDB">ALTDB</option>
              </select>
            </label>
          </div>
        </section>

        <!-- ===== Contact ===== -->
        <section class="space-y-3">
          <h2 class="text-[10px] tracking-widest text-emerald-500 uppercase border-b border-gray-800 pb-1">
            {{ t('peering_apply.sec.contact') }}
          </h2>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <label class="block">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.contact_name') }}</span>
              <input v-model="contactName" type="text" autocomplete="name" placeholder="Jane Doe"
                class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-sm focus:outline-none" />
            </label>
            <label class="block">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.noc_email') }} <span class="text-red-500">*</span></span>
              <input v-model="nocEmail" type="email" autocomplete="email" required placeholder="noc@example.com"
                class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-sm focus:outline-none" />
            </label>
            <label class="block sm:col-span-2">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.phone') }}</span>
              <input v-model="phone" type="tel" placeholder="+1-555-0100"
                class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-sm focus:outline-none" />
            </label>
          </div>
        </section>

        <!-- ===== Technical ===== -->
        <section class="space-y-3">
          <h2 class="text-[10px] tracking-widest text-emerald-500 uppercase border-b border-gray-800 pb-1">
            {{ t('peering_apply.sec.technical') }}
          </h2>
          <label class="block">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.prefixes6') }} <span class="text-red-500">*</span></span>
            <textarea v-model="prefixes6Raw" rows="3" required placeholder="2001:db8::/48&#10;2001:db8:1::/48"
              class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-xs font-mono focus:outline-none"></textarea>
            <span class="block text-[10px] text-gray-700 mt-1 normal-case">{{ t('peering_apply.f.prefixes_hint') }}</span>
          </label>
          <label class="block">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.prefixes4') }}</span>
            <textarea v-model="prefixes4Raw" rows="2" placeholder="(we're IPv6-only; v4 will be recorded but not announced)"
              class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-xs font-mono focus:outline-none"></textarea>
          </label>
          <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 items-end">
            <label class="block">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.max_prefix6') }}</span>
              <input v-model="maxPrefix6" type="number" min="1"
                class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-sm focus:outline-none" />
            </label>
            <label class="flex items-center gap-2 text-xs text-gray-300 cursor-pointer pt-5">
              <input v-model="hasRPKI" type="checkbox" class="accent-emerald-500" />
              <span>{{ t('peering_apply.f.has_rpki') }}</span>
            </label>
            <label class="flex items-center gap-2 text-xs text-gray-300 cursor-pointer pt-5">
              <input v-model="bfdDesired" type="checkbox" class="accent-emerald-500" />
              <span>{{ t('peering_apply.f.bfd_desired') }}</span>
            </label>
          </div>
        </section>

        <!-- ===== Connectivity ===== -->
        <section class="space-y-3">
          <h2 class="text-[10px] tracking-widest text-emerald-500 uppercase border-b border-gray-800 pb-1">
            {{ t('peering_apply.sec.connectivity') }}
          </h2>
          <div>
            <span class="text-[10px] tracking-widest text-gray-600 uppercase block mb-2">{{ t('peering_apply.f.locations') }}</span>
            <div class="grid grid-cols-1 sm:grid-cols-3 gap-2">
              <label v-for="l in LOCATIONS" :key="l.id" class="flex items-center gap-2 px-3 py-2 border border-gray-800 hover:border-emerald-500/60 cursor-pointer text-xs">
                <input :value="l.id" v-model="locations" type="checkbox" class="accent-emerald-500" />
                <span>{{ l.label }}</span>
              </label>
            </div>
          </div>
          <div>
            <span class="text-[10px] tracking-widest text-gray-600 uppercase block mb-2">{{ t('peering_apply.f.session_types') }}</span>
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
              <label v-for="s in SESSION_TYPES" :key="s.id" class="flex items-center gap-2 px-3 py-2 border border-gray-800 hover:border-emerald-500/60 cursor-pointer text-xs">
                <input :value="s.id" v-model="sessionTypes" type="checkbox" class="accent-emerald-500" />
                <span>{{ s.label }}</span>
              </label>
            </div>
          </div>
          <div v-if="sessionTypes.includes('ix')">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase block mb-2">{{ t('peering_apply.f.ix_member') }}</span>
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
              <label v-for="i in IX_OPTIONS" :key="i.id" class="flex items-center gap-2 px-3 py-2 border border-gray-800 hover:border-emerald-500/60 cursor-pointer text-xs">
                <input :value="i.id" v-model="ixMember" type="checkbox" class="accent-emerald-500" />
                <span>{{ i.label }}</span>
              </label>
            </div>
          </div>
          <label class="block">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('peering_apply.f.notes') }}</span>
            <textarea v-model="notes" rows="4" placeholder=""
              class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-xs font-mono focus:outline-none"></textarea>
          </label>
        </section>

        <!-- ===== Submit ===== -->
        <div class="flex items-center gap-3 flex-wrap">
          <button type="submit" :disabled="!valid || submitting"
            class="px-6 py-3 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase disabled:opacity-30 disabled:cursor-not-allowed transition-colors">
            {{ submitting ? t('peering_apply.submitting') : t('peering_apply.submit') }}
          </button>
          <router-link to="/" class="text-[11px] text-gray-600 hover:text-gray-300 tracking-widest uppercase">
            ← {{ t('peering_apply.cancel') }}
          </router-link>
        </div>

        <div v-if="errMsg" class="border border-red-500 bg-red-950/30 px-3 py-2 text-xs text-red-400 normal-case tracking-normal">
          ⨯ {{ errMsg }}
        </div>
      </form>
    </div>
  </main>
</template>
