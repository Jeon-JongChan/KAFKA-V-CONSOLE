// Kafka Connect 감시: REST 로 등록된 커넥터·태스크를 읽고,
// 클러스터 스냅샷의 토픽 유입률·컨슈머 그룹 소비율로 부하를 붙인다.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxConnectTopics = 40
	maxConnectBody   = 8 << 20
	maxTraceRunes    = 400
	maxConfigRunes   = 200
)

// connectClient 는 사용자가 지정한 Connect REST 만 호출한다.
// 다른 호스트로 리다이렉트되면 따라가지 않는다.
var connectClient = &http.Client{
	Timeout: 8 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		if req.URL.Host != via[0].URL.Host {
			return errors.New("connect 가 다른 호스트로 리다이렉트했습니다")
		}
		if len(via) >= 3 {
			return errors.New("connect 리다이렉트가 너무 많습니다")
		}
		return nil
	},
}

// ConnectView 는 Connect 화면·콘솔이 받아가는 한 번의 감시 결과다.
type ConnectView struct {
	URL          string          `json:"url"`
	Version      string          `json:"version"`
	Commit       string          `json:"commit"`
	ClusterID    string          `json:"kafkaClusterId"`
	Workers      []ConnectWorker `json:"workers"`
	Connectors   []ConnectorView `json:"connectors"`
	MsgRate      float64         `json:"msgRate"`
	ByteRate     float64         `json:"byteRate"`
	Lag          int64           `json:"lag"`
	Tasks        int             `json:"tasks"`
	Running      int             `json:"running"`
	Failed       int             `json:"failed"`
	Paused       int             `json:"paused"`
	Error        string          `json:"error,omitempty"`
	ClusterError string          `json:"clusterError,omitempty"`
}

// ConnectWorker 는 워커 한 대에 올라간 태스크 수다. 막대 그래프의 원천.
type ConnectWorker struct {
	ID      string `json:"id"`
	Tasks   int    `json:"tasks"`
	Running int    `json:"running"`
	Failed  int    `json:"failed"`
	Paused  int    `json:"paused"`
	Other   int    `json:"other"`
}

// ConnectorView 는 커넥터 하나의 등록 정보, 동작(태스크·토픽), 추정 부하다.
type ConnectorView struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	State      string          `json:"state"`
	Class      string          `json:"class"`
	TasksMax   string          `json:"tasksMax"`
	WorkerID   string          `json:"workerId"`
	Trace      string          `json:"trace,omitempty"`
	Topics     []ConnectTopic  `json:"topics"`
	TopicsMore int             `json:"topicsMore,omitempty"`
	TopicHint  string          `json:"topicHint,omitempty"`
	TopicFrom  string          `json:"topicFrom"` // api | config | group
	Tasks      []ConnectTask   `json:"tasks"`
	Config     []ConnectConfig `json:"config"`
	MsgRate    float64         `json:"msgRate"`
	ByteRate   float64         `json:"byteRate"`
	Lag        int64           `json:"lag"`
	GroupID    string          `json:"groupId,omitempty"`
	Measured   bool            `json:"measured"`
}

// ConnectTopic 은 커넥터가 맡는 토픽과 그 토픽에서 본 부하다.
type ConnectTopic struct {
	Name     string  `json:"name"`
	MsgRate  float64 `json:"msgRate"`
	ByteRate float64 `json:"byteRate"`
	Lag      int64   `json:"lag"`
}

// ConnectTask 는 커넥터 태스크 하나의 상태다.
type ConnectTask struct {
	ID       int    `json:"id"`
	State    string `json:"state"`
	WorkerID string `json:"workerId"`
	Trace    string `json:"trace,omitempty"`
}

// ConnectConfig 는 화면에 보여줄 설정 한 줄이다. 비밀 값은 비운다.
type ConnectConfig struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

type connectServerInfo struct {
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	KafkaClusterID string `json:"kafka_cluster_id"`
}

type connectExpanded struct {
	Info struct {
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
		Type   string            `json:"type"`
	} `json:"info"`
	Status struct {
		Connector struct {
			State    string `json:"state"`
			WorkerID string `json:"worker_id"`
			Trace    string `json:"trace"`
		} `json:"connector"`
		Tasks []struct {
			ID       int    `json:"id"`
			State    string `json:"state"`
			WorkerID string `json:"worker_id"`
			Trace    string `json:"trace"`
		} `json:"tasks"`
		Type string `json:"type"`
	} `json:"status"`
}

