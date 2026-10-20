// Redpanda Console 구성을 따르는 화면: Overview, Topics(+상세), Consumer Groups(+상세).
// 화면 문자열은 모두 locales/*.js 의 키로 참조한다 (템플릿의 t() 는 app.js 에서 전역 등록).
import { store, api, fmtBytes, fmtNum, fmtRate, fmtTime, stateClass, groupRate, groupByteRate, link, isInternal, matcher, health } from './util.js'
import { t } from './i18n.js'
const { ref, computed, watch } = Vue

/** 토픽별 소비 그룹 목록 (스냅샷에서 역참조) */
function consumersOf(topic) {
  const out = []
  for (const g of store.snap?.groups || []) {
    const x = g.topics.find((y) => y.topic === topic)
    if (x) out.push({ group: g, t: x })
  }
  return out
}

export const Overview = {
  setup() {
    const s = computed(() => store.snap)
    const h = computed(() => health(s.value))
    const primary = computed(() => s.value.topics.reduce((a, x) => a + x.size, 0))
    const total = computed(() => s.value.brokers.reduce((a, b) => a + b.size, 0))
    const version = computed(() => [...new Set(s.value.brokers.map((b) => b.version).filter(Boolean))].join(', ') || '-')
    const groupStates = computed(() => {
      const c = { Stable: 0, Empty: 0, Rebalancing: 0, Dead: 0 }
      for (const g of s.value.groups) c[g.state in c ? g.state : 'Rebalancing']++
      return c
    })
    const consume = computed(() => s.value.groups.reduce((a, g) => [a[0] + groupRate(g), a[1] + groupByteRate(g)], [0, 0]))
    /** lag 상위 그룹-토픽 5개 */
    const topLag = computed(() => s.value.groups.flatMap((g) => g.topics.map((x) => ({ g, t: x }))).filter((x) => x.t.lag > 0).sort((a, b) => b.t.lag - a.t.lag).slice(0, 5))
    return { s, h, primary, total, version, groupStates, consume, topLag, fmtBytes, fmtNum, fmtRate, stateClass, link }
  },
  template: `
  <div>
    <div class="stats">
      <div class="stat"><div class="label">{{ t('ov.status') }}</div><div class="value"><span class="badge lg" :class="h.cls">{{ t('health.' + h.status) }}</span></div></div>
      <div class="stat"><div class="label">{{ t('ov.storage') }}</div><div class="value">{{ fmtBytes(total) }}</div></div>
      <div class="stat"><div class="label">{{ t('ov.version') }}</div><div class="value sm">{{ version }}</div></div>
      <div class="stat"><div class="label">{{ t('ov.brokersOnline') }}</div><div class="value" :class="h.offlineBrokers.length ? 'bad' : ''">{{ s.brokers.length }} <small>{{ t('ov.of', { n: s.brokers.length + h.offlineBrokers.length }) }}</small></div></div>
      <div class="stat"><div class="label">{{ t('ov.topics') }}</div><div class="value">{{ s.topics.length }}</div></div>
      <div class="stat"><div class="label">{{ t('ov.partitions') }}</div><div class="value">{{ fmtNum(h.partitions) }}</div></div>
      <div class="stat"><div class="label">{{ t('ov.groups') }}</div><div class="value">{{ s.groups.length }}</div></div>
    </div>

    <section class="panel">
      <h3 class="panel-title">{{ t('ov.brokerDetails') }}</h3>
      <table>
        <thead><tr><th>{{ t('col.id') }}</th><th>{{ t('col.status') }}</th><th>{{ t('col.address') }}</th><th>{{ t('col.rack') }}</th><th>{{ t('col.version') }}</th><th class="r">{{ t('col.leaders') }}</th><th class="r">{{ t('col.replicas') }}</th><th class="r">{{ t('col.size') }}</th></tr></thead>
        <tbody>
          <tr v-for="b in s.brokers" :key="b.id">
            <td>{{ b.id }} <span v-if="b.id === s.controller" class="badge info">{{ t('badge.controller') }}</span></td>
            <td><span class="badge ok">{{ t('badge.running') }}</span></td>
            <td class="mono">{{ b.host }}:{{ b.port }}</td><td>{{ b.rack || '-' }}</td><td>{{ b.version || '-' }}</td>
            <td class="r">{{ b.leaders }}</td><td class="r">{{ b.replicas }}</td><td class="r">{{ fmtBytes(b.size) }}</td>
          </tr>
          <tr v-for="id in h.offlineBrokers" :key="'x' + id">
            <td>{{ id }}</td><td><span class="badge bad">{{ t('badge.offline') }}</span></td><td colspan="6" class="muted">{{ t('ov.offlineNote') }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <div class="grid2">
      <section class="panel">
        <h3 class="panel-title">{{ t('ov.clusterDetails') }}</h3>
        <dl class="details">
          <dt>{{ t('ov.secStorage') }}</dt>
          <dd><span>{{ t('ov.totalBytes') }}</span><b>{{ fmtBytes(total) }}</b></dd>
          <dd><span>{{ t('ov.primaryBytes') }}</span><b>{{ fmtBytes(primary) }}</b></dd>
          <dd><span>{{ t('ov.replicatedBytes') }}</span><b>{{ fmtBytes(Math.max(0, total - primary)) }}</b></dd>
          <dt>{{ t('ov.secPartitions') }}</dt>
          <dd><span>{{ t('ov.totalReplicas') }}</span><b>{{ fmtNum(h.partitions) }} / {{ fmtNum(h.replicas) }}</b></dd>
          <dd><span>{{ t('ov.urp') }}</span><b :class="h.urp ? 'warn' : 'ok'">{{ h.urp }}</b></dd>
          <dd><span>{{ t('ov.offline') }}</span><b :class="h.offline ? 'bad' : 'ok'">{{ h.offline }}</b></dd>
          <dt>{{ t('ov.secGroups') }}</dt>
          <dd><span>{{ t('ov.groupStates') }}</span><b>{{ groupStates.Stable }} / {{ groupStates.Empty }} / {{ groupStates.Rebalancing }} / {{ groupStates.Dead }}</b></dd>
          <dd><span>{{ t('ov.totalLag') }}</span><b>{{ fmtNum(s.groups.reduce((a, g) => a + g.lag, 0)) }}</b></dd>
          <dt>{{ t('ov.secThroughput') }}</dt>
          <dd><span>{{ t('ov.produce') }}</span><b>{{ fmtRate(s.topics.reduce((a, x) => a + x.inRate, 0)) }} msg/s</b></dd>
          <dd><span>{{ t('ov.consume') }}</span><b>{{ fmtRate(consume[0]) }} msg/s · {{ fmtBytes(consume[1]) }}/s ({{ t('common.est') }})</b></dd>
        </dl>
      </section>
      <section class="panel">
        <h3 class="panel-title">{{ t('ov.topLag') }}</h3>
        <table>
          <thead><tr><th>{{ t('col.groupId') }}</th><th>{{ t('col.topic') }}</th><th class="r">{{ t('col.msgRate') }}</th><th class="r">{{ t('col.lag') }}</th></tr></thead>
          <tbody>
            <tr v-for="x in topLag" :key="x.g.id + '/' + x.t.topic">
              <td><a :href="link('groups', x.g.id)">{{ x.g.id }}</a></td><td><a :href="link('topics', x.t.topic)">{{ x.t.topic }}</a></td>
              <td class="r">{{ fmtRate(x.t.rate) }}</td><td class="r">{{ fmtNum(x.t.lag) }}</td>
            </tr>
            <tr v-if="!topLag.length"><td colspan="4" class="empty">{{ t('empty.lag') }}</td></tr>
          </tbody>
        </table>
        <p class="muted small"><a href="#/topology">{{ t('ov.openTopology') }}</a></p>
      </section>
    </div>
  </div>`,
}

