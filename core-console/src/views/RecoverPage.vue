<!--
  RecoverPage.vue — break-glass account recovery via signed URL.

  The URL `/recover/<token>` is minted on tyo by `ncn-api admin mint-recover`
  and carries an HMAC-signed claim {user, exp, nonce}. The user opens it,
  sees the resolved username, picks a new password, and clicks Set. The
  token can only be redeemed once.

  No auth gate — possession of a valid token IS the auth. After success the
  user is bounced to /login and signs in normally.
-->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '@/api/client'
import { useI18n } from 'vue-i18n'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const token = (route.params.token as string) || ''

const stage = ref<'loading' | 'ready' | 'submitting' | 'done' | 'error'>('loading')
const resolvedUser = ref('')
const errMsg = ref('')
const pw1 = ref('')
const pw2 = ref('')
const showPw = ref(false)

onMounted(async () => {
  if (!token) {
    stage.value = 'error'
    errMsg.value = t('recover.err.no_token')
    return
  }
  try {
    const env = await api.bootstrapRecoverPreview(token)
    if (!env.ok || !env.data) {
      stage.value = 'error'
      errMsg.value = env.error || t('recover.err.invalid')
      return
    }
    resolvedUser.value = env.data.user
    stage.value = 'ready'
  } catch (e: unknown) {
    stage.value = 'error'
    errMsg.value = e instanceof Error ? e.message : String(e)
  }
})

async function submit() {
  errMsg.value = ''
  if (pw1.value.length < 8) {
    errMsg.value = t('recover.err.too_short')
    return
  }
  if (pw1.value !== pw2.value) {
    errMsg.value = t('recover.err.mismatch')
    return
  }
  stage.value = 'submitting'
  try {
    const env = await api.bootstrapRecoverSubmit(token, pw1.value)
    if (!env.ok) {
      errMsg.value = env.error || t('recover.err.submit_failed')
      stage.value = 'ready'
      return
    }
    stage.value = 'done'
    setTimeout(() => router.replace({ name: 'login' }), 2500)
  } catch (e: unknown) {
    errMsg.value = e instanceof Error ? e.message : String(e)
    stage.value = 'ready'
  }
}
</script>

<template>
  <div class="min-h-[70vh] flex items-center justify-center p-6">
    <div class="w-full max-w-md border border-gray-800 bg-gray-900 p-6 space-y-4">
      <h1 class="text-sm tracking-[0.25em] uppercase text-gray-200 mb-2">
        // {{ t('recover.title') }}
      </h1>

      <div v-if="stage === 'loading'" class="text-xs text-gray-400 flex items-center gap-2">
        <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
        <span>{{ t('recover.verifying') }}</span>
      </div>

      <div v-else-if="stage === 'ready' || stage === 'submitting'" class="space-y-3">
        <div class="text-xs text-gray-400 leading-relaxed">
          {{ t('recover.intro') }}
          <span class="text-emerald-300">{{ resolvedUser }}</span>
        </div>

        <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">
          {{ t('recover.new_password') }}
        </label>
        <div class="flex gap-2">
          <input v-model="pw1" :type="showPw ? 'text' : 'password'" minlength="8" required
                 autocomplete="new-password"
                 class="flex-1 bg-black border border-gray-800 text-gray-200 px-3 py-2 focus:border-emerald-700 focus:outline-none text-sm" />
          <button type="button" @click="showPw = !showPw"
                  class="px-2 text-[10px] tracking-widest uppercase border border-gray-800 text-gray-500 hover:text-gray-300">
            {{ showPw ? t('recover.hide') : t('recover.show') }}
          </button>
        </div>

        <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">
          {{ t('recover.confirm') }}
        </label>
        <input v-model="pw2" :type="showPw ? 'text' : 'password'" minlength="8" required
               autocomplete="new-password"
               class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-2 focus:border-emerald-700 focus:outline-none text-sm" />

        <p v-if="errMsg" class="text-[11px] text-red-400 break-all">⨯ {{ errMsg }}</p>

        <button @click="submit" :disabled="stage === 'submitting'"
                class="w-full px-3 py-2 border border-emerald-700 text-emerald-300 hover:bg-emerald-900/30 text-[11px] tracking-widest uppercase disabled:opacity-50">
          {{ stage === 'submitting' ? t('recover.submitting') : t('recover.submit') }}
        </button>

        <p class="text-[10px] text-gray-600 leading-relaxed">
          {{ t('recover.warn_one_shot') }}
        </p>
      </div>

      <div v-else-if="stage === 'done'" class="space-y-2 text-xs">
        <div class="flex items-center gap-2 text-emerald-400">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <span>{{ t('recover.done') }}</span>
        </div>
        <p class="text-gray-500">{{ t('recover.done_hint') }}</p>
      </div>

      <div v-else class="space-y-2 text-xs">
        <div class="flex items-center gap-2 text-red-400">
          <span class="w-1.5 h-1.5 bg-red-500"></span>
          <span>{{ t('recover.err.title') }}</span>
        </div>
        <p class="text-gray-500 break-all">⨯ {{ errMsg }}</p>
        <router-link to="/login"
                     class="block text-center px-3 py-2 border border-gray-700 hover:border-gray-500 text-[10px] tracking-widest uppercase">
          {{ t('recover.back_login') }}
        </router-link>
      </div>
    </div>
  </div>
</template>
