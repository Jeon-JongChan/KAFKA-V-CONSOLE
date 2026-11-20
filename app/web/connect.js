// Kafka Connect 화면: 등록된 커넥터, 토픽 흐름, 워커별 태스크 부하.
import { store, manage, managementState, fmtBytes, fmtNum, fmtRate, link } from './util.js'
import { t } from './i18n.js'
const { ref, computed, watch, onMounted, nextTick } = Vue

/** Connect 상태 문자열을 배지 색으로 바꾼다. */
function connectStateClass(s) {
  if (s === 'RUNNING') return 'ok'
  if (s === 'FAILED') return 'bad'
  if (s === 'PAUSED' || s === 'UNASSIGNED' || s === 'RESTARTING') return 'warn'
  return 'muted'
}

/** 클래스 전체 이름에서 마지막 조각만 남긴다. */
const shortClass = (c) => (c || '').split('.').pop() || '-'

/** 현재 연결된 Connect의 상태와 커넥터 관리 화면이다. */
export const Connect = {
  /** 조회, 등록 폼, 그리고 개별 관리 작업을 연결한다. */
  setup() {
    const data = ref(null)
    const error = ref('')
    const q = ref('')
    const loading = ref(false)
    const target = ref(null)
    const draft = ref(null)
    const pending = ref(null)
    const actionForm = ref(null)
    const busy = ref(false)
    const actionError = ref('')
    const notice = ref('')
    let loadID = 0
    /** /api/connect 를 받아 카드 데이터를 바꾼다. 주소 없음과 조회 실패를 구분한다. */
    const load = async () => {
      const id = ++loadID
      loading.value = true
      try {
        const { data: v, target: token } = await managementState('/api/connect')
        if (id !== loadID) return
        data.value = v
        target.value = token
        error.value = v.error || ''
      } catch (e) {
        if (id === loadID) error.value = e.message
      } finally {
        if (id === loadID) loading.value = false
      }
    }
    /** 새 커넥터 입력을 열면서 현재 Connect 대상을 고정한다. */
    async function startCreate() {
      pending.value = null
      draft.value = { text: '', url: data.value.url, target: target.value }
      actionError.value = ''
      notice.value = ''
      await nextTick()
      actionForm.value?.querySelector('textarea')?.focus()
    }
    /** 파일을 먼저 편집기에 넣어 내용을 검토하도록 한다. */
    async function upload(event) {
      const file = event.target.files?.[0]
      const current = draft.value
      event.target.value = ''
      if (!file || !current) return
      actionError.value = ''
      try {
        if (file.size > 1024 * 1024) throw new Error(t('connect.fileTooLarge'))
        const text = await file.text()
        if (draft.value === current) current.text = text
      } catch {
        if (draft.value === current) actionError.value = t(file.size > 1024 * 1024 ? 'connect.fileTooLarge' : 'connect.fileError')
      }
    }
    /** 재시작·삭제 대상을 확인 카드에 고정한다. */
    async function confirmAction(kind, name) {
      draft.value = null
      pending.value = { kind, name, url: data.value.url, target: target.value }
      actionError.value = ''
      notice.value = ''
      await nextTick()
      actionForm.value?.querySelector('button[type="button"]')?.focus()
    }
    /** 선택한 작업을 한 번 요청하고 성공 시 목록을 새로 읽는다. */
    async function applyAction() {
      if (busy.value || (!draft.value && !pending.value)) return
      busy.value = true
      actionError.value = ''
      const path = '/api/connect/connectors'
      try {
        if (draft.value) {
          // 원문을 전달해 설정 숫자의 자릿수를 브라우저 파싱으로 잃지 않는다.
          const { text, target: token } = draft.value
          await manage(path, 'POST', token, text.replace(/^\uFEFF/, ''))
          notice.value = t('connect.created')
          draft.value = null
        } else {
          const { kind, name, target: token } = pending.value
          const connector = `${path}/${encodeURIComponent(name)}`
          await manage(kind === 'restart' ? `${connector}/restart` : connector, kind === 'restart' ? 'POST' : 'DELETE', token)
          notice.value = t('connect.' + (kind === 'restart' ? 'restartRequested' : 'deleted'), { name })
          pending.value = null
        }
        await load()
      } catch (e) {
        actionError.value = e.message
      } finally {
        busy.value = false
      }
    }
    onMounted(load)
    // 상단 새로고침·자동 갱신이 끝나면 Connect 도 다시 읽는다.
    watch(() => store.loading, (cur, prev) => { if (prev && !cur && !loading.value && !busy.value) load() })
    const list = computed(() => {
      const rows = data.value?.connectors || []
      const s = q.value.trim().toLowerCase()
      if (!s) return rows
      return rows.filter((c) => (c.name + ' ' + c.class + ' ' + c.type + ' ' + (c.topics || []).map((tp) => tp.name).join(' ')).toLowerCase().includes(s))
    })
    const maxTasks = computed(() => Math.max(1, ...(data.value?.workers || []).map((w) => w.tasks)))
    /** 부하를 숫자로 붙이지 못한 이유를 한 줄로 고른다. */
    const measureNote = (c) => {
      // 스냅샷 자체가 없으면 상단 배너가 이유를 말한다. 그룹이 없다고 하면 틀린 말이다.
      if (c.measured || data.value?.clusterError) return ''
      if (c.type === 'sink') return t('connect.unmeasuredSink', { group: c.groupId || '-' })
      return t('connect.unmeasuredSource')
    }
    return { store, data, error, q, loading, list, maxTasks, target, draft, pending, actionForm, busy, actionError, notice, startCreate, upload, confirmAction, applyAction, measureNote, connectStateClass, shortClass, fmtBytes, fmtNum, fmtRate, link, t }
  },
  template: `
  <div>
    <p v-if="loading && !data" class="empty">{{ t('common.loading') }}</p>
    <p v-if="data && !data.url && !error" class="banner info">{{ t('connect.unset') }} <a href="#/settings">{{ t('connect.openSettings') }}</a></p>
    <p v-if="error" class="banner bad">{{ error }} <a href="#/settings">{{ t('connect.openSettings') }}</a></p>
    <p v-if="data?.clusterError && !store.error" class="banner warn">{{ t('connect.clusterError', { msg: data.clusterError }) }}</p>
    <p v-if="actionError" class="banner bad" role="alert">{{ actionError }}</p>
    <p v-if="notice" class="banner info" role="status">{{ notice }}</p>
    <template v-if="data?.url && !error">
      <div class="stats">
        <div class="stat"><div class="label">{{ t('connect.version') }}</div><div class="value sm">{{ data.version || '-' }}</div></div>
        <div class="stat"><div class="label">{{ t('connect.connectors') }}</div><div class="value">{{ data.connectors.length }}</div></div>
        <div class="stat"><div class="label">{{ t('connect.tasks') }}</div><div class="value">{{ data.running }}<small> / {{ data.tasks }}</small></div></div>
        <div class="stat"><div class="label">{{ t('connect.failed') }}</div><div class="value" :class="data.failed ? 'bad' : ''">{{ data.failed }}</div></div>
        <div class="stat"><div class="label">{{ t('connect.rate') }}</div><div class="value sm">{{ fmtRate(data.msgRate) }} msg/s</div></div>
        <div class="stat"><div class="label">{{ t('connect.lag') }}</div><div class="value" :class="data.lag ? 'warn' : ''">{{ fmtNum(data.lag) }}</div></div>
      </div>
      <p class="muted small">{{ t('connect.totalNote') }}</p>

      <section class="panel" v-if="data.workers.length">
        <h3 class="panel-title">{{ t('connect.workers') }}</h3>
        <p class="legend"><span class="swatch run"></span>{{ t('connect.legendRun') }} <span class="swatch fail"></span>{{ t('connect.legendFail') }} <span class="swatch pause"></span>{{ t('connect.legendPause') }}</p>
        <div v-for="w in data.workers" :key="w.id" class="worker">
          <span class="mono clip">{{ w.id }}</span>
          <div class="loadtrack">
            <div class="loadbar" :style="{ width: Math.max(8, 100 * w.tasks / maxTasks) + '%' }">
              <span v-if="w.running" class="run" :style="{ flex: w.running }"></span>
              <span v-if="w.failed" class="fail" :style="{ flex: w.failed }"></span>
              <span v-if="w.paused" class="pause" :style="{ flex: w.paused }"></span>
              <span v-if="w.other" class="other" :style="{ flex: w.other }"></span>
            </div>
          </div>
          <span class="small muted">{{ w.running }}/{{ w.tasks }}<template v-if="w.failed"> · {{ w.failed }} {{ t('connect.failed') }}</template></span>
        </div>
      </section>

      <section class="panel">
        <div class="toolbar">
          <input class="search" v-model="q" :placeholder="t('connect.search')">
          <button v-if="target" class="primary" :disabled="busy || !!draft" @click="startCreate">{{ t('connect.register') }}</button>
        </div>
        <form v-if="draft" ref="actionForm" class="banner info" @submit.prevent="applyAction">
          <p>{{ t('connect.target', { url: draft.url }) }}</p>
          <p class="small">{{ t('connect.jsonHint') }}</p>
          <div class="toolbar">
            <label>{{ t('connect.jsonUpload') }} <input type="file" accept=".json,application/json" :disabled="busy" @change="upload"></label>
          </div>
          <label>{{ t('connect.jsonLabel') }}
            <textarea v-model="draft.text" class="mono config-editor" :disabled="busy" rows="8" spellcheck="false" autocomplete="off" placeholder='{"name":"my-connector","config":{"connector.class":"…","tasks.max":"1"}}'></textarea>
          </label>
          <div class="toolbar">
            <button type="submit" class="primary" :disabled="busy || !draft.text.trim()">{{ t('connect.register') }}</button>
            <button type="button" :disabled="busy" @click="draft = null; actionError = ''">{{ t('manage.cancel') }}</button>
          </div>
        </form>
        <form v-else-if="pending" ref="actionForm" class="banner warn" @submit.prevent="applyAction">
          <p>{{ t('connect.target', { url: pending.url }) }}</p>
          <p>{{ t(pending.kind === 'restart' ? 'connect.confirmRestart' : 'connect.confirmDelete', { name: pending.name }) }}</p>
          <div class="toolbar">
            <button type="submit" class="primary" :disabled="busy">{{ t('connect.' + pending.kind) }}</button>
            <button type="button" :disabled="busy" @click="pending = null; actionError = ''">{{ t('manage.cancel') }}</button>
          </div>
        </form>
        <article v-for="c in list" :key="c.name" class="connector">
          <div class="conn-head">
            <b>{{ c.name }}</b>
            <span class="badge info">{{ c.type || '-' }}</span>
            <span class="badge" :class="connectStateClass(c.state)">{{ c.state || '-' }}</span>
            <span class="muted small">{{ shortClass(c.class) }}</span>
            <span class="spacer"></span>
            <b>{{ fmtRate(c.msgRate) }} msg/s</b>
            <span>{{ fmtBytes(c.byteRate) }}/s</span>
            <span :class="c.lag ? 'warn' : 'muted'">lag {{ fmtNum(c.lag) }}</span>
          </div>

          <div v-if="target" class="taskline">
            <button :disabled="busy || !!draft" :aria-label="t('connect.restart') + ': ' + c.name" @click="confirmAction('restart', c.name)">{{ t('connect.restart') }}</button>
            <button :disabled="busy || !!draft" :aria-label="t('connect.delete') + ': ' + c.name" @click="confirmAction('delete', c.name)">{{ t('connect.delete') }}</button>
          </div>

          <div class="flow" v-if="c.type === 'sink'">
            <template v-if="c.topics.length">
              <a v-for="tp in c.topics" :key="tp.name" class="box" :href="link('topics', tp.name)">
                <b>{{ tp.name }}</b>
                <small>{{ fmtRate(tp.msgRate) }} msg/s · lag {{ fmtNum(tp.lag) }}</small>
              </a>
            </template>
            <span v-else class="box">{{ c.topicHint || t('connect.noTopics') }}</span>
            <span class="arrow">→</span>
            <div class="box me">
              <b>{{ shortClass(c.class) }}</b>
              <small>{{ t('connect.tasksN', { n: c.tasks.length, max: c.tasksMax || '-' }) }}</small>
            </div>
          </div>
          <div class="flow" v-else>
            <div class="box me">
              <b>{{ shortClass(c.class) }}</b>
              <small>{{ t('connect.tasksN', { n: c.tasks.length, max: c.tasksMax || '-' }) }}</small>
            </div>
            <span class="arrow">→</span>
            <template v-if="c.topics.length">
              <a v-for="tp in c.topics" :key="tp.name" class="box" :href="link('topics', tp.name)">
                <b>{{ tp.name }}</b>
                <small>{{ fmtRate(tp.msgRate) }} msg/s</small>
              </a>
            </template>
            <span v-else class="box">{{ c.topicHint || t('connect.noTopics') }}</span>
          </div>
          <p v-if="c.topicsMore" class="muted small">{{ t('connect.moreTopics', { n: c.topicsMore }) }}</p>
          <p v-if="c.topicFrom" class="muted small">{{ t('connect.topicFrom.' + c.topicFrom) }}</p>

          <div class="taskline">
            <span v-for="task in c.tasks" :key="task.id" class="badge" :class="connectStateClass(task.state)" :title="task.workerId">
              #{{ task.id }} {{ task.state }} · {{ task.workerId || '-' }}
            </span>
            <span v-if="!c.tasks.length" class="muted small">{{ t('connect.noTasks') }}</span>
          </div>
          <pre v-for="task in c.tasks.filter((task) => task.trace)" :key="'tr' + task.id">#{{ task.id }} {{ task.trace }}</pre>
          <p v-if="c.trace" class="trace-note">{{ c.trace }}</p>
          <p v-if="measureNote(c)" class="muted small">{{ measureNote(c) }}</p>
          <details v-if="c.config.length">
            <summary>{{ t('connect.config') }}</summary>
            <table>
              <tbody>
                <tr v-for="cfg in c.config" :key="cfg.key">
                  <td class="mono">{{ cfg.key }}</td>
                  <td class="mono">{{ cfg.sensitive ? t('connect.hidden') : (cfg.value || '-') }}</td>
                </tr>
              </tbody>
            </table>
          </details>
        </article>
        <p v-if="!list.length" class="empty">{{ q ? t('connect.noMatch') : t('connect.empty') }}</p>
      </section>
    </template>
  </div>`,
}
