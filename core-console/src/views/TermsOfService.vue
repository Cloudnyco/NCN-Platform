<script setup lang="ts">
import { onMounted, ref } from 'vue'

// Document language toggle. Defaults to whatever the user picked last; if
// no prior choice exists, fall back to the user's browser preference
// (anything starting with `zh` → Chinese, otherwise English). Stored in
// its own localStorage key (separate from the site-wide i18n locale) so
// readers can keep the legal doc in their preferred legal language even
// if the rest of the UI is set to a different locale.
type DocLang = 'en' | 'zh'
function initialLang(): DocLang {
  if (typeof window === 'undefined') return 'en'
  const saved = window.localStorage.getItem('ncn:legal-lang') as DocLang | null
  if (saved === 'en' || saved === 'zh') return saved
  const nav = (navigator.language || '').toLowerCase()
  return nav.startsWith('zh') ? 'zh' : 'en'
}
const lang = ref<DocLang>(initialLang())
function setLang(l: DocLang) {
  lang.value = l
  if (typeof window !== 'undefined') window.localStorage.setItem('ncn:legal-lang', l)
}

// Static document metadata. Bump `version` + `effectiveDate` together
// when the substance changes; minor typo/style fixes don't bump version.
const version = '2.0'
const effectiveDate = '2026-05-23'

// Numbered ToC entries — anchors match the in-page <section id="…">.
const toc = [
  { n: '1',  id: 'definitions',  en: 'Definitions and Interpretation',                 zh: '定义与解释' },
  { n: '2',  id: 'acceptance',   en: 'Acceptance of Terms',                            zh: '条款的接受' },
  { n: '3',  id: 'operator',     en: 'Operator Identification and Statutory Basis',    zh: '运营者识别与法律基础' },
  { n: '4',  id: 'nature',       en: 'Nature and Scope of the Service',                zh: '服务的性质与范围' },
  { n: '5',  id: 'eligibility',  en: 'Eligibility',                                    zh: '合格使用者' },
  { n: '6',  id: 'aup',          en: 'Acceptable Use Policy',                          zh: '可接受使用政策' },
  { n: '7',  id: 'bgp',          en: 'BGP and Routing Obligations',                    zh: 'BGP 与路由义务' },
  { n: '8',  id: 'credentials',  en: 'Credentials and Authentication',                 zh: '凭证与认证' },
  { n: '9',  id: 'enforcement',  en: 'Enforcement, Suspension and Termination',        zh: '执法、暂停与终止' },
  { n: '10', id: 'no-warranty',  en: 'Disclaimer of Warranties',                       zh: '保证免责' },
  { n: '11', id: 'liability',    en: 'Limitation of Liability',                        zh: '责任限制' },
  { n: '12', id: 'indemnity',    en: 'Indemnification',                                zh: '赔偿义务' },
  { n: '13', id: 'privacy',      en: 'Privacy and Data Protection',                    zh: '隐私与数据保护' },
  { n: '14', id: 'export',       en: 'Export Controls, Sanctions, Counter-Terrorism',  zh: '出口管制、制裁与反恐合规' },
  { n: '15', id: 'ripe',         en: 'RIPE NCC Policies and Resource Registration',    zh: 'RIPE NCC 政策与资源登记' },
  { n: '16', id: 'nis2',         en: 'Network and Information Systems Security',       zh: '网络与信息系统安全' },
  { n: '17', id: 'jurisdictions', en: 'Server-Location Jurisdictions',                 zh: '服务器所在地司法管辖' },
  { n: '18', id: 'force-majeure', en: 'Force Majeure',                                 zh: '不可抗力' },
  { n: '19', id: 'notices',      en: 'Notices',                                        zh: '通知' },
  { n: '20', id: 'severability', en: 'Severability',                                   zh: '可分割性' },
  { n: '21', id: 'waiver',       en: 'Waiver and Non-Assignment',                      zh: '弃权与不可转让' },
  { n: '22', id: 'entire',       en: 'Entire Agreement',                               zh: '完整协议' },
  { n: '23', id: 'governing',    en: 'Governing Law',                                  zh: '适用法律' },
  { n: '24', id: 'disputes',     en: 'Dispute Resolution',                             zh: '争议解决' },
  { n: '25', id: 'modifications', en: 'Modifications and Document Control',            zh: '修订与版本控制' },
  { n: '26', id: 'survival',     en: 'Survival of Provisions',                         zh: '条款存续' },
  { n: '27', id: 'contact',      en: 'Contact',                                        zh: '联系方式' },
]

function scrollTo(id: string) {
  const el = document.getElementById(id)
  if (!el) return
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  el.scrollIntoView({ behavior: reduced ? 'auto' : 'smooth', block: 'start' })
}

const showBackToTop = ref(false)
onMounted(() => {
  const onScroll = () => { showBackToTop.value = window.scrollY > 600 }
  window.addEventListener('scroll', onScroll, { passive: true })
  onScroll()
})

function scrollTop() {
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  window.scrollTo({ top: 0, behavior: reduced ? 'auto' : 'smooth' })
}
</script>

