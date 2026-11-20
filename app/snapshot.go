package main

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
)

const snapshotTTL = 3 * time.Second

type snapshotProvider interface {
	Snapshot(context.Context) (*Snapshot, error)
}

type snapshotCollector interface {
	Collect(context.Context) (*Snapshot, map[string]int64, error)
}

type snapshotService struct {
	collector snapshotCollector
	now       func() time.Time
	mu        sync.Mutex
	snap      *Snapshot
	counters  map[string]int64
}

func newSnapshotService(collector snapshotCollector, now func() time.Time) *snapshotService {
	if now == nil {
		now = time.Now
	}
	return &snapshotService{collector: collector, now: now}
}

// replaceCollector 는 수집기만 교체한다. TTL 캐시·처리량 카운터는 유지한다.
func (s *snapshotService) replaceCollector(c snapshotCollector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector = c
}

func (s *snapshotService) Snapshot(ctx context.Context) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil && s.now().Sub(time.UnixMilli(s.snap.Time)) < snapshotTTL {
		return s.snap, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	snap, counters, err := s.collector.Collect(ctx)
	if err != nil {
		return nil, err
	}
	if s.snap != nil {
		dt := float64(snap.Time-s.snap.Time) / 1000
		r := rates(s.counters, counters, dt)
		for i := range snap.Topics {
			snap.Topics[i].InRate = r["t\x00"+snap.Topics[i].Name]
		}
		avg := map[string]float64{}
		for _, topic := range snap.Topics {
			avg[topic.Name] = topic.AvgMsgBytes
		}
		for i := range snap.Groups {
			for j := range snap.Groups[i].Topics {
				groupTopic := &snap.Groups[i].Topics[j]
				groupTopic.Rate = r["g\x00"+snap.Groups[i].ID+"\x00"+groupTopic.Topic]
				groupTopic.ByteRate = groupTopic.Rate * avg[groupTopic.Topic]
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

type kafkaSnapshotCollector struct {
	adm *kadm.Client
	now func() time.Time
}

// collect 는 메타데이터/오프셋/로그디렉터리/그룹 랙을 모아 스냅샷과 rate 용 누적 카운터를 만든다.
// 메타데이터 외 단계의 실패는 Errors 에 담고 가능한 만큼 채운다.
func (c kafkaSnapshotCollector) Collect(ctx context.Context) (*Snapshot, map[string]int64, error) {
	meta, err := c.adm.Metadata(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := c.now
	if now == nil {
		now = time.Now
	}
	snap := &Snapshot{Time: now().UnixMilli(), ClusterID: meta.Cluster, Controller: meta.Controller, Errors: []string{}}
	counters := map[string]int64{}
	fail := func(stage string, err error) {
		if err != nil {
			snap.Errors = append(snap.Errors, stage+": "+err.Error())
		}
	}

	names := meta.Topics.Names()
	var starts, ends kadm.ListedOffsets
	if len(names) > 0 {
		starts, err = c.adm.ListStartOffsets(ctx, names...)
		fail("start offsets", err)
		ends, err = c.adm.ListEndOffsets(ctx, names...)
		fail("end offsets", err)
	}

	// 파티션 크기는 복제본 중 최대값(=논리 크기), 브로커 크기는 해당 브로커의 모든 복제본 합
	partSize := map[string]map[int32]int64{}
	brokerSize := map[int32]int64{}
	dirs, err := c.adm.DescribeAllLogDirs(ctx, nil)
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
	if vs, err := c.adm.ApiVersions(ctx); err == nil {
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
	listed, err := c.adm.ListGroups(ctx)
	fail("list groups", err)
	if ids := listed.Groups(); len(ids) > 0 {
		lags, err := c.adm.Lag(ctx, ids...)
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
