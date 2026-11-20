package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

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
func (s *session) handleCluster(w http.ResponseWriter, r *http.Request) {
	snap, err := s.Snapshot(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleTopicConfig 는 토픽 설정 목록을 돌려준다.
func (s *session) handleTopicConfig(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rcs, err := s.Admin().DescribeTopicConfigs(ctx, r.PathValue("name"))
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
	Partition int32       `json:"partition"`
	Offset    int64       `json:"offset"`
	Timestamp int64       `json:"timestamp"`
	Key       *string     `json:"key"`
	Value     *string     `json:"value"`
	Encoding  string      `json:"encoding"` // text | base64
	Size      int         `json:"size"`
	Truncated bool        `json:"truncated"`
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
func (s *session) handleMessages(w http.ResponseWriter, r *http.Request) {
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
	adm := s.Admin()
	so, err := adm.ListStartOffsets(ctx, topic)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	eo, err := adm.ListEndOffsets(ctx, topic)
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
	cl, err := kgo.NewClient(append(append([]kgo.Opt{}, s.ConsumerOpts()...), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: at}))...)
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
