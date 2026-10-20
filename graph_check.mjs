// graph.js 자기 검사. `.build` 의 node 또는 시스템 node 로 실행.
import { prefixOf, planGraph, NODE_CAP } from './web/graph.js'

function assert(cond, msg) {
  if (!cond) throw new Error(msg)
}

assert(prefixOf('orders.v1.created', 1) === 'orders.', 'depth 1')
assert(prefixOf('orders.v1.created', 2) === 'orders.v1.', 'depth 2')
assert(prefixOf('orders', 1) === null, '묶을 단계 없음')
assert(prefixOf('plain', 0) === null, 'depth 0')

const topics = []
const pairs = []
for (let i = 0; i < 200; i++) {
  const name = 'orders.evt.' + i
  topics.push({ name, size: 100, inRate: 1, partitions: [0, 1] })
  pairs.push({ group: { id: 'svc.orders.' + (i % 30), members: [{}], state: 'Stable' }, topic: { topic: name, rate: 1, byteRate: 10, lag: 200 - i } })
}
for (let i = 0; i < 120; i++) {
  const name = 'pay.evt.' + i
  topics.push({ name, size: 50, inRate: 2, partitions: [0] })
  pairs.push({ group: { id: 'svc.pay.' + (i % 10), members: [{}], state: 'Stable' }, topic: { topic: name, rate: 2, byteRate: 20, lag: i } })
}
assert(topics.length === 320, '320 topics')

const auto = planGraph(topics, pairs, { groupBy: 'auto', limit: 50, sort: 'lag' })
assert(auto.depth >= 1, 'auto 는 접두사로 묶어야 함, depth=' + auto.depth)
assert(auto.shownT.length <= 50 && auto.shownG.length <= 50, '한도 50 초과')
assert(auto.totalT < 320, '묶인 뒤 토픽 노드 수가 원본보다 작아야 함: ' + auto.totalT)

const none = planGraph(topics, pairs, { groupBy: 0, limit: 50, sort: 'lag' })
assert(none.shownT.length === 50 && none.totalT === 320, '묶지 않으면 320 중 50')
assert(none.shownT[0].lag >= none.shownT[49].lag, 'lag 내림차순')

const all = planGraph(topics, pairs, { groupBy: 0, limit: 0, sort: 'name' })
assert(all.shownT.length <= NODE_CAP, '"전체"도 CAP 을 넘으면 안 됨')

console.log('graph_check ok', { autoDepth: auto.depth, autoNodes: auto.totalT, shown: auto.shownT.length })