type connectItem struct {
	Name          string
	Expanded      connectExpanded
	TopicNames    []string
	TopicsFromAPI bool
}

// ConnectReport 는 프로필의 Connect URL 로 감시 결과를 만든다.
// URL 이 비어 있으면 빈 뷰와 nil 오류를 돌려준다.
func (s *session) ConnectReport(ctx context.Context, snap *Snapshot) (ConnectView, error) {
	s.mu.Lock()
	raw := s.profile.ConnectURL
	s.mu.Unlock()
	if strings.TrimSpace(raw) == "" {
		return ConnectView{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return buildConnectView(ctx, raw, snap)
}

// handleConnect 는 Connect 감시 JSON 을 돌려준다.
// Connect 가 죽어도 Kafka 스냅샷 오류와 구분되도록 본문에 error 를 담아 200 으로 응답한다.
func (s *session) handleConnect(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	raw := s.profile.ConnectURL
	s.mu.Unlock()
	if strings.TrimSpace(raw) == "" {
		writeJSON(w, http.StatusOK, ConnectView{Connectors: []ConnectorView{}, Workers: []ConnectWorker{}})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	base, err := parseConnectURL(raw)
	if err != nil {
		writeJSON(w, http.StatusOK, ConnectView{URL: raw, Error: err.Error(), Connectors: []ConnectorView{}, Workers: []ConnectWorker{}})
		return
	}

	var (
		snap     *Snapshot
		snapErr  error
		items    []connectItem
		info     connectServerInfo
		fetchErr error
		wg       sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		// 스냅샷은 자체 타임아웃이 있다. Connect HTTP 8초와 같이 끊어지지 않게 요청 컨텍스트를 쓴다.
		snap, snapErr = s.Snapshot(r.Context())
	}()
	go func() {
		defer wg.Done()
		items, info, fetchErr = fetchConnect(ctx, base)
	}()
	wg.Wait()
	if fetchErr != nil {
		writeJSON(w, http.StatusOK, ConnectView{URL: raw, Error: fetchErr.Error(), Connectors: []ConnectorView{}, Workers: []ConnectWorker{}})
		return
	}
	view := assembleConnect(raw, info, items, snap)
	if snapErr != nil {
		view.ClusterError = snapErr.Error()
	}
	writeJSON(w, http.StatusOK, view)
}

// buildConnectView 는 Connect REST 를 읽어 스냅샷 부하까지 붙인 뷰를 만든다.
func buildConnectView(ctx context.Context, raw string, snap *Snapshot) (ConnectView, error) {
	base, err := parseConnectURL(raw)
	if err != nil {
		return ConnectView{URL: raw}, err
	}
	items, info, err := fetchConnect(ctx, base)
	if err != nil {
		return ConnectView{URL: raw}, err
	}
	return assembleConnect(raw, info, items, snap), nil
}

// parseConnectURL 은 http(s) 절대 주소만 허용한다.
func parseConnectURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("connect URL 은 http(s)://호스트 형태여야 합니다")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	// 끝이 / 가 아니면 ResolveReference 가 prefix 를 한 단계 올려 버린다.
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u, nil
}

// redactURL 은 주소에 붙은 비밀번호를 로그·콘솔에서 가린다.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Redacted()
}

// fetchConnect 는 서버 정보, 커넥터 expand, 토픽 목록을 가져온다.
func fetchConnect(ctx context.Context, base *url.URL) ([]connectItem, connectServerInfo, error) {
	var info connectServerInfo
	// 버전 조회가 실패해도 커넥터 목록은 보여 준다.
	_ = connectGet(ctx, base, "", &info)

	var expanded map[string]connectExpanded
	if err := connectGet(ctx, base, "connectors?expand=info&expand=status", &expanded); err != nil {
		return nil, info, err
	}
	items := make([]connectItem, 0, len(expanded))
	for name, ex := range expanded {
		if ex.Info.Name == "" {
			ex.Info.Name = name
		}
		items = append(items, connectItem{Name: name, Expanded: ex})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			var body map[string]struct {
				Topics []string `json:"topics"`
			}
			path := "connectors/" + url.PathEscape(items[i].Name) + "/topics"
			if err := connectGet(ctx, base, path, &body); err != nil {
				return
			}
			names := body[items[i].Name].Topics
			items[i].TopicNames = append([]string(nil), names...)
			items[i].TopicsFromAPI = true
		}(i)
	}
	wg.Wait()
	return items, info, nil
}

