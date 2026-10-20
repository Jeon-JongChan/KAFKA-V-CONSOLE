// 공용 상태(store), API 호출, 표시용 포맷터, 클러스터 상태 판정.
import { i18n } from './i18n.js'
const { reactive } = Vue

/** 전역 상태: 최신 클러스터 스냅샷과 폴링 설정 */
export const store = reactive({ snap: null, error: '', loading: false, interval: 5 })

/** JSON API 호출. 실패 시 서버가 준 error 메시지로 예외를 던진다. */
export async function api(path) {
  const r = await fetch(path)
  const j = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(j.error || r.statusText)
  return j
}

/** 클러스터 스냅샷을 새로 받아 store 에 반영한다. */
export async function refresh() {
  store.loading = true
  try {
    store.snap = await api('/api/cluster')
    store.error = ''
  } catch (e) {
    store.error = e.message
  } finally {
    store.loading = false
  }
}

/** 바이트 수를 KB/MB/GB 단위 문자열로 */
export function fmtBytes(n) {
  if (!n || n < 1) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return (n / 1024 ** i).toFixed(i ? 1 : 0) + ' ' + u[i]
}

/** 큰 수를 1.2K / 3.4M 처럼 축약 */
export function fmtNum(n) {
  if (n == null) return '-'
  return Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

/** 초당 처리량. 0.05 미만은 0 으로 본다. */
export function fmtRate(r) {
  if (!r || r < 0.05) return '0'
  return r < 10 ? r.toFixed(1) : fmtNum(Math.round(r))
}

/** epoch ms → 현재 UI 언어 형식의 시각 */
export const fmtTime = (ms) => new Date(ms).toLocaleString(i18n.lang)

/** 그룹 상태 → 색상 클래스 */
export function stateClass(s) {
  if (s === 'Stable') return 'ok'
  if (s === 'Dead') return 'bad'
  if (s === 'Empty') return 'muted'
  return 'warn' // PreparingRebalance, CompletingRebalance 등
}

/** 그룹의 토픽별 처리량 합계 */
export const groupRate = (g) => g.topics.reduce((a, t) => a + t.rate, 0)
export const groupByteRate = (g) => g.topics.reduce((a, t) => a + t.byteRate, 0)

/** 여러 상태 중 가장 나쁜 색상 클래스 (bad > warn > ok > muted) */
export function worstState(states) {
  const c = states.map(stateClass)
  return ['bad', 'warn', 'ok'].find((x) => c.includes(x)) || 'muted'
}

/** 해시 경로 링크 생성 (토픽/그룹 이름은 인코딩) */
export const link = (...parts) => '#/' + parts.map(encodeURIComponent).join('/')

/** 내부 토픽 여부 (__consumer_offsets, _schemas 등) */
export const isInternal = (t) => t.internal || t.name.startsWith('_')

/** 검색어 매처: 정규식으로 해석하고, 잘못된 정규식이면 부분 문자열 검색 */
export function matcher(q) {
  if (!q) return () => true
  try {
    const re = new RegExp(q, 'i')
    return (s) => re.test(s)
  } catch {
    const l = q.toLowerCase()
    return (s) => s.toLowerCase().includes(l)
  }
}

/**
 * 클러스터 건강도 요약.
 * 복제본으로 참조되지만 메타데이터에 없는 브로커 ID 를 오프라인 브로커로 본다.
 */
export function health(snap) {
  const known = new Set(snap.brokers.map((b) => b.id))
  const down = new Set()
  let urp = 0, offline = 0, partitions = 0, replicas = 0
  for (const t of snap.topics) {
    urp += t.urp
    offline += t.offline
    partitions += t.partitions.length
    for (const p of t.partitions) {
      replicas += p.replicas.length
      for (const r of p.replicas) if (!known.has(r)) down.add(r)
    }
  }
  const status = offline || down.size ? 'Unhealthy' : urp ? 'Degraded' : 'Running'
  return { status, cls: { Running: 'ok', Degraded: 'warn', Unhealthy: 'bad' }[status], offlineBrokers: [...down].sort((a, b) => a - b), urp, offline, partitions, replicas }
}
