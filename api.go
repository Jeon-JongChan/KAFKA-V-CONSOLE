package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// snapshotTTL 동안은 같은 스냅샷을 재사용해 여러 브라우저가 붙어도 Kafka 부하를 일정하게 유지한다.
const snapshotTTL = 3 * time.Second

// server 는 HTTP 핸들러가 공유하는 상태다.
type server struct {
	opts []kgo.Opt
	adm  *kadm.Client

	mu       sync.Mutex
	snap     *Snapshot
	counters map[string]int64 // 직전 스냅샷의 누적 오프셋 합계 (rate 계산용)
}

// Snapshot 은 UI 가 한 번에 받아가는 클러스터 전체 상태다.
type Snapshot struct {
	Time       int64    `json:"time"`
	ClusterID  string   `json:"clusterId"`
	Controller int32    `json:"controller"`
	Brokers    []Broker `json:"brokers"`
	Topics     []Topic  `json:"topics"`
	Groups     []Group  `json:"groups"`
	Errors     []string `json:"errors"`
}

// Broker 는 브로커 한 대의 요약이다.
type Broker struct {
	ID       int32  `json:"id"`
	Host     string `json:"host"`
	Port     int32  `json:"port"`
	Rack     string `json:"rack"`
	Version  string `json:"version"`
	Size     int64  `json:"size"`
	Leaders  int    `json:"leaders"`
	Replicas int    `json:"replicas"`
}

// Topic 은 토픽 요약과 파티션 상세다.
type Topic struct {
	Name        string      `json:"name"`
	Internal    bool        `json:"internal"`
	Partitions  []Partition `json:"partitions"`
	Size        int64       `json:"size"`
	Messages    int64       `json:"messages"`
	InRate      float64     `json:"inRate"` // 초당 유입 메시지 수
	AvgMsgBytes float64     `json:"avgMsgBytes"`
	URP         int         `json:"urp"` // under-replicated 파티션 수
	Offline     int         `json:"offline"`
}

// Partition 은 파티션 한 개의 상태다.
type Partition struct {
	ID       int32   `json:"id"`
	Leader   int32   `json:"leader"`
	Replicas []int32 `json:"replicas"`
	ISR      []int32 `json:"isr"`
	Start    int64   `json:"start"`
	End      int64   `json:"end"`
	Size     int64   `json:"size"`
}

// Group 은 컨슈머 그룹 요약이다.
type Group struct {
	ID          string       `json:"id"`
	State       string       `json:"state"`
	Protocol    string       `json:"protocol"`
	Coordinator int32        `json:"coordinator"`
	Members     []Member     `json:"members"`
	Topics      []GroupTopic `json:"topics"`
	Lag         int64        `json:"lag"`
	Error       string       `json:"error,omitempty"`
}

// Member 는 그룹 멤버(실제 컨슈머 인스턴스)다.
type Member struct {
	ID       string             `json:"id"`
	ClientID string             `json:"clientId"`
	Host     string             `json:"host"`
	Assigned map[string][]int32 `json:"assigned"`
}

// GroupTopic 은 그룹이 소비하는 토픽 하나에 대한 처리량/랙이다.
type GroupTopic struct {
	Topic      string           `json:"topic"`
	Lag        int64            `json:"lag"`
	Committed  int64            `json:"committed"` // 커밋 오프셋 합계 (누적 소비 메시지 수 근사)
	Rate       float64          `json:"rate"`      // 초당 소비 메시지 수
	ByteRate   float64          `json:"byteRate"`  // 초당 소비 바이트 추정치 (rate x 토픽 평균 메시지 크기)
	Partitions []GroupPartition `json:"partitions"`
}

// GroupPartition 은 그룹-파티션 단위 커밋/랙이다.
type GroupPartition struct {
	Partition int32  `json:"partition"`
	Commit    int64  `json:"commit"`
	End       int64  `json:"end"`
	Lag       int64  `json:"lag"`
	Member    string `json:"member"` // 담당 컨슈머 clientId (미할당이면 빈 값)
	Host      string `json:"host"`
}