// connectGet 는 base 아래 상대 경로의 JSON 을 dest 에 담는다. path 가 빈 문자열이면 루트다.
func connectGet(ctx context.Context, base *url.URL, path string, dest any) error {
	ref := path
	if ref == "" {
		ref = "."
	}
	u, err := url.Parse(ref)
	if err != nil {
		return err
	}
	full := base.ResolveReference(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kv-console")
	resp, err := connectClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect 연결 실패: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConnectBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 240 {
			msg = msg[:240]
		}
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("connect %s %s: %s", req.URL.Path, resp.Status, msg)
	}
	trimmed := bytes.TrimSpace(body)
	if strings.Contains(path, "expand=") && len(trimmed) > 0 && trimmed[0] == '[' {
		return fmt.Errorf("이 Connect 는 expand 조회를 지원하지 않습니다 (Kafka Connect 2.3 이상 필요)")
	}
	if err := json.Unmarshal(trimmed, dest); err != nil {
		return fmt.Errorf("connect %s 응답 파싱 실패: %w", req.URL.Path, err)
	}
	return nil
}

// assembleConnect 는 REST 결과와 스냅샷을 화면용 뷰로 합친다.
func assembleConnect(raw string, info connectServerInfo, items []connectItem, snap *Snapshot) ConnectView {
	known := snapshotTopicNames(snap)
	view := ConnectView{
		URL:        raw,
		Version:    info.Version,
		Commit:     info.Commit,
		ClusterID:  info.KafkaClusterID,
		Workers:    []ConnectWorker{},
		Connectors: make([]ConnectorView, 0, len(items)),
	}
	workers := map[string]*ConnectWorker{}
	for _, item := range items {
		c := connectorFromItem(item, known)
		applyConnectorLoad(&c, item.Expanded.Info.Config, snap)
		view.Connectors = append(view.Connectors, c)
		view.MsgRate += c.MsgRate
		view.ByteRate += c.ByteRate
		view.Lag += c.Lag
		addWorkerTasks(workers, c)
	}
	sort.SliceStable(view.Connectors, func(i, j int) bool {
		ib, jb := connectorFailed(view.Connectors[i]), connectorFailed(view.Connectors[j])
		if ib != jb {
			return ib
		}
		return view.Connectors[i].Name < view.Connectors[j].Name
	})
	for _, w := range workers {
		view.Workers = append(view.Workers, *w)
		view.Tasks += w.Tasks
		view.Running += w.Running
		view.Failed += w.Failed
		view.Paused += w.Paused
	}
	sort.Slice(view.Workers, func(i, j int) bool { return view.Workers[i].ID < view.Workers[j].ID })
	return view
}

// connectorFromItem 은 expand 응답 하나를 화면 모델로 바꾼다. 부하는 아직 0 이다.
func connectorFromItem(item connectItem, known []string) ConnectorView {
	ex := item.Expanded
	cfg := ex.Info.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	class := cfg["connector.class"]
	c := ConnectorView{
		Name:      item.Name,
		Type:      inferConnectType(firstNonEmpty(ex.Status.Type, ex.Info.Type), class),
		State:     ex.Status.Connector.State,
		Class:     class,
		TasksMax:  cfg["tasks.max"],
		WorkerID:  ex.Status.Connector.WorkerID,
		Trace:     clip(ex.Status.Connector.Trace, maxTraceRunes),
		Topics:    []ConnectTopic{},
		Tasks:     []ConnectTask{},
		Config:    redactConfig(cfg),
		TopicHint: topicHint(cfg),
	}
	names := item.TopicNames
	from := ""
	if item.TopicsFromAPI {
		from = "api"
	} else {
		names = topicsFromConfig(cfg, known)
		if len(names) > 0 {
			from = "config"
		}
	}
	names, more := capTopics(uniq(names))
	c.TopicsMore = more
	c.TopicFrom = from
	for _, name := range names {
		c.Topics = append(c.Topics, ConnectTopic{Name: name})
	}
	for _, task := range ex.Status.Tasks {
		c.Tasks = append(c.Tasks, ConnectTask{
			ID:       task.ID,
			State:    task.State,
			WorkerID: task.WorkerID,
			Trace:    clip(task.Trace, maxTraceRunes),
		})
	}
	sort.Slice(c.Tasks, func(i, j int) bool { return c.Tasks[i].ID < c.Tasks[j].ID })
	return c
}

