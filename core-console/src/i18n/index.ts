import { createI18n } from 'vue-i18n'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import en from './en'
import zhCN from './zh-CN'
import zhTW from './zh-TW'

export type Locale = 'en' | 'zh-CN' | 'zh-TW'
type Schema = typeof en

const STORAGE_KEY = 'ncn:locale'

export const LOCALES: { code: Locale; label: string; latin: boolean }[] = [
  { code: 'en',    label: 'EN',   latin: true  },
  { code: 'zh-TW', label: '繁體', latin: false },
  { code: 'zh-CN', label: '简体', latin: false }
]

function detect(): Locale {
  if (typeof window === 'undefined') return 'en'
  const stored = localStorage.getItem(STORAGE_KEY) as Locale | null
  if (stored && LOCALES.some((l) => l.code === stored)) return stored
  const langs = navigator.languages?.length ? navigator.languages : [navigator.language || 'en']
  for (const l of langs) {
    if (/^zh[-_](TW|HK|MO)/i.test(l)) return 'zh-TW'
    if (/^zh/i.test(l))                return 'zh-CN'
    if (/^en/i.test(l))                return 'en'
  }
  return 'en'
}

export const i18n = createI18n<[Schema], Locale>({
  legacy: false,
  locale: detect(),
  fallbackLocale: 'en',
  messages: {
    en,
    'zh-CN': zhCN,
    'zh-TW': zhTW
  },
  warnHtmlMessage: false
})

export function persistLocale(code: Locale) {
  // Composition-mode `locale` is a WritableComputedRef; cast around the
  // legacy/composition union the createI18n generics infer.
  const loc = i18n.global.locale as unknown as { value: Locale }
  loc.value = code
  if (typeof window !== 'undefined') {
    localStorage.setItem(STORAGE_KEY, code)
    document.documentElement.setAttribute('lang', code)
  }
}

// Convenience composable used by templates: gives `t`, current locale,
// and the Latin/CJK flag for typography decisions (tracking, etc.).
export function useLocaleAware() {
  const { t, locale } = useI18n()
  const isLatin = computed(() => locale.value === 'en')
  const isCJK   = computed(() => !isLatin.value)
  // Latin: wide tracking + uppercase aesthetic.
  // CJK: tighter tracking, no forced uppercase (汉字 doesn't have case).
  const trackWide   = computed(() => isLatin.value ? 'tracking-[0.5em]' : 'tracking-[0.15em]')
  const trackMed    = computed(() => isLatin.value ? 'tracking-[0.3em]' : 'tracking-[0.1em]')
  const trackTight  = computed(() => isLatin.value ? 'tracking-widest'   : 'tracking-wide')
  const caseClass   = computed(() => isLatin.value ? 'uppercase'         : 'normal-case')
  return { t, locale, isLatin, isCJK, trackWide, trackMed, trackTight, caseClass }
}