export const Topics = {
  setup() {
    const q = ref('')
    const showInternal = ref(false)
    const list = computed(() => {
      const m = matcher(q.value)
      return (store.snap?.topics || []).filter((x) => (showInternal.value || !isInternal(x)) && m(x.name))
    })
    const consumers = (name) => consumersOf(name).length
    const rf = (x) => x.partitions[0]?.replicas.length ?? 0
    const totalParts = computed(() => list.value.reduce((a, x) => a + x.partitions.length, 0))
    return { q, showInternal, list, totalParts, consumers, rf, fmtBytes, fmtNum, fmtRate, link }
  },
  template: `
  <div>
    <div class="stats">
      <div class="stat"><div class="label">{{ t('tp.total') }}</div><div class="value">{{ list.length }}</div></div>
      <div class="stat"><div class="label">{{ t('tp.totalPartitions') }}</div><div class="value">{{ fmtNum(totalParts) }}</div></div>
    </div>
    <section class="panel">
      <div class="toolbar">
        <input class="search" v-model="q" :placeholder="t('common.search')">
        <label><input type="checkbox" v-model="showInternal"> {{ t('tp.showInternal') }}</label>
      </div>
      <table>
        <thead><tr><th>{{ t('col.name') }}</th><th class="r">{{ t('col.partitions') }}</th><th class="r">{{ t('col.replicas') }}</th><th class="r">{{ t('col.urp') }}</th><th class="r">{{ t('col.messages') }}</th><th class="r">{{ t('col.size') }}</th><th class="r">{{ t('col.produceRate') }}</th><th class="r">{{ t('col.consumerGroups') }}</th></tr></thead>
        <tbody>
          <tr v-for="x in list" :key="x.name">
            <td><a class="strong" :href="link('topics', x.name)">{{ x.name }}</a> <span v-if="x.offline" class="badge bad">{{ t('badge.offlineN', { n: x.offline }) }}</span></td>
            <td class="r">{{ x.partitions.length }}</td>
            <td class="r">{{ rf(x) }}</td>
            <td class="r" :class="x.urp ? 'warn' : 'muted'">{{ x.urp }}</td>
            <td class="r">{{ fmtNum(x.messages) }}</td>
            <td class="r">{{ fmtBytes(x.size) }}</td>
            <td class="r">{{ fmtRate(x.inRate) }}</td>
            <td class="r">{{ consumers(x.name) }}</td>
          </tr>
          <tr v-if="!list.length"><td colspan="8" class="empty">{{ t('empty.topics') }}</td></tr>
        </tbody>
      </table>
    </section>
  </div>`,
}

