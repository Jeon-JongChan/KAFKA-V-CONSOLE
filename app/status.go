package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// runStatus 는 웹 서버 없이 클러스터 상태를 텍스트로 출력한다.
// 처리량(msg/s)은 두 스냅샷의 차이로 구하므로 첫 출력 전에 interval 만큼 기다린다.
func runStatus(s *session, c config, w io.Writer) error {
	c.interval = max(c.interval, snapshotTTL) // 캐시 TTL 보다 짧으면 같은 스냅샷이 재사용되어 rate 가 0 이 된다
	ctx := context.Background()
	if _, err := s.Snapshot(ctx); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "measuring throughput for %s ...\n", c.interval)
	for {
		time.Sleep(c.interval)
		snap, err := s.Snapshot(ctx)
		if err != nil {
			// watch 모드에서는 일시적 연결 오류로 종료하지 않고 다음 주기에 재시도
			if !c.watch {
				return err
			}
			fmt.Fprintln(w, "ERROR:", err)
			continue
		}
		if c.watch {
			fmt.Fprint(w, "\033[H\033[2J") // 화면 지우고 커서 맨 위로
		}
		renderStatus(w, snap, c)
		cv, cerr := s.ConnectReport(ctx, snap)
		renderConnect(w, cv, cerr, c.lagWarn)
		if !c.watch {
			return nil
		}
	}
}

