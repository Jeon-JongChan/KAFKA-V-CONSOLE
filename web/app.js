// 앱 셸: Redpanda Console 형태의 사이드바 + 상단바 레이아웃, 해시 라우팅, 주기적 스냅샷 갱신, 언어 선택.
import { store, refresh, fmtTime } from './util.js'
import { t, i18n, setLang, LOCALES } from './i18n.js'
import { Topology } from './topology.js'
import { Overview, Topics, TopicDetail, Groups, GroupDetail } from './views.js'
const { createApp, ref, computed, watch, onMounted } = Vue

/** "#/topics/foo" → ['topics', 'foo'] */
const parse = () => location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent)

/** 사이드바 아이콘 (Feather Icons, MIT) */
const ICON = {
  overview: '<path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/>',
  topology: '<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><line x1="8.59" y1="13.51" x2="15.42" y2="17.49"/><line x1="15.41" y1="6.51" x2="8.59" y2="10.49"/>',
  topics: '<polygon points="12 2 2 7 12 12 22 7 12 2"/><polyline points="2 17 12 22 22 17"/><polyline points="2 12 12 17 22 12"/>',
  groups: '<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>',
}
const NAV = [['', 'nav.overview', ICON.overview], ['topology', 'nav.topology', ICON.topology], ['topics', 'nav.topics', ICON.topics], ['groups', 'nav.groups', ICON.groups]]

const App = {
  components: { Overview, Topology, Topics, TopicDetail, Groups, GroupDetail },
  setup() {
    const path = ref(parse())
    window.addEventListener('hashchange', () => { path.value = parse(); window.scrollTo(0, 0) })

    /** 경로 → [컴포넌트, props, 제목, 브레드크럼]. 상세 화면은 두 번째 세그먼트를 이름으로 쓴다 */
    const view = computed(() => {
      i18n.lang // 언어 변경 시 제목/크럼 재계산
      const [a, b] = path.value
      if (a === 'topics') return b ? ['TopicDetail', { name: b }, b, [['topics', t('nav.topics')]]] : ['Topics', {}, t('nav.topics'), []]
      if (a === 'groups') return b ? ['GroupDetail', { id: b }, b, [['groups', t('nav.groups')]]] : ['Groups', {}, t('nav.groups'), []]
      if (a === 'topology') return ['Topology', {}, t('nav.topology'), []]
      return ['Overview', {}, t('nav.overview'), []]
    })

    // interval 이 0 이면 자동 갱신 끔
    let timer
    watch(() => store.interval, (s) => { clearInterval(timer); if (s) timer = setInterval(refresh, s * 1000) }, { immediate: true })
    onMounted(refresh)
    const langs = Object.entries(LOCALES).map(([code, loc]) => ({ code, name: loc._name }))
    return { store, path, view, nav: NAV, refresh, fmtTime, t, i18n, setLang, langs }
  },
  template: `
  <div class="layout">
    <aside class="sidebar">
      <a class="logo" href="#/">
        <svg viewBox="0 0 32 32" width="28" height="28"><rect width="32" height="32" rx="7" fill="#e14226"/><path d="M8 9l8 15 8-15" fill="none" stroke="#fff" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round"/></svg>
        <span>V-Console</span>
      </a>
      <nav>
        <a v-for="[k, key, icon] in nav" :href="'#/' + k" :class="{ on: (path[0] || '') === k }">
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" v-html="icon"></svg>
          <span>{{ t(key) }}</span>
        </a>
      </nav>
      <div class="side-foot" v-if="store.snap">
        <div>{{ t('side.cluster') }} <b>{{ store.snap.clusterId || '-' }}</b></div>
        <div>{{ t('side.brokers', { n: store.snap.brokers.length }) }}</div>
      </div>
    </aside>
    <div class="content">
      <header class="topbar">
        <nav class="crumbs">
          <template v-for="[k, l] in view[3]"><a :href="'#/' + k">{{ l }}</a><span class="sep">/</span></template>
          <span>{{ view[2] }}</span>
        </nav>
        <span class="spacer"></span>
        <span class="muted small" v-if="store.snap">{{ t('top.updated', { time: fmtTime(store.snap.time) }) }}</span>
        <select v-model="i18n.lang" @change="setLang(i18n.lang)" :title="t('top.language')">
          <option v-for="l in langs" :value="l.code">{{ l.name }}</option>
        </select>
        <select v-model.number="store.interval" :title="t('top.autoTitle')">
          <option :value="0">{{ t('top.autoOff') }}</option>
          <option v-for="s in [5, 10, 30, 60]" :value="s">{{ t('top.autoEvery', { s }) }}</option>
        </select>
        <button @click="refresh" :disabled="store.loading">{{ store.loading ? t('top.refreshing') : t('top.refresh') }}</button>
      </header>
      <main class="page">
        <h1>{{ view[2] }}</h1>
        <p v-if="store.error" class="banner bad">{{ t('err.kafka', { msg: store.error }) }}</p>
        <p v-for="e in store.snap?.errors || []" class="banner warn">{{ e }}</p>
        <p v-if="!store.snap && !store.error" class="empty">{{ t('common.loading') }}</p>
        <component v-if="store.snap" :is="view[0]" v-bind="view[1]" />
      </main>
    </div>
  </div>`,
}

const app = createApp(App)
app.config.globalProperties.t = t
app.mount('#app')