// applyConnectorLoad 는 싱크는 컨슈머 그룹, 소스는 토픽 유입률로 부하를 채운다.
func applyConnectorLoad(c *ConnectorView, cfg map[string]string, snap *Snapshot) {
	if c == nil {
		return
	}
	if cfg == nil {
		cfg = map[string]string{}
	}
	// 스냅샷이 없어도 어떤 그룹을 볼 계획인지는 보여 준다.
	if c.Type == "sink" {
		c.GroupID = sinkGroupID(c.Name, cfg)
	}
	if snap == nil {
		return
	}
	if c.Type == "sink" {
		group, ok := findGroup(snap, c.GroupID)
		if ok && len(c.Topics) == 0 {
			// 설정·토픽 API 에 이름이 없어도 그룹이 소비 중이면 그 토픽이 실제 작업이다.
			for _, topic := range group.Topics {
				c.Topics = append(c.Topics, ConnectTopic{Name: topic.Topic})
			}
			if c.TopicFrom == "" {
				c.TopicFrom = "group"
			}
		}
		byTopic := map[string]GroupTopic{}
		if ok {
			for _, topic := range group.Topics {
				byTopic[topic.Topic] = topic
			}
		}
		matched := false
		for i := range c.Topics {
			topic, found := byTopic[c.Topics[i].Name]
			if !found {
				continue
			}
			matched = true
			c.Topics[i].MsgRate = topic.Rate
			c.Topics[i].ByteRate = topic.ByteRate
			c.Topics[i].Lag = topic.Lag
		}
		c.Measured = ok && (matched || len(c.Topics) == 0)
	} else {
		// shortcut: 소스 부하는 대상 토픽 유입률이다. 다른 프로듀서가 같은 토픽에 쓰면 과대 계상된다. Connect JMX 가 필요해지면 교체.
		byName := map[string]Topic{}
		for _, topic := range snap.Topics {
			byName[topic.Name] = topic
		}
		for i := range c.Topics {
			topic, ok := byName[c.Topics[i].Name]
			if !ok {
				continue
			}
			c.Measured = true
			c.Topics[i].MsgRate = topic.InRate
			c.Topics[i].ByteRate = topic.InRate * topic.AvgMsgBytes
		}
	}
	c.MsgRate, c.ByteRate, c.Lag = 0, 0, 0
	for _, topic := range c.Topics {
		c.MsgRate += topic.MsgRate
		c.ByteRate += topic.ByteRate
		c.Lag += topic.Lag
	}
}

// sinkGroupID 는 싱크 커넥터의 컨슈머 그룹이다. 기본값은 connect-<name> 이다.
func sinkGroupID(name string, cfg map[string]string) string {
	if id := strings.TrimSpace(cfg["consumer.override.group.id"]); id != "" {
		return id
	}
	return "connect-" + name
}

