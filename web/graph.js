// 토폴로지 축소 로직 (Vue/mermaid 없음). 300+ 토픽에서도 mermaid 가 살아남게
// 접두사 묶기 + 노드 수 상한을 먼저 적용한다.

/** mermaid 한 번에 그릴 노드 수 상한. 이보다 크면 브라우저가 수 초~수십 초 멈춘다. */
export const NODE_CAP = 200

/** 0(전체)이거나 음수면 CAP, 그 외는 CAP 이하로 자른다. */
export function nodeLimit(n) {
  if (!n || n < 0) return NODE_CAP
  return Math.min(n, NODE_CAP)
}

/**
 * 이름의 앞 depth 단계 접두사 ("orders.v1.created", 1 → "orders.").
 * 구분자는 . - _ 이며, 단계가 모자라거나 이름 전체와 같으면 null (묶지 않음).
 */
export function prefixOf(name, depth) {
  if (!depth) return null
  const m = name.match(new RegExp(`^(?:[^.\\-_]+[.\\-_]){${depth}}`))
  return m && m[0].length < name.length ? m[0] : null
}

/** 접두사 depth 로 토픽/그룹을 노드로 묶고, 노드 간 링크의 처리량·lag 를 합산한다. 구성원이 1개뿐인 접두사는 묶지 않는다. */
export function aggregate(topics, pairs, depth) {
  const count = (names) => names.reduce((m, n) => (n && m.set(n, (m.get(n) || 0) + 1), m), new Map())
  const tnodes = new Map(), gnodes = new Map(), edges = new Map(), tkey = new Map(), gkey = new Map()

  const tc = count(topics.map((x) => prefixOf(x.name, depth)))
  for (const x of topics) {
    const p = prefixOf(x.name, depth)
    const agg = !!p && tc.get(p) > 1
    const key = agg ? 'P' + p : 'T' + x.name
    tkey.set(x.name, key)
    if (!tnodes.has(key)) tnodes.set(key, { key, kind: 'topic', agg, name: agg ? p : x.name, members: [], size: 0, inRate: 0, partitions: 0, lag: 0, bytes: 0 })
    const n = tnodes.get(key)
    n.members.push(x); n.size += x.size; n.inRate += x.inRate; n.partitions += x.partitions.length
  }

  const groups = [...new Map(pairs.map((p) => [p.group.id, p.group])).values()]
  const gc = count(groups.map((g) => prefixOf(g.id, depth)))
  for (const g of groups) {
    const p = prefixOf(g.id, depth)
    const agg = !!p && gc.get(p) > 1
    const key = agg ? 'Q' + p : 'G' + g.id
    gkey.set(g.id, key)
    if (!gnodes.has(key)) gnodes.set(key, { key, kind: 'group', agg, name: agg ? p : g.id, members: [], memberCount: 0, lag: 0, bytes: 0 })
    const n = gnodes.get(key)
    n.members.push(g); n.memberCount += g.members.length
  }

  for (const { group, topic } of pairs) {
    const from = tkey.get(topic.topic), to = gkey.get(group.id)
    if (!from || !to) continue
    const k = from + '|' + to
    if (!edges.has(k)) edges.set(k, { from, to, rate: 0, bytes: 0, lag: 0 })
    const e = edges.get(k)
    e.rate += topic.rate; e.bytes += topic.byteRate; e.lag += topic.lag
    for (const n of [tnodes.get(from), gnodes.get(to)]) { n.lag += topic.lag; n.bytes += topic.byteRate }
  }
  return { tnodes, gnodes, edges }
}

/** 순위 기준별 정렬 함수 (동률은 이름순) */
const RANK = {
  lag: (n) => n.lag,
  bytes: (n) => n.bytes,
  produce: (n) => (n.kind === 'topic' ? n.inRate : n.bytes),
  name: () => 0,
}
const ranked = (nodes, by) => [...nodes].sort((a, b) => RANK[by](b) - RANK[by](a) || a.name.localeCompare(b.name))

/** 노드 수가 한도 안에 들어오는 가장 얕은 접두사 단계 (0~2). */
export function autoDepth(topics, pairs, limit) {
  let depth = 0, agg
  for (depth = 0; depth <= 2; depth++) {
    agg = aggregate(topics, pairs, depth)
    if (agg.tnodes.size <= limit && agg.gnodes.size <= limit) break
  }
  return { depth: Math.min(depth, 2), agg: agg || aggregate(topics, pairs, 0) }
}

/** 상위 N 토픽 노드와 그에 연결된 상위 N 그룹 노드, 둘 다 보이는 링크. */
export function pickShown(agg, sort, limit) {
  const shownT = ranked(agg.tnodes.values(), sort).slice(0, limit)
  const tset = new Set(shownT.map((n) => n.key))
  const linked = new Set([...agg.edges.values()].filter((e) => tset.has(e.from)).map((e) => e.to))
  const shownG = ranked([...agg.gnodes.values()].filter((n) => linked.has(n.key)), sort).slice(0, limit)
  const gset = new Set(shownG.map((n) => n.key))
  const edges = [...agg.edges.values()].filter((e) => tset.has(e.from) && gset.has(e.to))
  return { shownT, shownG, edges, totalT: agg.tnodes.size, totalG: linked.size }
}

/**
 * 필터된 토픽/쌍에 묶기+상한을 적용한 결과.
 * 반환 depth, shownT/G, edges, totalT/G.
 */
export function planGraph(topics, pairs, opt) {
  const limit = nodeLimit(opt.limit)
  let depth = opt.groupBy, agg
  if (depth === 'auto') {
    ({ depth, agg } = autoDepth(topics, pairs, limit))
  } else {
    agg = aggregate(topics, pairs, depth || 0)
  }
  return { depth, limit, ...pickShown(agg, opt.sort || 'lag', limit) }
}
