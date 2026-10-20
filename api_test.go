package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestRenderStatus 는 텍스트 상태 출력의 경고 판정(오프라인 브로커, URP, lag)과 토폴로지 트리를 검증한다.
func TestRenderStatus(t *testing.T) {
	snap := &Snapshot{
		Time: 0, ClusterID: "c1", Controller: 1,
		Brokers: []Broker{{ID: 1, Host: "h1", Port: 9092}},
		Topics: []Topic{
			{Name: "orders", URP: 1, InRate: 12, Partitions: []Partition{{ID: 0, Leader: 1, Replicas: []int32{1, 2}, ISR: []int32{1}}}},
			{Name: "idle"},
			{Name: "__consumer_offsets", Internal: true},
		},
		Groups: []Group{{ID: "billing", State: "Stable", Topics: []GroupTopic{{Topic: "orders", Lag: 5000, Rate: 3}}}},
	}
	var b strings.Builder
	renderStatus(&b, snap, config{lagWarn: 1000})
	out := b.String()
	for _, want := range []string{"brokers=1/2", "CRIT 1 broker(s) offline [2]", "WARN 1 under-replicated", "WARN 1 group-topic(s) lag >= 1000", "`-> billing", "lag 5.0k !", "no consumers: idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("출력에 %q 없음:\n%s", want, out)
		}
	}
	if strings.Contains(out, "__consumer_offsets") {
		t.Error("내부 토픽은 기본 숨김이어야 함")
	}
}

// TestRates 는 누적 카운터 차이 → 초당 증가율 계산과 리셋/신규 키 처리를 검증한다.
func TestRates(t *testing.T) {
	r := rates(map[string]int64{"a": 100, "b": 50}, map[string]int64{"a": 160, "b": 10, "c": 5}, 2)
	if r["a"] != 30 {
		t.Fatalf("a: want 30, got %v", r["a"])
	}
	if _, ok := r["b"]; ok {
		t.Fatal("b: 카운터 감소(리셋)는 rate 를 내지 않아야 함")
	}
	if _, ok := r["c"]; ok {
		t.Fatal("c: 첫 관측은 rate 를 내지 않아야 함")
	}
	if len(rates(nil, map[string]int64{"a": 1}, 0)) != 0 {
		t.Fatal("dt=0 이면 빈 결과")
	}
}

// TestReadPlan 은 메시지 조회 범위 계산(최신 N, 오프셋 지정, 빈 파티션, 분배)을 검증한다.
func TestReadPlan(t *testing.T) {
	starts := map[int32]int64{0: 0, 1: 90, 2: 5}
	ends := map[int32]int64{0: 100, 1: 100, 2: 5}

	// 단일 파티션 최신 20개
	if p := readPlan(starts, ends, 0, -1, 20); p[0] != [2]int64{80, 20} || len(p) != 1 {
		t.Fatalf("latest: %v", p)
	}
	// 시작 오프셋보다 앞을 요구하면 start 로 당겨짐
	if p := readPlan(starts, ends, 1, -1, 50); p[1] != [2]int64{90, 10} {
		t.Fatalf("clamp: %v", p)
	}
	// 오프셋 지정
	if p := readPlan(starts, ends, 0, 30, 10); p[0] != [2]int64{30, 10} {
		t.Fatalf("offset: %v", p)
	}
	// 끝을 넘는 오프셋 → 없음
	if p := readPlan(starts, ends, 0, 100, 10); len(p) != 0 {
		t.Fatalf("past end: %v", p)
	}
	// 전체 파티션: 빈 파티션(2) 제외, 나머지 2개에 20개를 10개씩 분배
	p := readPlan(starts, ends, -1, -1, 20)
	if len(p) != 2 || p[0] != [2]int64{90, 10} || p[1] != [2]int64{90, 10} {
		t.Fatalf("all: %v", p)
	}
}

// localeKeys 는 locales/*.js 의 'key': 항목을 모은다.
func localeKeys(path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+'([^']+)':`).FindAllStringSubmatch(string(b), -1) {
		out[m[1]] = true
	}
	return out
}

// TestLocaleKeys 는 한국어 파일에 영어 키가 빠지지 않았는지 확인한다.
func TestLocaleKeys(t *testing.T) {
	en, ko := localeKeys("web/locales/en.js"), localeKeys("web/locales/ko.js")
	if len(en) < 50 {
		t.Fatalf("영어 키가 너무 적음: %d", len(en))
	}
	var missing []string
	for k := range en {
		if !ko[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("ko.js 에 없는 키: %s", strings.Join(missing, ", "))
	}
}