export const TopicDetail = {
  props: ['name'],
  setup(props) {
    const tab = ref('messages')
    const topic = computed(() => store.snap?.topics.find((x) => x.name === props.name))
    const consumers = computed(() => consumersOf(props.name))

    // 설정 탭: 열 때 한 번 조회
    const configs = ref(null)
    const hideDefault = ref(false)
    const cfgError = ref('')
    const shownConfigs = computed(() => (configs.value || []).filter((c) => !hideDefault.value || c.source !== 'DEFAULT_CONFIG'))
    async function loadConfig() {
      try { configs.value = await api(`/api/topics/${encodeURIComponent(props.name)}/config`); cfgError.value = '' } catch (e) { cfgError.value = e.message }
    }

    // 메시지 탭: 탭 진입 시 최신 메시지를 한 번 읽고, 이후는 조회 버튼으로만 다시 읽는다
    const mq = ref({ partition: -1, limit: 50, offset: '' })
    const msgs = ref(null)
    const msgError = ref('')
    const msgLoading = ref(false)
    const open = ref({})
    async function loadMessages() {
      msgLoading.value = true
      const p = new URLSearchParams({ partition: mq.value.partition, limit: mq.value.limit })
      if (mq.value.offset !== '' && mq.value.partition >= 0) p.set('offset', mq.value.offset)
      try { msgs.value = await api(`/api/topics/${encodeURIComponent(props.name)}/messages?${p}`); msgError.value = ''; open.value = {} } catch (e) { msgError.value = e.message } finally { msgLoading.value = false }
    }
    /** JSON 이면 들여쓰기해서 보여준다 */
    function pretty(v) {
      if (v == null) return 'null'
      try { return JSON.stringify(JSON.parse(v), null, 2) } catch { return v }
    }
    const toggle = (m) => { const k = m.partition + ':' + m.offset; open.value[k] = !open.value[k] }

    watch(tab, (x) => {
      if (x === 'config' && !configs.value) loadConfig()
      if (x === 'messages' && !msgs.value) loadMessages()
    }, { immediate: true })
    // 다른 토픽으로 이동: 이미 messages 탭이면 직접 다시 읽고, 아니면 탭 전환 watch 가 읽는다
    watch(() => props.name, () => {
      configs.value = null; msgs.value = null
      if (tab.value === 'messages') loadMessages(); else tab.value = 'messages'
    })
    const tabs = computed(() => [
      ['messages', t('td.tabMessages')],
      ['consumers', t('td.tabConsumers', { n: consumers.value.length })],
      ['partitions', t('td.tabPartitions', { n: topic.value?.partitions.length ?? 0 })],
      ['config', t('td.tabConfig')],
    ])
    return { store, tab, tabs, topic, consumers, configs, shownConfigs, hideDefault, cfgError, mq, msgs, msgError, msgLoading, open, toggle, loadMessages, pretty, fmtBytes, fmtNum, fmtRate, fmtTime, stateClass, link }
  },
  template: `
  <div v-if="topic">
    <div class="stats">
      <div class="stat"><div class="label">{{ t('td.size') }}</div><div class="value">{{ fmtBytes(topic.size) }}</div></div>
      <div class="stat"><div class="label">{{ t('td.messages') }}</div><div class="value">{{ fmtNum(topic.messages) }}</div></div>
      <div class="stat"><div class="label">{{ t('td.partitions') }}</div><div class="value">{{ topic.partitions.length }}</div></div>
      <div class="stat"><div class="label">{{ t('td.rf') }}</div><div class="value">{{ topic.partitions[0]?.replicas.length ?? 0 }}</div></div>
      <div class="stat"><div class="label">{{ t('td.produceRate') }}</div><div class="value">{{ fmtRate(topic.inRate) }} <small>msg/s</small></div></div>
      <div class="stat"><div class="label">{{ t('td.avgMsg') }}</div><div class="value">{{ fmtBytes(topic.avgMsgBytes) }}</div></div>
      <div class="stat"><div class="label">{{ t('td.urp') }}</div><div class="value" :class="topic.urp ? 'warn' : 'ok'">{{ topic.urp }}</div></div>
    </div>
    <section class="panel">
      <div class="tabs"><button v-for="[k, l] in tabs" :class="{ on: tab === k }" @click="tab = k">{{ l }}</button></div>

      <div v-if="tab === 'messages'">
        <div class="toolbar">
          <label>{{ t('td.partition') }} <select v-model.number="mq.partition"><option :value="-1">{{ t('td.all') }}</option><option v-for="p in topic.partitions" :value="p.id">{{ p.id }}</option></select></label>
          <label>{{ t('td.startOffset') }} <input v-model="mq.offset" :disabled="mq.partition < 0" :placeholder="t('td.newest')" style="width:110px"></label>
          <label>{{ t('td.max') }} <select v-model.number="mq.limit"><option v-for="n in [20, 50, 100, 200, 500]" :value="n">{{ n }}</option></select></label>
          <button class="primary" @click="loadMessages" :disabled="msgLoading">{{ msgLoading ? t('td.loading') : t('td.search') }}</button>
          <span class="muted small" v-if="msgs">{{ t('td.count', { n: msgs.length }) }}</span>
        </div>
        <p v-if="msgError" class="bad">{{ msgError }}</p>
        <table v-if="msgs">
          <thead><tr><th class="r">{{ t('col.offset') }}</th><th class="r">{{ t('col.partition') }}</th><th>{{ t('col.timestamp') }}</th><th>{{ t('col.key') }}</th><th>{{ t('col.value') }}</th><th class="r">{{ t('col.size') }}</th></tr></thead>
          <tbody>
            <template v-for="m in msgs" :key="m.partition + ':' + m.offset">
              <tr class="click" @click="toggle(m)">
                <td class="r">{{ m.offset }}</td><td class="r">{{ m.partition }}</td><td class="nowrap">{{ fmtTime(m.timestamp) }}</td>
                <td class="mono clip">{{ m.key ?? 'null' }}</td><td class="mono clip wide">{{ m.value ?? 'null' }}</td><td class="r">{{ fmtBytes(m.size) }}</td>
              </tr>
              <tr v-if="open[m.partition + ':' + m.offset]" class="expanded"><td colspan="6">
                <div class="muted small" v-if="m.headers.length">{{ t('td.headers') }}: <span v-for="h in m.headers" class="mono">{{ h[0] }}={{ h[1] }}&nbsp; </span></div>
                <div class="muted small" v-if="m.encoding !== 'text' || m.truncated">{{ m.encoding }}{{ m.truncated ? ' · ' + t('td.truncated') : '' }}</div>
                <pre>{{ m.encoding === 'text' ? pretty(m.value) : m.value }}</pre>
              </td></tr>
            </template>
            <tr v-if="!msgs.length"><td colspan="6" class="empty">{{ t('empty.messages') }}</td></tr>
          </tbody>
        </table>
      </div>

      <table v-if="tab === 'consumers'">
        <thead><tr><th>{{ t('col.groupId') }}</th><th>{{ t('col.state') }}</th><th class="r">{{ t('col.members') }}</th><th class="r">{{ t('col.msgRate') }}</th><th class="r">{{ t('col.byteRate') }}</th><th class="r">{{ t('col.lag') }}</th></tr></thead>
        <tbody>
          <tr v-for="c in consumers" :key="c.group.id">
            <td><a class="strong" :href="link('groups', c.group.id)">{{ c.group.id }}</a></td>
            <td><span class="badge" :class="stateClass(c.group.state)">{{ c.group.state }}</span></td>
            <td class="r">{{ c.group.members.length }}</td>
            <td class="r">{{ fmtRate(c.t.rate) }}</td><td class="r">{{ fmtBytes(c.t.byteRate) }}/s</td><td class="r">{{ fmtNum(c.t.lag) }}</td>
          </tr>
          <tr v-if="!consumers.length"><td colspan="6" class="empty">{{ t('empty.groups') }}</td></tr>
        </tbody>
      </table>

      <table v-if="tab === 'partitions'">
        <thead><tr><th class="r">{{ t('col.partitionId') }}</th><th class="r">{{ t('col.leader') }}</th><th>{{ t('col.replicas') }}</th><th>{{ t('col.isr') }}</th><th class="r">{{ t('col.low') }}</th><th class="r">{{ t('col.high') }}</th><th class="r">{{ t('col.messages') }}</th><th class="r">{{ t('col.size') }}</th></tr></thead>
        <tbody><tr v-for="p in topic.partitions" :key="p.id">
          <td class="r">{{ p.id }}</td>
          <td class="r"><span v-if="p.leader < 0" class="badge bad">{{ t('badge.offline') }}</span><template v-else>{{ p.leader }}</template></td>
          <td>{{ p.replicas.join(', ') }}</td>
          <td :class="p.isr.length < p.replicas.length ? 'warn' : ''">{{ p.isr.join(', ') }}</td>
          <td class="r">{{ p.start }}</td><td class="r">{{ p.end }}</td>
          <td class="r">{{ fmtNum(p.end - p.start) }}</td><td class="r">{{ fmtBytes(p.size) }}</td>
        </tr></tbody>
      </table>

      <div v-if="tab === 'config'">
        <div class="toolbar"><label><input type="checkbox" v-model="hideDefault"> {{ t('td.hideDefault') }}</label></div>
        <p v-if="cfgError" class="bad">{{ cfgError }}</p>
        <table v-if="configs">
          <thead><tr><th>{{ t('col.cfgName') }}</th><th>{{ t('col.cfgValue') }}</th><th>{{ t('col.source') }}</th></tr></thead>
          <tbody>
            <tr v-for="c in shownConfigs" :key="c.key"><td class="mono">{{ c.key }}</td><td class="mono">{{ c.sensitive ? '******' : c.value }}</td><td><span class="badge" :class="c.source === 'DEFAULT_CONFIG' ? 'muted' : 'info'">{{ c.source }}</span></td></tr>
            <tr v-if="!shownConfigs.length"><td colspan="3" class="empty">{{ t('empty.config') }}</td></tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
  <p v-else-if="store.snap" class="empty">{{ t('td.notFound', { name }) }}</p>`,
}