// topicsFromConfig 는 설정에 적힌 토픽 이름을 뽑는다.
// topics.regex 는 스냅샷에 있는 토픽 이름과만 맞춘다.
func topicsFromConfig(cfg map[string]string, known []string) []string {
	var out []string
	if cfg == nil {
		return out
	}
	for _, part := range strings.Split(cfg["topics"], ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if one := strings.TrimSpace(cfg["topic"]); one != "" {
		out = append(out, one)
	}
	if pattern := strings.TrimSpace(cfg["topics.regex"]); pattern != "" {
		re, err := regexp.Compile(pattern)
		if err == nil {
			for _, name := range known {
				if re.MatchString(name) {
					out = append(out, name)
				}
			}
		}
	}
	return uniq(out)
}

// topicHint 는 토픽 이름이 고정이 아닐 때 화면에 보여줄 설정 힌트다.
func topicHint(cfg map[string]string) string {
	if cfg == nil {
		return ""
	}
	if pattern := strings.TrimSpace(cfg["topics.regex"]); pattern != "" {
		return pattern
	}
	if prefix := strings.TrimSpace(cfg["topic.prefix"]); prefix != "" {
		return prefix + ".*"
	}
	return ""
}

// inferConnectType 은 status 의 type 이 없으면 클래스 이름으로 source/sink 를 가른다.
func inferConnectType(kind, class string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "source" || kind == "sink" {
		return kind
	}
	class = strings.ToLower(class)
	switch {
	case strings.Contains(class, "sink"):
		return "sink"
	case strings.Contains(class, "source"):
		return "source"
	default:
		return kind
	}
}

// sensitiveKey 는 비밀번호·토큰류 설정 키다. 화면과 JSON 에 값을 싣지 않는다.
func sensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, word := range []string{"password", "secret", "token", "credential", "jaas", "passwd"} {
		if strings.Contains(k, word) {
			return true
		}
	}
	return false
}

// redactConfig 는 설정을 키 순으로 정렬하고 비밀 값을 비운다.
func redactConfig(cfg map[string]string) []ConnectConfig {
	out := make([]ConnectConfig, 0, len(cfg))
	for key, value := range cfg {
		item := ConnectConfig{Key: key, Value: clip(value, maxConfigRunes)}
		if sensitiveKey(key) {
			item.Value = ""
			item.Sensitive = true
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// addWorkerTasks 는 태스크를 워커별 상태 카운트에 더한다.
func addWorkerTasks(workers map[string]*ConnectWorker, c ConnectorView) {
	seen := map[string]bool{}
	for _, task := range c.Tasks {
		id := task.WorkerID
		if id == "" {
			id = "(unassigned)"
		}
		w := workers[id]
		if w == nil {
			w = &ConnectWorker{ID: id}
			workers[id] = w
		}
		seen[id] = true
		w.Tasks++
		switch strings.ToUpper(task.State) {
		case "RUNNING":
			w.Running++
		case "FAILED":
			w.Failed++
		case "PAUSED":
			w.Paused++
		default:
			w.Other++
		}
	}
	// 태스크가 아직 없으면 커넥터가 붙은 워커에 자리만 남긴다.
	if len(c.Tasks) == 0 && c.WorkerID != "" && !seen[c.WorkerID] {
		if workers[c.WorkerID] == nil {
			workers[c.WorkerID] = &ConnectWorker{ID: c.WorkerID}
		}
	}
}

// connectorFailed 는 커넥터 또는 태스크가 FAILED 이면 참이다. 목록에서 위로 올린다.
func connectorFailed(c ConnectorView) bool {
	if strings.EqualFold(c.State, "FAILED") {
		return true
	}
	for _, task := range c.Tasks {
		if strings.EqualFold(task.State, "FAILED") {
			return true
		}
	}
	return false
}

// findGroup 은 스냅샷에서 그룹 ID 가 같은 그룹을 찾는다.
func findGroup(snap *Snapshot, id string) (Group, bool) {
	if snap == nil {
		return Group{}, false
	}
	for _, group := range snap.Groups {
		if group.ID == id {
			return group, true
		}
	}
	return Group{}, false
}

// snapshotTopicNames 는 regex 매칭에 쓸 토픽 이름 목록이다.
func snapshotTopicNames(snap *Snapshot) []string {
	if snap == nil {
		return nil
	}
	out := make([]string, 0, len(snap.Topics))
	for _, topic := range snap.Topics {
		out = append(out, topic.Name)
	}
	return out
}

// uniq 는 빈 문자열을 버리고 처음 나온 순서를 유지한다.
func uniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// capTopics 는 화면이 감당하는 토픽 수만 남기고 잘린 개수를 돌려준다.
func capTopics(in []string) ([]string, int) {
	if len(in) <= maxConnectTopics {
		return in, 0
	}
	return in[:maxConnectTopics], len(in) - maxConnectTopics
}

// clip 은 화면·JSON 에 실리는 긴 오류 문구를 자른다.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
