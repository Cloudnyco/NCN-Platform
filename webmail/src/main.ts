import { createApp } from 'vue'
import { createI18n } from 'vue-i18n'
import App from './App.vue'
import en from './i18n/en'
import zhCN from './i18n/zh-CN'
import zhTW from './i18n/zh-TW'
import './style.css'

const stored = localStorage.getItem('webmail.locale')
const detected =
  stored ||
  (navigator.language.startsWith('zh-TW') || navigator.language.startsWith('zh-Hant')
    ? 'zh-TW'
    : navigator.language.startsWith('zh')
      ? 'zh-CN'
      : 'en')

const i18n = createI18n({
  legacy: false,
  locale: detected,
  fallbackLocale: 'en',
  messages: { en, 'zh-CN': zhCN, 'zh-TW': zhTW },
})

createApp(App).use(i18n).mount('#app')