export const Groups = {
  setup() {
    const q = ref('')
    const list = computed(() => {
      const m = matcher(q.value)
      return (store.snap?.groups || []).filter((g) => m(g.id))
    })
    return { q, list, fmtBytes, fmtNum, fmtRate, stateClass, groupRate, groupByteRate, link }
  },
  template: `
  <section class="panel">
    <div class="toolbar"><input class="search" v-model="q" :placeholder="t('common.search')"><span class="muted small">{{ t('gr.count', { n: list.length }) }}</span></div>
    <table>
      <thead><tr><th>{{ t('col.state') }}</th><th>{{ t('col.groupId') }}</th><th class="r">{{ t('col.coordinator') }}</th><th>{{ t('col.protocol') }}</th><th class="r">{{ t('col.members') }}</th><th>{{ t('col.topics') }}</th><th class="r">{{ t('col.msgRate') }}</th><th class="r">{{ t('col.byteRate') }}</th><th class="r">{{ t('col.lagSum') }}</th></tr></thead>
      <tbody>
        <tr v-for="g in list" :key="g.id">
          <td><span class="badge" :class="stateClass(g.state)">{{ g.state }}</span></td>
          <td><a class="strong" :href="link('groups', g.id)">{{ g.id }}</a> <span v-if="g.error" class="badge bad" :title="g.error">{{ t('badge.error') }}</span></td>
          <td class="r">{{ g.coordinator }}</td>
          <td>{{ g.protocol || '-' }}</td>
          <td class="r">{{ g.members.length }}</td>
          <td class="muted">{{ g.topics.map((x) => x.topic).join(', ') }}</td>
          <td class="r">{{ fmtRate(groupRate(g)) }}</td>
          <td class="r">{{ fmtBytes(groupByteRate(g)) }}/s</td>
          <td class="r">{{ fmtNum(g.lag) }}</td>
        </tr>
        <tr v-if="!list.length"><td colspan="9" class="empty">{{ t('empty.groups') }}</td></tr>
      </tbody>
    </table>
  </section>`,
}