<template>
  <article :data-lang="lang" class="max-w-4xl mx-auto px-4 sm:px-6 py-10 sm:py-16 font-mono text-gray-300 leading-relaxed">
    <!-- ===== Header ===== -->
    <header class="mb-12">
      <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-3 flex flex-wrap gap-x-2">
        <span class="text-emerald-500">// LEGAL</span>
        <span>·</span>
        <span>Terms of Service</span>
      </div>
      <h1 class="text-3xl sm:text-5xl text-gray-100 tracking-tight mb-2 font-bold">
        Terms of Service
      </h1>
      <h2 class="text-base sm:text-xl text-gray-500 tracking-wide normal-case mb-6">
        服务条款
      </h2>
      <div class="text-xs text-gray-500 normal-case tracking-normal border border-gray-800 bg-gray-900/40 backdrop-blur p-4 sm:p-5 grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-1">Version</div>
          <div class="text-gray-200">{{ version }}</div>
        </div>
        <div>
          <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-1">Effective</div>
          <div class="text-gray-200 tabular-nums">{{ effectiveDate }}</div>
        </div>
        <div>
          <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-1">Applies to</div>
          <div class="text-gray-200">AS64500 · example.com</div>
        </div>
      </div>
    </header>

    <!-- ===== Recitals / Lead ===== -->
    <section class="mb-12 border-l-2 border-emerald-500/60 pl-4 sm:pl-6 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed space-y-3">
      <p>
        These Terms of Service (the <strong class="text-gray-200">"Terms"</strong>) constitute a
        legally binding agreement between the Operator (as defined in §1 and identified in §3)
        and the User (as defined in §1) and govern the User's access to and use of the
        Acme Net (<strong class="text-gray-200">"NCN"</strong>,
        <strong class="text-gray-200">"AS64500"</strong>, or the
        <strong class="text-gray-200">"Service"</strong>), an experimental autonomous system
        operated under the <code class="text-emerald-400">example.com</code> domain and
        registered with the Réseaux IP Européens Network Coordination Centre
        (<strong class="text-gray-200">"RIPE NCC"</strong>).
      </p>
      <p class="text-gray-500 lang-zh">
        本服务条款("条款")构成运营者(定义见 §1,身份见 §3)与用户(定义见 §1)之间具有法律约束力的协议,
        规范用户访问和使用 Acme Net("NCN"、"AS64500"或"本服务")—
        一个在 <code class="text-emerald-400">example.com</code> 域名下运营、向欧洲 IP 网络协调中心
        ("RIPE NCC")登记的实验性自治系统。
      </p>
      <p class="text-emerald-400 text-sm">
        <strong>READ CAREFULLY.</strong> §6 (Acceptable Use), §7 (BGP Obligations), §9 (Enforcement),
        §10 (No Warranty), §11 (Liability Cap), §14 (Sanctions) and §24 (Disputes) contain
        provisions that materially limit your rights, expose you to forfeiture of credentials,
        or alter the forum or law applicable to disputes. The Privacy Policy referenced in §13
        forms an integral part of this agreement.
      </p>
      <p class="text-emerald-400 text-sm">
        <strong>请仔细阅读。</strong> §6(可接受使用)、§7(BGP 义务)、§9(执法)、§10(不保证)、§11(责任上限)、
        §14(制裁合规)与 §24(争议解决)载有实质性限制您权利、使您面临凭证销毁、或改变争议适用法律或管辖之条款。
        §13 所引用的隐私政策构成本协议不可分割之组成部分。
      </p>
    </section>

    <!-- ===== Language Toggle =====
         Sticky-position-light pill that lets the reader flip the entire
         document between English and Chinese. The two languages share
         every section's <h3> numbering + the metadata block; only the
         body paragraphs swap, so the toggle is instant (no route change,
         no layout shift). Persisted in localStorage `ncn:legal-lang`. -->
    <div class="mb-8 flex items-center justify-end gap-2">
      <span class="text-[10px] tracking-widest text-gray-600 uppercase mr-1">language</span>
      <button type="button" @click="setLang('en')"
              :class="['px-3 py-1.5 text-xs border tracking-widest uppercase transition-colors',
                       lang === 'en'
                         ? 'border-emerald-500 text-emerald-500 bg-emerald-500/10'
                         : 'border-gray-800 text-gray-500 hover:border-gray-600 hover:text-gray-300']">
        English
      </button>
      <button type="button" @click="setLang('zh')"
              :class="['px-3 py-1.5 text-xs border tracking-wide transition-colors',
                       lang === 'zh'
                         ? 'border-emerald-500 text-emerald-500 bg-emerald-500/10'
                         : 'border-gray-800 text-gray-500 hover:border-gray-600 hover:text-gray-300']">
        中文
      </button>
    </div>

    <!-- ===== Table of Contents ===== -->
    <nav aria-label="Table of contents" class="mb-12 border border-gray-800 bg-gray-900/40 backdrop-blur p-4 sm:p-5">
      <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-3 flex items-center gap-2">
        <span class="text-emerald-500">$</span>
        <span>cat /toc</span>
      </div>
      <ol class="text-xs sm:text-sm space-y-1.5">
        <li v-for="item in toc" :key="item.id">
          <a :href="'#' + item.id" @click.prevent="scrollTo(item.id)"
             class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline hover:text-emerald-400 transition-colors group">
            <span class="text-gray-600 tabular-nums group-hover:text-emerald-500 shrink-0">§{{ item.n }}</span>
            <span class="text-gray-300 group-hover:text-emerald-400">
              {{ item.en }}
              <span class="text-gray-600 normal-case tracking-normal ml-1">· {{ item.zh }}</span>
            </span>
          </a>
        </li>
      </ol>
    </nav>

    <!-- ===== § 1 Definitions ===== -->
    <section id="definitions" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§1.</span>Definitions and Interpretation
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">定义与解释</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">1.1.</strong> For the purposes of these Terms, the
          following capitalised expressions shall have the meanings set out below; references
          to statutory instruments shall be construed as references to the relevant
          instrument as amended, supplemented, or re-enacted from time to time.
        </p>
        <dl class="space-y-2 pl-1">
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Applicable Law"</dt><dd>the laws of the Kingdom of the Netherlands, the directly applicable instruments of European Union law, the laws of any jurisdiction in which a PoP physically operates (as listed in §17), and the laws of the User's habitual residence to the extent they apply on a mandatory basis;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"AS"</dt><dd>an Autonomous System as defined in RFC 1930 (BCP 6);</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"AS64500"</dt><dd>the AS Number allocated to the Operator by the RIPE NCC, used to identify the Service for the purposes of inter-domain routing;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"BGP"</dt><dd>the Border Gateway Protocol, version 4, as specified in RFC 4271, RFC 4760, and successor standards;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Credential"</dt><dd>any authentication token issued to or in respect of the User, including but not limited to WireGuard peer keys, BGP TCP-AO or MD5 shared secrets, panel passwords, time-based one-time-password (TOTP) secrets, recovery codes, WebAuthn / passkey credentials, and API tokens;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Effective Date"</dt><dd>the date stated in the metadata block at the head of these Terms;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"GDPR"</dt><dd>Regulation (EU) 2016/679 of the European Parliament and of the Council of 27 April 2016 on the protection of natural persons with regard to the processing of personal data;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"IRR"</dt><dd>an Internet Routing Registry, including the RIPE Database and any other registry recognised by the RIPE NCC;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Operator"</dt><dd>the natural or legal person registered with the RIPE NCC as the holder of AS64500 under the maintainer object <code class="text-emerald-400">ACMECLOUD-MNT</code>, identified more fully in §3;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"PoP"</dt><dd>a Point of Presence, namely a discrete physical location at which the Service operates network equipment, as enumerated in §17;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Privacy Policy"</dt><dd>the privacy policy published at <code class="text-emerald-400">https://example.com/privacy</code>, as updated from time to time;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"RIPE NCC"</dt><dd>Réseaux IP Européens Network Coordination Centre, the Regional Internet Registry for Europe, the Middle East, and parts of Central Asia, established as an association (vereniging) under the laws of the Netherlands and registered in Amsterdam;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"ROA"</dt><dd>a Route Origin Authorisation as defined in RFC 6482, signed under the RPKI of the relevant Regional Internet Registry;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"RPKI"</dt><dd>the Resource Public Key Infrastructure as defined in RFC 6480 and successor standards;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Service"</dt><dd>the set of networking, control-plane, and ancillary resources made available by the Operator under the <code class="text-emerald-400">example.com</code> domain and any subdomain thereof, including without limitation BGP sessions, Tunnels, Credentials, the administrative subsystem at <code class="text-emerald-400">admin.example.com</code>, and the publicly accessible Looking Glass;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Tunnel"</dt><dd>any virtual point-to-point or point-to-multipoint link established between a User and the Service, including without limitation WireGuard, GRE, IPv6-in-IPv6, IP-in-IP, GRETAP, ip6gre, VXLAN, and IPsec encapsulations;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"User"</dt><dd>any natural person, legal person, or autonomous system that accesses, connects to, or otherwise interacts with the Service in any capacity.</dd></div>
        </dl>
        <p>
          <strong class="text-gray-200">1.2.</strong> Headings are inserted for convenience
          only and shall not affect the construction of these Terms. References to the
          singular include the plural and vice versa. Words importing one gender include all
          genders. The expressions "including" and "in particular" shall be construed
          without limitation.
        </p>
        <p class="text-gray-500 lang-zh">
          1.1 就本条款之目的,下列大写表述具有以下含义;对法规之引用应作为对该法规经不时修订、补充或重新颁布版本之引用:
          "适用法律"—— 荷兰王国法律、欧盟法律之直接适用文书、PoP 物理运营所在的任何司法管辖区法律(见 §17 列示),
          以及在强制适用范围内用户惯常居所地之法律;
          "AS"—— RFC 1930 (BCP 6) 所定义之自治系统;
          "AS64500"—— RIPE NCC 分配给运营者的 AS 号,用于域间路由识别本服务;
          "BGP"—— 第 4 版边界网关协议,见 RFC 4271、RFC 4760 及后续标准;
          "凭证"—— 发放给用户或就用户而发放之任何身份验证令牌,包括但不限于 WireGuard 对等密钥、BGP TCP-AO 或 MD5 共享密钥、
          面板密码、TOTP 密钥、恢复码、WebAuthn/passkey 凭证及 API 令牌;
          "生效日"—— 本条款顶部元数据块所载日期;
          "GDPR"—— 欧洲议会和理事会 2016 年 4 月 27 日关于个人数据保护之欧盟 2016/679 号条例;
          "IRR"—— 互联网路由注册库,含 RIPE 数据库及 RIPE NCC 认可之任何其他注册库;
          "运营者"—— 在 RIPE NCC 以维护对象 <code class="text-emerald-400">ACMECLOUD-MNT</code>
          登记为 AS64500 持有人之自然人或法人,详见 §3;
          "PoP"—— 接入点,即本服务运营网络设备之具体物理位置,详列于 §17;
          "隐私政策"—— 发布于 <code class="text-emerald-400">https://example.com/privacy</code> 之隐私政策,随时更新;
          "RIPE NCC"—— 欧洲 IP 网络协调中心,欧洲、中东及中亚部分地区之地区性互联网注册机构,
          依荷兰法律设立为协会(vereniging),登记地阿姆斯特丹;
          "ROA"—— RFC 6482 所定义、由相关地区性互联网注册机构之 RPKI 签发之路由起源授权;
          "RPKI"—— RFC 6480 及后续标准所定义之资源公钥基础设施;
          "本服务"—— 运营者在 <code class="text-emerald-400">example.com</code> 域名及其任何子域名下提供之网络、控制面板及辅助资源之总和,
          包括但不限于 BGP 会话、隧道、凭证、<code class="text-emerald-400">admin.example.com</code> 处的管理子系统,
          以及可公开访问的 Looking Glass;
          "隧道"—— 用户与本服务之间所建立之任何虚拟点对点或点对多点链路,包括但不限于
          WireGuard、GRE、IPv6-in-IPv6、IP-in-IP、GRETAP、ip6gre、VXLAN 及 IPsec 封装;
          "用户"—— 以任何身份访问、连接或与本服务交互之任何自然人、法人或自治系统。
          1.2 标题仅为方便而插入,不影响本条款之解释。单数包含复数,反之亦然。表示一种性别之词包含所有性别。
          "包括"和"特别是"等表达应作非穷尽解释。
        </p>
      </div>
    </section>

    <!-- ===== § 2 Acceptance ===== -->
    <section id="acceptance" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§2.</span>Acceptance of Terms
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">条款的接受</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">2.1.</strong> By accessing, connecting to, or
          otherwise interacting with the Service in any capacity — including, without
          limitation, the establishment of a BGP session, the configuration of any Tunnel,
          authentication against any administrative subsystem, or the routing of any
          packet across NCN-controlled prefixes — the User affirms that the User has read,
          understood, and irrevocably agreed to be bound by these Terms in their entirety,
          together with the Privacy Policy and any policy incorporated by reference herein.
        </p>
        <p>
          <strong class="text-gray-200">2.2.</strong> If the User is acting on behalf of a
          legal person, the natural person clicking, configuring, or otherwise effecting
          acceptance hereby represents and warrants that they have the requisite authority
          to bind such legal person to these Terms.
        </p>
        <p>
          <strong class="text-gray-200">2.3.</strong> If the User does not agree to any
          provision of these Terms, the User shall immediately cease all use of the
          Service and shall, where applicable, request the destruction of any Credentials
          issued to the User pursuant to §27 (Contact).
        </p>
        <p class="text-gray-500 lang-zh">
          2.1 用户以任何方式访问、连接或与本服务交互(包括但不限于建立 BGP 会话、配置任何隧道、
          对任何管理子系统进行身份验证或在 NCN 控制的前缀上路由任何数据包)即确认其已阅读、理解并
          不可撤销地同意受本条款全部内容、隐私政策及任何在本条款中以引用方式纳入之政策之约束。
          2.2 用户代表法人行事者,执行点击、配置或以其他方式完成接受行为之自然人特此声明并保证其具有
          使该法人受本条款约束之必要权限。
          2.3 用户若不同意本条款任何规定,应立即停止使用本服务,并(适用情况下)依 §27(联系方式)
          请求销毁已发放给用户之任何凭证。
        </p>
      </div>
    </section>

    <!-- ===== § 3 Operator Identification ===== -->
    <section id="operator" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§3.</span>Operator Identification and Statutory Basis
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">运营者识别与法律基础</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">3.1 Operator.</strong> The Service is operated by
          the natural or legal person registered with the RIPE NCC as the holder of
          AS64500, identified in the RIPE Database under the maintainer object
          <code class="text-emerald-400">ACMECLOUD-MNT</code>. Current contact information,
          including administrative-c (<code class="text-emerald-400">admin-c</code>),
          technical-c (<code class="text-emerald-400">tech-c</code>), and abuse-c
          (<code class="text-emerald-400">abuse-c</code>) attributes, is publicly available
          via WHOIS at <code class="text-emerald-400">whois.ripe.net</code>.
        </p>
        <p>
          <strong class="text-gray-200">3.2 Statutory basis of allocation.</strong>
          The Operator holds AS64500 and the associated IPv6 address resources (currently
          <code class="text-emerald-400">2001:db8::/32</code> with sub-allocations
          referenced in the RIPE Database and signed in RPKI) by virtue of:
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) the <strong>RIPE NCC Standard Service Agreement</strong>, governed by the laws of the Netherlands;</li>
          <li>(b) the <strong>RIPE Policies</strong> adopted from time to time pursuant to the RIPE Policy Development Process; and</li>
          <li>(c) the Operator's <strong>membership of the RIPE NCC association</strong> (vereniging) under Articles 26-50 of Book 2 of the Dutch Civil Code (Burgerlijk Wetboek Boek 2).</li>
        </ul>
        <p>
          <strong class="text-gray-200">3.3 Consequential effect of registry actions.</strong>
          The User acknowledges that any suspension, reclamation, transfer, or non-renewal
          of AS64500 or the associated address resources by the RIPE NCC, whether
          undertaken pursuant to ripe-715 (Closure of Registration), to the RIPE NCC
          Arbiters Procedure (ripe-733), or otherwise, may materially affect the User's
          ability to access or use the Service. The Operator shall use reasonable
          endeavours to provide advance notice of any such anticipated event but shall not
          be liable for any failure to do so where the action is taken summarily by the
          RIPE NCC.
        </p>
        <p>
          <strong class="text-gray-200">3.4 Not-for-profit, hobbyist nature.</strong>
          The Service is provided on a not-for-profit, hobbyist basis. The Operator does
          not, by virtue of operating the Service, hold itself out as carrying on the
          business of an electronic communications service provider, internet service
          provider, or telecommunications operator within the meaning of any national law,
          and the Service shall not be construed as such.
        </p>
        <p class="text-gray-500 lang-zh">
          3.1 运营者 —— 本服务由在 RIPE NCC 登记为 AS64500 持有人之自然人或法人运营,
          在 RIPE 数据库下以维护对象 <code class="text-emerald-400">ACMECLOUD-MNT</code> 标识。
          当前联系信息(含 <code class="text-emerald-400">admin-c</code>、
          <code class="text-emerald-400">tech-c</code> 与
          <code class="text-emerald-400">abuse-c</code> 属性)可于
          <code class="text-emerald-400">whois.ripe.net</code> 公开查询。
          3.2 分配之法律基础 —— 运营者持有 AS64500 及相关 IPv6 地址资源(目前为
          <code class="text-emerald-400">2001:db8::/32</code> 及 RIPE 数据库中所记录、RPKI 签名之子分配)之依据为:
          (a) 受荷兰法律管辖之 <strong>RIPE NCC 标准服务协议</strong>;
          (b) 不时依 RIPE 政策制定程序所采纳之 <strong>RIPE 政策</strong>;
          (c) 运营者依《荷兰民法典》第 2 卷第 26-50 条所成之 <strong>RIPE NCC 协会(vereniging)成员资格</strong>。
          3.3 注册行为之后果 —— 用户承认,RIPE NCC 依 ripe-715(注册关闭)、ripe-733(RIPE NCC 仲裁程序)或其他规定
          对 AS64500 或相关地址资源所作之任何暂停、回收、转让或不续期,均可能实质性影响用户访问或使用本服务之能力。
          运营者应尽合理努力就任何预期事件提前通知,但就 RIPE NCC 即时采取之行动不承担未通知之责任。
          3.4 非营利性、业余性 —— 本服务以非营利、业余基础提供。运营者不因运营本服务而表示从事任何国家法律意义上之电子通信服务提供商、
          互联网服务提供商或电信运营商业务,本服务不得作此解释。
        </p>
      </div>
    </section>

    <!-- ===== § 4 Nature of Service ===== -->
    <section id="nature" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§4.</span>Nature and Scope of the Service
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">服务的性质与范围</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">4.1.</strong> NCN is an
          <strong class="text-amber-400">experimental, hobbyist, operator-run</strong>
          autonomous system whose primary purpose is the educational and recreational
          study of inter-domain routing, IPv6 protocol behaviour, and tunnel-overlay
          topologies. The Service is provided on a strict
          <strong class="text-amber-400">"AS IS" and "AS AVAILABLE"</strong> basis.
          <strong class="text-amber-400">No service-level agreement, uptime guarantee,
          throughput floor, or latency commitment of any kind is offered, implied, or
          warranted</strong>, whether by the Operator, the Operator's peers, the
          Operator's upstreams, or any third party associated with the Service.
        </p>
        <p>
          <strong class="text-gray-200">4.2 Variability.</strong>
          The User acknowledges and accepts that the Service may at any time, and without
          prior notice: (a) become unreachable, in whole or in part; (b) lose, reorder,
          or duplicate packets in transit; (c) be subject to maintenance, reconfiguration,
          or removal of any PoP; (d) be temporarily or permanently shut down at the sole
          discretion of the Operator; (e) be affected by upstream routing decisions,
          interconnect failures, or actions of third parties beyond the Operator's
          reasonable control.
        </p>
        <p>
          <strong class="text-gray-200">4.3 IPv6-only.</strong>
          The Service provides BGP transit and tunnelled connectivity for the IPv6
          unicast Address Family Identifier (AFI 2, SAFI 1) only. IPv4 unicast services
          (AFI 1, SAFI 1) are not supported, are not represented as supported, and are
          not within the scope of these Terms.
        </p>
        <p>
          <strong class="text-gray-200">4.4 No transit reselling.</strong>
          Unless expressly authorised in writing by the Operator, the User shall not
          resell, sublicense, or otherwise commercially redistribute connectivity
          obtained through the Service to any third party.
        </p>
        <p class="text-gray-500 lang-zh">
          4.1 NCN 是一个<strong class="text-amber-400">实验性、业余、由运营者运行</strong>之自治系统,
          首要目的为对域间路由、IPv6 协议行为及隧道叠加拓扑之教育与研究。本服务严格按"现状"
          ("AS IS")和"现有可用性"("AS AVAILABLE")基础提供。
          <strong class="text-amber-400">不提供任何服务等级协议(SLA)、可用性保证、吞吐下限或延迟承诺</strong>,
          无论由运营者、对等方、上游或与本服务相关之任何第三方作出。
          4.2 变动性 —— 用户承认并接受本服务在任何时候、无须事先通知,可能:(a) 全部或部分不可达;
          (b) 在传输中丢弃、乱序或重复数据包;(c) 进行维护、重新配置或移除任一 PoP;
          (d) 由运营者全权决定临时或永久关闭;(e) 受上游路由决定、互联故障或运营者合理控制范围外的第三方行为之影响。
          4.3 仅支持 IPv6 —— 本服务仅就 IPv6 单播地址族标识(AFI 2,SAFI 1)提供 BGP 中转及隧道连接。
          不支持、亦未表示支持 IPv4 单播服务(AFI 1,SAFI 1),其不在本条款范围之内。
          4.4 不得转售中转 —— 除非运营者另有书面明示授权,用户不得向任何第三方转售、再许可或以其他商业方式再分发通过本服务获得之连接。
        </p>
      </div>
    </section>

    <!-- ===== § 5 Eligibility ===== -->
    <section id="eligibility" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§5.</span>Eligibility
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">合格使用者</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The User represents and warrants that: (a) the User is at least the age of
          majority in the User's jurisdiction of habitual residence; (b) the User
          possesses full legal capacity to enter into a binding agreement; (c) the User
          is not located in, ordinarily resident in, or organised under the laws of any
          jurisdiction subject to comprehensive sanctions of the European Union, the
          United Nations Security Council, the United Kingdom, or the United States
          (including, without limitation, as at the Effective Date, the territories
          enumerated in §14); (d) the User has not been designated on any restricted-party
          list referenced in §14; and (e) the User is not, has not been, and shall not
          become subject to any judicial or administrative prohibition against using
          telecommunications services or operating an autonomous system.
        </p>
        <p class="text-gray-500 lang-zh">
          用户声明并保证:(a) 用户已达到其惯常居所地法律规定之成年年龄;(b) 用户具有完全民事行为能力以订立具有约束力之协议;
          (c) 用户不位于、不通常居住于、亦不依据任何受欧盟、联合国安理会、英国或美国全面制裁之司法管辖区(含但不限于截至生效日 §14 所列地区)法律组建;
          (d) 用户未被列入 §14 所引用之任何受限方名单;(e) 用户未曾、未将受到任何禁止使用电信服务或运营自治系统之司法或行政禁令。
        </p>
      </div>
    </section>

    <!-- ===== § 6 Acceptable Use Policy ===== -->
    <section id="aup" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§6.</span>Acceptable Use Policy
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">可接受使用政策</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The following conduct is <strong class="text-red-400">strictly prohibited</strong>
          on, from, or through the Service. This enumeration is illustrative and not
          exhaustive; it operates in addition to, and not in lieu of, the
          <strong>RIPE Anti-Abuse Policy (ripe-409)</strong> and any other binding
          instrument incorporated by reference in §15. The Operator retains sole
          discretion to determine whether conduct falls within the spirit of this policy.
        </p>
        <ul class="space-y-2 pl-1">
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-red-400 shrink-0 font-bold">6.1</span>
            <span><strong class="text-gray-200">Port scanning and network reconnaissance.</strong>
              Any unauthorised enumeration of network services on hosts that the User
              does not own or does not have explicit written authorisation to test —
              whether against hosts within NCN, peering networks, transit upstreams, or
              the broader Internet. Conduct in breach of this clause may also constitute
              an offence under the Crimes Ordinance (Cap. 200) of Region C SAR, the
              Unauthorized Computer Access Law (Act No. 128 of 1999) of Japan, and the
              Computer Fraud and Abuse Act (18 U.S.C. § 1030) of the United States.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-red-400 shrink-0 font-bold">6.2</span>
            <span><strong class="text-gray-200">Denial-of-service attacks.</strong>
              Originating, amplifying, reflecting, proxying, or otherwise facilitating
              any form of denial-of-service attack, including without limitation SYN
              floods, UDP floods, ICMP floods, application-layer floods, slowloris-class
              attacks, DNS / NTP / SSDP / Memcached / CharGen amplification, and
              resource-exhaustion attacks of any kind.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-red-400 shrink-0 font-bold">6.3</span>
            <span><strong class="text-gray-200">Unsolicited bulk communication.</strong>
              Originating, relaying, or facilitating spam or other unsolicited bulk
              communication, in breach of (without limitation) Directive (EU) 2002/58/EC
              (ePrivacy Directive) Art. 13, the CAN-SPAM Act of 2003 (15 U.S.C. § 7701
              et seq.), the Unsolicited Electronic Messages Ordinance (Cap. 593) of
              Region C SAR, or the Act on Regulation of Transmission of Specified
              Electronic Mail of Japan.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-red-400 shrink-0 font-bold">6.4</span>
            <span><strong class="text-gray-200">Malware and command-and-control infrastructure.</strong>
              Hosting, transmitting, or otherwise distributing malicious code; operating
              command-and-control infrastructure for botnets; or using the Service as a
              staging point for malware delivery, ransomware operations, or supply-chain
              compromise.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-red-400 shrink-0 font-bold">6.5</span>
            <span><strong class="text-gray-200">Credential abuse and impersonation.</strong>
              Sharing, selling, or transferring Credentials issued to the User; using
              Credentials issued to another User without their explicit written
              authorisation; impersonating any person, organisation, or autonomous
              system in connection with the Service.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-red-400 shrink-0 font-bold">6.6</span>
            <span><strong class="text-gray-200">Routing abuse.</strong>
              Announcing prefixes that the User is not the legitimate origin holder of;
              originating routes inconsistent with the User's IRR records or RPKI ROAs;
              propagating routes received from peers in a manner inconsistent with the
              announcing peer's published export policy; or otherwise engaging in route
              hijacking or route leaks within the meaning of RFC 7908. Further
              obligations are set out in §7.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-red-400 shrink-0 font-bold">6.7</span>
            <span><strong class="text-gray-200">Unlawful activity.</strong>
              Any activity that is unlawful under Applicable Law, including in particular
              the laws of the User's habitual residence, the Netherlands, the European
              Union, and the jurisdiction of any PoP through which the User's traffic
              is processed. The legality of an activity at the User's habitual residence
              shall not be construed as authorisation to perform that activity through a
              PoP located in a jurisdiction where it is unlawful.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-red-400 shrink-0 font-bold">6.8</span>
            <span><strong class="text-gray-200">CSAM.</strong>
              Without prejudice to the foregoing, any conduct involving child sexual abuse
              material (within the meaning of Directive 2011/93/EU and equivalent
              national instruments) is prohibited, and will be reported to the
              appropriate national authority and to the National Center for Missing &amp;
              Exploited Children (NCMEC) or its regional equivalent where applicable,
              without prior notice to the User.</span>
          </li>
        </ul>
        <p class="text-gray-500 mt-4 lang-zh">
          以下行为在本服务上、来自本服务或经由本服务<strong class="text-red-400">均严格禁止</strong>。
          本枚举仅为示例,非为穷尽;其与 §15 中以引用方式纳入之 <strong>RIPE 反滥用政策 (ripe-409)</strong>
          及任何其他有约束力之文书并行适用,而非取而代之:
          (6.1) 未授权端口扫描与网络侦察 — 亦可能构成中国香港《刑事罪行条例》(第 200 章)、
          日本《不正アクセス禁止法》(平成 11 年法律第 128 号)及美国《计算机欺诈与滥用法》
          (18 U.S.C. § 1030)项下之罪行;
          (6.2) 任何形式之拒绝服务攻击,包含 SYN/UDP/ICMP 洪水、应用层洪水、慢速攻击、
          DNS/NTP/SSDP/Memcached/CharGen 放大及任何资源耗尽攻击;
          (6.3) 未经请求之批量通信,违反(但不限于)欧盟 2002/58/EC 指令第 13 条、美国《CAN-SPAM 法》、
          中国香港《非应邀电子讯息条例》(第 593 章)或日本《特定电子邮件之送信适正化法》;
          (6.4) 恶意软件分发、僵尸网络 C&C 基础设施运营或将本服务作为恶意软件/勒索软件/供应链攻击之中转点;
          (6.5) 凭证滥用、共享、转售或冒用;
          (6.6) 路由滥用 — 未授权前缀通告、与 IRR/RPKI 记录不一致之路由起源、违反对端导出策略之路由扩散,
          以及 RFC 7908 意义之路由劫持或路由泄漏。进一步义务见 §7;
          (6.7) 任何违反适用法律之活动(尤其是用户惯常居所、荷兰、欧盟及流量经过之 PoP 所在司法管辖区之法律)。
          活动在用户惯常居所合法,不构成在该活动违法之 PoP 所在司法管辖区执行该活动之授权;
          (6.8) 儿童性虐待材料 — 任何涉及欧盟 2011/93/EU 指令及同等国家法律意义上之儿童性虐待材料之行为均被禁止,
          并将在适用情况下不经通知用户即报告予相应国家机关及失踪与受虐儿童国家中心(NCMEC)或其地区性对应机构。
        </p>
      </div>
    </section>

    <!-- ===== § 7 BGP and Routing Obligations ===== -->
    <section id="bgp" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§7.</span>BGP and Routing Obligations
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">BGP 与路由义务</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Where the User establishes, or seeks to establish, a BGP session with the
          Service, the User shall comply on a continuing basis with each of the
          following obligations:
        </p>
        <ul class="space-y-2 pl-1">
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.1</span>
            <span><strong class="text-gray-200">Origin Authority.</strong>
              The User shall announce only those address prefixes for which the User is
              the legitimate origin holder, as evidenced by valid IRR records and signed
              RPKI ROAs (or, in the case of unrouted experimental space, by written
              authorisation from the address-resource holder).</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.2</span>
            <span><strong class="text-gray-200">RPKI ROA Coverage.</strong>
              The User shall maintain current RPKI ROAs covering all prefixes announced
              to the Service, with a maxLength attribute no greater than the prefix
              length actually announced. The User shall not announce a prefix for which
              an applicable ROA exists if such announcement would be evaluated as
              <strong class="text-red-400">INVALID</strong> under RFC 6811.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.3</span>
            <span><strong class="text-gray-200">IRR Maintenance.</strong>
              The User shall maintain current and accurate route, route6, and aut-num
              objects in a recognised IRR (the RIPE Database for RIPE-allocated
              resources; the RADB, ARIN-IRR, APNIC-IRR, AFRINIC-IRR, or LACNIC-IRR for
              non-RIPE resources, in each case subject to the registry's acceptance).</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.4</span>
            <span><strong class="text-gray-200">MANRS Conformance.</strong>
              The User shall implement the MANRS Network Operator Actions (Action 1:
              Filtering; Action 2: Anti-Spoofing; Action 3: Coordination; Action 4:
              Global Validation) to the extent technically feasible within the User's
              network architecture.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.5</span>
            <span><strong class="text-gray-200">No Route Leaks.</strong>
              The User shall not propagate routes in a manner that violates RFC 7908.
              In particular, the User shall not propagate routes received from a transit
              upstream to a peer, or vice versa, unless expressly authorised by the
              source of those routes.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.6</span>
            <span><strong class="text-gray-200">Max-Prefix Limits.</strong>
              The User shall not exceed the maximum-prefix limit established by the
              Operator for the User's BGP session, currently 200 000 IPv6 prefixes per
              session. Exceeding this limit may trigger automatic session shutdown
              consistent with RFC 7196 §2.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.7</span>
            <span><strong class="text-gray-200">GTSM.</strong>
              Where supported by the User's BGP implementation, the User shall configure
              the Generalised TTL Security Mechanism (RFC 5082) with a Hop Count of one
              for adjacent eBGP sessions.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.8</span>
            <span><strong class="text-gray-200">Abuse Response.</strong>
              The User shall maintain a current
              <code class="text-emerald-400">abuse-c</code> contact in the IRR and shall
              acknowledge bona-fide abuse complaints concerning the User's announced
              prefixes within seventy-two (72) hours of receipt.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-emerald-500 shrink-0 font-bold">7.9</span>
            <span><strong class="text-gray-200">Cooperation.</strong>
              The User shall cooperate with the Operator in any investigation of
              suspected route leaks, BGP hijacks, prefix-origin disputes, or
              RPKI-validation incidents involving the User's announcements.</span>
          </li>
        </ul>
        <p>
          Failure to comply with any obligation set out in this §7 shall constitute
          grounds for the Operator to exercise its powers under §9, in addition to any
          remedy available under Applicable Law.
        </p>
        <p class="text-gray-500 lang-zh">
          用户建立或寻求建立与本服务之 BGP 会话时,应持续遵守下列各项义务:
          (7.1) 起源权 — 用户仅可通告其为合法起源持有人之地址前缀,以有效 IRR 记录与已签 RPKI ROA 证明
          (或对未路由之实验空间,以地址资源持有人之书面授权证明);
          (7.2) RPKI ROA 覆盖 — 用户应维持涵盖向本服务通告之所有前缀之 RPKI ROA,maxLength 属性不大于实际通告之前缀长度;
          若适用之 ROA 存在且使通告依 RFC 6811 评估为 <strong class="text-red-400">INVALID</strong>,
          用户不得通告该前缀;
          (7.3) IRR 维护 — 用户应在认可之 IRR 中维持当前且准确之 route、route6 与 aut-num 对象
          (RIPE 分配资源使用 RIPE 数据库;非 RIPE 资源使用 RADB、ARIN-IRR、APNIC-IRR、AFRINIC-IRR 或 LACNIC-IRR,
          以注册库接受为限);
          (7.4) MANRS 合规 — 用户应在其网络架构技术可行范围内实施 MANRS 网络运营者四项行动
          (行动 1:过滤;行动 2:防伪;行动 3:协调;行动 4:全球验证);
          (7.5) 不得路由泄漏 — 用户不得以违反 RFC 7908 之方式扩散路由,特别是除非路由来源明示授权,
          不得将自上游中转收到之路由扩散至对等方,或反之;
          (7.6) Max-prefix 上限 — 用户不得超出运营者就用户 BGP 会话所定之最大前缀上限,
          当前为每会话 200 000 条 IPv6 前缀。超出可能引发与 RFC 7196 §2 一致之自动会话关闭;
          (7.7) GTSM — 用户 BGP 实现支持时,应就相邻 eBGP 会话配置 RFC 5082 通用 TTL 安全机制,Hop Count 为 1;
          (7.8) 滥用响应 — 用户应在 IRR 中维持当前之 <code class="text-emerald-400">abuse-c</code> 联系人,
          并应在收到关于其所通告前缀之善意滥用投诉后 72 小时内确认;
          (7.9) 合作 — 用户应就涉及其通告之疑似路由泄漏、BGP 劫持、前缀起源争议或 RPKI 校验事件之调查与运营者合作。
          未能履行本 §7 所定任何义务,构成运营者依 §9 行使权力之依据,并不排除适用法律下之任何救济。
        </p>
      </div>
    </section>

    <!-- ===== § 8 Credentials ===== -->
    <section id="credentials" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§8.</span>Credentials and Authentication
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">凭证与认证</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">8.1.</strong> The User is solely responsible for
          maintaining the confidentiality, integrity, and physical security of all
          Credentials issued to or in respect of the User, and for all activity
          conducted under such Credentials, whether or not authorised by the User.
        </p>
        <p>
          <strong class="text-gray-200">8.2.</strong> The User shall, upon becoming
          aware or having reasonable cause to suspect any unauthorised disclosure of, or
          access to, the User's Credentials, immediately notify the Operator pursuant
          to §19 (Notices) and request rotation of the affected Credentials.
        </p>
        <p>
          <strong class="text-gray-200">8.3.</strong> The Operator may, in its sole
          discretion, require the User to enrol in second-factor authentication
          (including TOTP, WebAuthn / passkey, or hardware security keys) as a condition
          of continued access to the administrative subsystem.
        </p>
        <p>
          <strong class="text-gray-200">8.4.</strong> Credentials are not transferable
          and shall not be shared with, or made accessible to, any third party (including
          other Users) without the prior written authorisation of the Operator. Any
          such unauthorised disclosure shall be deemed a material breach of these Terms.
        </p>
        <p class="text-gray-500 lang-zh">
          8.1 用户对发放给用户或就用户发放之所有凭证之保密性、完整性与物理安全,
          以及在该等凭证下进行之所有活动(无论是否经用户授权)负有唯一责任。
          8.2 用户在获悉或有合理理由怀疑用户凭证遭到未授权披露或访问时,应依 §19(通知)立即通知运营者,
          并请求轮换受影响之凭证。
          8.3 运营者可全权决定要求用户启用第二因素认证(含 TOTP、WebAuthn/passkey 或硬件安全密钥)
          作为继续访问管理子系统之条件。
          8.4 凭证不可转让,未经运营者事先书面授权不得与任何第三方(含其他用户)共享或对其开放。
          任何此类未授权披露视为对本条款之重大违反。
        </p>
      </div>
    </section>

    <!-- ===== § 9 Enforcement ===== -->
    <section id="enforcement" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§9.</span>Enforcement, Suspension and Termination
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">执法、暂停与终止</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">9.1 Operator's Reserved Powers.</strong>
          Upon detection of any conduct that the Operator, in its sole and unfettered
          discretion, believes in good faith to constitute a violation of these Terms,
          an imminent threat to the integrity or security of the Service, or a breach
          of Applicable Law, the Operator reserves the
          <strong class="text-amber-400">unilateral right</strong>, exercisable at any
          time and without prior notice or judicial process, to:
        </p>
        <ul class="space-y-2 pl-4">
          <li class="text-gray-300"><span class="text-amber-400">(a)</span> Forcibly terminate any active Tunnel or BGP session associated with the User;</li>
          <li class="text-gray-300"><span class="text-amber-400">(b)</span> Permanently revoke and irrevocably destroy any Credentials issued to the User;</li>
          <li class="text-gray-300"><span class="text-amber-400">(c)</span> Blackhole or null-route prefixes originated by, transiting through, or destined for the User;</li>
          <li class="text-gray-300"><span class="text-amber-400">(d)</span> Block all future connections from any IP address, ASN, or other identifier associated with the User;</li>
          <li class="text-gray-300"><span class="text-amber-400">(e)</span> Suspend or permanently terminate the User's access to the Service in whole or in part;</li>
          <li class="text-gray-300"><span class="text-amber-400">(f)</span> Cooperate with law-enforcement, peer networks, the RIPE NCC, or affected third parties to the extent required by Applicable Law or by good operational practice.</li>
        </ul>
        <p>
          <strong class="text-gray-200">9.2 No Refunds.</strong>
          The Service is offered without charge; consequently, no refund of fees,
          consideration, or other compensation is or can be owed in connection with
          any enforcement action under §9.1.
        </p>
        <p>
          <strong class="text-gray-200">9.3 Preservation of Evidence.</strong>
          The Operator may retain logs, traffic counters, and Credential records
          sufficient to establish the fact of an abuse event for a period not exceeding
          ninety (90) days following termination, after which such records will be
          deleted in accordance with the Privacy Policy.
        </p>
        <p>
          <strong class="text-gray-200">9.4 Right to Appeal.</strong>
          The User may submit a written request for reconsideration to the address in
          §27 within thirty (30) calendar days of termination. The Operator shall
          consider any such request in good faith but is not obliged to reinstate access.
        </p>
        <p class="text-gray-500 lang-zh">
          9.1 运营者保留权力 —— 一旦运营者依其单方善意判断认为存在违反本条款之行为、对本服务完整性或安全性之紧迫威胁、
          或违反适用法律之情形,运营者保留<strong class="text-amber-400">单方权利</strong>,
          可在任何时候、无须事先通知或司法程序,采取以下行动:
          (a) 强制终止与用户有关之任何活动隧道或 BGP 会话;(b) 永久撤销并不可恢复地销毁发放给用户之任何凭证;
          (c) 对其前缀进行黑洞或空路由;(d) 阻断与用户有关之任何 IP、ASN 或其他标识符之所有后续连接;
          (e) 全部或部分暂停或永久终止用户对本服务之访问;
          (f) 在适用法律或良好运营惯例所要求范围内,与执法机关、对等网络、RIPE NCC 或受影响之第三方合作。
          9.2 无退款 —— 本服务免费提供,因此就 §9.1 任何执法行动,不存在亦无任何费用、对价或补偿之退还义务。
          9.3 证据保留 —— 运营者可保留足以证明滥用事件事实之日志、流量计数及凭证记录,保留期不超过终止后 90 日,
          其后按隐私政策删除。
          9.4 申诉权 —— 用户可在终止后 30 个日历日内向 §27 所列地址提交书面复议请求。
          运营者应本着诚信原则审议任何此等请求,但无义务恢复访问。
        </p>
      </div>
    </section>

    <!-- ===== § 10 No Warranty ===== -->
    <section id="no-warranty" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§10.</span>Disclaimer of Warranties
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">保证免责</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p class="uppercase tracking-wide text-gray-300 text-xs sm:text-sm border border-amber-500/40 bg-amber-900/10 p-4 leading-relaxed">
          To the maximum extent permitted by applicable law, the service is provided
          "AS IS" and "AS AVAILABLE", without warranty of any kind, express, implied,
          statutory, or otherwise, including but not limited to the implied warranties
          of merchantability, fitness for a particular purpose, non-infringement,
          accuracy, completeness, satisfactory quality, and uninterrupted operation.
          The Operator does not warrant that the service will be uninterrupted, error-free,
          secure, virus-free, or free of harmful components.
        </p>
        <p>
          Nothing in this §10 shall purport to exclude or limit any liability that
          cannot be excluded or limited under Applicable Law, including in particular
          liability for fraud, gross negligence, or wilful misconduct.
        </p>
        <p class="text-gray-500 lang-zh">
          在适用法律允许之最大限度内,本服务按"现状"与"现有可用性"基础提供,不附任何形式之明示、默示、法定或其他保证,
          包括但不限于适销性、特定用途适用性、不侵权、准确性、完整性、令人满意之质量及不间断运行之默示保证。
          运营者不保证本服务将不间断、无错误、安全、无病毒或无有害组件。本 §10 不应视为排除或限制任何在适用法律下不得排除或限制之责任,
          特别是对欺诈、重大过失或故意不当行为之责任。
        </p>
      </div>
    </section>

    <!-- ===== § 11 Limitation of Liability ===== -->
    <section id="liability" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§11.</span>Limitation of Liability
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">责任限制</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">11.1.</strong>
          To the maximum extent permitted by Applicable Law, in no event shall the
          Operator, the Operator's affiliates, contributors, peers, or upstreams be
          liable for any indirect, incidental, special, consequential, exemplary, or
          punitive damages, or for any loss of profits, revenue, data, business
          opportunity, or goodwill, arising out of or in connection with the User's
          use of, or inability to use, the Service — even if advised in advance of the
          possibility of such damages.
        </p>
        <p>
          <strong class="text-gray-200">11.2.</strong>
          The aggregate liability of the Operator in connection with these Terms and
          the User's use of the Service shall not exceed
          <strong class="text-gray-200">EUR 0.00 (zero euros)</strong>, reflecting the
          no-fee, hobbyist nature of the Service.
        </p>
        <p>
          <strong class="text-gray-200">11.3.</strong>
          Nothing in this §11 shall purport to exclude or limit any liability that
          cannot be excluded or limited under Applicable Law, including in particular:
          (a) liability for death or personal injury caused by negligence; (b)
          liability for fraud or fraudulent misrepresentation; (c) liability that
          cannot be excluded under Council Directive 85/374/EEC on liability for
          defective products; or (d) any liability under Art. 82 GDPR for material or
          non-material damage suffered as a result of unlawful processing of personal
          data.
        </p>
        <p class="text-gray-500 lang-zh">
          11.1 在适用法律允许之最大限度内,运营者、其关联人、贡献者、对等方或上游对因用户使用或无法使用本服务而产生之任何间接、附带、特殊、
          后果性、惩戒性或惩罚性损害,或任何利润、收入、数据、业务机会或商誉损失,均不承担责任,即使已被事先告知此类损害之可能性。
          11.2 运营者就本条款及用户使用本服务之总责任额上限为<strong class="text-gray-300">零欧元(EUR 0.00)</strong>,
          反映本服务无偿、业余之性质。
          11.3 本 §11 不应视为排除或限制任何在适用法律下不得排除或限制之责任,特别是:
          (a) 因疏忽造成之死亡或人身伤害责任;(b) 欺诈或欺诈性虚假陈述责任;
          (c) 不能依《理事会指令 85/374/EEC》(产品缺陷责任)排除之责任;
          (d) 依 GDPR 第 82 条因非法处理个人数据所产生之物质或非物质损害责任。
        </p>
      </div>
    </section>

    <!-- ===== § 12 Indemnification ===== -->
    <section id="indemnity" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§12.</span>Indemnification
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">赔偿义务</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The User shall, on a continuing basis, defend, indemnify, and hold harmless
          the Operator and the Operator's affiliates and contributors from and against
          any and all claims, damages, obligations, losses, liabilities, costs, and
          expenses (including reasonable attorneys' fees) arising from:
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) the User's use of, or activity through, the Service;</li>
          <li>(b) the User's breach of these Terms or any policy incorporated by reference herein;</li>
          <li>(c) the User's breach of any third-party right (including intellectual property and privacy rights);</li>
          <li>(d) any claim by a third party that traffic originating from, or transiting through, Credentials issued to the User caused damage;</li>
          <li>(e) any administrative penalty imposed on the Operator by a national supervisory authority arising from the User's processing activities; and</li>
          <li>(f) any RIPE-NCC closure, reclamation, or enforcement action arising from the User's conduct.</li>
        </ul>
        <p class="text-gray-500 lang-zh">
          用户应持续为运营者、其关联人及贡献者辩护、赔偿并使之免受因下列原因引起之所有索赔、损害、义务、损失、责任、费用及开支
          (含合理律师费):
          (a) 用户对本服务之使用或经其进行之活动;
          (b) 用户对本条款或在本条款中以引用方式纳入之任何政策之违反;
          (c) 用户侵犯任何第三方权利(含知识产权与隐私权);
          (d) 任何第三方主张称源自或经由发放给用户之凭证之流量造成损害;
          (e) 国家监管机构因用户之处理活动对运营者所处之任何行政罚款;
          (f) 因用户行为而引起之 RIPE NCC 关闭、回收或执法行动。
        </p>
      </div>
    </section>

    <!-- ===== § 13 Privacy ===== -->
    <section id="privacy" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§13.</span>Privacy and Data Protection
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">隐私与数据保护</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Processing of personal data by the Operator is governed by the Privacy
          Policy published at
          <a href="/privacy" class="text-emerald-400 hover:underline">example.com/privacy</a>,
          which forms an integral part of these Terms and which the User is deemed to
          have read and accepted upon accepting these Terms. The Privacy Policy
          identifies the Operator as the data controller (within the meaning of
          Art. 4(7) GDPR) and sets out the categories of data processed, the legal
          bases for processing, the periods of retention, the recipients of data,
          arrangements for international transfers, and the rights available to data
          subjects.
        </p>
        <p>
          Where there is any conflict between these Terms and the Privacy Policy with
          respect to the processing of personal data, the Privacy Policy shall prevail.
        </p>
        <p class="text-gray-500 lang-zh">
          运营者对个人数据之处理受发布于
          <a href="/privacy" class="text-emerald-400 hover:underline">example.com/privacy</a>
          之隐私政策管辖,该政策构成本条款不可分割之组成部分,
          用户接受本条款时视同已阅读并接受隐私政策。隐私政策识别运营者为 GDPR 第 4(7) 条意义之数据控制者,
          并载明处理之数据类别、处理之法律依据、保留期限、数据接收方、跨境传输安排及数据主体可享有之权利。
          本条款与隐私政策就个人数据处理事项有冲突时,以隐私政策为准。
        </p>
      </div>
    </section>

    <!-- ===== § 14 Export Controls and Sanctions ===== -->
    <section id="export" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§14.</span>Export Controls, Sanctions, Counter-Terrorism
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">出口管制、制裁与反恐合规</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">14.1 Sanctions framework.</strong>
          The User represents and warrants that the User is not, and shall not for the
          duration of these Terms become, a Sanctioned Person. For the purposes of
          this §14, a "Sanctioned Person" is any natural or legal person, vessel, or
          aircraft that is:
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) designated on any sanctions list maintained by the European Union, in particular pursuant to <strong>Council Regulation (EU) No 269/2014</strong> (Russia), <strong>Council Regulation (EC) No 765/2006</strong> (Belarus), <strong>Council Regulation (EU) No 36/2012</strong> (Syria), <strong>Council Regulation (EU) 2017/1509</strong> (DPRK), <strong>Council Regulation (EU) 2023/1529</strong> (Iran drones), or successor instruments;</li>
          <li>(b) designated on the Specially Designated Nationals and Blocked Persons List ("SDN List") maintained by the U.S. Department of the Treasury's Office of Foreign Assets Control ("OFAC");</li>
          <li>(c) designated on the Consolidated List of Financial Sanctions Targets maintained by Her Majesty's Treasury, United Kingdom (OFSI);</li>
          <li>(d) designated on the Consolidated United Nations Security Council Sanctions List; or</li>
          <li>(e) owned 50% or more by, or otherwise controlled by, a person designated under any of the foregoing.</li>
        </ul>
        <p>
          <strong class="text-gray-200">14.2 Geographic restrictions.</strong>
          The User shall not use the Service in, or for the benefit of any natural or
          legal person located in, any jurisdiction subject to comprehensive sanctions
          of the European Union, including as at the Effective Date: Belarus,
          North Korea (DPRK), Iran, the territories of Crimea, Donetsk, Kherson, Luhansk,
          and Zaporizhzhia under non-government-controlled status, Syria, and Russia
          to the extent prohibited by Regulation (EU) No 833/2014.
        </p>
        <p>
          <strong class="text-gray-200">14.3 Dual-use items.</strong>
          The User shall not use the Service to violate
          <strong>Regulation (EU) 2021/821</strong> (the recast Dual-Use Regulation) or
          any equivalent national export-control statute, including without limitation
          the U.S. Export Administration Regulations (15 C.F.R. Parts 730-774) and the
          U.K. Export Control Order 2008.
        </p>
        <p>
          <strong class="text-gray-200">14.4 Counter-terrorism.</strong>
          The User shall not use the Service to facilitate, finance, or otherwise
          support any act of terrorism within the meaning of
          <strong>Directive (EU) 2017/541</strong> on combating terrorism,
          <strong>Council Common Position 2001/931/CFSP</strong>, or any equivalent
          national counter-terrorism legislation.
        </p>
        <p class="text-gray-500 lang-zh">
          14.1 制裁框架 —— 用户声明并保证其未,在本条款存续期间亦不会成为受制裁人。
          就本 §14 之目的,"受制裁人"系指任何被以下列示之自然人、法人、船舶或航空器:
          (a) 欧盟维持之任何制裁名单,特别是依据 <strong>欧盟理事会 269/2014 号条例</strong>(俄罗斯)、
          <strong>欧盟理事会 765/2006 号条例</strong>(白俄罗斯)、<strong>欧盟理事会 36/2012 号条例</strong>(叙利亚)、
          <strong>欧盟理事会 2017/1509 号条例</strong>(朝鲜)、<strong>欧盟理事会 2023/1529 号条例</strong>(伊朗无人机)
          或后续文书;
          (b) 美国财政部外国资产管制办公室(OFAC)所维持之特别指定国民及被冻结人员名单("SDN 名单");
          (c) 英国财政部维持之金融制裁目标综合名单(OFSI);
          (d) 联合国安全理事会综合制裁名单;或
          (e) 被前述任一名单所指定之人持有 50% 或以上股权,或以其他方式受其控制者。
          14.2 地域限制 —— 用户不得在受欧盟全面制裁之任何司法管辖区,或为该等司法管辖区之任何自然人或法人之利益使用本服务,
          包括截至生效日:白俄罗斯、朝鲜(DPRK)、伊朗、克里米亚、顿涅茨克、赫尔松、卢甘斯克及扎波罗热中非政府控制之地区、
          叙利亚,以及在 833/2014 号条例所禁止范围内之俄罗斯。
          14.3 两用物项 —— 用户不得使用本服务违反<strong>欧盟 2021/821 号条例</strong>(经修订之两用物项条例)
          或任何同等之国家出口管制法规(含美国出口管理条例 15 C.F.R. 第 730-774 部及英国 2008 年出口管制令)。
          14.4 反恐 —— 用户不得使用本服务以任何方式促进、资助或支持
          <strong>欧盟 2017/541 号指令</strong>(打击恐怖主义)、
          <strong>欧盟理事会 2001/931/CFSP 共同立场</strong>或任何同等国家反恐立法意义上之恐怖主义行为。
        </p>
      </div>
    </section>

    <!-- ===== § 15 RIPE NCC Policies ===== -->
    <section id="ripe" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§15.</span>RIPE NCC Policies and Resource Registration
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">RIPE NCC 政策与资源登记</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The User acknowledges and agrees that the Service is operated by the
          Operator in accordance with the following authoritative instruments, each as
          amended from time to time, and that compliance with these Terms shall not
          relieve the User of any independent obligation to comply with any of the same:
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) the <strong>RIPE NCC Standard Service Agreement</strong> entered into between the Operator and the RIPE NCC;</li>
          <li>(b) the <strong>RIPE NCC Service Region Acceptable Use Policy</strong>;</li>
          <li>(c) <strong>ripe-409</strong> — the RIPE Anti-Abuse Policy;</li>
          <li>(d) <strong>ripe-715</strong> — Closure of Registration and Reclamation of Internet Resources;</li>
          <li>(e) <strong>ripe-733</strong> — RIPE NCC Conflict Arbitration Procedure;</li>
          <li>(f) <strong>ripe-738</strong> — IPv6 Address Allocation and Assignment Policy;</li>
          <li>(g) the <strong>RIPE Database Acceptable Use Policy</strong>;</li>
          <li>(h) any binding policy adopted by the RIPE Community through the RIPE Policy Development Process or by the RIPE NCC General Meeting from time to time.</li>
        </ul>
        <p>
          The User shall, upon reasonable written request, cooperate with the Operator
          in any investigation, audit, or arbitration conducted by the RIPE NCC
          concerning the use of resources allocated to AS64500. A confirmed and
          material breach by the User of any RIPE policy enumerated above, where such
          breach exposes the Operator to a loss of registry standing, shall be deemed
          a breach of these Terms and may be enforced under §9.
        </p>
        <p class="text-gray-500 lang-zh">
          用户承认并同意运营者依下列权威文书运营本服务(各文书以现行有效版本为准),
          且符合本条款不得免除用户独立遵守该等文书之任何义务:
          (a) 运营者与 RIPE NCC 订立之 <strong>RIPE NCC 标准服务协议</strong>;
          (b) <strong>RIPE NCC 服务区域可接受使用政策</strong>;
          (c) <strong>ripe-409</strong> RIPE 反滥用政策;
          (d) <strong>ripe-715</strong> 注册关闭与互联网资源回收政策;
          (e) <strong>ripe-733</strong> RIPE NCC 冲突仲裁程序;
          (f) <strong>ripe-738</strong> IPv6 地址分配与指派政策;
          (g) <strong>RIPE 数据库可接受使用政策</strong>;
          (h) RIPE 社区通过 RIPE 政策制定程序或 RIPE NCC 全体会员大会随时采纳之任何具约束力之政策。
          用户应在合理书面请求下,就 RIPE NCC 针对 AS64500 资源使用所进行之任何调查、审计或仲裁与运营者合作。
          用户对上列任一 RIPE 政策之经证实之重大违反,致运营者面临注册机构地位损失之风险者,视同违反本条款,可依 §9 执行。
        </p>
      </div>
    </section>

    <!-- ===== § 16 NIS2 ===== -->
    <section id="nis2" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§16.</span>Network and Information Systems Security
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">网络与信息系统安全</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Service is not, as at the Effective Date, classified as an "essential
          entity" or "important entity" within the meaning of
          <strong>Directive (EU) 2022/2555 ("NIS2")</strong>. Nevertheless, in voluntary
          alignment with the cybersecurity risk-management principles set out in
          Art. 21 NIS2, the Operator endeavours to implement and maintain appropriate
          technical, operational, and organisational measures having regard to the
          state of the art and the scale of the Service.
        </p>
        <p>
          In the event of a significant incident materially affecting the Service's
          availability, integrity, authenticity, or confidentiality, the Operator will,
          to the extent reasonably practicable: (a) take prompt mitigation action;
          (b) notify affected Users via the email address on file at the earliest
          reasonable opportunity; (c) inform peer networks and upstream providers as
          necessary to contain the incident; (d) cooperate with the relevant national
          Computer Security Incident Response Team (CSIRT) where applicable; and
          (e) publish a post-incident summary at <code class="text-emerald-400">example.com</code>
          once the incident is resolved and no further investigative needs are
          prejudiced.
        </p>
        <p class="text-gray-500 lang-zh">
          截至生效日,本服务并不被归类为<strong>《欧盟 2022/2555 号指令》("NIS2")</strong>意义之"基本实体"或"重要实体"。
          然而,运营者自愿与 NIS2 第 21 条所列网络安全风险管理原则保持一致,并在考虑技术现状与本服务规模之前提下,
          致力于实施并维持适当之技术、运营与组织措施。
          就实质上影响本服务可用性、完整性、真实性或机密性之重要事件,运营者将在合理可行之范围内:
          (a) 采取及时缓解行动;(b) 尽早通过留存邮箱通知受影响用户;
          (c) 在控制事件所必要范围内告知对等网络与上游提供商;(d) 在适用情况下与相关国家计算机安全事件响应小组(CSIRT)合作;
          (e) 事件解决且不损害进一步调查需求后,在 <code class="text-emerald-400">example.com</code> 发布事件摘要。
        </p>
      </div>
    </section>

    <!-- ===== § 17 Server Locations ===== -->
    <section id="jurisdictions" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§17.</span>Server-Location Jurisdictions
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">服务器所在地司法管辖</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Service operates Points of Presence in the following jurisdictions. Each
          PoP is subject to the territorial laws of its host jurisdiction, including
          but not limited to those listed below; the User shall comply with the
          territorial laws of any PoP at which the User's traffic is processed.
        </p>
        <div class="border border-gray-800 bg-gray-900/40 p-3 sm:p-4 space-y-3">
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">Region C SAR</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· Personal Data (Privacy) Ordinance (Cap. 486)</li>
              <li>· Crimes Ordinance (Cap. 200), §§ 161, 161A — unauthorised access to computer by telecommunications</li>
              <li>· Unsolicited Electronic Messages Ordinance (Cap. 593)</li>
              <li>· Telecommunications Ordinance (Cap. 106)</li>
            </ul>
          </div>
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">Japan</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· Act on the Protection of Personal Information ("APPI"), Act No. 57 of 2003</li>
              <li>· Unauthorized Computer Access Law, Act No. 128 of 1999</li>
              <li>· Telecommunications Business Act, Act No. 86 of 1984</li>
              <li>· Act on Regulation of Transmission of Specified Electronic Mail, Act No. 26 of 2002</li>
            </ul>
          </div>
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">United States · California</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· Computer Fraud and Abuse Act, 18 U.S.C. § 1030</li>
              <li>· Electronic Communications Privacy Act, 18 U.S.C. §§ 2510-2523</li>
              <li>· CAN-SPAM Act of 2003, 15 U.S.C. §§ 7701-7713</li>
              <li>· California Consumer Privacy Act of 2018, Cal. Civ. Code § 1798.100 et seq.</li>
              <li>· DMCA, 17 U.S.C. § 512 (safe harbour for transitory digital network communications)</li>
            </ul>
          </div>
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">Region D</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· Personal Data Protection Act 2012 (No. 26 of 2012)</li>
              <li>· Computer Misuse Act (Cap. 50A) — unauthorised access to / modification of computer material</li>
              <li>· Spam Control Act 2007</li>
              <li>· Telecommunications Act 1999</li>
            </ul>
          </div>
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">Germany · within the EEA</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· GDPR, supplemented by the Bundesdatenschutzgesetz (BDSG)</li>
              <li>· Strafgesetzbuch (StGB) §§ 202a-202c — unauthorised data access and interception</li>
              <li>· Telekommunikation-Telemedien-Datenschutz-Gesetz (TTDSG) and Telekommunikationsgesetz (TKG)</li>
              <li>· Gesetz gegen den unlauteren Wettbewerb (UWG) § 7 — unsolicited electronic messages</li>
            </ul>
          </div>
        </div>
        <p>
          The Operator shall not be obliged to host or transit any User traffic that,
          in the Operator's reasonable judgment, would expose the Operator to a
          violation of any law applicable at the PoP through which such traffic would
          be processed. Nothing in this §17 shall limit the application of EU law,
          including the GDPR, to processing operations carried out by the Operator
          regardless of the location of the PoP.
        </p>
        <p class="text-gray-500 lang-zh">
          本服务在下列司法管辖区运营接入点。每一接入点受其所在司法管辖区领土法律之约束,
          包括但不限于下列所示;用户应遵守流量经由处理之任一接入点所在司法管辖区之领土法律。
          中国香港特别行政区:《个人资料(私隐)条例》(第 486 章)、
          《刑事罪行条例》(第 200 章)§ 161、§ 161A(未经授权访问电信计算机)、
          《非应邀电子讯息条例》(第 593 章)、《电讯条例》(第 106 章);
          日本:《个人信息保护法》(平成 15 年法律第 57 号)、
          《不正アクセス禁止法》(平成 11 年法律第 128 号)、
          《电气通信事业法》(昭和 59 年法律第 86 号)、
          《特定電子メールの送信の適正化等に関する法律》(平成 14 年法律第 26 号);
          新加坡:《2012 年个人数据保护法》(2012 年第 26 号法令)、《计算机滥用法》(第 50A 章)、
          《2007 年垃圾邮件控制法》、《1999 年电信法》;
          美国 · 加利福尼亚州:《计算机欺诈与滥用法》18 U.S.C. § 1030、
          《电子通信隐私法》18 U.S.C. §§ 2510-2523、《CAN-SPAM 法》15 U.S.C. §§ 7701-7713、
          《加利福尼亚消费者隐私法》Cal. Civ. Code § 1798.100 等、
          《DMCA》17 U.S.C. § 512(短时数字网络通信安全港);
          德国(EEA 之内):GDPR 及《联邦数据保护法》(BDSG)、《刑法典》(StGB)§§ 202a-202c(未经授权数据访问与拦截)、
          《电信电信媒体数据保护法》(TTDSG)与《电信法》(TKG)、《反不正当竞争法》(UWG)§ 7(非应邀电子讯息)。
          若运营者依其合理判断认为承载或中转用户流量将使其在该流量经由处理之 PoP 适用法律下面临违法风险,
          运营者无义务承载或中转该等流量。本 §17 不限制欧盟法律(含 GDPR)对运营者实施之处理活动之适用,无论 PoP 位置。
        </p>
      </div>
    </section>

    <!-- ===== § 18 Force Majeure ===== -->
    <section id="force-majeure" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§18.</span>Force Majeure
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">不可抗力</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Neither party shall be liable for any failure or delay in the performance of
          its obligations under these Terms to the extent such failure or delay is
          caused by circumstances beyond its reasonable control, including but not
          limited to: acts of God, natural disasters, fire, flood, earthquake, war,
          civil disorder, terrorism, governmental action, embargo, export-control
          restriction, fibre cuts, submarine-cable failure, large-scale routing
          incident (including widespread BGP hijack, route leak, or RPKI anchor outage),
          third-party denial-of-service attack, or failure of any public utility,
          telecommunications carrier, or upstream Internet service provider on which
          the party reasonably relies. The affected party shall take reasonable steps
          to mitigate the effect and resume performance as soon as reasonably
          practicable after cessation.
        </p>
        <p class="text-gray-500 lang-zh">
          任何一方就其依本条款应履行之义务,因超出其合理控制范围之情形所致之不履行或延迟履行,概不承担责任,
          包括但不限于:天灾、自然灾害、火灾、洪水、地震、战争、内乱、恐怖主义、政府行为、禁运、出口管制限制、
          光缆中断、海底电缆故障、大规模路由事件(含广泛之 BGP 劫持、路由泄漏、RPKI 信任锚故障)、
          第三方拒绝服务攻击,或该方合理依赖之任何公用事业、电信运营商或上游互联网服务提供商之故障。
          受影响一方应采取合理步骤减轻影响,并应在该事件停止后尽快恢复履行。
        </p>
      </div>
    </section>

    <!-- ===== § 19 Notices ===== -->
    <section id="notices" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§19.</span>Notices
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">通知</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">19.1 Notice to the Operator.</strong>
          Any notice to the Operator under these Terms shall be made by electronic mail
          to <a href="mailto:noc@example.com" class="text-emerald-400 hover:underline">noc@example.com</a>
          for operational matters, or to <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>
          for abuse reports, privacy enquiries, and legal notices. Notice shall be
          deemed received on the next business day in the Netherlands following
          dispatch, absent evidence of non-delivery.
        </p>
        <p>
          <strong class="text-gray-200">19.2 Notice to the User.</strong>
          Any notice to the User under these Terms shall be made (at the Operator's
          option) by: (a) electronic mail to the address on file for the User; (b)
          publication on <code class="text-emerald-400">example.com</code> in a section
          reasonably calculated to come to the User's attention; or (c) where
          applicable, by message to the User's registered RIPE Database
          <code class="text-emerald-400">abuse-c</code> contact.
        </p>
        <p>
          <strong class="text-gray-200">19.3 Public emergency notices.</strong>
          In the event of an imminent operational, security, or legal matter requiring
          public notice, the Operator may publish such notice at
          <code class="text-emerald-400">example.com</code> with immediate effect.
        </p>
        <p class="text-gray-500 lang-zh">
          19.1 向运营者发出通知 —— 本条款项下向运营者发出之任何通知,应通过电子邮件:
          运营事项发至 <a href="mailto:noc@example.com" class="text-emerald-400 hover:underline">noc@example.com</a>,
          滥用举报、隐私查询及法律通知发至 <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>。
          在无未投递证明之情形下,通知视为于发送后下一荷兰工作日送达。
          19.2 向用户发出通知 —— 本条款项下向用户发出之任何通知,运营者可选择通过以下方式发出:
          (a) 电子邮件至用户留存地址;(b) 在 <code class="text-emerald-400">example.com</code> 之合理使用户注意之版位发布;
          (c) 适用情况下,通过消息发至用户登记之 RIPE 数据库 <code class="text-emerald-400">abuse-c</code> 联系人。
          19.3 公共紧急通知 —— 就需公开通知之即时运营、安全或法律事项,运营者可在
          <code class="text-emerald-400">example.com</code> 发布即时生效之通知。
        </p>
      </div>
    </section>

    <!-- ===== § 20 Severability ===== -->
    <section id="severability" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§20.</span>Severability
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">可分割性</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          If any provision of these Terms is held by a court of competent jurisdiction,
          arbitral tribunal, or competent supervisory authority to be invalid,
          illegal, or unenforceable in any respect, such invalidity, illegality, or
          unenforceability shall not affect any other provision of these Terms, and
          these Terms shall be construed as if such invalid, illegal, or
          unenforceable provision had never been included, save that the parties shall
          negotiate in good faith to replace the offending provision with a valid,
          legal, and enforceable provision that comes as close as reasonably possible
          to expressing the parties' original intent.
        </p>
        <p class="text-gray-500 lang-zh">
          本条款之任一规定若被有管辖权之法院、仲裁庭或主管监管机构认定为在任何方面无效、违法或不可执行,
          该等无效、违法或不可执行性不影响本条款其他规定,本条款应作如同该等无效、违法或不可执行规定从未列入般解释,
          但双方应本着诚信原则协商以一项有效、合法且可执行之规定取代之,该等替代规定应在合理可行之范围内尽量贴近双方之原意。
        </p>
      </div>
    </section>

    <!-- ===== § 21 Waiver and Non-Assignment ===== -->
    <section id="waiver" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§21.</span>Waiver and Non-Assignment
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">弃权与不可转让</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">21.1 Waiver.</strong>
          No failure or delay by either party in exercising any right, power, or
          privilege under these Terms shall operate as a waiver thereof. A single or
          partial exercise of any such right, power, or privilege shall not preclude
          any other or further exercise thereof or the exercise of any other right,
          power, or privilege.
        </p>
        <p>
          <strong class="text-gray-200">21.2 Non-Assignment.</strong>
          The User shall not assign, transfer, sublicense, or otherwise dispose of any
          right or obligation under these Terms without the prior written consent of
          the Operator. The Operator may assign these Terms in connection with any
          transfer of AS64500 to a successor entity, subject to the assignee assuming
          the obligations of the Operator hereunder.
        </p>
        <p class="text-gray-500 lang-zh">
          21.1 弃权 —— 任一方就本条款项下任何权利、权力或特权之未行使或延迟行使,不构成对该等权利、权力或特权之弃权。
          单次或部分行使任何该等权利、权力或特权,不排除该等权利、权力或特权之其他或进一步行使,或任何其他权利、权力或特权之行使。
          21.2 不可转让 —— 用户未经运营者事先书面同意,不得转让、移转、再许可或以其他方式处置本条款项下之任何权利或义务。
          运营者可在 AS64500 转让予继任实体之相关情形下转让本条款,以受让人承担运营者于本条款项下义务为限。
        </p>
      </div>
    </section>

    <!-- ===== § 22 Entire Agreement ===== -->
    <section id="entire" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§22.</span>Entire Agreement
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">完整协议</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          These Terms, together with the Privacy Policy and the RIPE policies
          incorporated by reference in §15, constitute the entire agreement between
          the User and the Operator with respect to the subject matter hereof and
          supersede all prior or contemporaneous understandings, agreements,
          negotiations, representations, and warranties, whether written or oral, with
          respect to such subject matter. No modification, amendment, or waiver of any
          provision of these Terms shall be effective unless made in accordance with
          §25 (Modifications) or in writing and signed by an authorised representative
          of the Operator.
        </p>
        <p class="text-gray-500 lang-zh">
          本条款,连同隐私政策及 §15 中以引用方式纳入之 RIPE 政策,构成用户与运营者就本条款标的事项之完整协议,
          并取代就该标的事项之所有先前或同期之理解、协议、协商、陈述与保证(无论书面或口头)。
          对本条款任何规定之修改、修订或弃权,除依 §25(修订)作出或以书面形式由运营者授权代表签署外,概不生效。
        </p>
      </div>
    </section>

    <!-- ===== § 23 Governing Law ===== -->
    <section id="governing" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§23.</span>Governing Law
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">适用法律</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          These Terms, and any non-contractual obligations arising out of or in
          connection with them, shall be governed by, and construed in accordance
          with, the laws of <strong class="text-gray-200">the Kingdom of the
          Netherlands</strong> (excluding its conflict-of-law principles), being the
          jurisdiction in which the RIPE NCC is established. The United Nations
          Convention on Contracts for the International Sale of Goods (Vienna, 1980)
          shall not apply.
        </p>
        <p>
          Nothing in this §23 shall affect:
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) any mandatory consumer-protection right available to a User who qualifies as a consumer under the laws of their habitual residence within the European Union, including without limitation the protection of <strong>Art. 6 of Regulation (EC) No 593/2008 (Rome I)</strong> and <strong>Section 4 of Regulation (EU) No 1215/2012 (Brussels I bis)</strong>;</li>
          <li>(b) any mandatory data-protection right of EU/EEA-located data subjects under the GDPR and corresponding national implementing legislation, including the right under <strong>Art. 79 GDPR</strong> to seek a judicial remedy before the courts of the data subject's habitual residence;</li>
          <li>(c) the binding effect of any policy adopted by the RIPE Community or the RIPE NCC, which shall apply concurrently to the extent of any conflict; or</li>
          <li>(d) any overriding mandatory rule (lois de police) of the law of the User's habitual residence.</li>
        </ul>
        <p class="text-gray-500 lang-zh">
          本条款及因之或与之有关之任何非合同义务,受<strong class="text-gray-300">荷兰王国</strong>
          法律管辖并据其解释(排除其冲突法规则),因 RIPE NCC 系于该国设立。
          《联合国国际货物销售合同公约》(1980 年维也纳)不予适用。本条款不影响:
          (a) 依其欧盟内惯常居所地法律具有消费者身份之用户所享有之任何强制性消费者保护权利,
          含 <strong>《2008/593 号条例》(罗马 I)第 6 条</strong>与
          <strong>《2012/1215 号条例》(布鲁塞尔 I 重订)第 4 节</strong>之保护;
          (b) 位于欧盟/欧洲经济区之数据主体依 GDPR 及相应国家实施立法所享有之任何强制性数据保护权利,
          含 <strong>GDPR 第 79 条</strong>就向数据主体惯常居所地法院寻求司法救济之权利;
          (c) RIPE 社区或 RIPE NCC 所采纳之任何政策之约束力,该等政策在冲突范围内并行适用;
          (d) 用户惯常居所地法律之任何凌驾性强制规则(lois de police)。
        </p>
      </div>
    </section>

    <!-- ===== § 24 Dispute Resolution ===== -->
    <section id="disputes" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§24.</span>Dispute Resolution
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">争议解决</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The parties shall make a good-faith attempt to resolve any dispute, claim,
          or controversy arising out of or relating to these Terms (including the
          existence, validity, interpretation, performance, breach, or termination
          thereof) through informal negotiation, initiated by written notice sent to
          the address set out in §19. If the dispute is not resolved within
          thirty (30) calendar days of the date of such notice, either party may, by
          written notice to the other:
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) where the dispute relates to the allocation, use, or transfer of resources by the RIPE NCC, submit the dispute to the <strong>RIPE NCC Conflict Arbitration Procedure (ripe-733)</strong> in force on the date of submission; or</li>
          <li>(b) commence proceedings before the competent court of Amsterdam, the Netherlands, subject to the mandatory-jurisdiction caveats set out in §23.</li>
        </ul>
        <p>
          The User retains, at all times, the right to seek any judicial or
          administrative remedy available as a matter of law, including the right to
          lodge a complaint with a competent data-protection supervisory authority as
          described in the Privacy Policy.
        </p>
        <p class="text-gray-500 lang-zh">
          双方就因本条款引起或与之有关之任何争议、主张或异议(包括本条款之存在、有效性、解释、履行、违约或终止),
          应本着诚信原则,通过向 §19 所列地址发出书面通知发起非正式协商,以求解决。
          若争议自该通知发出之日起 30 个日历日内未获解决,任一方可通过书面通知对方:
          (a) 就涉及 RIPE NCC 资源分配、使用或转让之争议,依提交时有效之
          <strong>RIPE NCC 冲突仲裁程序 (ripe-733)</strong> 提交争议;或
          (b) 受 §23 所定强制管辖保留条款约束,在荷兰阿姆斯特丹之有管辖权法院提起诉讼。
          用户在任何时候均保留依法所享有之任何司法或行政救济权利,
          包括依隐私政策所述向有管辖权之数据保护监管机构投诉之权利。
        </p>
      </div>
    </section>

    <!-- ===== § 25 Modifications ===== -->
    <section id="modifications" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§25.</span>Modifications and Document Control
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">修订与版本控制</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Operator may revise these Terms from time to time by publishing an
          updated version at <code class="text-emerald-400">https://example.com/terms</code>.
          Material revisions shall be accompanied by an incremented version number and
          a revised Effective Date. The Operator shall use reasonable endeavours to
          notify Users of material revisions in advance pursuant to §19.2. The User's
          continued use of the Service after the Effective Date of revised Terms
          constitutes acceptance of those revised Terms. Where the User does not
          consent to a revision, the User shall cease use of the Service and may
          request destruction of the User's Credentials.
        </p>
        <p class="text-gray-500 lang-zh">
          运营者可随时通过在 <code class="text-emerald-400">https://example.com/terms</code>
          发布更新版本对本条款进行修订。重大修订应伴随版本号递增与新生效日。运营者应尽合理努力依 §19.2
          就重大修订提前通知用户。用户在修订条款生效日后继续使用本服务,即视为接受该等修订条款。
          用户不同意某项修订时,应停止使用本服务并可请求销毁其凭证。
        </p>
      </div>
    </section>

    <!-- ===== § 26 Survival ===== -->
    <section id="survival" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§26.</span>Survival of Provisions
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">条款存续</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The provisions of §1 (Definitions), §10 (Disclaimer of Warranties),
          §11 (Limitation of Liability), §12 (Indemnification), §13 (Privacy and Data
          Protection) — in respect of obligations that by their nature endure beyond
          termination, §14 (Sanctions), §17 (Server-Location Jurisdictions),
          §20 (Severability), §21 (Waiver and Non-Assignment), §22 (Entire Agreement),
          §23 (Governing Law), §24 (Dispute Resolution), and this §26 shall survive
          any termination or expiry of these Terms and shall continue in full force
          and effect.
        </p>
        <p class="text-gray-500 lang-zh">
          §1(定义)、§10(保证免责)、§11(责任限制)、§12(赔偿)、§13(隐私与数据保护)
          —— 就性质上于终止后存续之义务而言、§14(制裁)、§17(服务器所在地司法管辖)、§20(可分割性)、
          §21(弃权与不可转让)、§22(完整协议)、§23(适用法律)、§24(争议解决)及本 §26 之规定,
          应在本条款之任何终止或到期后继续存续并保持完全效力。
        </p>
      </div>
    </section>

    <!-- ===== § 27 Contact ===== -->
    <section id="contact" class="mb-16 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§27.</span>Contact
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">联系方式</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Operational inquiries (peering, technical, account):
          <a href="mailto:noc@example.com" class="text-emerald-400 hover:underline">noc@example.com</a>
        </p>
        <p>
          Abuse reports (24-hour acknowledgement target):
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>
        </p>
        <p>
          Privacy and data-protection enquiries, credential-destruction requests,
          breach notifications:
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>
        </p>
        <p>
          Registry contact details (administrative-c, technical-c, abuse-c) are
          publicly available via the RIPE Database under
          <code class="text-emerald-400">ACMECLOUD-MNT</code> at
          <code class="text-emerald-400">whois.ripe.net</code>.
        </p>

        <p class="text-gray-500 lang-zh">
          运营事宜(对等互联、技术咨询、账户问题):
          <a href="mailto:noc@example.com" class="text-emerald-400 hover:underline">noc@example.com</a>。
        </p>
        <p class="text-gray-500 lang-zh">
          滥用举报(承诺 24 小时内确认收悉):
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>。
        </p>
        <p class="text-gray-500 lang-zh">
          隐私与数据保护咨询、凭证销毁请求、违约通报:
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>。
        </p>
        <p class="text-gray-500 lang-zh">
          注册信息联络人(administrative-c、technical-c、abuse-c)可经 RIPE 数据库
          <code class="text-emerald-400">whois.ripe.net</code> 查询维护者对象
          <code class="text-emerald-400">ACMECLOUD-MNT</code> 获取。
        </p>
      </div>
    </section>

    <!-- ===== Footer notice ===== -->
    <div class="border-t border-gray-800 pt-6 text-[10px] tracking-widest text-gray-600 uppercase flex flex-wrap items-center justify-between gap-2">
      <span>End of document · v{{ version }} · {{ effectiveDate }}</span>
      <a href="#" @click.prevent="scrollTop"
         v-show="showBackToTop"
         class="text-emerald-500 hover:text-emerald-400 transition-colors normal-case tracking-normal">↑ back to top</a>
    </div>
  </article>
</template>
<style scoped>
/* ============================================================================
   Document-language toggle — visibility rules.
   ============================================================================
   The toggle pill at the top of the page flips the article between
   English and Chinese. To avoid restructuring 27 sections we rely on
   two CSS rules:

   1. When `data-lang="en"` is active, hide every node explicitly
      tagged `.lang-zh`. Those are the Chinese subtitle <h4> blocks and
      the final Chinese summary <p> inside each section body.

   2. When `data-lang="zh"` is active, hide every DIRECT CHILD of a
      `.legal-body` wrapper that is NOT itself a `.lang-zh` node. That
      hides English paragraphs, lists, and tables inside each section
      body without affecting the ToC, metadata block, lead recital,
      "PLAIN-LANGUAGE SUMMARY" highlight, or the language toggle itself
      (none of which sit inside `.legal-body`).

   The Chinese summary paragraph in each section is intentionally written
   to fully cover the substantive content of the English paragraphs, so
   ZH readers still get every disclosure, list, and table’s meaning —
   just in compact paragraph form rather than the structured EN layout. */
article[data-lang="en"] .lang-zh { display: none !important; }
article[data-lang="zh"] .legal-body > :not(.lang-zh) { display: none !important; }
</style>