// writeJSON 은 값을 JSON 으로 응답한다.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 는 오류를 {"error": "..."} 형태로 응답한다.
func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// handleCluster 는 캐시된(또는 새로 수집한) 클러스터 스냅샷을 돌려준다.
func (s *server) handleCluster(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// snapshot 은 TTL 이 지났을 때만 Kafka 에서 다시 수집한다. 락을 잡은 채 수집해 동시 요청을 한 번으로 합친다.
func (s *server) snapshot(ctx context.Context) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil && time.Since(time.UnixMilli(s.snap.Time)) < snapshotTTL {
		return s.snap, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	snap, counters, err := s.collect(ctx)
	if err != nil {
		return nil, err
	}
	// 직전 스냅샷과의 누적값 차이로 초당 처리량을 계산한다
	if s.snap != nil {
		dt := float64(snap.Time-s.snap.Time) / 1000
		r := rates(s.counters, counters, dt)
		for i := range snap.Topics {
			snap.Topics[i].InRate = r["t\x00"+snap.Topics[i].Name]
		}
		avg := map[string]float64{}
		for _, t := range snap.Topics {
			avg[t.Name] = t.AvgMsgBytes
		}
		for i := range snap.Groups {
			for j := range snap.Groups[i].Topics {
				gt := &snap.Groups[i].Topics[j]
				gt.Rate = r["g\x00"+snap.Groups[i].ID+"\x00"+gt.Topic]
				gt.ByteRate = gt.Rate * avg[gt.Topic]
			}
		}
	}
	s.snap, s.counters = snap, counters
	return snap, nil
}

// rates 는 두 시점의 누적 카운터로 초당 증가율을 구한다. 첫 관측이거나 카운터가 줄었으면(리셋) 0 이다.
func rates(prev, cur map[string]int64, dt float64) map[string]float64 {
	out := make(map[string]float64, len(cur))
	if dt <= 0 {
		return out
	}
	for k, v := range cur {
		if p, ok := prev[k]; ok && v >= p {
			out[k] = float64(v-p) / dt
		}
	}
	return out
}

// collect 는 메타데이터/오프셋/로그디렉터리/그룹 랙을 모아 스냅샷과 rate 용 누적 카운터를 만든다.
// 메타데이터 외 단계의 실패는 Errors 에 담고 가능한 만큼 채운다.
func (s *server) collect(ctx context.Context) (*Snapshot, map[string]int64, error) {
	meta, err := s.adm.Metadata(ctx)
	if err != nil {
		return nil, nil, err
	}
	snap := &Snapshot{Time: time.Now().UnixMilli(), ClusterID: meta.Cluster, Controller: meta.Controller, Errors: []string{}}
	counters := map[string]int64{}
	fail := func(stage string, err error) {
		if err != nil {
			snap.Errors = append(snap.Errors, stage+": "+err.Error())
		}
	}

	names := meta.Topics.Names()
	var starts, ends kadm.ListedOffsets
	if len(names) > 0 {
		starts, err = s.adm.ListStartOffsets(ctx, names...)
		fail("start offsets", err)
		ends, err = s.adm.ListEndOffsets(ctx, names...)
		fail("end offsets", err)
	}

	// 파티션 크기는 복제본 중 최대값(=논리 크기), 브로커 크기는 해당 브로커의 모든 복제본 합
	partSize := map[string]map[int32]int64{}
	brokerSize := map[int32]int64{}
	dirs, err := s.adm.DescribeAllLogDirs(ctx, nil)
	fail("log dirs", err)
	for broker, bd := range dirs {
		for _, d := range bd {
			for topic, parts := range d.Topics {
				if partSize[topic] == nil {
					partSize[topic] = map[int32]int64{}
				}
				for p, dp := range parts {
					brokerSize[broker] += dp.Size
					partSize[topic][p] = max(partSize[topic][p], dp.Size)
				}
			}
		}
	}

	versions := map[int32]string{}
	if vs, err := s.adm.ApiVersions(ctx); err == nil {
		for _, v := range vs.Sorted() {
			if v.Err == nil {
				versions[v.NodeID] = v.VersionGuess()
			}
		}
	}

	leaders, replicas := map[int32]int{}, map[int32]int{}
	for _, td := range meta.Topics.Sorted() {
		t := Topic{Name: td.Topic, Internal: td.IsInternal, Partitions: []Partition{}}
		if td.Err != nil {
			fail("topic "+td.Topic, td.Err)
		}
		var total int64
		for _, pd := range td.Partitions.Sorted() {
			p := Partition{ID: pd.Partition, Leader: pd.Leader, Replicas: pd.Replicas, ISR: pd.ISR, Size: partSize[td.Topic][pd.Partition]}
			if o, ok := starts.Lookup(td.Topic, pd.Partition); ok && o.Err == nil {
				p.Start = o.Offset
			}
			if o, ok := ends.Lookup(td.Topic, pd.Partition); ok && o.Err == nil {
				p.End = o.Offset
			}
			total += p.End
			t.Messages += p.End - p.Start
			t.Size += p.Size
			if pd.Leader < 0 {
				t.Offline++
			} else {
				leaders[pd.Leader]++
			}
			if len(pd.ISR) < len(pd.Replicas) {
				t.URP++
			}
			for _, b := range pd.Replicas {
				replicas[b]++
			}
			t.Partitions = append(t.Partitions, p)
		}
		// ponytail: 압축(compaction)/트랜잭션 마커로 오프셋 수와 실제 메시지 수가 달라 평균 크기는 근사치
		if t.Messages > 0 {
			t.AvgMsgBytes = float64(t.Size) / float64(t.Messages)
		}
		counters["t\x00"+t.Name] = total
		snap.Topics = append(snap.Topics, t)
	}

	for _, b := range meta.Brokers {
		br := Broker{ID: b.NodeID, Host: b.Host, Port: b.Port, Version: versions[b.NodeID], Size: brokerSize[b.NodeID], Leaders: leaders[b.NodeID], Replicas: replicas[b.NodeID]}
		if b.Rack != nil {
			br.Rack = *b.Rack
		}
		snap.Brokers = append(snap.Brokers, br)
	}
	sort.Slice(snap.Brokers, func(i, j int) bool { return snap.Brokers[i].ID < snap.Brokers[j].ID })

	snap.Groups = []Group{}
	listed, err := s.adm.ListGroups(ctx)
	fail("list groups", err)
	if ids := listed.Groups(); len(ids) > 0 {
		lags, err := s.adm.Lag(ctx, ids...)
		fail("group lag", err)
		for _, gl := range lags.Sorted() {
			snap.Groups = append(snap.Groups, toGroup(gl, counters))
		}
	}
	return snap, counters, nil
}

// toGroup 은 kadm 의 그룹 랙 결과를 UI 용 구조로 바꾸고, 그룹-토픽별 커밋 합계를 counters 에 기록한다.
func toGroup(gl kadm.DescribedGroupLag, counters map[string]int64) Group {
	g := Group{ID: gl.Group, State: gl.State, Protocol: gl.Protocol, Coordinator: gl.Coordinator.NodeID, Members: []Member{}, Topics: []GroupTopic{}}
	if err := gl.Error(); err != nil {
		g.Error = err.Error()
	}
	for _, m := range gl.Members {
		mem := Member{ID: m.MemberID, ClientID: m.ClientID, Host: m.ClientHost, Assigned: map[string][]int32{}}
		if a, ok := m.Assigned.AsConsumer(); ok {
			for _, t := range a.Topics {
				mem.Assigned[t.Topic] = t.Partitions
			}
		}
		g.Members = append(g.Members, mem)
	}
	topics := make([]string, 0, len(gl.Lag))
	for t := range gl.Lag {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	for _, t := range topics {
		gt := GroupTopic{Topic: t, Partitions: []GroupPartition{}}
		for _, l := range gl.Lag.Sorted() {
			if l.Topic != t {
				continue
			}
			gp := GroupPartition{Partition: l.Partition, Commit: l.Commit.At, End: l.End.Offset, Lag: max(l.Lag, 0)}
			if l.Member != nil {
				gp.Member, gp.Host = l.Member.ClientID, l.Member.ClientHost
			}
			// 커밋이 없는 파티션(-1)은 누적 소비량 계산에서 제외
			if l.Commit.At >= 0 {
				gt.Committed += l.Commit.At
			}
			gt.Lag += gp.Lag
			gt.Partitions = append(gt.Partitions, gp)
		}
		g.Lag += gt.Lag
		counters["g\x00"+g.ID+"\x00"+t] = gt.Committed
		g.Topics = append(g.Topics, gt)
	}
	return g
}

// handleTopicConfig 는 토픽 설정 목록을 돌려준다.
func (s *server) handleTopicConfig(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rcs, err := s.adm.DescribeTopicConfigs(ctx, r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	type cfg struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		Source    string `json:"source"`
		Sensitive bool   `json:"sensitive"`
	}
	out := []cfg{}
	for _, rc := range rcs {
		if rc.Err != nil {
			writeErr(w, http.StatusBadGateway, rc.Err)
			return
		}
		for _, c := range rc.Configs {
			out = append(out, cfg{Key: c.Key, Value: c.MaybeValue(), Source: c.Source.String(), Sensitive: c.Sensitive})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	writeJSON(w, http.StatusOK, out)
}

// Message 는 메시지 뷰어에 보여줄 레코드 하나다.
type Message struct {
	Partition int32     `json:"partition"`
	Offset    int64     `json:"offset"`
	Timestamp int64     `json:"timestamp"`
	Key       *string   `json:"key"`
	Value     *string   `json:"value"`
	Encoding  string    `json:"encoding"` // text | base64
	Size      int       `json:"size"`
	Truncated bool      `json:"truncated"`
	Headers   [][2]string `json:"headers"`
}

// maxValueBytes 를 넘는 값은 잘라서 보낸다 (브라우저 보호).
const maxValueBytes = 64 << 10

// readPlan 은 파티션별 [시작 오프셋, 읽을 개수] 를 계산한다.
// partition<0 이면 데이터가 있는 모든 파티션에 limit 을 나눠 최신 메시지를 읽는다.
// offset>=0 이면 그 오프셋부터(단일 파티션 지정 시) 읽고, 아니면 끝에서 거꾸로 센 위치부터 읽는다.
func readPlan(starts, ends map[int32]int64, partition int32, offset int64, limit int) map[int32][2]int64 {
	plan := map[int32][2]int64{}
	var parts []int32
	for p, end := range ends {
		if (partition < 0 || p == partition) && end > starts[p] {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return plan
	}
	// ponytail: 전체 파티션 조회 시 파티션별 균등 분배라 트래픽이 한 파티션에 몰리면 전역 최신 N개와 다를 수 있음
	per := int64((limit + len(parts) - 1) / len(parts))
	for _, p := range parts {
		start, end := starts[p], ends[p]
		from := end - per
		if offset >= 0 && partition >= 0 {
			from = offset
		}
		from = max(from, start)
		if from >= end {
			continue
		}
		plan[p] = [2]int64{from, min(per, end-from)}
	}
	return plan
}

// handleMessages 는 토픽의 메시지를 일회용 컨슈머로 읽어 돌려준다 (그룹 미사용 → 오프셋 커밋 없음).
func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("name")
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	limit = min(max(limit, 1), 500)
	partition := int32(-1)
	if v, err := strconv.Atoi(q.Get("partition")); err == nil {
		partition = int32(v)
	}
	offset := int64(-1)
	if v, err := strconv.ParseInt(q.Get("offset"), 10, 64); err == nil {
		offset = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	so, err := s.adm.ListStartOffsets(ctx, topic)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	eo, err := s.adm.ListEndOffsets(ctx, topic)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	starts, ends := map[int32]int64{}, map[int32]int64{}
	so.Each(func(o kadm.ListedOffset) { starts[o.Partition] = o.Offset })
	eo.Each(func(o kadm.ListedOffset) { ends[o.Partition] = o.Offset })

	plan := readPlan(starts, ends, partition, offset, limit)
	msgs := []Message{}
	if len(plan) == 0 {
		writeJSON(w, http.StatusOK, msgs)
		return
	}
	at := map[int32]kgo.Offset{}
	remain := map[int32]int64{}
	for p, pl := range plan {
		at[p] = kgo.NewOffset().At(pl[0])
		remain[p] = pl[1]
	}
	cl, err := kgo.NewClient(append(append([]kgo.Opt{}, s.opts...), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: at}))...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer cl.Close()

	for len(remain) > 0 && ctx.Err() == nil {
		// 압축/트랜잭션 마커로 기대 개수만큼 레코드가 없을 수 있으므로, 한동안 새 레코드가 없으면 종료
		// 레코드 없는 poll 결과(브로커별 응답 도착 차이, 일시 오류)는 종료 사유가 아니다. 2초간 정말 아무것도 없을 때만 멈춘다
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
		fs := cl.PollFetches(pctx)
		idle := pctx.Err() != nil
		pcancel()
		got := 0
		fs.EachRecord(func(rec *kgo.Record) {
			if remain[rec.Partition] <= 0 {
				return
			}
			got++
			msgs = append(msgs, toMessage(rec))
			if remain[rec.Partition]--; remain[rec.Partition] == 0 || rec.Offset >= ends[rec.Partition]-1 {
				delete(remain, rec.Partition)
			}
		})
		if got == 0 && idle {
			break
		}
	}
	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].Timestamp != msgs[j].Timestamp {
			return msgs[i].Timestamp > msgs[j].Timestamp
		}
		return msgs[i].Offset > msgs[j].Offset
	})
	if offset < 0 && len(msgs) > limit {
		msgs = msgs[:limit]
	}
	writeJSON(w, http.StatusOK, msgs)
}

// toMessage 는 레코드를 표시용 구조로 바꾼다. UTF-8 이 아니면 base64 로 인코딩한다.
func toMessage(r *kgo.Record) Message {
	m := Message{Partition: r.Partition, Offset: r.Offset, Timestamp: r.Timestamp.UnixMilli(), Size: len(r.Key) + len(r.Value), Encoding: "text", Headers: [][2]string{}}
	if r.Key != nil {
		k := string(r.Key)
		m.Key = &k
	}
	if r.Value != nil {
		v := r.Value
		if len(v) > maxValueBytes {
			v, m.Truncated = v[:maxValueBytes], true
			// 잘린 지점이 멀티바이트 문자 중간이면 텍스트가 base64 로 바뀌지 않도록 최대 3바이트 되돌린다
			for i := 0; i < 3 && !utf8.Valid(v); i++ {
				v = v[:len(v)-1]
			}
		}
		var s string
		if utf8.Valid(v) {
			s = string(v)
		} else {
			s, m.Encoding = base64.StdEncoding.EncodeToString(v), "base64"
		}
		m.Value = &s
	}
	for _, h := range r.Headers {
		m.Headers = append(m.Headers, [2]string{h.Key, string(h.Value)})
	}
	return m
}
