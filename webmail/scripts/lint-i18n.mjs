#!/usr/bin/env node
//
// lint-i18n.mjs — catch vue-i18n footguns at build time, not at runtime.
//
// Checks every string leaf value in src/i18n/*.ts for:
//
//   1. Literal `@` followed by alphanumeric — vue-i18n 9's tokenizer reads
//      `@…` as a linked-message reference, so `noc@example.com` blows up
//      createI18n with "Invalid linked format" and the SPA mounts blank.
//      Escape as {'@'} to keep the literal.
//
//   2. Emoji / non-BMP chars inside i18n values — they should live in
//      components via <Icon /> for visual consistency, not in translation
//      strings. Skips a small allow-list of bullets/arrows we use as text.
//
//   3. Missing keys between base (en) and other locales.
//
// Each violation gets a one-line report; non-zero exit on any.
//
// Usage:   node scripts/lint-i18n.mjs               (from package root)
// Or:      npm run lint:i18n
//
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here   = path.dirname(fileURLToPath(import.meta.url))
const i18nDir = path.resolve(here, '..', 'src', 'i18n')

// ---------- 1. read locales ----------
//
// Each src/i18n/*.ts default-exports an object literal. We don't run vue-tsc
// here — strip the imports / exports and eval the literal in a sandbox.
// That's lighter than spinning up the TS compiler for a 200-line file and
// avoids the linter inheriting our TS toolchain.
function loadLocale(file) {
  const src = fs.readFileSync(file, 'utf8')
  // Strip imports / `export default ` / type-only bits.
  let body = src
    .replace(/^\s*import .*?$/gm, '')
    .replace(/^\s*export\s+default\s+/m, 'module.exports = ')
    .replace(/:\s*typeof\s+\w+/g, '') // strip `: typeof en` annotations
  // Append "module.exports" if `export default m` form (where m was assigned)
  if (!/^module\.exports\s*=/m.test(body) && /^\s*const\s+m\s*[:=]/m.test(body)) {
    body = body.replace(/^\s*const\s+m\s*[:=]/m, 'const m =')
    body += '\nmodule.exports = m\n'
  }
  const m = { exports: {} }
  const fn = new Function('module', 'exports', body)
  fn(m, m.exports)
  return m.exports.default ?? m.exports
}

// ---------- 2. walk every string leaf in a message tree ----------
function* walkStrings(obj, prefix = '') {
  if (obj == null) return
  if (typeof obj === 'string') {
    yield [prefix, obj]
    return
  }
  if (Array.isArray(obj)) {
    for (let i = 0; i < obj.length; i++) yield* walkStrings(obj[i], `${prefix}[${i}]`)
    return
  }
  if (typeof obj === 'object') {
    for (const k of Object.keys(obj)) {
      yield* walkStrings(obj[k], prefix ? `${prefix}.${k}` : k)
    }
  }
}

// ---------- 3. rules ----------
//
// Returns array of { key, value, msg }
const ALLOW_EMOJI = new Set([
  // small text-style glyphs we deliberately put in i18n values
  '·', '✎', '✓', '⨯', '⤺', '▶', '◌', '⏱', '🔑',
  // CJK punctuation that emoji-aware regex flags
])

const AT_LINK_RE = /(?<!\{'@'\})@[A-Za-z0-9]/

function checkValue(key, value) {
  const out = []
  if (typeof value !== 'string') return out

  if (AT_LINK_RE.test(value)) {
    out.push({
      key, value,
      msg: 'literal "@" followed by alphanumeric — vue-i18n parses as linked-message ref. Escape with {\'@\'}.',
    })
  }

  // emoji / non-BMP detection: any char outside BMP, or BMP emoji ranges
  for (const ch of value) {
    const cp = ch.codePointAt(0)
    if (cp == null) continue
    if (ALLOW_EMOJI.has(ch)) continue
    // pure-emoji ranges (rough): supplementary plane, dingbats above 0x2700,
    // misc symbols above 0x2600, and pictographs.
    if (
      cp >= 0x1F300 || // supplementary symbols + emoji
      (cp >= 0x2600 && cp < 0x2800) ||
      (cp >= 0x2900 && cp < 0x2A00)
    ) {
      out.push({
        key, value,
        msg: `emoji ${JSON.stringify(ch)} (U+${cp.toString(16).toUpperCase()}) in i18n string — render via <Icon /> in the template, not in translations.`,
      })
      break // one emoji report per value is enough
    }
  }
  return out
}

// ---------- 4. main ----------
const files = fs.readdirSync(i18nDir)
  .filter(f => f.endsWith('.ts') && f !== 'index.ts')
  .map(f => path.join(i18nDir, f))

if (files.length === 0) {
  console.error(`lint-i18n: no locale files found in ${i18nDir}`)
  process.exit(2)
}

const locales = {}
for (const f of files) {
  const name = path.basename(f, '.ts')
  try {
    locales[name] = loadLocale(f)
  } catch (e) {
    console.error(`lint-i18n: failed to parse ${f}: ${e.message}`)
    process.exit(2)
  }
}

const violations = []

// per-locale content rules
for (const [name, tree] of Object.entries(locales)) {
  for (const [key, val] of walkStrings(tree)) {
    for (const v of checkValue(key, val)) {
      violations.push({ locale: name, ...v })
    }
  }
}

// key-parity check: every key in `en` should exist in others; report missing
const baseLocale = locales.en ?? Object.values(locales)[0]
const baseKeys   = new Set([...walkStrings(baseLocale)].map(([k]) => k))
for (const [name, tree] of Object.entries(locales)) {
  if (tree === baseLocale) continue
  const have = new Set([...walkStrings(tree)].map(([k]) => k))
  for (const k of baseKeys) {
    if (!have.has(k)) {
      violations.push({
        locale: name, key: k, value: '',
        msg: `key present in base locale but missing here.`,
      })
    }
  }
}

// ---------- 5. report ----------
if (violations.length === 0) {
  const n = Object.values(locales).reduce((s, t) => s + [...walkStrings(t)].length, 0)
  console.log(`lint-i18n: ${n} message strings across ${Object.keys(locales).length} locales — clean.`)
  process.exit(0)
}

console.error(`lint-i18n: ${violations.length} violation(s):\n`)
for (const v of violations) {
  console.error(`  [${v.locale}] ${v.key}`)
  if (v.value) console.error(`    value:  ${JSON.stringify(v.value).slice(0, 120)}`)
  console.error(`    issue:  ${v.msg}\n`)
}
process.exit(1)