// renderStatus 는 스냅샷을 ASCII 전용 텍스트로 그린다 (로케일/폰트가 빈약한 서버 터미널 고려).
func renderStatus(w io.Writer, snap *Snapshot, c config) {
	internal := func(t Topic) bool { return t.Internal || strings.HasPrefix(t.Name, "_") }
	known := map[int32]bool{}
	for _, b := range snap.Brokers {
		known[b.ID] = true
	}
	// 복제본으로 참조되는데 메타데이터에 없는 브로커 = 내려간 브로커
	offlineBrokers := map[int32]bool{}
	var urp, offline int
	for _, t := range snap.Topics {
		urp += t.URP
		offline += t.Offline
		for _, p := range t.Partitions {
			for _, r := range p.Replicas {
				if !known[r] {
					offlineBrokers[r] = true
				}
			}
		}
	}
	var lagged int
	for _, g := range snap.Groups {
		for _, t := range g.Topics {
			if t.Lag >= c.lagWarn {
				lagged++
			}
		}
	}

	fmt.Fprintf(w, "Kafka V-Console  cluster=%s  brokers=%d/%d  %s  (rates over %s)\n",
		orDash(snap.ClusterID), len(snap.Brokers), len(snap.Brokers)+len(offlineBrokers), time.UnixMilli(snap.Time).Format("2006-01-02 15:04:05"), c.interval)
	var issues []string
	if len(offlineBrokers) > 0 {
		issues = append(issues, fmt.Sprintf("CRIT %d broker(s) offline %v", len(offlineBrokers), sortedIDs(offlineBrokers)))
	}
	if offline > 0 {
		issues = append(issues, fmt.Sprintf("CRIT %d offline partition(s)", offline))
	}
	if urp > 0 {
		issues = append(issues, fmt.Sprintf("WARN %d under-replicated partition(s)", urp))
	}
	if lagged > 0 {
		issues = append(issues, fmt.Sprintf("WARN %d group-topic(s) lag >= %d", lagged, c.lagWarn))
	}
	for _, e := range snap.Errors {
		issues = append(issues, "WARN "+e)
	}
	if len(issues) == 0 {
		fmt.Fprintln(w, "STATUS: OK")
	} else {
		fmt.Fprintln(w, "STATUS: "+strings.Join(issues, " | "))
	}

	fmt.Fprintln(w, "\nBROKERS  (* = controller)")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tADDRESS\tRACK\tVERSION\tLEADERS\tREPLICAS\tDISK")
	for _, b := range snap.Brokers {
		id := fmt.Sprint(b.ID)
		if b.ID == snap.Controller {
			id += "*"
		}
		fmt.Fprintf(tw, "%s\t%s:%d\t%s\t%s\t%d\t%d\t%s\n", id, b.Host, b.Port, orDash(b.Rack), orDash(b.Version), b.Leaders, b.Replicas, human(float64(b.Size), true))
	}
	tw.Flush()

	var topics []Topic
	for _, t := range snap.Topics {
		if c.all || !internal(t) {
			topics = append(topics, t)
		}
	}
	scope := "internal hidden, -all to show"
	if c.all {
		scope = "including internal"
	}
	fmt.Fprintf(w, "\nTOPICS  (%d shown, %s)\n", len(topics), scope)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPARTS\tRF\tURP\tOFFLINE\tMESSAGES\tSIZE\tIN msg/s")
	for _, t := range topics {
		rf := 0
		if len(t.Partitions) > 0 {
			rf = len(t.Partitions[0].Replicas)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n", t.Name, len(t.Partitions), rf, t.URP, t.Offline, human(float64(t.Messages), false), human(float64(t.Size), true), rate(t.InRate))
	}
	tw.Flush()

	fmt.Fprintf(w, "\nCONSUMER GROUPS  (%d)\n", len(snap.Groups))
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "GROUP\tSTATE\tMEMBERS\tTOPIC\tmsg/s\tB/s(est)\tLAG")
	for _, g := range snap.Groups {
		if len(g.Topics) == 0 {
			fmt.Fprintf(tw, "%s\t%s\t%d\t-\t-\t-\t-\n", g.ID, g.State, len(g.Members))
		}
		for i, t := range g.Topics {
			id, state, members := g.ID, g.State, fmt.Sprint(len(g.Members))
			if i > 0 { // 같은 그룹의 두 번째 토픽부터는 그룹 칸을 비워 묶음을 드러낸다
				id, state, members = "", "", ""
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s/s\t%s%s\n", id, state, members, t.Topic, rate(t.Rate), human(t.ByteRate, true), human(float64(t.Lag), false), warnMark(t.Lag, c.lagWarn))
		}
	}
	tw.Flush()

	// 토폴로지: 토픽별로 소비 그룹을 트리로 나열하고, 소비자가 없는 토픽은 한 줄로 모은다
	consumers := map[string][]string{}
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, g := range snap.Groups {
		for _, t := range g.Topics {
			line := fmt.Sprintf("%s\t%s msg/s\t%s/s\tlag %s%s", g.ID, rate(t.Rate), human(t.ByteRate, true), human(float64(t.Lag), false), warnMark(t.Lag, c.lagWarn))
			consumers[t.Topic] = append(consumers[t.Topic], line)
		}
	}
	fmt.Fprintln(w, "\nTOPOLOGY  (topic -> consumer group)")
	var idle []string
	for _, t := range topics {
		lines := consumers[t.Name]
		if len(lines) == 0 {
			idle = append(idle, t.Name)
			continue
		}
		fmt.Fprintf(tw, "%s  (in %s msg/s)\n", t.Name, rate(t.InRate))
		for i, l := range lines {
			branch := "  +-> "
			if i == len(lines)-1 {
				branch = "  `-> "
			}
			fmt.Fprintln(tw, branch+l)
		}
	}
	tw.Flush()
	if len(idle) > 0 {
		fmt.Fprintf(w, "no consumers: %s\n", strings.Join(idle, ", "))
	}
}

// renderConnect 는 Connect 커넥터 상태와 추정 부하를 텍스트로 그린다.
// URL 이 없으면 섹션을 생략한다. 조회 실패는 Kafka 상태 출력과 분리해 한 줄로만 알린다.
func renderConnect(w io.Writer, v ConnectView, err error, lagWarn int64) {
	if v.URL == "" && err == nil {
		return
	}
	if err != nil {
		fmt.Fprintf(w, "\nCONNECT  %s  ERROR: %v\n", orDash(redactURL(v.URL)), err)
		return
	}
	fmt.Fprintf(w, "\nCONNECT  %s  version=%s  connectors=%d  tasks=%d  failed=%d  %s msg/s  lag %s\n",
		redactURL(v.URL), orDash(v.Version), len(v.Connectors), v.Tasks, v.Failed, rate(v.MsgRate), human(float64(v.Lag), false))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSTATE\tTASKS\tWORKER\tTOPICS\tmsg/s\tB/s\tLAG")
	for _, c := range v.Connectors {
		running := 0
		for _, task := range c.Tasks {
			if strings.EqualFold(task.State, "RUNNING") {
				running++
			}
		}
		topics := make([]string, 0, len(c.Topics))
		for _, topic := range c.Topics {
			topics = append(topics, topic.Name)
		}
		label := strings.Join(topics, ",")
		if label == "" {
			label = orDash(c.TopicHint)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d/%d\t%s\t%s\t%s\t%s/s\t%s%s\n",
			c.Name, orDash(c.Type), orDash(c.State), running, len(c.Tasks), orDash(c.WorkerID), label,
			rate(c.MsgRate), human(c.ByteRate, true), human(float64(c.Lag), false), warnMark(c.Lag, lagWarn))
	}
	tw.Flush()
}

// human 은 수를 1.2k / 3.4 MB 처럼 축약한다. bytes 가 참이면 1024 단위 + 바이트 접미사.
func human(n float64, bytes bool) string {
	base, units := 1000.0, []string{"", "k", "M", "G", "T"}
	if bytes {
		base, units = 1024, []string{" B", " KB", " MB", " GB", " TB"}
	}
	i := 0
	for n >= base && i < len(units)-1 {
		n /= base
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f%s", n, units[0])
	}
	return fmt.Sprintf("%.1f%s", n, units[i])
}

// rate 는 초당 처리량을 표시용으로 바꾼다. 0.05 미만은 0.
func rate(r float64) string {
	if r < 0.05 {
		return "0"
	}
	if r < 10 {
		return fmt.Sprintf("%.1f", r)
	}
	return human(r, false)
}

// warnMark 는 lag 이 임계치 이상이면 " !" 를 붙인다.
func warnMark(lag, warn int64) string {
	if lag >= warn {
		return " !"
	}
	return ""
}

// orDash 는 빈 문자열을 "-" 로 바꾼다.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// sortedIDs 는 브로커 ID 집합을 정렬된 슬라이스로 바꾼다.
func sortedIDs(m map[int32]bool) []int32 {
	out := make([]int32, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