export const GroupDetail = {
  props: ['id'],
  setup(props) {
    const group = computed(() => store.snap?.groups.find((g) => g.id === props.id))
    const assignedCount = computed(() => group.value?.members.reduce((a, m) => a + Object.values(m.assigned).reduce((b, ps) => b + ps.length, 0), 0) ?? 0)
    const assigned = (m) => Object.entries(m.assigned).map(([x, ps]) => `${x} [${ps.join(',')}]`).join('  ')
    const collapsed = ref({})
    return { store, group, assignedCount, assigned, collapsed, fmtBytes, fmtNum, fmtRate, stateClass, groupRate, groupByteRate, link }
  },
  template: `
  <div v-if="group">
    <div class="stats">
      <div class="stat"><div class="label">{{ t('gd.state') }}</div><div class="value"><span class="badge lg" :class="stateClass(group.state)">{{ group.state }}</span></div></div>
      <div class="stat"><div class="label">{{ t('gd.members') }}</div><div class="value">{{ group.members.length }}</div></div>
      <div class="stat"><div class="label">{{ t('gd.assigned') }}</div><div class="value">{{ assignedCount }}</div></div>
      <div class="stat"><div class="label">{{ t('gd.protocol') }}</div><div class="value sm">{{ group.protocol || '-' }}</div></div>
      <div class="stat"><div class="label">{{ t('gd.coordinator') }}</div><div class="value">{{ group.coordinator }}</div></div>
      <div class="stat"><div class="label">{{ t('gd.consumeRate') }}</div><div class="value">{{ fmtRate(groupRate(group)) }} <small>msg/s</small></div></div>
      <div class="stat"><div class="label">{{ t('gd.throughput') }}</div><div class="value">{{ fmtBytes(groupByteRate(group)) }}<small>/s</small></div></div>
      <div class="stat"><div class="label">{{ t('gd.totalLag') }}</div><div class="value">{{ fmtNum(group.lag) }}</div></div>
    </div>
    <p v-if="group.error" class="banner bad">{{ group.error }}</p>
    <section class="panel">
      <h3 class="panel-title">{{ t('gd.membersTitle') }}</h3>
      <table>
        <thead><tr><th>{{ t('col.clientId') }}</th><th>{{ t('col.host') }}</th><th>{{ t('col.memberId') }}</th><th>{{ t('col.assignedParts') }}</th></tr></thead>
        <tbody>
          <tr v-for="m in group.members" :key="m.id"><td>{{ m.clientId }}</td><td class="mono">{{ m.host }}</td><td class="mono muted clip">{{ m.id }}</td><td class="mono">{{ assigned(m) }}</td></tr>
          <tr v-if="!group.members.length"><td colspan="4" class="empty">{{ t('empty.members') }}</td></tr>
        </tbody>
      </table>
    </section>
    <section class="panel" v-for="x in group.topics" :key="x.topic">
      <div class="toolbar click" @click="collapsed[x.topic] = !collapsed[x.topic]">
        <span class="caret">{{ collapsed[x.topic] ? '▸' : '▾' }}</span>
        <h3 class="panel-title inline"><a :href="link('topics', x.topic)" @click.stop>{{ x.topic }}</a></h3>
        <span class="muted small">{{ t('gd.topicSummary', { rate: fmtRate(x.rate), bytes: fmtBytes(x.byteRate), lag: fmtNum(x.lag), n: x.partitions.length, committed: fmtNum(x.committed) }) }}</span>
      </div>
      <table v-if="!collapsed[x.topic]">
        <thead><tr><th class="r">{{ t('col.partition') }}</th><th>{{ t('col.assignedMember') }}</th><th>{{ t('col.host') }}</th><th class="r">{{ t('col.logEnd') }}</th><th class="r">{{ t('col.groupOffset') }}</th><th class="r">{{ t('col.lag') }}</th></tr></thead>
        <tbody><tr v-for="p in x.partitions" :key="p.partition">
          <td class="r">{{ p.partition }}</td><td>{{ p.member || '-' }}</td><td class="mono muted">{{ p.host || '-' }}</td>
          <td class="r">{{ p.end }}</td><td class="r">{{ p.commit < 0 ? '-' : p.commit }}</td>
          <td class="r" :class="p.lag ? 'warn' : ''">{{ fmtNum(p.lag) }}</td>
        </tr></tbody>
      </table>
    </section>
  </div>
  <p v-else-if="store.snap" class="empty">{{ t('gd.notFound', { id }) }}</p>`,
}
