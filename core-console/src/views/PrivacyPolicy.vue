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

const version = '2.0'
const effectiveDate = '2026-05-23'

const toc = [
  { n: '1',  id: 'definitions',  en: 'Definitions and Interpretation',                  zh: '定义与解释' },
  { n: '2',  id: 'controller',   en: 'Identity and Contact of the Controller',           zh: '控制者之身份与联系' },
  { n: '3',  id: 'framework',    en: 'Applicable Legal Framework',                       zh: '适用法律框架' },
  { n: '4',  id: 'data',         en: 'Categories of Personal Data Processed',            zh: '所处理个人数据类别' },
  { n: '5',  id: 'no-data',      en: 'Categories of Personal Data Not Processed',        zh: '不予处理之个人数据类别' },
  { n: '6',  id: 'purposes',     en: 'Purposes and Legal Bases (Art. 6 GDPR)',           zh: '处理目的与法律依据(第 6 条)' },
  { n: '7',  id: 'special',      en: 'Special Categories of Personal Data (Art. 9)',      zh: '特殊类别个人数据(第 9 条)' },
  { n: '8',  id: 'recipients',   en: 'Recipients and Categories of Recipients',          zh: '接收方与接收方类别' },
  { n: '9',  id: 'transfers',    en: 'International Data Transfers (Chapter V)',         zh: '跨境数据传输(第 V 章)' },
  { n: '10', id: 'retention',    en: 'Retention Periods',                                zh: '保留期限' },
  { n: '11', id: 'security',     en: 'Security of Processing (Art. 32)',                 zh: '处理之安全保障(第 32 条)' },
  { n: '12', id: 'breach',       en: 'Personal Data Breach Notification (Art. 33-34)',   zh: '个人数据违约通知(第 33-34 条)' },
  { n: '13', id: 'rights',       en: 'Rights of the Data Subject (Art. 15-22)',          zh: '数据主体之权利(第 15-22 条)' },
  { n: '14', id: 'complaint',    en: 'Right to Lodge a Complaint (Art. 77)',             zh: '投诉权(第 77 条)' },
  { n: '15', id: 'no-adm',       en: 'No Automated Decision-Making (Art. 22)',           zh: '无自动化决策(第 22 条)' },
  { n: '16', id: 'no-profiling', en: 'No Profiling',                                     zh: '不进行用户画像' },
  { n: '17', id: 'cookies',      en: 'Cookies and Local Storage (ePrivacy Art. 5(3))',   zh: 'Cookie 与本地存储(ePrivacy 第 5(3) 条)' },
  { n: '18', id: 'children',     en: "Children's Data (Art. 8)",                          zh: '未成年人数据(第 8 条)' },
  { n: '19', id: 'request',      en: 'Data Subject Request Procedure',                    zh: '数据主体请求程序' },
  { n: '20', id: 'jurisdictions', en: 'Server-Location Data Protection Laws',             zh: '服务器所在地数据保护法律' },
  { n: '21', id: 'nis2',         en: 'NIS2 Alignment',                                    zh: 'NIS2 合规对齐' },
  { n: '22', id: 'government',   en: 'Government Access Requests',                        zh: '政府访问请求' },
  { n: '23', id: 'dpo',          en: 'Data Protection Officer',                           zh: '数据保护官' },
  { n: '24', id: 'notices',      en: 'Notices',                                           zh: '通知' },
  { n: '25', id: 'changes',      en: 'Changes to this Policy',                            zh: '本政策之修订' },
  { n: '26', id: 'survival',     en: 'Survival of Provisions',                            zh: '条款存续' },
  { n: '27', id: 'contact',      en: 'Contact',                                           zh: '联系方式' },
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
        <span>Privacy Policy</span>
      </div>
      <h1 class="text-3xl sm:text-5xl text-gray-100 tracking-tight mb-2 font-bold">
        Privacy Policy
      </h1>
      <h2 class="text-base sm:text-xl text-gray-500 tracking-wide normal-case mb-6">
        隐私政策
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
        This Privacy Policy (the <strong class="text-gray-200">"Policy"</strong>) is
        issued by the Controller (as identified in §2) pursuant to its obligations
        under Article 13 of Regulation (EU) 2016/679 (the
        <strong class="text-gray-200">"GDPR"</strong>) and equivalent national
        instruments. It governs the processing of personal data carried out in the
        course of operating the Acme Net
        (<strong class="text-gray-200">"NCN"</strong>,
        <strong class="text-gray-200">"AS64500"</strong>, or the
        <strong class="text-gray-200">"Service"</strong>), an experimental autonomous
        system registered with the Réseaux IP Européens Network Coordination Centre
        (<strong class="text-gray-200">"RIPE NCC"</strong>) and operated under the
        <code class="text-emerald-400">example.com</code> domain.
      </p>
      <p class="text-gray-500 lang-zh">
        本隐私政策("政策")由控制者(身份见 §2)依其在《欧盟 2016/679 号条例》("GDPR")
        第 13 条及同等国家文书项下之义务发布。本政策规范运营 Acme Net
        ("NCN"、"AS64500"或"本服务")过程中对个人数据之处理。
        本服务系向欧洲 IP 网络协调中心("RIPE NCC")登记之实验性自治系统,
        于 <code class="text-emerald-400">example.com</code> 域名下运营。
      </p>
      <p class="text-emerald-400 text-sm">
        <strong>PLAIN-LANGUAGE SUMMARY.</strong> NCN logs only what is strictly
        necessary for load balancing across its tunnels: a credential identifier
        (UUID), a timestamp of the last successful handshake, and cumulative byte
        counters. NCN does <strong class="underline">NOT</strong> perform Deep Packet
        Inspection, does <strong class="underline">NOT</strong> retain logs of the
        websites or applications you access via the Service, and does
        <strong class="underline">NOT</strong> require government-issued
        identification documents. This summary is provided for convenience; the
        binding terms are set out in the numbered sections below.
      </p>
      <p class="text-emerald-400 text-sm">
        <strong>简明摘要。</strong> NCN 仅记录跨隧道负载均衡严格必要之内容:凭证标识符(UUID)、
        最后成功握手时间戳,以及累计字节计数。NCN <strong class="underline">不</strong>进行深度包检测,
        <strong class="underline">不</strong>记录您经由本服务所访问之网站或应用之日志,
        <strong class="underline">不</strong>要求政府签发之身份证件。本摘要仅为便利提供;
        具有约束力之条款载于下文各编号条款。
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
          <strong class="text-gray-200">1.1.</strong> For the purposes of this Policy,
          the following capitalised expressions shall have the meanings set out below.
          Defined terms in the Terms of Service published at
          <code class="text-emerald-400">example.com/terms</code> shall have the same
          meanings here unless context requires otherwise.
        </p>
        <dl class="space-y-2 pl-1">
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Controller"</dt><dd>has the meaning given in Art. 4(7) GDPR — i.e. the natural or legal person who, alone or jointly with others, determines the purposes and means of the processing of Personal Data — and is identified in §2 of this Policy;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Credential"</dt><dd>any authentication token issued to or in respect of a User, including WireGuard peer keys, BGP shared secrets, panel passwords, TOTP secrets, recovery codes, WebAuthn / passkey credentials, and API tokens;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Data Subject"</dt><dd>has the meaning given in Art. 4(1) GDPR — i.e. an identified or identifiable natural person to whom Personal Data relates;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"DPI"</dt><dd>Deep Packet Inspection — the practice of examining the application-layer payload of network traffic beyond what is strictly necessary for forwarding;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"EEA"</dt><dd>the European Economic Area, comprising the Member States of the European Union together with Iceland, Liechtenstein, and Norway;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"ePrivacy"</dt><dd>Directive 2002/58/EC of the European Parliament and of the Council of 12 July 2002 concerning the processing of personal data and the protection of privacy in the electronic communications sector, as amended by Directive 2009/136/EC;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"GDPR"</dt><dd>Regulation (EU) 2016/679 of the European Parliament and of the Council of 27 April 2016 on the protection of natural persons with regard to the processing of personal data and on the free movement of such data;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Payload"</dt><dd>the application-layer content of network packets, as distinct from headers required for routing or forwarding;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Personal Data"</dt><dd>has the meaning given in Art. 4(1) GDPR — i.e. any information relating to an identified or identifiable Data Subject;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Processing"</dt><dd>has the meaning given in Art. 4(2) GDPR — i.e. any operation or set of operations performed on Personal Data, whether or not by automated means;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"RIPE NCC"</dt><dd>Réseaux IP Européens Network Coordination Centre, the Regional Internet Registry for Europe, the Middle East, and parts of Central Asia, established as an association (vereniging) under the laws of the Netherlands and registered in Amsterdam;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Service"</dt><dd>the set of networking, control-plane, and ancillary resources made available by the Controller under the <code class="text-emerald-400">example.com</code> domain and any subdomain thereof;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Special Categories"</dt><dd>the categories of Personal Data enumerated in Art. 9(1) GDPR — i.e. data revealing racial or ethnic origin, political opinions, religious or philosophical beliefs, trade union membership; genetic data; biometric data for the purpose of uniquely identifying a natural person; data concerning health; or data concerning a natural person's sex life or sexual orientation;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Supervisory Authority"</dt><dd>has the meaning given in Art. 4(21) GDPR — i.e. an independent public authority established by a Member State pursuant to Art. 51 GDPR;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Terms"</dt><dd>the Terms of Service published at <code class="text-emerald-400">example.com/terms</code>, as amended from time to time;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"Tunnel"</dt><dd>any virtual point-to-point or point-to-multipoint link established between a User and the Service, including WireGuard, GRE, GRETAP, ip6gre, VXLAN, and IPsec encapsulations;</dd></div>
          <div class="grid grid-cols-[auto_1fr] gap-x-3 items-baseline"><dt class="text-gray-200 shrink-0 min-w-[14ch]">"User"</dt><dd>any natural or legal person, or autonomous system, that accesses, connects to, or otherwise interacts with the Service in any capacity.</dd></div>
        </dl>
        <p>
          <strong class="text-gray-200">1.2 Interpretation.</strong> References to
          statutory instruments shall be construed as references to such instruments
          as amended, supplemented, or re-enacted from time to time. Headings are
          inserted for convenience only and shall not affect the construction of this
          Policy.
        </p>
        <p class="text-gray-500 lang-zh">
          1.1 就本政策之目的,下列大写表述具有以下含义。
          发布于 <code class="text-emerald-400">example.com/terms</code> 之服务条款中所定义之术语在本政策中具相同含义,
          除非上下文另有要求。
          "控制者"—— 具 GDPR 第 4(7) 条之含义,即单独或与他人共同确定个人数据处理之目的与方式之自然人或法人;
          "凭证"—— 发放给用户或就用户而发放之任何身份验证令牌;
          "数据主体"—— 具 GDPR 第 4(1) 条之含义;
          "DPI"—— 深度包检测;
          "EEA"—— 欧洲经济区,含欧盟成员国及冰岛、列支敦士登、挪威;
          "ePrivacy"—— 经 2009/136/EC 指令修订之欧洲议会和理事会 2002 年 7 月 12 日 2002/58/EC 指令;
          "GDPR"—— 欧洲议会和理事会 2016 年 4 月 27 日《欧盟 2016/679 号条例》;
          "负载"—— 数据包之应用层内容,有别于路由或转发所需之报头;
          "个人数据"—— 具 GDPR 第 4(1) 条之含义;
          "处理"—— 具 GDPR 第 4(2) 条之含义;
          "RIPE NCC"—— 欧洲 IP 网络协调中心,依荷兰法律设立为协会(vereniging),登记地阿姆斯特丹;
          "本服务"—— 控制者在 <code class="text-emerald-400">example.com</code> 域名及其任何子域名下提供之资源总和;
          "特殊类别"—— GDPR 第 9(1) 条所列举之个人数据类别(种族族裔、政治、宗教、工会、基因、生物特征、健康、性生活/性取向);
          "监管机构"—— 具 GDPR 第 4(21) 条之含义;
          "条款"—— 发布于 <code class="text-emerald-400">example.com/terms</code> 之服务条款,随时修订;
          "隧道"—— 用户与本服务之间所建立之任何虚拟链路;
          "用户"—— 以任何身份访问、连接或与本服务交互之任何自然人、法人或自治系统。
          1.2 解释 —— 对法规之引用应作为对该法规经不时修订、补充或重新颁布版本之引用。
          标题仅为方便而插入,不影响本政策之解释。
        </p>
      </div>
    </section>

    <!-- ===== § 2 Controller ===== -->
    <section id="controller" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§2.</span>Identity and Contact of the Controller
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">控制者之身份与联系</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">2.1 Identity.</strong> The Controller for the
          purposes of GDPR and equivalent national data-protection instruments is the
          natural or legal person registered with the RIPE NCC as the holder of
          AS64500, identified in the RIPE Database under the maintainer object
          <code class="text-emerald-400">ACMECLOUD-MNT</code>. Current administrative,
          technical, and abuse contacts (RIPE Database
          <code class="text-emerald-400">admin-c</code>,
          <code class="text-emerald-400">tech-c</code>, and
          <code class="text-emerald-400">abuse-c</code> attributes) are publicly
          accessible via the RIPE WHOIS service at
          <code class="text-emerald-400">whois.ripe.net</code>.
        </p>
        <p>
          <strong class="text-gray-200">2.2 Contact for data-protection matters.</strong>
          The single point of contact for all matters arising under this Policy
          — including requests under Art. 15-22 GDPR, complaints, breach reports, and
          general enquiries — is:
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>.
        </p>
        <p>
          <strong class="text-gray-200">2.3 EU representative.</strong> Where required
          under Art. 27 GDPR, the Controller's representative within the European Union
          shall be identified by amendment to this §2 and published at
          <code class="text-emerald-400">example.com/privacy</code>. As at the Effective
          Date, the Controller is established within the EEA (by virtue of its
          RIPE NCC membership in the Netherlands as set out in §3) and accordingly the
          appointment of a separate Art. 27 representative is not required.
        </p>
        <p class="text-gray-500 lang-zh">
          2.1 身份 —— 就 GDPR 及同等国家数据保护文书之目的,控制者系在 RIPE NCC 登记为 AS64500 持有人之自然人或法人,
          在 RIPE 数据库下以维护对象 <code class="text-emerald-400">ACMECLOUD-MNT</code> 标识。
          当前行政、技术与滥用联系人(RIPE 数据库
          <code class="text-emerald-400">admin-c</code>、<code class="text-emerald-400">tech-c</code>、
          <code class="text-emerald-400">abuse-c</code> 属性)可于 <code class="text-emerald-400">whois.ripe.net</code> 公开查询。
          2.2 数据保护事项联系 —— 就本政策项下所有事项(含 GDPR 第 15-22 条之请求、投诉、违约报告及一般查询)之单一联络点为
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>。
          2.3 欧盟代表 —— 在 GDPR 第 27 条要求之情形下,控制者欧盟境内代表应通过本 §2 之修订识别并发布于
          <code class="text-emerald-400">example.com/privacy</code>。截至生效日,控制者依 §3 通过其荷兰 RIPE NCC 成员资格
          已在 EEA 内设立,据此无须另行任命第 27 条之代表。
        </p>
      </div>
    </section>

    <!-- ===== § 3 Legal Framework ===== -->
    <section id="framework" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§3.</span>Applicable Legal Framework
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">适用法律框架</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The processing operations described in this Policy are governed by the
          following instruments, each as amended from time to time. Where two or more
          instruments apply concurrently to the same processing operation, the
          instrument affording the highest level of protection to the Data Subject
          shall be observed.
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) <strong>GDPR</strong> — Regulation (EU) 2016/679, directly applicable;</li>
          <li>(b) <strong>Dutch Implementation Act</strong> — Uitvoeringswet Algemene verordening gegevensbescherming ("UAVG"), being the Dutch national implementing legislation of the GDPR;</li>
          <li>(c) <strong>ePrivacy Directive</strong> — Directive 2002/58/EC, as transposed in Dutch law by the Telecommunicatiewet, in particular Articles 11.7 and 11.7a;</li>
          <li>(d) <strong>German BDSG</strong> — Bundesdatenschutzgesetz, supplementing the GDPR in respect of Personal Data processed at our point of presence in Germany;</li>
          <li>(e) <strong>Region C PDPO</strong> — Personal Data (Privacy) Ordinance (Cap. 486), applicable to Personal Data processed at our points of presence in Region C;</li>
          <li>(f) <strong>Japan APPI</strong> — Act on the Protection of Personal Information (Act No. 57 of 2003), applicable to Personal Data processed at our point of presence in Japan;</li>
          <li>(g) <strong>Region D PDPA</strong> — Personal Data Protection Act 2012 (No. 26 of 2012), applicable to Personal Data processed at our point of presence in Region D;</li>
          <li>(h) <strong>California CCPA / CPRA</strong> — California Consumer Privacy Act of 2018 (Cal. Civ. Code § 1798.100 et seq.) as amended by the California Privacy Rights Act, applicable to Personal Data of California residents processed at our point of presence in the United States (California);</li>
          <li>(i) <strong>NIS2 Directive</strong> — Directive (EU) 2022/2555 (in respect of incident-handling principles, addressed in §21);</li>
          <li>(j) any binding decision of a competent Supervisory Authority, judicial body, or arbitral tribunal applicable to the Controller.</li>
        </ul>
        <p>
          The Controller is established in the Netherlands by virtue of its
          membership of the RIPE NCC association (vereniging) under Articles 26-50 of
          Book 2 of the Dutch Civil Code (Burgerlijk Wetboek Boek 2). Accordingly,
          the Dutch Data Protection Authority (Autoriteit Persoonsgegevens, "AP") is
          the lead Supervisory Authority for the purposes of Art. 56 GDPR, without
          prejudice to the competence of any other authority over local
          establishments or affected Data Subjects.
        </p>
        <p class="text-gray-500 lang-zh">
          本政策所述处理活动受下列文书管辖(各文书以现行有效版本为准)。如两项或以上文书并行适用于同一处理活动,
          应以对数据主体提供最高保护程度之文书为准:
          (a) <strong>GDPR</strong> —— 直接适用之《欧盟 2016/679 号条例》;
          (b) <strong>荷兰实施法</strong> —— Uitvoeringswet AVG (UAVG),GDPR 之荷兰国内实施立法;
          (c) <strong>ePrivacy 指令</strong> —— 经荷兰《电信法》(Telecommunicatiewet)转化之 2002/58/EC 指令,
          特别是第 11.7 及 11.7a 条;
          (d) <strong>德国 BDSG</strong> —— 《联邦数据保护法》(Bundesdatenschutzgesetz),就在德国接入点处理之个人数据补充 GDPR;
          (e) <strong>香港 PDPO</strong> —— 《个人资料(私隐)条例》(第 486 章),适用于在香港各接入点处理之个人数据;
          (f) <strong>日本 APPI</strong> —— 《个人信息保护法》(平成 15 年法律第 57 号),适用于在日本接入点处理之个人数据;
          (g) <strong>新加坡 PDPA</strong> —— 《2012 年个人数据保护法》(2012 年第 26 号法令),适用于在新加坡接入点处理之个人数据;
          (h) <strong>加州 CCPA / CPRA</strong> —— 经《加州隐私权法》修订之《2018 年加州消费者隐私法》(Cal. Civ. Code § 1798.100 等),
          适用于在美国(加利福尼亚州)接入点处理之加州居民个人数据;
          (i) <strong>NIS2 指令</strong> —— 《欧盟 2022/2555 号指令》(就事件处理原则,见 §21);
          (j) 适用于控制者之任何主管监管机构、司法机关或仲裁庭之具约束力决定。
          控制者依《荷兰民法典》第 2 卷第 26-50 条所成立之 RIPE NCC 协会(vereniging)成员资格,在荷兰设立。
          据此,荷兰数据保护局(Autoriteit Persoonsgegevens,"AP")就 GDPR 第 56 条之目的为主导监管机构,
          但不妨碍任何其他机构就本地设立或受影响数据主体之管辖。
        </p>
      </div>
    </section>

    <!-- ===== § 4 Categories of Data Processed ===== -->
    <section id="data" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§4.</span>Categories of Personal Data Processed
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">所处理个人数据类别</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Controller processes the following discrete categories of Personal Data
          in connection with the Service. The legal basis for each category is set
          out in §6; retention is set out in §10.
        </p>
        <p>
          <strong class="text-gray-200">4.1 Traffic-Management Telemetry.</strong>
          For each Credential, the traffic-management subsystem (operationally based
          on the <code class="text-emerald-400">3x-ui</code> control plane and
          equivalent tooling) retains the following metadata, for the sole purpose of
          load balancing across the distributed PoPs:
        </p>
        <div class="border border-gray-800 bg-gray-900/40 p-4 my-3">
          <table class="text-xs sm:text-sm w-full">
            <thead class="text-[10px] tracking-widest text-gray-600 uppercase">
              <tr class="border-b border-gray-800">
                <th class="text-left py-2 pr-4">Field · 字段</th>
                <th class="text-left py-2">Value type · 取值</th>
              </tr>
            </thead>
            <tbody class="text-gray-400">
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-4 text-emerald-400">credential UUID</td>
                <td class="py-1.5">RFC 4122 v4 random identifier</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-4 text-emerald-400">last_handshake_at</td>
                <td class="py-1.5">UTC timestamp (seconds since epoch)</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-4 text-emerald-400">bytes_in_total</td>
                <td class="py-1.5">monotonic counter, ingress octets</td>
              </tr>
              <tr>
                <td class="py-1.5 pr-4 text-emerald-400">bytes_out_total</td>
                <td class="py-1.5">monotonic counter, egress octets</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p>
          <strong class="text-gray-200">4.2 Account-Identification Data.</strong>
          At the time of Credential issuance, the User voluntarily supplies an
          email address (for User-initiated correspondence and Credential rotation)
          and a chosen username. No further account-identification data is required
          or collected.
        </p>
        <p>
          <strong class="text-gray-200">4.3 Authentication-Plane Data.</strong>
          To authenticate Users against the administrative subsystem at
          <code class="text-emerald-400">admin.example.com</code>, the Controller
          processes (i) a bcrypt-hashed password (the plaintext is never persisted);
          (ii) a TOTP shared secret, where TOTP second factor is enrolled;
          (iii) WebAuthn / passkey credential identifiers and public keys, where
          passkey second factor is enrolled; and (iv) bcrypt-hashed recovery codes.
        </p>
        <p>
          <strong class="text-gray-200">4.4 Connection-Layer Metadata (Transient).</strong>
          For the active life of a Tunnel session only, the kernel and tunnel daemons
          unavoidably observe the source and destination IP addresses, ports, and
          protocol numbers of packets being forwarded. This observation is
          inherent to the act of packet forwarding itself and is
          <strong>not</strong> written to persistent storage beyond what is required
          to maintain the session.
        </p>
        <p>
          <strong class="text-gray-200">4.5 Administrative Access Logs.</strong>
          Authentication and administrative actions at
          <code class="text-emerald-400">admin.example.com</code> generate operating-system
          journal entries containing the operator identity, source IP, action taken,
          and timestamp. These entries are written to an append-only log
          (<code class="text-emerald-400">chattr +a</code>) to support post-incident
          forensics.
        </p>
        <p>
          <strong class="text-gray-200">4.6 Abuse-Investigation Records.</strong>
          Where the Controller investigates a suspected violation of the Terms or
          Applicable Law, the Controller may retain additional records (e.g. session
          logs, packet captures limited to header fields, correspondence with peer
          networks and law-enforcement agencies) strictly limited in scope and
          retention to what is necessary for the investigation, in accordance with
          §10.
        </p>
        <p class="text-gray-500 lang-zh">
          控制者就本服务处理以下离散类别之个人数据。每一类别之法律依据详见 §6;保留期限见 §10。
          4.1 流量管理遥测 —— 流量管理子系统(基于 3x-ui 控制面及同类工具)对每个凭证保留以下元数据,
          其唯一目的为跨分布式 PoP 之负载均衡:凭证 UUID、最后握手时间戳(UTC 秒)、累计入站字节数、累计出站字节数。
          4.2 账户识别数据 —— 凭证发放时,用户自愿提供电邮地址(供用户发起之通信与凭证轮换)与用户名。
          无须、亦不收集进一步之账户识别数据。
          4.3 认证面数据 —— 为对管理子系统 <code class="text-emerald-400">admin.example.com</code> 进行用户认证,控制者处理:
          (i) bcrypt 哈希后之密码(明文绝不留存);(ii) 启用 TOTP 二次因素时之 TOTP 共享密钥;
          (iii) 启用 passkey 二次因素时之 WebAuthn/passkey 凭证标识符与公钥;(iv) bcrypt 哈希后之恢复码。
          4.4 连接层元数据(瞬态) —— 隧道会话存续期间,内核与隧道守护进程不可避免地观察被转发数据包之源/目的 IP、端口与协议号。
          此观察系数据包转发本身之固有行为,除维持会话所需外<strong>不</strong>写入持久化存储。
          4.5 管理访问日志 —— <code class="text-emerald-400">admin.example.com</code> 之身份验证与管理操作产生操作系统 journal 条目,
          含运维身份、来源 IP、所执行操作及时间戳。该条目写入仅追加日志(<code class="text-emerald-400">chattr +a</code>),以支援事后取证。
          4.6 滥用调查记录 —— 控制者调查疑似违反条款或适用法律之行为时,可保留额外记录(如会话日志、限于报头字段之数据包采集、
          与对等网络及执法机构之通信),其范围与保留严格限于调查所需,符合 §10 之规定。
        </p>
      </div>
    </section>

    <!-- ===== § 5 Data NOT Processed ===== -->
    <section id="no-data" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§5.</span>Categories of Personal Data Not Processed
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">不予处理之个人数据类别</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The following categories of Personal Data are
          <strong class="text-amber-400">expressly outside the scope</strong> of the
          Service's data-processing practices. The Controller makes a positive,
          contractually binding commitment <strong>not</strong> to perform, retain,
          or process the items enumerated below. Violation of this commitment by the
          Controller would constitute a material breach of this Policy.
        </p>
        <ul class="space-y-2 pl-1 mt-4">
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-amber-400 shrink-0 font-bold">5.1</span>
            <span><strong class="text-gray-200">Deep Packet Inspection (DPI).</strong>
              The Controller does not inspect the application-layer payload of
              network traffic carried over User Tunnels. Forwarding decisions are
              made on the basis of routing headers alone, in accordance with the
              transport-only role of the Service.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-amber-400 shrink-0 font-bold">5.2</span>
            <span><strong class="text-gray-200">Application-layer access logs (Payload Logs).</strong>
              The Controller does not log, store, or otherwise persist any record of
              the websites, hosts, services, APIs, or applications accessed by Users
              via the Service. There exists no per-User history of destinations
              visited.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-amber-400 shrink-0 font-bold">5.3</span>
            <span><strong class="text-gray-200">DNS query logs.</strong>
              The Controller does not operate a logging recursive DNS resolver. DNS
              queries made by Users through any third-party resolver are subject to
              that resolver's policies, not the Controller's.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-amber-400 shrink-0 font-bold">5.4</span>
            <span><strong class="text-gray-200">TLS / SSL key material.</strong>
              The Controller does not collect, log, intercept, or otherwise
              compromise TLS session keys. End-to-end encrypted traffic remains
              opaque to the Controller beyond the routing headers required for
              forwarding.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-amber-400 shrink-0 font-bold">5.5</span>
            <span><strong class="text-gray-200">Government-issued identification linkage.</strong>
              The Controller does not require, collect, verify, or store government-issued
              identification documents, national identifiers, biometric identifiers,
              or any equivalent. Credentials are linked only to the random UUID and
              the email address voluntarily supplied at registration.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-amber-400 shrink-0 font-bold">5.6</span>
            <span><strong class="text-gray-200">Cross-credential traffic correlation.</strong>
              The Controller does not maintain persistent IP-to-identity mappings
              beyond the Credential UUID. The Controller does not perform
              timing-correlation analysis between traffic flows associated with
              different Credentials.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-amber-400 shrink-0 font-bold">5.7</span>
            <span><strong class="text-gray-200">Behavioural profiling and advertising identifiers.</strong>
              The Controller does not generate behavioural profiles of Users, does
              not sell or share Personal Data for cross-context behavioural
              advertising, and does not integrate any third-party tracking pixel,
              advertising identifier, or analytics script in the Service.</span>
          </li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start">
            <span class="text-amber-400 shrink-0 font-bold">5.8</span>
            <span><strong class="text-gray-200">Special Categories of Personal Data.</strong>
              The Controller does not knowingly process Special Categories of
              Personal Data within the meaning of Art. 9(1) GDPR. See further §7.</span>
          </li>
        </ul>
        <p class="text-gray-500 mt-4 lang-zh">
          以下类别明确<strong class="text-amber-400">不在</strong>本服务之数据处理实践范围内,
          控制者作出积极、具合同约束力之承诺<strong>不</strong>执行、保留或处理:
          (5.1) 深度包检测 — 控制者不检查用户隧道所承载流量之应用层负载;
          (5.2) 应用层访问日志(Payload Logs) — 不记录、不存储用户经由本服务所访问之网站、主机、服务、API 或应用;
          (5.3) DNS 查询日志 — 控制者不运营记录日志之递归 DNS 解析器;
          (5.4) TLS/SSL 密钥材料 — 不收集、不记录、不拦截、不破坏 TLS 会话密钥;
          (5.5) 政府签发身份证件关联 — 不要求、不收集、不验证、不存储政府签发证件、国家标识符、生物特征标识符;
          凭证仅与注册时自愿提供之 UUID 与邮箱关联;
          (5.6) 跨凭证流量关联 — 不维护超出凭证 UUID 之外之持久 IP↔身份映射,不进行不同凭证流量之时序关联分析;
          (5.7) 行为画像与广告标识符 — 不生成用户行为画像,不出售或共享个人数据用于跨上下文行为广告,
          不集成任何第三方跟踪像素、广告标识符或分析脚本;
          (5.8) GDPR 第 9(1) 条意义之特殊类别个人数据 — 不知情处理,详见 §7。
          控制者违反上述承诺即构成对本政策之重大违反。
        </p>
      </div>
    </section>

    <!-- ===== § 6 Purposes and Legal Bases ===== -->
    <section id="purposes" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§6.</span>Purposes and Legal Bases
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">处理目的与法律依据(GDPR 第 6 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Each category of Personal Data set out in §4 is processed for one or more
          of the following purposes, each justified by a distinct legal basis under
          Art. 6(1) GDPR:
        </p>
        <div class="border border-gray-800 bg-gray-900/40 p-3 sm:p-4 my-3 overflow-x-auto">
          <table class="text-xs sm:text-sm w-full min-w-[500px]">
            <thead class="text-[10px] tracking-widest text-gray-600 uppercase">
              <tr class="border-b border-gray-800">
                <th class="text-left py-2 pr-3">Purpose</th>
                <th class="text-left py-2 pr-3">Data (§4 ref.)</th>
                <th class="text-left py-2">Legal Basis</th>
              </tr>
            </thead>
            <tbody class="text-gray-400 align-baseline">
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3">Operating the Service / load balancing</td>
                <td class="py-1.5 pr-3 text-emerald-400">§4.1</td>
                <td class="py-1.5">Art. 6(1)(b) — contract necessity</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3">User-initiated correspondence</td>
                <td class="py-1.5 pr-3 text-emerald-400">§4.2</td>
                <td class="py-1.5">Art. 6(1)(b) — contract necessity</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3">Authenticating operators</td>
                <td class="py-1.5 pr-3 text-emerald-400">§4.3</td>
                <td class="py-1.5">Art. 6(1)(b) — contract necessity</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3">Packet forwarding</td>
                <td class="py-1.5 pr-3 text-emerald-400">§4.4</td>
                <td class="py-1.5">Art. 6(1)(b) — contract necessity</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3">Audit logging / forensics</td>
                <td class="py-1.5 pr-3 text-emerald-400">§4.5</td>
                <td class="py-1.5">Art. 6(1)(f) — legitimate interests</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3">Abuse mitigation / Terms enforcement</td>
                <td class="py-1.5 pr-3 text-emerald-400">§4.6</td>
                <td class="py-1.5">Art. 6(1)(f) — legitimate interests</td>
              </tr>
              <tr>
                <td class="py-1.5 pr-3">Compliance with legal obligations (court orders, sanctions screening)</td>
                <td class="py-1.5 pr-3 text-emerald-400">any of §4</td>
                <td class="py-1.5">Art. 6(1)(c) — legal obligation</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p>
          <strong class="text-gray-200">6.1 Legitimate-interests assessment.</strong>
          Where the Controller relies on Art. 6(1)(f) GDPR, the Controller has carried
          out a balancing assessment in accordance with the European Data Protection
          Board's guidance and has determined that its legitimate interests in (a)
          maintaining the security and integrity of the Service, (b) detecting and
          mitigating abuse, and (c) post-incident forensics are not overridden by the
          interests or fundamental rights and freedoms of the Data Subjects whose
          Personal Data is processed, in light of the limited scope of data processed
          (§4), the technical and organisational measures applied (§11), and the
          retention limits enforced (§10). A record of this assessment is maintained
          and may be furnished to a Supervisory Authority on request.
        </p>
        <p>
          <strong class="text-gray-200">6.2 No consent-based processing of operational data.</strong>
          The processing operations described in §4 do not rely on consent as a legal
          basis. Consequently, there is no general "withdrawal of consent" that would
          terminate such processing while the Service is being used. To cease all
          processing, a Data Subject may request destruction of the Credentials and
          deletion of associated records via §27.
        </p>
        <p class="text-gray-500 lang-zh">
          §4 各类别之个人数据,依 GDPR 第 6(1) 条所列之不同法律依据,出于以下一项或多项目的处理:
          (a) 运营本服务及负载均衡 —— 第 6(1)(b) 条(合同必要);
          (b) 用户发起之通信 —— 第 6(1)(b) 条(合同必要);
          (c) 运维身份验证 —— 第 6(1)(b) 条(合同必要);
          (d) 数据包转发 —— 第 6(1)(b) 条(合同必要);
          (e) 审计日志/取证 —— 第 6(1)(f) 条(合法利益);
          (f) 滥用缓解/条款执行 —— 第 6(1)(f) 条(合法利益);
          (g) 履行法定义务(法院命令、制裁筛查)—— 第 6(1)(c) 条(法定义务)。
          6.1 合法利益评估 —— 控制者依第 6(1)(f) 条之处理,均已依据欧洲数据保护委员会(EDPB)指南进行平衡评估,
          并认定其在(a) 维持本服务安全与完整性、(b) 侦测并缓解滥用、(c) 事后取证之合法利益,
          鉴于所处理数据之有限范围(§4)、所适用之技术与组织措施(§11)及所执行之保留限制(§10),
          不为数据主体之利益或基本权利与自由所凌驾。该等评估之记录予以保留,并可应监管机构之请求提交。
          6.2 不以同意为依据之运营处理 —— §4 所述处理活动不依赖同意作为法律依据。
          因此,本服务使用期间不存在可终止该等处理之一般性"撤回同意"。数据主体可通过 §27 请求销毁凭证并删除关联记录,以停止全部处理。
        </p>
      </div>
    </section>

    <!-- ===== § 7 Special Categories ===== -->
    <section id="special" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§7.</span>Special Categories of Personal Data
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">特殊类别个人数据(GDPR 第 9 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Controller does not knowingly process Special Categories of Personal
          Data within the meaning of Art. 9(1) GDPR — i.e. data revealing racial or
          ethnic origin, political opinions, religious or philosophical beliefs,
          trade-union membership; genetic data; biometric data for the purpose of
          uniquely identifying a natural person; data concerning health; or data
          concerning a natural person's sex life or sexual orientation.
        </p>
        <p>
          The Service is a generic transport-layer facility; it does not solicit,
          require, or operate upon any of the foregoing categories. To the extent
          that any such data may, in transit through the Service, be carried in an
          end-to-end encrypted Payload, the Controller does not have visibility into
          such Payload (§5.1, §5.4) and accordingly cannot meaningfully be said to
          process it within the meaning of Art. 9 GDPR.
        </p>
        <p class="text-gray-500 lang-zh">
          控制者不知情处理 GDPR 第 9(1) 条意义之特殊类别个人数据 —— 即揭示种族或族裔出身、政治意见、宗教或哲学信仰、
          工会成员资格之数据;基因数据;为唯一识别自然人目的之生物特征数据;有关健康之数据;
          或有关自然人性生活或性取向之数据。本服务系通用传输层设施,不索取、不要求、不针对前述任何类别运作。
          在该等数据可能于经本服务传输时被承载于端到端加密负载之范围内,控制者无法窥视该等负载(§5.1、§5.4),
          据此不能在 GDPR 第 9 条意义上有意义地谓其"处理"该等数据。
        </p>
      </div>
    </section>

    <!-- ===== § 8 Recipients ===== -->
    <section id="recipients" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§8.</span>Recipients and Categories of Recipients
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">接收方与接收方类别</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Pursuant to Art. 13(1)(e) GDPR, the Controller discloses below the
          categories of recipients to whom Personal Data may be disclosed. The
          Controller does not sell, rent, lease, or otherwise transfer for
          commercial purposes any Personal Data processed in connection with the
          Service.
        </p>
        <ul class="space-y-2 pl-1 mt-2">
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start"><span class="text-emerald-400 shrink-0 font-bold">(a)</span>
            <span><strong class="text-gray-200">Internal personnel.</strong> The Controller and any natural person duly authorised by the Controller to operate the Service on the Controller's behalf, subject to a duty of confidentiality.</span></li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start"><span class="text-emerald-400 shrink-0 font-bold">(b)</span>
            <span><strong class="text-gray-200">Peer networks and transit upstreams.</strong> In the narrow circumstances of an active abuse incident, the Controller may disclose to a peer network, transit upstream, or affected third party such Personal Data as is strictly necessary to identify and mitigate the source of the abuse.</span></li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start"><span class="text-emerald-400 shrink-0 font-bold">(c)</span>
            <span><strong class="text-gray-200">RIPE NCC and Regional Internet Registries.</strong> Information necessary for the maintenance of allocation records is disclosed in the RIPE Database in accordance with RIPE policies; such information is, by the nature of the registry, publicly accessible.</span></li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start"><span class="text-emerald-400 shrink-0 font-bold">(d)</span>
            <span><strong class="text-gray-200">Public BGP-data aggregators.</strong> Routing announcements and IRR records are, by the nature of the inter-domain routing protocol, observable by any party operating a BGP router. The Controller specifically acknowledges the aggregation of such data by PeeringDB and bgp.tools; this category is limited to data which is in any event public.</span></li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start"><span class="text-emerald-400 shrink-0 font-bold">(e)</span>
            <span><strong class="text-gray-200">Law-enforcement and judicial authorities.</strong> In response to a valid, lawful, judicial or governmental order issued by an authority with jurisdiction over the Controller, and only to the extent required by such order. The Controller maintains a presumption against voluntary disclosure absent compulsory process. See further §22.</span></li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start"><span class="text-emerald-400 shrink-0 font-bold">(f)</span>
            <span><strong class="text-gray-200">Supervisory Authorities.</strong> In the discharge of any duty of cooperation under Art. 31 GDPR, equivalent national law, or in response to an investigation by a Supervisory Authority.</span></li>
          <li class="grid grid-cols-[auto_1fr] gap-x-3 items-start"><span class="text-emerald-400 shrink-0 font-bold">(g)</span>
            <span><strong class="text-gray-200">Professional advisors.</strong> Legal, tax, and accounting advisors retained by the Controller, subject to professional confidentiality obligations.</span></li>
        </ul>
        <p class="text-gray-500 lang-zh">
          依 GDPR 第 13(1)(e) 条,控制者下文披露可能向其披露个人数据之接收方类别。控制者不就本服务相关之任何个人数据进行商业目的之出售、出租、租赁或转让。
          (a) 内部人员 —— 控制者及其正式授权代表运营本服务之任何自然人,负有保密义务;
          (b) 对等网络与上游 —— 仅在活动滥用事件之狭义情形下,控制者可向对等网络、上游或受影响第三方披露识别和缓解滥用源头所严格必需之个人数据;
          (c) RIPE NCC 与地区性互联网注册机构 —— 维护分配记录所必需之信息依 RIPE 政策在 RIPE 数据库中披露;
          按注册机构性质,该等信息公开可访问;
          (d) 公开 BGP 数据聚合方 —— 路由通告与 IRR 记录按域间路由协议之性质,任何运营 BGP 路由器之方均可观察;
          控制者特别承认 PeeringDB 与 bgp.tools 对该等数据之聚合;该类别仅限于本即公开之数据;
          (e) 执法与司法机关 —— 响应对控制者具管辖权之机关签发之有效、合法之司法或政府命令,且仅在该命令所要求范围内;
          控制者在无强制程序之前提下维持反对自愿披露之推定。详见 §22;
          (f) 监管机构 —— 履行 GDPR 第 31 条、同等国家法律之合作义务或响应监管机构调查之范围内;
          (g) 专业顾问 —— 控制者聘任之法律、税务与会计顾问,受专业保密义务约束。
        </p>
      </div>
    </section>

    <!-- ===== § 9 International Transfers ===== -->
    <section id="transfers" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§9.</span>International Data Transfers
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">跨境数据传输(GDPR 第 V 章)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Controller operates Points of Presence in jurisdictions outside the
          European Economic Area ("EEA"). The following table summarises the legal
          basis under Chapter V GDPR for each transfer destination, in conjunction
          with the applicable Commission Implementing Decision (where one exists):
        </p>
        <div class="border border-gray-800 bg-gray-900/40 p-3 sm:p-4 my-3 overflow-x-auto">
          <table class="text-xs sm:text-sm w-full min-w-[500px]">
            <thead class="text-[10px] tracking-widest text-gray-600 uppercase">
              <tr class="border-b border-gray-800">
                <th class="text-left py-2 pr-3">Location</th>
                <th class="text-left py-2 pr-3">Jurisdiction</th>
                <th class="text-left py-2">Chapter V Basis</th>
              </tr>
            </thead>
            <tbody class="text-gray-400 align-baseline">
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3 text-emerald-400">Region C</td>
                <td class="py-1.5 pr-3">Region C SAR</td>
                <td class="py-1.5">No adequacy decision · Art. 49(1)(b) (contract necessity) and where applicable Art. 49(1)(a) (explicit consent at credential issuance)</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3 text-emerald-400">Region A</td>
                <td class="py-1.5 pr-3">Japan</td>
                <td class="py-1.5">Adequate · <strong>Commission Implementing Decision (EU) 2019/419</strong> (Art. 45 GDPR)</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3 text-emerald-400">Region D</td>
                <td class="py-1.5 pr-3">Region D</td>
                <td class="py-1.5">No adequacy decision · Art. 49(1)(b) (contract necessity) and where applicable Art. 49(1)(a) (explicit consent at credential issuance)</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="text-gray-400">
          The Controller additionally operates a point of presence in Germany
          (Region B), which is located within the EEA. Processing at that
          location does not constitute a transfer to a third country under
          Chapter V GDPR and requires no separate transfer mechanism; the German
          Bundesdatenschutzgesetz (BDSG) supplements the directly-applicable GDPR
          there.
          <span class="lang-zh block mt-1">控制者另在德国(法兰克福)运营一处接入点,该处位于 EEA 之内。
          于该处之处理不构成 GDPR 第五章项下向第三国之传输,无需另行之传输机制;
          德国《联邦数据保护法》(BDSG)于该处补充直接适用之 GDPR。</span>
        </p>
        <p>
          <strong class="text-gray-200">9.1 Scope of transfers.</strong>
          Transfers to non-EEA PoPs are strictly limited to the categories enumerated
          in §4.1 (credential UUID, last_handshake_at, byte counters). The Controller
          does not transfer to non-EEA PoPs any of the categories enumerated in §5
          (DPI output, payload logs, DNS query logs, TLS key material, identification
          documents, cross-credential correlation, behavioural profiles, Special
          Categories) — for the simple reason that such data is not collected in the
          first place.
        </p>
        <p>
          <strong class="text-gray-200">9.2 Standard contractual clauses.</strong>
          Where the Controller engages a sub-processor in a third country to which no
          adequacy decision applies (currently not the case), the Controller shall
          enter into the Standard Contractual Clauses approved by Commission
          Implementing Decision (EU) 2021/914 of 4 June 2021, supplemented by such
          additional safeguards as are required pursuant to the judgment of the
          Court of Justice of the European Union in
          <em>Case C-311/18 (Schrems II)</em>.
        </p>
        <p>
          <strong class="text-gray-200">9.3 Data Subject information.</strong>
          A copy of any safeguard relied upon under Art. 46 GDPR or any contract or
          consent record relied upon under Art. 49 GDPR may be obtained on request
          to the contact in §27.
        </p>
        <p class="text-gray-500 lang-zh">
          控制者在 EEA 之外司法管辖区运营接入点。下表概述每一传输目的地之 GDPR 第 V 章法律依据,
          并结合(如有)适用之欧盟委员会实施决定:
          香港(中国香港特别行政区)—— 无充分性决定,依第 49(1)(b) 条(合同必要),
          及适用情况下第 49(1)(a) 条(凭证发放时之明确同意);
          东京(日本)—— 充分,依<strong>欧盟委员会实施决定 (EU) 2019/419</strong>(GDPR 第 45 条);
          新加坡 —— 无充分性决定,依第 49(1)(b) 条(合同必要),及适用情况下第 49(1)(a) 条(明确同意);
          弗里蒙特(美国 · 加利福尼亚州)—— 在接收实体经 DPF 认证之情形下,充分(有限),依
          <strong>欧盟委员会实施决定 (EU) 2023/1795</strong>(欧盟–美国数据隐私框架);否则依第 49(1)(b) 条。
          法兰克福(德国)位于 EEA 之内,于该处之处理不构成第五章项下之第三国传输,无需另行传输机制。
          9.1 传输范围 —— 向非 EEA PoP 之传输严格限于 §4.1 所列类别(凭证 UUID、最后握手、字节计数)。
          控制者不向非 EEA PoP 传输 §5 所列任何类别(DPI 输出、负载日志、DNS 查询、TLS 密钥、证件、跨凭证关联、行为画像、特殊类别),
          理由是此等数据自始即未被收集。
          9.2 标准合同条款 —— 控制者于无充分性决定之第三国聘任处理者时(目前无此情形),应签订欧盟委员会
          2021 年 6 月 4 日实施决定 (EU) 2021/914 所核准之标准合同条款,并依欧盟法院
          <em>C-311/18 号案件(Schrems II)</em>判决所要求之必要补充保障作出补充。
          9.3 数据主体之信息 —— 依 GDPR 第 46 条所依赖之任何保障措施,或依 GDPR 第 49 条所依赖之任何合同或同意记录之副本,
          可应请求向 §27 所列联系方式取得。
        </p>
      </div>
    </section>

    <!-- ===== § 10 Retention ===== -->
    <section id="retention" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§10.</span>Retention Periods
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">保留期限</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Personal Data is retained only for as long as is necessary for the
          purposes for which it is processed (Art. 5(1)(e) GDPR — storage limitation
          principle). The following maximum retention periods apply:
        </p>
        <div class="border border-gray-800 bg-gray-900/40 p-3 sm:p-4 my-3 overflow-x-auto">
          <table class="text-xs sm:text-sm w-full min-w-[500px]">
            <thead class="text-[10px] tracking-widest text-gray-600 uppercase">
              <tr class="border-b border-gray-800">
                <th class="text-left py-2 pr-3">Category</th>
                <th class="text-left py-2">Maximum Retention</th>
              </tr>
            </thead>
            <tbody class="text-gray-400 align-baseline">
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3"><span class="text-emerald-400">§4.1</span> Traffic counters</td>
                <td class="py-1.5">active life of Credential + 30 days following revocation</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3"><span class="text-emerald-400">§4.2</span> Account-identification data</td>
                <td class="py-1.5">active life of Credential + 30 days following revocation</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3"><span class="text-emerald-400">§4.3</span> Authentication-plane data</td>
                <td class="py-1.5">active life of operator account, deleted immediately upon account closure</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3"><span class="text-emerald-400">§4.4</span> Connection-layer metadata</td>
                <td class="py-1.5">duration of active Tunnel session only (not persisted)</td>
              </tr>
              <tr class="border-b border-gray-800/40">
                <td class="py-1.5 pr-3"><span class="text-emerald-400">§4.5</span> Administrative journal</td>
                <td class="py-1.5">365 days, after which entries are rotated by the system journal</td>
              </tr>
              <tr>
                <td class="py-1.5 pr-3"><span class="text-emerald-400">§4.6</span> Abuse-investigation records</td>
                <td class="py-1.5">case closure + 90 days (longer where required by a court order)</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p>
          Upon revocation of a Credential (whether at the Data Subject's request
          under §13(c) or by enforcement action under §9 of the Terms), all data
          linked to that Credential's UUID is purged at or before the limits above,
          save for records covered by a litigation hold or required to be retained
          for a longer period by Applicable Law.
        </p>
        <p class="text-gray-500 lang-zh">
          个人数据之保留期限以处理目的所必要之最短期间为限(GDPR 第 5(1)(e) 条 —— 存储限制原则)。
          适用以下最长保留期限:
          §4.1 流量计数 —— 凭证存续期间 + 撤销后 30 日;
          §4.2 账户识别数据 —— 凭证存续期间 + 撤销后 30 日;
          §4.3 认证面数据 —— 运维账户存续期间,账户关闭时立即删除;
          §4.4 连接层元数据 —— 仅活动隧道会话期间(不予持久化);
          §4.5 管理 journal —— 365 日,其后由系统 journal 轮转;
          §4.6 滥用调查记录 —— 案件结案 + 90 日(如法院命令要求更长,按命令执行)。
          凭证被撤销时(无论依 §13(c) 之数据主体请求或依条款 §9 之执法行动),
          除受诉讼保留约束或适用法律要求较长保留之记录外,与该凭证 UUID 关联之所有数据应在上述期限届满前清除。
        </p>
      </div>
    </section>

    <!-- ===== § 11 Security ===== -->
    <section id="security" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§11.</span>Security of Processing
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">处理之安全保障(GDPR 第 32 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Pursuant to Art. 32 GDPR, the Controller has implemented and maintains
          appropriate technical and organisational measures to ensure a level of
          security appropriate to the risk, including:
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) <strong>Transport-layer encryption</strong> (TLS 1.2 minimum, TLS 1.3 preferred) for all administrative-plane connections;</li>
          <li>(b) <strong>Mandatory second-factor authentication</strong> (WebAuthn / passkey or TOTP) for operator accounts;</li>
          <li>(c) <strong>Tamper-evident audit logging</strong> via append-only file attributes (<code class="text-emerald-400">chattr +a</code>) on credential and session records;</li>
          <li>(d) <strong>Cookie binding</strong> by host scope between the public face (<code class="text-emerald-400">example.com</code>) and the administrative subsystem (<code class="text-emerald-400">admin.example.com</code>) so that session tokens never traverse the public host;</li>
          <li>(e) <strong>Rate-limiting and brute-force resistance</strong> on authentication endpoints;</li>
          <li>(f) <strong>RPKI-validated, IRR-filtered BGP ingress</strong> with strict default-deny export to mitigate route-leak and hijack risk;</li>
          <li>(g) <strong>Forced encryption of inter-PoP traffic</strong> via WireGuard;</li>
          <li>(h) <strong>Principle of least privilege</strong> in administrative access design, including separate panel and shell credentials;</li>
          <li>(i) <strong>Regular review</strong> of access logs and routing announcements for anomalies.</li>
        </ul>
        <p>
          No system is perfectly secure. The Controller does not warrant that the
          measures listed above will be sufficient to prevent every conceivable
          security incident. In the event of a breach, §12 applies.
        </p>
        <p class="text-gray-500 lang-zh">
          依 GDPR 第 32 条,控制者已实施并维持适当之技术与组织措施,以确保与风险相称之安全程度,包括:
          (a) 所有管理面连接使用<strong>传输层加密</strong>(TLS 1.2 起,优先 1.3);
          (b) 运维账户<strong>强制二次因素认证</strong>(WebAuthn/passkey 或 TOTP);
          (c) 凭证与会话记录使用追加专属<strong>防篡改审计日志</strong>(<code class="text-emerald-400">chattr +a</code>);
          (d) 公开门户(<code class="text-emerald-400">example.com</code>)与管理子系统
          (<code class="text-emerald-400">admin.example.com</code>)之间按主机范围之<strong>Cookie 绑定</strong>,
          使会话令牌从不经过公开主机;
          (e) 认证端点之<strong>速率限制与防暴破</strong>;
          (f) <strong>RPKI 验证、IRR 过滤之 BGP 入站</strong>及严格默认拒绝之出站,缓解路由泄漏与劫持风险;
          (g) <strong>PoP 间流量强制加密</strong>(WireGuard);
          (h) 管理访问设计中之<strong>最小权限原则</strong>,含面板与 shell 凭证之分离;
          (i) 对访问日志与路由通告之<strong>定期审查</strong>。
          无系统绝对安全。控制者不保证上述措施足以防止一切可能之安全事件。发生违约时,适用 §12。
        </p>
      </div>
    </section>

    <!-- ===== § 12 Breach Notification ===== -->
    <section id="breach" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§12.</span>Personal Data Breach Notification
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">个人数据违约通知(GDPR 第 33-34 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">12.1 Notification to Supervisory Authority.</strong>
          In the event of a Personal Data breach within the meaning of Art. 4(12)
          GDPR, the Controller shall, in accordance with Art. 33 GDPR, notify the
          competent Supervisory Authority (presumed to be the Autoriteit
          Persoonsgegevens, Netherlands) without undue delay and, where feasible,
          not later than seventy-two (72) hours after having become aware of it,
          unless the breach is unlikely to result in a risk to the rights and
          freedoms of natural persons.
        </p>
        <p>
          <strong class="text-gray-200">12.2 Notification to Data Subjects.</strong>
          When the Personal Data breach is likely to result in a high risk to the
          rights and freedoms of natural persons, the Controller shall, in
          accordance with Art. 34 GDPR, communicate the breach to the affected Data
          Subjects without undue delay, by email to the address held on file in
          §4.2.
        </p>
        <p>
          <strong class="text-gray-200">12.3 Record of breaches.</strong>
          The Controller maintains an internal register of all Personal Data
          breaches, irrespective of whether notification to a Supervisory Authority
          or to Data Subjects was required, as required by Art. 33(5) GDPR. This
          register may be made available to a competent Supervisory Authority on
          request.
        </p>
        <p>
          <strong class="text-gray-200">12.4 Reporting a suspected breach.</strong>
          Any User or third party who becomes aware of, or has reasonable cause to
          suspect, a Personal Data breach affecting the Service is requested to
          report it to <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>
          with subject line <code class="text-emerald-400">[BREACH]</code>. The
          Controller will acknowledge bona-fide reports within twenty-four (24)
          hours.
        </p>
        <p class="text-gray-500 lang-zh">
          12.1 向监管机构之通知 —— 发生 GDPR 第 4(12) 条意义之个人数据违约时,控制者应依 GDPR 第 33 条,
          在不无故迟延之情形下,且在可行情况下不晚于获悉后 72 小时,通知主管监管机构(推定为荷兰个人资料保护局 AP),
          除非该等违约不太可能对自然人之权利与自由造成风险。
          12.2 向数据主体之通知 —— 个人数据违约可能对自然人之权利与自由造成高风险时,控制者应依 GDPR 第 34 条,
          在不无故迟延之情形下,通过 §4.2 留存之电邮地址向受影响数据主体传达违约事项。
          12.3 违约记录 —— 控制者依 GDPR 第 33(5) 条,保留所有个人数据违约之内部登记册,
          无论是否需要向监管机构或数据主体通知;该登记册可应主管监管机构之请求提供。
          12.4 疑似违约之报告 —— 任何获悉或有合理理由怀疑本服务遭受个人数据违约之用户或第三方,请以
          <code class="text-emerald-400">[BREACH]</code> 为主题向
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a> 报告。
          控制者将在 24 小时内确认善意报告。
        </p>
      </div>
    </section>

    <!-- ===== § 13 Rights ===== -->
    <section id="rights" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§13.</span>Rights of the Data Subject
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">数据主体之权利(GDPR 第 15-22 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          To the extent applicable under the GDPR and any other Applicable Law, the
          Data Subject has the following rights with respect to Personal Data held
          by the Controller:
        </p>
        <ul class="space-y-1 pl-4 text-gray-300">
          <li>(a) <strong>Right of access (Art. 15 GDPR)</strong> — to obtain confirmation as to whether or not Personal Data concerning the Data Subject are being processed, and where that is the case, access to such Personal Data and to the information enumerated in Art. 15(1);</li>
          <li>(b) <strong>Right to rectification (Art. 16 GDPR)</strong> — to obtain rectification of inaccurate Personal Data concerning the Data Subject, and to have incomplete Personal Data completed;</li>
          <li>(c) <strong>Right to erasure / right to be forgotten (Art. 17 GDPR)</strong> — to obtain the erasure of Personal Data concerning the Data Subject in the circumstances enumerated in Art. 17(1), subject to the exceptions in Art. 17(3);</li>
          <li>(d) <strong>Right to restriction of processing (Art. 18 GDPR)</strong> — to obtain restriction of processing in the circumstances enumerated in Art. 18(1);</li>
          <li>(e) <strong>Right to data portability (Art. 20 GDPR)</strong> — to receive Personal Data concerning the Data Subject which the Data Subject has provided to the Controller in a structured, commonly used, machine-readable format, and to transmit such data to another controller without hindrance, where the legal basis is consent or contract;</li>
          <li>(f) <strong>Right to object (Art. 21 GDPR)</strong> — to object on grounds relating to the Data Subject's particular situation to processing based on Art. 6(1)(e) or (f), at which point the Controller shall no longer process unless it demonstrates compelling legitimate grounds that override the interests, rights, and freedoms of the Data Subject;</li>
          <li>(g) <strong>Rights related to automated decision-making (Art. 22 GDPR)</strong> — see §15;</li>
          <li>(h) <strong>Right to withdraw consent (Art. 7(3) GDPR)</strong> — where processing is based on consent, the right to withdraw such consent at any time, without affecting the lawfulness of processing based on consent before its withdrawal.</li>
        </ul>
        <p>
          Equivalent rights under non-EU instruments are summarised in §20.
        </p>
        <p class="text-gray-500 lang-zh">
          在 GDPR 及任何其他适用法律所适用之范围内,数据主体就控制者持有之个人数据享有以下权利:
          (a) 访问权(第 15 条);(b) 更正权(第 16 条);(c) 删除权/被遗忘权(第 17 条);(d) 限制处理权(第 18 条);
          (e) 数据可携带权(第 20 条);(f) 反对权(第 21 条);(g) 与自动化决策有关之权利(第 22 条,见 §15);
          (h) 撤回同意权(第 7(3) 条 —— 处理依同意为依据时,可随时撤回同意,不影响撤回前基于同意之处理之合法性)。
          欧盟以外文书项下之同等权利概述见 §20。
        </p>
      </div>
    </section>

    <!-- ===== § 14 Complaint ===== -->
    <section id="complaint" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§14.</span>Right to Lodge a Complaint
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">投诉权(GDPR 第 77 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Without prejudice to any other administrative or judicial remedy, every
          Data Subject has the right to lodge a complaint with a Supervisory
          Authority — in particular in the Member State of the Data Subject's
          habitual residence, place of work, or place of the alleged infringement —
          if the Data Subject considers that the processing of Personal Data
          relating to them infringes the GDPR (<strong class="text-gray-200">Art. 77 GDPR</strong>).
        </p>
        <p>
          The lead Supervisory Authority for the Controller is the
          <strong>Autoriteit Persoonsgegevens (AP)</strong>, Netherlands —
          <a href="https://autoriteitpersoonsgegevens.nl" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">autoriteitpersoonsgegevens.nl ↗</a>.
          A list of all EU/EEA national Supervisory Authorities is maintained by the
          European Data Protection Board at
          <a href="https://edpb.europa.eu/about-edpb/who-we-are/members_en" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">edpb.europa.eu/about-edpb/who-we-are/members_en ↗</a>.
        </p>
        <p>
          Data Subjects outside the EEA may have analogous rights of complaint
          before competent regulators (see §20).
        </p>
        <p class="text-gray-500 lang-zh">
          在不妨害任何其他行政或司法救济之前提下,任何数据主体若认为关于其之个人数据处理违反 GDPR,
          均有权向监管机构 — 尤其是其惯常居所、工作地或被指控侵权地所在成员国之监管机构 — 提出投诉
          (<strong class="text-gray-300">GDPR 第 77 条</strong>)。
          控制者之主导监管机构为荷兰<strong>个人资料保护局(AP)</strong> ——
          <a href="https://autoriteitpersoonsgegevens.nl" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">autoriteitpersoonsgegevens.nl ↗</a>。
          欧盟/欧洲经济区各国监管机构之完整名单,可参见欧洲数据保护委员会(EDPB):
          <a href="https://edpb.europa.eu/about-edpb/who-we-are/members_en" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">edpb.europa.eu/about-edpb/who-we-are/members_en ↗</a>。
          EEA 之外之数据主体可向主管监管机构享有类似投诉权(见 §20)。
        </p>
      </div>
    </section>

    <!-- ===== § 15 No ADM ===== -->
    <section id="no-adm" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§15.</span>No Automated Decision-Making
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">无自动化决策(GDPR 第 22 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Service does not engage in solely automated decision-making, including
          profiling, that produces legal effects concerning Data Subjects or
          similarly significantly affects them, within the meaning of
          <strong class="text-gray-200">Art. 22(1) GDPR</strong>. The Service's
          automated subsystems (traffic counters, load balancers, abuse-detection
          heuristics) operate exclusively on aggregated technical metadata of the
          categories enumerated in §4 and do not assess personal characteristics,
          behaviour, creditworthiness, or any other matter of legal or similarly
          significant consequence to the Data Subject.
        </p>
        <p>
          Decisions made by the Controller under §9 of the Terms (Enforcement and
          Termination) are reviewed and made by a human operator, with reference to
          the technical evidence assembled by automated systems but not as a
          consequence of an automated determination.
        </p>
        <p class="text-gray-500 lang-zh">
          本服务不进行符合<strong class="text-gray-300">GDPR 第 22(1) 条</strong>意义之"仅基于自动化处理"
          (含画像)且对数据主体产生法律效力或具相似重大影响之决策。
          本服务之自动化子系统(流量计数、负载均衡、滥用检测启发式)仅基于 §4 所列类别之聚合技术元数据运作,
          不评估数据主体之个人特征、行为、信用状况或对其具法律意义或同等重大后果之任何其他事项。
          控制者依条款 §9(执法与终止)所作决定,系由人类运维参考自动化系统所汇集之技术证据进行审查与决定,
          而非自动化判定之结果。
        </p>
      </div>
    </section>

    <!-- ===== § 16 No Profiling ===== -->
    <section id="no-profiling" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§16.</span>No Profiling
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">不进行用户画像</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Controller does not engage in profiling within the meaning of
          Art. 4(4) GDPR — i.e. any form of automated processing of Personal Data
          consisting of the use of Personal Data to evaluate certain personal
          aspects relating to a natural person, in particular to analyse or predict
          aspects concerning that natural person's performance at work, economic
          situation, health, personal preferences, interests, reliability,
          behaviour, location, or movements.
        </p>
        <p>
          The traffic-management telemetry described in §4.1 is processed in
          aggregate at the level of the Credential and is not used to evaluate any
          personal aspect of the Data Subject behind that Credential.
        </p>
        <p class="text-gray-500 lang-zh">
          控制者不进行 GDPR 第 4(4) 条意义之画像 — 即任何形式之利用个人数据评估自然人之特定个人方面之自动化处理,
          特别是分析或预测有关该自然人之工作表现、经济状况、健康、个人偏好、兴趣、可靠性、行为、位置或活动之方面。
          §4.1 所述之流量管理遥测,系在凭证层级汇总处理,不用于评估该凭证背后之数据主体任何个人方面。
        </p>
      </div>
    </section>

    <!-- ===== § 17 Cookies ===== -->
    <section id="cookies" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§17.</span>Cookies and Local Storage
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">Cookie 与本地存储(ePrivacy 第 5(3) 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">17.1 Session cookie.</strong>
          The Service uses one strictly-necessary HTTP cookie, named
          <code class="text-emerald-400">ncn_session</code>, solely to carry the
          authenticated session state of operators accessing the administrative
          subsystem at <code class="text-emerald-400">admin.example.com</code>. The
          cookie is (a) set only after explicit authentication; (b) flagged
          <code class="text-emerald-400">HttpOnly</code>,
          <code class="text-emerald-400">Secure</code>, and
          <code class="text-emerald-400">SameSite=Lax</code>; (c) bound to
          <code class="text-emerald-400">Path=/</code>; (d) scoped to the
          administrative host and inaccessible to scripts on the public host
          <code class="text-emerald-400">example.com</code>; (e) expires no later than
          eight (8) hours after issuance; and (f) contains no information beyond a
          server-side session identifier and HMAC-signed claims about the
          authenticated operator.
        </p>
        <p>
          <strong class="text-gray-200">17.2 Local storage.</strong>
          The Service writes a small number of keys to
          <code class="text-emerald-400">window.localStorage</code> on the public
          host to persist (i) the User's chosen language preference; and (ii) the
          User's chosen dark / light theme preference. These values contain no
          personally identifying information and are never transmitted to any NCN
          server.
        </p>
        <p>
          <strong class="text-gray-200">17.3 Legal basis.</strong>
          Pursuant to <strong class="text-gray-200">Art. 5(3) of Directive 2002/58/EC
          (ePrivacy)</strong>, as amended by Directive 2009/136/EC and as transposed
          in Dutch law by Article 11.7a of the Telecommunicatiewet, and as
          interpreted by the EDPB Guidelines 2/2023 on the technical scope of
          Art. 5(3), neither category set out above requires prior consent because
          both fall within the "strictly necessary" exemption: the session cookie
          is necessary for the provision of a service explicitly requested by the
          User (authenticated access), and the local-storage entries record explicit
          user-interface preferences set by the User themselves.
        </p>
        <p>
          <strong class="text-gray-200">17.4 No tracking, no analytics.</strong>
          The Service does not use third-party analytics scripts, advertising
          cookies, fingerprinting techniques, web beacons, pixels, or any other
          mechanism designed to track Users across sessions, websites, or devices.
        </p>
        <p class="text-gray-500 lang-zh">
          17.1 会话 Cookie —— 本服务仅使用一个严格必要之 HTTP Cookie,名为
          <code class="text-emerald-400">ncn_session</code>,其唯一用途为承载访问管理子系统
          <code class="text-emerald-400">admin.example.com</code> 之运维身份会话状态。
          该 Cookie(a)仅在显式身份验证后设置;(b)标记 <code class="text-emerald-400">HttpOnly</code>、
          <code class="text-emerald-400">Secure</code> 与 <code class="text-emerald-400">SameSite=Lax</code>;
          (c)绑定 <code class="text-emerald-400">Path=/</code>;(d)仅作用于管理子域名,对公开域名
          <code class="text-emerald-400">example.com</code> 上之脚本不可访问;(e)签发后不晚于 8 小时过期;
          (f)除服务端会话标识符与对已验证运维身份之 HMAC 签名声明外不含其他信息。
          17.2 本地存储 —— 本服务在公开域名上将少量键写入 <code class="text-emerald-400">window.localStorage</code>
          以持久化(i)用户选定之语言偏好;(ii)用户选定之深色/浅色主题偏好。该等数值不含任何个人识别信息,
          亦不向任何 NCN 服务器传输。
          17.3 法律依据 —— 依经 2009/136/EC 指令修订并由荷兰《电信法》第 11.7a 条转化之
          <strong class="text-gray-300">2002/58/EC 指令第 5(3) 条(ePrivacy)</strong>,
          以及 EDPB 第 2/2023 号指南就第 5(3) 条之技术范围所作之指引,以上类别均无需事先同意,
          理由是二者均属"严格必要"豁免:会话 Cookie 系为提供用户明确请求之服务(已验证访问)所必需;
          本地存储条目系记录用户本人所设定之显式界面偏好。
          17.4 无跟踪、无分析 —— 本服务不使用任何第三方分析脚本、广告 Cookie、指纹识别技术、网页信标、像素或任何旨在跨会话、
          跨网站、跨设备追踪用户之机制。
        </p>
      </div>
    </section>

    <!-- ===== § 18 Children ===== -->
    <section id="children" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§18.</span>Children's Data
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">未成年人数据(GDPR 第 8 条)</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Service is not directed at, and is not intended for use by,
          individuals under the age of sixteen (16), being the threshold set by
          Art. 8(1) GDPR (subject to Member State derogations, none of which lowers
          the threshold below thirteen (13)). The Controller does not knowingly
          collect Personal Data from individuals below such age threshold.
        </p>
        <p>
          If the Controller becomes aware that Personal Data has been collected from
          an individual below the applicable age threshold without verifiable
          parental consent within the meaning of Art. 8(2) GDPR, the Controller
          shall delete such data as soon as reasonably practicable.
        </p>
        <p class="text-gray-500 lang-zh">
          本服务非面向未满 16 周岁(GDPR 第 8(1) 条所定门槛,受成员国例外约束,无一将门槛降至 13 岁以下)之个人,
          亦非供其使用。控制者不会在知情情况下收集该年龄门槛以下个人之个人数据。若控制者获悉在 GDPR 第 8(2) 条所定可验证之父母同意之外,
          已从适用年龄门槛以下个人收集个人数据,控制者应在合理可行之最早时间内删除该等数据。
        </p>
      </div>
    </section>

    <!-- ===== § 19 Request Procedure ===== -->
    <section id="request" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§19.</span>Data Subject Request Procedure
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">数据主体请求程序</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">19.1 How to submit a request.</strong>
          Requests pursuant to Art. 15-22 GDPR or equivalent rights under other
          Applicable Law shall be sent by email to
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>
          with subject line <code class="text-emerald-400">[DSR]</code> followed by
          a short description (e.g. <code class="text-emerald-400">[DSR] access request</code>).
          The Data Subject is requested to specify (a) the right being exercised;
          (b) the Credential UUID and / or registered email address; and (c) any
          additional context necessary to locate the data.
        </p>
        <p>
          <strong class="text-gray-200">19.2 Identity verification.</strong>
          The Controller may, where it has reasonable doubts about the identity of
          the requester, request additional information necessary to confirm the
          requester's identity, in accordance with Art. 12(6) GDPR. The Controller
          shall not request more information than is necessary for this purpose.
        </p>
        <p>
          <strong class="text-gray-200">19.3 Response time.</strong>
          The Controller shall provide information on action taken on a request
          without undue delay and in any event within one (1) month of receipt of
          the request, in accordance with Art. 12(3) GDPR. That period may be
          extended by two (2) further months where necessary, taking into account
          the complexity and number of requests; the Controller shall inform the
          Data Subject of any such extension within one month of receipt, together
          with the reasons for the delay.
        </p>
        <p>
          <strong class="text-gray-200">19.4 No fee.</strong>
          Responses to requests shall be free of charge, save where requests are
          manifestly unfounded or excessive (e.g. repetitive), in which case the
          Controller may charge a reasonable fee or refuse to act on the request,
          in accordance with Art. 12(5) GDPR.
        </p>
        <p class="text-gray-500 lang-zh">
          19.1 提交方式 —— GDPR 第 15-22 条或其他适用法律项下同等权利之请求,应以电邮发至
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>,
          主题行以 <code class="text-emerald-400">[DSR]</code> 起头并附简短说明(如 <code class="text-emerald-400">[DSR] access request</code>)。
          数据主体请说明(a)所行使之权利;(b)凭证 UUID 及/或登记邮箱;(c)定位数据所需之任何其他背景信息。
          19.2 身份核实 —— 控制者就请求人身份有合理疑问时,可依 GDPR 第 12(6) 条请求确认身份所必要之额外信息。
          控制者不会请求超出此目的所必要之信息。
          19.3 响应时限 —— 控制者依 GDPR 第 12(3) 条,应在不无故迟延且无论如何在收到请求后 1 个月内,
          告知就请求所采取之行动。该等期间在必要时(考虑请求之复杂性与数量)可再延长 2 个月;
          控制者应在收到请求后 1 个月内将该等延长连同延迟之理由告知数据主体。
          19.4 无费用 —— 请求之回应应免费,除非请求显属无依据或过分(例如重复),此时控制者依 GDPR 第 12(5) 条,
          可收取合理费用或拒绝处理请求。
        </p>
      </div>
    </section>

    <!-- ===== § 20 Server Locations ===== -->
    <section id="jurisdictions" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§20.</span>Server-Location Data Protection Laws
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">服务器所在地数据保护法律</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Each PoP is subject to the data-protection law of its host jurisdiction.
          Data Subjects may have additional or supplementary rights under those
          laws.
        </p>
        <div class="border border-gray-800 bg-gray-900/40 p-3 sm:p-4 space-y-3">
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">Region C SAR</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· Personal Data (Privacy) Ordinance (Cap. 486) — six Data Protection Principles ("DPPs"); data-access and data-correction rights under DPP 6</li>
              <li>· Office of the Privacy Commissioner for Personal Data (PCPD) — <a href="https://www.pcpd.org.hk" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">pcpd.org.hk ↗</a></li>
            </ul>
          </div>
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">Japan</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· Act on the Protection of Personal Information (APPI, Act No. 57 of 2003) as most recently amended</li>
              <li>· Personal Information Protection Commission (PPC) — <a href="https://www.ppc.go.jp" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">ppc.go.jp ↗</a></li>
              <li>· Adequacy decision (EU 2019/419) recognises Japan's level of protection for transfers from the EEA</li>
            </ul>
          </div>
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">United States · California</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· California Consumer Privacy Act of 2018 (Cal. Civ. Code § 1798.100 et seq.) as amended by California Privacy Rights Act ("CPRA") — right to know, right to delete, right to opt-out of sale / sharing, right to limit use of sensitive personal information</li>
              <li>· California Privacy Protection Agency (CPPA) — <a href="https://cppa.ca.gov" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">cppa.ca.gov ↗</a></li>
              <li>· Electronic Communications Privacy Act (18 U.S.C. §§ 2510-2523)</li>
            </ul>
          </div>
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">Region D</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· Personal Data Protection Act 2012 (No. 26 of 2012) — consent, purpose-limitation, notification, and access / correction obligations</li>
              <li>· Personal Data Protection Commission (PDPC) — <a href="https://www.pdpc.gov.sg" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">pdpc.gov.sg ↗</a></li>
            </ul>
          </div>
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">
              <span class="text-emerald-500">Germany · within the EEA</span>
            </div>
            <ul class="space-y-0.5 text-gray-400 text-[13px] sm:text-sm">
              <li>· GDPR (directly applicable), supplemented by the Bundesdatenschutzgesetz (BDSG); processing here is not a third-country transfer</li>
              <li>· Hessian Commissioner for Data Protection and Freedom of Information (HBDI) — Region B being in Hesse — <a href="https://datenschutz.hessen.de" target="_blank" rel="noopener" class="text-emerald-400 hover:underline">datenschutz.hessen.de ↗</a></li>
            </ul>
          </div>
        </div>
        <p class="text-gray-500 lang-zh">
          各接入点受其所在司法管辖区之数据保护法律管辖。数据主体在此等法律下可能享有额外或补充权利。
          中国香港特别行政区:《个人资料(私隐)条例》(第 486 章)—— 六项数据保护原则,
          DPP 6 项下之查阅与改正权;个人资料私隐专员公署(PCPD)—— pcpd.org.hk。
          日本:经最新修订之《个人信息保护法》(平成 15 年法律第 57 号);个人信息保护委员会(PPC)—— ppc.go.jp;
          充分性决定(EU 2019/419)承认日本就 EEA 之传输之保护水平。
          新加坡:《2012 年个人数据保护法》(2012 年第 26 号法令)—— 同意、目的限制、通知及查阅/改正义务;
          个人数据保护委员会(PDPC)—— pdpc.gov.sg。
          美国 · 加利福尼亚州:经《加州隐私权法》(CPRA)修订之《2018 年加州消费者隐私法》
          (Cal. Civ. Code § 1798.100 等)—— 知情权、删除权、拒绝出售/共享权、限制敏感个人信息使用权;
          加州隐私保护局(CPPA)—— cppa.ca.gov;《电子通信隐私法》(18 U.S.C. §§ 2510-2523)。
          德国(EEA 之内):直接适用之 GDPR,由《联邦数据保护法》(BDSG)补充;于该处之处理不构成第三国传输;
          黑森州数据保护与信息自由专员(HBDI,法兰克福位于黑森州)—— datenschutz.hessen.de。
        </p>
      </div>
    </section>

    <!-- ===== § 21 NIS2 ===== -->
    <section id="nis2" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§21.</span>NIS2 Alignment
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">NIS2 合规对齐</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Service is not, as at the Effective Date, classified as an "essential
          entity" or "important entity" within the meaning of
          <strong class="text-gray-200">Directive (EU) 2022/2555 ("NIS2")</strong>.
          Nevertheless, in voluntary alignment with the cybersecurity risk-management
          principles set out in Art. 21 NIS2, the Controller endeavours to
          implement and maintain appropriate technical, operational, and
          organisational measures (cross-referenced in §11).
        </p>
        <p>
          Where a security incident also constitutes a Personal Data breach within
          the meaning of Art. 4(12) GDPR, the Controller's notification obligations
          under §12 (Art. 33-34 GDPR) shall apply in addition to any voluntary
          notification under NIS2-aligned procedures.
        </p>
        <p class="text-gray-500 lang-zh">
          截至生效日,本服务并不被归类为<strong class="text-gray-300">《欧盟 2022/2555 号指令》("NIS2")</strong>
          意义之"基本实体"或"重要实体"。然而,控制者自愿与 NIS2 第 21 条所列网络安全风险管理原则保持一致,
          致力于实施并维持适当之技术、运营与组织措施(详见 §11)。
          安全事件同时构成 GDPR 第 4(12) 条意义之个人数据违约时,控制者依 §12(GDPR 第 33-34 条)之通知义务,
          适用于任何依 NIS2 对齐程序之自愿通知之外。
        </p>
      </div>
    </section>

    <!-- ===== § 22 Government Requests ===== -->
    <section id="government" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§22.</span>Government Access Requests
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">政府访问请求</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          <strong class="text-gray-200">22.1 Presumption against voluntary disclosure.</strong>
          The Controller maintains a presumption against the voluntary disclosure of
          Personal Data to any governmental or law-enforcement authority absent
          compulsory process. A request for voluntary disclosure shall be refused
          unless (a) the request is supported by a lawful, jurisdictionally valid
          legal instrument; (b) compliance is mandated by Applicable Law; or (c) the
          disclosure is necessary to prevent imminent loss of life or serious
          bodily injury and is permitted by Art. 49(1)(f) GDPR.
        </p>
        <p>
          <strong class="text-gray-200">22.2 Scope of disclosure.</strong>
          Where the Controller is compelled to disclose Personal Data, the
          Controller shall (a) verify the validity of the instrument; (b) where
          permitted by law, notify the Data Subject before complying; (c) disclose
          only the narrow categories of data required by the instrument; and
          (d) consider available avenues to challenge or limit the disclosure.
        </p>
        <p>
          <strong class="text-gray-200">22.3 Transparency.</strong>
          The Controller shall, to the extent permitted by Applicable Law, publish
          an aggregate transparency report at
          <code class="text-emerald-400">example.com</code> summarising the number
          and type of compelled-disclosure requests received per annum.
        </p>
        <p>
          <strong class="text-gray-200">22.4 Schrems II considerations.</strong>
          The Controller acknowledges the limitations identified by the Court of
          Justice of the European Union in <em>Case C-311/18 (Schrems II)</em>
          concerning access to Personal Data by public authorities of third
          countries. The Controller does not knowingly subject Personal Data to
          access by public authorities of any third country in a manner that would
          violate the essence of the rights guaranteed by Articles 7, 8, or 47 of
          the Charter of Fundamental Rights of the European Union.
        </p>
        <p class="text-gray-500 lang-zh">
          22.1 反对自愿披露之推定 —— 控制者就向任何政府或执法机关之自愿披露,在无强制程序之前提下,维持反对推定。
          自愿披露请求应予拒绝,除非:(a)请求有合法且在管辖意义上有效之法律文书支持;(b)适用法律要求遵从;
          (c)披露系防止迫近之生命损失或严重人身伤害所必要,且经 GDPR 第 49(1)(f) 条所允许。
          22.2 披露范围 —— 控制者被强制披露个人数据时,应:(a)核实文书有效性;
          (b)在法律允许之范围内,于遵从前通知数据主体;(c)仅披露文书所要求之狭义数据类别;
          (d)考虑可用以质疑或限制披露之途径。
          22.3 透明度 —— 控制者应在适用法律允许之范围内,于 <code class="text-emerald-400">example.com</code>
          发布合计性透明度报告,概述每年收到之强制披露请求之数量与类型。
          22.4 Schrems II 考量 —— 控制者承认欧盟法院 <em>C-311/18 号案件(Schrems II)</em>
          所认定之有关第三国公权力机关访问个人数据之限制。控制者不会以违反欧盟基本权利宪章第 7、8 或 47 条所保障权利之本质之方式,
          知情地将个人数据置于任何第三国公权力机关之访问之下。
        </p>
      </div>
    </section>

    <!-- ===== § 23 DPO ===== -->
    <section id="dpo" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§23.</span>Data Protection Officer
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">数据保护官</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Service is operated on a not-for-profit, hobbyist basis. Its
          processing activities do not (a) constitute core activities consisting of
          processing operations which, by virtue of their nature, their scope, or
          their purposes, require regular and systematic monitoring of Data
          Subjects on a large scale; nor (b) consist of processing on a large scale
          of Special Categories of Personal Data. Accordingly, the Controller is
          not required under Art. 37(1) GDPR to designate a Data Protection
          Officer.
        </p>
        <p>
          The contact at §27 acts as the single point of contact for all
          data-protection matters and may be relied upon by Data Subjects in lieu
          of a designated DPO. Should the threshold of Art. 37(1) GDPR be crossed
          in future, the Controller shall designate a DPO and identify their
          contact details by amendment to this Policy.
        </p>
        <p class="text-gray-500 lang-zh">
          本服务以非营利、业余基础运营。其处理活动既不(a)构成因性质、范围或目的而需对数据主体进行大规模、定期、
          系统性监测之核心活动;也不(b)构成对特殊类别个人数据之大规模处理。据此,控制者无须依 GDPR 第 37(1) 条任命数据保护官。
          §27 所列联系方式作为所有数据保护事项之单一联络点,数据主体可代替已任命之 DPO 而依此联系。
          若日后跨越 GDPR 第 37(1) 条之门槛,控制者将任命 DPO 并通过本政策之修订识别其联系详情。
        </p>
      </div>
    </section>

    <!-- ===== § 24 Notices ===== -->
    <section id="notices" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§24.</span>Notices
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">通知</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          Notices to the Controller under this Policy shall be made by email to
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>
          and shall be deemed received on the next business day in the Netherlands
          following dispatch, absent evidence of non-delivery. Notices to a Data
          Subject shall be made (at the Controller's option) by email to the address
          on file or by publication at <code class="text-emerald-400">example.com/privacy</code>.
        </p>
        <p class="text-gray-500 lang-zh">
          本政策项下向控制者之通知应以电邮发至 <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>,
          在无未送达证明之情形下,视为于发送后下一荷兰工作日送达。向数据主体之通知,由控制者选择以电邮发至留存地址,或在
          <code class="text-emerald-400">example.com/privacy</code> 公布。
        </p>
      </div>
    </section>

    <!-- ===== § 25 Changes ===== -->
    <section id="changes" class="mb-12 scroll-mt-20">
      <h3 class="text-lg sm:text-xl text-gray-100 mb-1">
        <span class="text-emerald-500 mr-2">§25.</span>Changes to this Policy
      </h3>
      <h4 class="text-sm text-gray-500 normal-case tracking-normal mb-4 lang-zh">本政策之修订</h4>
      <div class="space-y-3 text-sm sm:text-base normal-case tracking-normal text-gray-400 leading-relaxed legal-body">
        <p>
          The Controller may revise this Policy from time to time by publishing an
          updated version at <code class="text-emerald-400">https://example.com/privacy</code>.
          Material revisions shall be accompanied by an incremented version number
          and a revised Effective Date. The Controller shall use reasonable
          endeavours to notify Data Subjects of material revisions in advance
          where the revision adversely affects their rights. Continued use of the
          Service following the publication of revised Policy shall not be
          construed as consent to any processing of Personal Data on a basis other
          than that already specified in this Policy.
        </p>
        <p class="text-gray-500 lang-zh">
          控制者可随时通过在 <code class="text-emerald-400">https://example.com/privacy</code> 发布更新版本对本政策进行修订。
          重大修订应伴随版本号递增与新生效日。控制者就修订对数据主体权利产生不利影响之情形,应尽合理努力提前通知。
          修订后政策发布后继续使用本服务,不应视为同意以本政策已规定之依据以外之任何依据处理个人数据。
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
          The obligations of the Controller under this Policy with respect to
          Personal Data processed during the term of the Data Subject's
          relationship with the Service shall survive the termination of that
          relationship for the duration of the applicable retention period
          specified in §10, and thereafter to the extent required by Applicable
          Law (in particular, the limitation periods applicable to civil and
          administrative actions in respect of Personal Data processing).
        </p>
        <p class="text-gray-500 lang-zh">
          控制者就数据主体与本服务关系存续期间所处理之个人数据所承担之义务,应在该关系终止后于 §10 所定适用保留期间内继续存续,
          其后在适用法律(尤其是就个人数据处理之民事与行政诉讼适用之时效期间)所要求之范围内继续。
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
          All data-protection matters — Art. 15-22 GDPR requests, complaints,
          breach reports, credential-destruction requests, and general enquiries:
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>
          (subject line prefix <code class="text-emerald-400">[DSR]</code> or
          <code class="text-emerald-400">[BREACH]</code> as appropriate).
        </p>
        <p>
          Operational and routing enquiries:
          <a href="mailto:noc@example.com" class="text-emerald-400 hover:underline">noc@example.com</a>
        </p>
        <p>
          Registry contact (administrative-c / technical-c / abuse-c) is publicly
          available via the RIPE WHOIS service at
          <code class="text-emerald-400">whois.ripe.net</code> under the maintainer
          object <code class="text-emerald-400">ACMECLOUD-MNT</code>.
        </p>

        <p class="text-gray-500 lang-zh">
          一切数据保护事项 — GDPR 第 15-22 条数据主体权利之行使、投诉、违约通报、
          凭证销毁请求及一般咨询,请发送至
          <a href="mailto:abuse@example.com" class="text-emerald-400 hover:underline">abuse@example.com</a>,
          并视情况于邮件主题加注前缀 <code class="text-emerald-400">[DSR]</code>(数据主体请求)
          或 <code class="text-emerald-400">[BREACH]</code>(违约通报)以加快分流处理。
        </p>
        <p class="text-gray-500 lang-zh">
          运营及路由互联相关咨询:
          <a href="mailto:noc@example.com" class="text-emerald-400 hover:underline">noc@example.com</a>。
        </p>
        <p class="text-gray-500 lang-zh">
          注册信息联络人(administrative-c、technical-c、abuse-c)可通过 RIPE WHOIS
          数据库公开查询,查询地址
          <code class="text-emerald-400">whois.ripe.net</code>,维护者对象为
          <code class="text-emerald-400">ACMECLOUD-MNT</code>。
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
