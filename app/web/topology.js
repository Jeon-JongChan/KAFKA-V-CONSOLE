// Topology: 토픽 → 컨슈머 그룹 mermaid 그래프 + 컨슈머별 처리량 표.
// 대규모(수백 토픽) 대응: graph.js 의 접두사 묶기 + 노드 상한, 확대/축소·이동, 표 페이지, 렌더 시간 비례 재그리기 간격.
import { store, fmtBytes, fmtNum, fmtRate, fmtTime, stateClass, worstState, link, isInternal, matcher } from './util.js'
import { planGraph } from './graph.js'
import { t, i18n } from './i18n.js'
const { ref, reactive, computed, watch, onMounted, onBeforeUnmount, nextTick } = Vue

/** mermaid 라벨 안에서 구문을 깨는 문자를 엔티티로 치환 */
const esc = (s) => String(s).replace(/"/g, '#quot;').replace(/</g, '#lt;').replace(/>/g, '#gt;')
/** 정규식 특수문자 이스케이프 (드릴다운 필터용) */
const reEsc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/** 링크 색상 */
const COLOR = { lag: '#e14226', active: '#2f9e55', idle: '#b4bcc8' }
/** 상태 색상 클래스 → mermaid classDef (묶음 노드는 A 접미사: 점선 테두리) */
const GROUP_STYLE = { ok: 'fill:#eaf7ee,stroke:#2f9e55', warn: 'fill:#fff4e5,stroke:#d9822b', bad: 'fill:#fdecea,stroke:#e14226', muted: 'fill:#f1f3f5,stroke:#98a2b3' }
const CLASS_DEFS = [
  'classDef topic fill:#eef4ff,stroke:#3b6fd8,color:#1d2433',
  'classDef topicA fill:#e1eaff,stroke:#3b6fd8,color:#1d2433,stroke-width:2px,stroke-dasharray:5 3',
  ...Object.entries(GROUP_STYLE).flatMap(([k, v]) => [`classDef g${k} ${v},color:#1d2433`, `classDef g${k}A ${v},color:#1d2433,stroke-width:2px,stroke-dasharray:5 3`]),
]

/** 필터를 적용한 토픽 목록과 그 토픽들에 대한 (그룹, 그룹-토픽) 쌍 */
function scope(snap, opt) {
  const match = matcher(opt.filter.trim())
  const consumed = new Set(snap.groups.flatMap((g) => g.topics.map((x) => x.topic)))
  // 검색어는 토픽명 또는 그 토픽을 소비하는 그룹 ID 에 걸리면 통과
  const byGroup = new Set(snap.groups.filter((g) => match(g.id)).flatMap((g) => g.topics.map((x) => x.topic)))
  const topics = snap.topics.filter((x) =>
    !(opt.hideInternal && isInternal(x)) && (opt.showIdle || consumed.has(x.name)) && (match(x.name) || byGroup.has(x.name)))
  const names = new Set(topics.map((x) => x.name))
  const pairs = snap.groups.flatMap((g) => g.topics.filter((x) => names.has(x.topic)).map((x) => ({ group: g, topic: x })))
  return { topics, pairs }
}

/**
 * 스냅샷으로 mermaid flowchart 소스를 만든다.
 * 반환: src(소스), key(레이아웃 구조 키), routes(노드 ID → 이동 해시 또는 드릴다운 필터), pairs(표용 원본 쌍), 표시/전체 노드 수, 사용된 묶음 단계.
 */
export function buildGraph(snap, opt) {
  const { topics, pairs } = scope(snap, opt)
  const { depth, shownT, shownG, edges, totalT, totalG } = planGraph(topics, pairs, opt)

  const id = new Map()
  const routes = {}
  const lines = ['flowchart LR', ...CLASS_DEFS]
  shownT.forEach((n, i) => {
    const nid = 't' + i
    id.set(n.key, nid)
    routes[nid] = n.agg ? { filter: '^' + reEsc(n.name) } : link('topics', n.name)
    const sub = n.agg ? t('topo.nTopics', { n: n.members.length }) : t('topo.nPartitions', { n: n.partitions })
    lines.push(`${nid}["<b>${esc(n.name)}${n.agg ? '*' : ''}</b><br/>${esc(sub)} · ${fmtBytes(n.size)}<br/>${esc(t('topo.in', { rate: fmtRate(n.inRate) }))}"]:::${n.agg ? 'topicA' : 'topic'}`)
  })
  shownG.forEach((n, i) => {
    const nid = 'g' + i
    id.set(n.key, nid)
    routes[nid] = n.agg ? { filter: '^' + reEsc(n.name) } : link('groups', n.name)
    const head = n.agg ? t('topo.nGroups', { n: n.members.length }) : n.members[0].state
    lines.push(`${nid}(["<b>${esc(n.name)}${n.agg ? '*' : ''}</b><br/>${esc(head)} · ${esc(t('topo.nMembers', { n: n.memberCount }))}<br/>${esc(t('topo.lagN', { n: fmtNum(n.lag) }))}"]):::g${worstState(n.members.map((g) => g.state))}${n.agg ? 'A' : ''}`)
  })
  const styles = []
  edges.forEach((e, i) => {
    // 링크 색: lag 임계치 초과 = 빨강, 소비 중 = 초록, 유휴 = 회색. 두께는 바이트 처리량의 로그 스케일
    const color = e.lag >= opt.lagWarn ? COLOR.lag : e.rate > 0.05 ? COLOR.active : COLOR.idle
    const width = (1.5 + Math.min(4, Math.log10(1 + e.bytes) / 1.5)).toFixed(1)
    styles.push(`linkStyle ${i} stroke:${color},stroke-width:${width}px`)
    lines.push(`${id.get(e.from)} -->|"${fmtRate(e.rate)} msg/s · ${fmtBytes(e.bytes)}/s<br/>${esc(t('topo.lagN', { n: fmtNum(e.lag) }))}"| ${id.get(e.to)}`)
  })

  // 레이아웃 구조 키: 노드/링크 구성 + 사용자 설정(언어, 경고치)이 바뀌면 즉시 다시 그린다
  const key = [i18n.lang, opt.lagWarn, ...id.keys(), ...edges.map((e) => e.from + '|' + e.to)].join('\n')
  return {
    src: lines.concat(styles).join('\n'), key, routes, pairs, depth,
    topicCount: topics.length,
    shown: { t: shownT.length, g: shownG.length },
    total: { t: totalT, g: totalG },
  }
}

export const Topology = {
  setup() {
    const opt = ref({ filter: '', hideInternal: true, showIdle: false, lagWarn: 1000, groupBy: 'auto', limit: 50, sort: 'lag' })
    const box = ref(null)
    const vp = ref(null)
    const renderError = ref('')
    const copied = ref(false)
    const drawing = ref(false)
    const drawn = ref(null)
    const size = ref({ w: 0, h: 0 })
    const pz = reactive({ k: 1, x: 0, y: 0 })
    let last = { src: '', key: '', at: 0, ms: 0 }
    let busy = false, again = false, seq = 0

    const graph = computed(() => (store.snap ? buildGraph(store.snap, opt.value) : null))
    const summary = computed(() => {
      const p = graph.value?.pairs || []
      return {
        groups: new Set(p.map((x) => x.group.id)).size,
        rate: p.reduce((a, x) => a + x.topic.rate, 0),
        bytes: p.reduce((a, x) => a + x.topic.byteRate, 0),
        lag: p.reduce((a, x) => a + x.topic.lag, 0),
        lagging: p.filter((x) => x.topic.lag >= opt.value.lagWarn).length,
      }
    })
    /** 컨슈머별 처리량 표: 바이트 처리량 내림차순. 300+ 행은 페이지로 자른다. */
    const rows = computed(() => [...(graph.value?.pairs || [])].sort((a, b) => b.topic.byteRate - a.topic.byteRate || b.topic.lag - a.topic.lag))
    const page = ref(1)
    const pageSize = 50
    watch(() => [opt.value.filter, rows.value.length], () => { page.value = 1 })
    const paged = computed(() => {
      const pages = Math.max(1, Math.ceil(rows.value.length / pageSize))
      const p = Math.min(page.value, pages)
      return { rows: rows.value.slice((p - 1) * pageSize, p * pageSize), page: p, pages, total: rows.value.length }
    })
    const truncated = computed(() => graph.value && (graph.value.shown.t < graph.value.total.t || graph.value.shown.g < graph.value.total.g))
    /** 뷰포트 높이: 그래프 높이에 맞추되 화면의 72% 를 넘지 않게 */
    const vpHeight = computed(() => Math.round(Math.min(Math.max(220, size.value.h + 32), window.innerHeight * 0.72)))

    async function mermaidReady() {
      while (!window.mermaid) await new Promise((r) => setTimeout(r, 50))
      if (!seq) {
        mermaid.initialize({
          startOnLoad: false, theme: 'base', securityLevel: 'strict', maxTextSize: 5e6, maxEdges: 20000,
          themeVariables: { fontFamily: 'system-ui, "Segoe UI", "Malgun Gothic", sans-serif', fontSize: '13px', lineColor: '#b4bcc8', edgeLabelBackground: '#ffffff', primaryTextColor: '#1d2433' },
          flowchart: { htmlLabels: true, curve: 'basis', nodeSpacing: 30, rankSpacing: 120, useMaxWidth: false },
        })
      }
    }

    /**
     * mermaid 로 다시 그린다. 구조가 같고 값만 바뀐 경우에는 직전 렌더 시간의 10배가 지나야 다시 그린다
     * (50ms 그래프는 매 갱신, 2초 그래프는 20초마다). 렌더 중 들어온 요청은 끝난 뒤 한 번만 처리.
     */
    async function render() {
      const g = graph.value
      if (!g || !box.value) return
      if (busy) { again = true; return }
      if (g.src === last.src) return
      if (g.key === last.key && Date.now() - last.at < last.ms * 10) return
      if (!g.topicCount) { box.value.innerHTML = ''; last = { src: g.src, key: g.key, at: Date.now(), ms: 0 }; return }
      busy = true; drawing.value = true
      try {
        await mermaidReady()
        const t0 = performance.now()
        const { svg } = await mermaid.render('topo' + ++seq, g.src)
        box.value.innerHTML = svg
        const el = box.value.querySelector('svg')
        const vb = el.viewBox.baseVal
        el.setAttribute('width', vb.width); el.setAttribute('height', vb.height); el.style.maxWidth = 'none'
        size.value = { w: vb.width, h: vb.height }
        const structural = g.key !== last.key
        last = { src: g.src, key: g.key, at: Date.now(), ms: Math.round(performance.now() - t0) }
        drawn.value = { time: fmtTime(last.at), ms: last.ms }
        renderError.value = ''
        if (structural) { await nextTick(); fit() }
      } catch (e) {
        renderError.value = String(e.message || e)
        last.src = g.src // 같은 소스로 무한 재시도하지 않음
      } finally {
        busy = false; drawing.value = false
        if (again) { again = false; render() }
      }
    }

    /** 그래프 전체가 뷰포트에 들어오게 (100% 초과 확대는 하지 않음) */
    function fit() {
      if (!vp.value || !size.value.w) return
      const cw = vp.value.clientWidth, ch = vp.value.clientHeight
      pz.k = Math.min(1, (cw - 24) / size.value.w, (ch - 24) / size.value.h)
      pz.x = (cw - size.value.w * pz.k) / 2
      pz.y = (ch - size.value.h * pz.k) / 2
    }
    /** (px, py) 지점을 고정한 채 f 배 확대/축소 */
    function zoomAt(px, py, f) {
      const k = Math.min(4, Math.max(0.03, pz.k * f))
      pz.x = px - ((px - pz.x) * k) / pz.k
      pz.y = py - ((py - pz.y) * k) / pz.k
      pz.k = k
    }
    const zoomBtn = (f) => zoomAt(vp.value.clientWidth / 2, vp.value.clientHeight / 2, f)
    function onWheel(e) {
      const r = vp.value.getBoundingClientRect()
      zoomAt(e.clientX - r.left, e.clientY - r.top, e.deltaY < 0 ? 1.15 : 1 / 1.15)
    }
    // 드래그 이동. 4px 이상 움직였으면 이어지는 click 은 노드 이동으로 처리하지 않는다
    let drag = null, moved = false
    const onMove = (e) => {
      if (Math.abs(e.clientX - drag.sx) + Math.abs(e.clientY - drag.sy) > 4) moved = true
      pz.x = e.clientX - drag.dx; pz.y = e.clientY - drag.dy
    }
    const onUp = () => { window.removeEventListener('pointermove', onMove); window.removeEventListener('pointerup', onUp); drag = null }
    function onDown(e) {
      if (e.button !== 0) return
      drag = { sx: e.clientX, sy: e.clientY, dx: e.clientX - pz.x, dy: e.clientY - pz.y }
      moved = false
      window.addEventListener('pointermove', onMove)
      window.addEventListener('pointerup', onUp)
    }
    /** 노드 클릭: 개별 노드는 상세 화면으로, 묶음 노드는 그 접두사로 필터(드릴다운) */
    function onClick(e) {
      if (moved) return
      const node = e.target.closest('.node')
      const m = node && /flowchart-([tg]\d+)-/.exec(node.id)
      const r = m && graph.value.routes[m[1]]
      if (typeof r === 'string') location.hash = r
      else if (r) opt.value.filter = r.filter
    }
    async function copySrc() {
      await navigator.clipboard?.writeText(graph.value.src)
      copied.value = true
      setTimeout(() => (copied.value = false), 1500)
    }
    watch(graph, () => nextTick(render))
    onMounted(render)
    onBeforeUnmount(onUp)
    return { store, opt, box, vp, pz, vpHeight, graph, summary, paged, page, truncated, renderError, copied, drawing, drawn, onWheel, onDown, onClick, zoomBtn, fit, copySrc, fmtBytes, fmtNum, fmtRate, stateClass, link }
  },
  template: `
  <div>
    <div class="stats">
      <div class="stat"><div class="label">{{ t('topo.shown') }}</div><div class="value">{{ (graph?.shown.t ?? 0) + (graph?.shown.g ?? 0) }}</div></div>
      <div class="stat"><div class="label">{{ t('topo.groups') }}</div><div class="value">{{ summary.groups }}</div></div>
      <div class="stat"><div class="label">{{ t('topo.consumeRate') }}</div><div class="value">{{ fmtRate(summary.rate) }} <small>msg/s</small></div></div>
      <div class="stat"><div class="label">{{ t('topo.consumeBytes') }}</div><div class="value">{{ fmtBytes(summary.bytes) }}<small>/s</small></div></div>
      <div class="stat"><div class="label">{{ t('topo.totalLag') }}</div><div class="value">{{ fmtNum(summary.lag) }}</div></div>
      <div class="stat"><div class="label">{{ t('topo.lagging') }}</div><div class="value" :class="summary.lagging ? 'bad' : 'ok'">{{ summary.lagging }}</div></div>
    </div>
    <section class="panel">
      <div class="toolbar">
        <input class="search" v-model="opt.filter" :placeholder="t('topo.filter')">
        <button v-if="opt.filter" @click="opt.filter = ''">{{ t('topo.clear') }}</button>
        <label>{{ t('topo.groupBy') }}
          <select v-model="opt.groupBy">
            <option value="auto">{{ t('topo.groupAuto') }}</option><option :value="0">{{ t('topo.groupNone') }}</option>
            <option :value="1">{{ t('topo.groupDepth', { n: 1 }) }}</option><option :value="2">{{ t('topo.groupDepth', { n: 2 }) }}</option>
          </select>
        </label>
        <label>{{ t('topo.limit') }}
          <select v-model.number="opt.limit"><option v-for="n in [25, 50, 100, 200]" :value="n">{{ n }}</option></select>
        </label>
        <label>{{ t('topo.sort') }}
          <select v-model="opt.sort">
            <option value="lag">{{ t('topo.sortLag') }}</option><option value="bytes">{{ t('topo.sortBytes') }}</option>
            <option value="produce">{{ t('topo.sortProduce') }}</option><option value="name">{{ t('topo.sortName') }}</option>
          </select>
        </label>
      </div>
      <div class="toolbar">
        <label><input type="checkbox" v-model="opt.hideInternal"> {{ t('topo.hideInternal') }}</label>
        <label><input type="checkbox" v-model="opt.showIdle"> {{ t('topo.showIdle') }}</label>
        <label>{{ t('topo.lagWarn') }} <input type="number" v-model.number="opt.lagWarn" min="0" style="width:90px"></label>
        <span class="spacer"></span>
        <button @click="copySrc">{{ copied ? t('topo.copied') : t('topo.copy') }}</button>
      </div>
      <p v-if="graph && graph.depth > 0" class="banner info">{{ t('topo.grouped', { n: graph.depth, topics: graph.topicCount, nodes: graph.total.t }) }}</p>
      <p v-if="truncated" class="banner warn">{{ t('topo.truncated', { shownT: graph.shown.t, totalT: graph.total.t, shownG: graph.shown.g, totalG: graph.total.g }) }}</p>
      <p class="legend muted">
        <span class="swatch" style="background:#2f9e55"></span> {{ t('topo.legendConsuming') }}
        <span class="swatch" style="background:#e14226"></span> {{ t('topo.legendLag') }}
        <span class="swatch" style="background:#b4bcc8"></span> {{ t('topo.legendIdle') }}
        <span class="swatch dashed"></span> {{ t('topo.legendAgg') }}
        · {{ t('topo.legendNote') }}
      </p>
      <p v-if="renderError" class="bad">{{ renderError }}</p>
      <p v-if="graph && !graph.topicCount" class="empty">{{ t('topo.empty') }}</p>
      <div v-show="graph && graph.topicCount" ref="vp" class="viewport" :style="{ height: vpHeight + 'px' }" @wheel.prevent="onWheel" @pointerdown="onDown" @click="onClick">
        <div ref="box" class="canvas" :style="{ transform: 'translate(' + pz.x + 'px,' + pz.y + 'px) scale(' + pz.k + ')' }"></div>
        <div class="zoom">
          <button @click.stop="zoomBtn(1.25)" @pointerdown.stop :title="t('topo.zoomIn')">+</button>
          <button @click.stop="zoomBtn(0.8)" @pointerdown.stop :title="t('topo.zoomOut')">−</button>
          <button @click.stop="fit" @pointerdown.stop>{{ t('topo.fit') }}</button>
          <span class="muted small">{{ Math.round(pz.k * 100) }}%</span>
        </div>
        <div class="drawn muted small">{{ drawing ? t('topo.rendering') : drawn ? t('topo.drawn', drawn) : '' }}</div>
      </div>
    </section>
    <section class="panel">
      <h3 class="panel-title">{{ t('topo.table') }}</h3>
      <table>
        <thead><tr><th>{{ t('col.groupId') }}</th><th>{{ t('col.state') }}</th><th>{{ t('col.topic') }}</th><th class="r">{{ t('col.msgRate') }}</th><th class="r">{{ t('col.byteRate') }}</th><th class="r">{{ t('col.lag') }}</th><th class="r">{{ t('col.committed') }}</th></tr></thead>
        <tbody>
          <tr v-for="e in paged.rows" :key="e.group.id + '/' + e.topic.topic">
            <td><a :href="link('groups', e.group.id)">{{ e.group.id }}</a></td>
            <td><span class="badge" :class="stateClass(e.group.state)">{{ e.group.state }}</span></td>
            <td><a :href="link('topics', e.topic.topic)">{{ e.topic.topic }}</a></td>
            <td class="r">{{ fmtRate(e.topic.rate) }}</td>
            <td class="r">{{ fmtBytes(e.topic.byteRate) }}/s</td>
            <td class="r" :class="e.topic.lag >= opt.lagWarn ? 'bad' : ''">{{ fmtNum(e.topic.lag) }}</td>
            <td class="r">{{ fmtNum(e.topic.committed) }}</td>
          </tr>
          <tr v-if="!paged.total"><td colspan="7" class="empty">{{ t('empty.groups') }}</td></tr>
        </tbody>
      </table>
      <div class="pager" v-if="paged.pages > 1">
        <button :disabled="paged.page <= 1" @click="page--">{{ t('pager.prev') }}</button>
        <span class="muted small">{{ t('pager.of', { page: paged.page, pages: paged.pages, n: paged.total }) }}</span>
        <button :disabled="paged.page >= paged.pages" @click="page++">{{ t('pager.next') }}</button>
      </div>
    </section>
  </div>`,
}
