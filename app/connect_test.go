package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTopicsFromConfig 는 설정에서 토픽 이름을 뽑고 비밀 값을 비우는지 확인한다.
func TestTopicsFromConfig(t *testing.T) {
	got := topicsFromConfig(map[string]string{"topics": "a, b", "topic": "a"}, nil)
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("topics=%v", got)
	}
	got = topicsFromConfig(map[string]string{"topics.regex": "^ord"}, []string{"orders", "users"})
	if strings.Join(got, ",") != "orders" {
		t.Fatalf("regex=%v", got)
	}
	for _, item := range redactConfig(map[string]string{"connection.password": "s3cret", "topics": "a"}) {
		if item.Key == "connection.password" && (item.Value != "" || !item.Sensitive) {
			t.Fatalf("password leaked: %+v", item)
		}
	}
}

// TestBuildConnectView 는 Connect REST 와 스냅샷을 합쳐 부하·상태·비밀 가림이 맞는지 확인한다.
func TestBuildConnectView(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Write([]byte(`{"version":"3.7.0","commit":"abc","kafka_cluster_id":"kid"}`))
		case "/connectors":
			w.Write([]byte(`{
				"s1":{"info":{"name":"s1","type":"sink","config":{"connector.class":"io.FileStreamSinkConnector","tasks.max":"2","topics":"orders,payments","connection.password":"s3cret","name":"s1"}},
					"status":{"type":"sink","connector":{"state":"RUNNING","worker_id":"w1:8083"},"tasks":[{"id":0,"state":"RUNNING","worker_id":"w1:8083"},{"id":1,"state":"FAILED","worker_id":"w1:8083","trace":"boom"}]}},
				"src":{"info":{"name":"src","type":"source","config":{"connector.class":"io.FileStreamSourceConnector","topic":"out","tasks.max":"1"}},
					"status":{"type":"source","connector":{"state":"RUNNING","worker_id":"w1:8083"},"tasks":[{"id":0,"state":"RUNNING","worker_id":"w1:8083"}]}},
				"custom":{"info":{"name":"custom","type":"sink","config":{"connector.class":"io.JdbcSinkConnector","consumer.override.group.id":"my-group","tasks.max":"1"}},
					"status":{"type":"sink","connector":{"state":"RUNNING","worker_id":"w2:8083"},"tasks":[{"id":0,"state":"RUNNING","worker_id":"w2:8083"}]}},
				"rx":{"info":{"name":"rx","type":"source","config":{"connector.class":"io.RegexSource","topics.regex":"^ord","tasks.max":"1"}},
					"status":{"type":"source","connector":{"state":"PAUSED","worker_id":"w2:8083"},"tasks":[{"id":0,"state":"PAUSED","worker_id":"w2:8083"}]}}
			}`))
		default:
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/connectors/"), "/topics")
			if name == "rx" || name == "custom" {
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			w.Write([]byte(`{"` + name + `":{"topics":["` + strings.Join(map[string][]string{"s1": {"orders", "payments"}, "src": {"out"}}[name], `","`) + `"]}}`))
		}
	}))
	defer srv.Close()

	snap := &Snapshot{
		Topics: []Topic{
			{Name: "out", InRate: 10, AvgMsgBytes: 100},
			{Name: "orders", InRate: 8, AvgMsgBytes: 50},
		},
		Groups: []Group{
			{ID: "connect-s1", Topics: []GroupTopic{
				{Topic: "orders", Rate: 4, ByteRate: 40, Lag: 7},
				{Topic: "payments", Rate: 1, ByteRate: 10, Lag: 3},
			}},
			{ID: "my-group", Topics: []GroupTopic{{Topic: "ev", Rate: 5, ByteRate: 20, Lag: 2}}},
		},
	}
	view, err := buildConnectView(context.Background(), srv.URL, snap)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("password leaked into view")
	}
	if view.Version != "3.7.0" || view.Failed != 1 || view.Paused != 1 || view.Running != 3 || view.Tasks != 5 {
		t.Fatalf("summary %+v", view)
	}
	byName := map[string]ConnectorView{}
	for _, c := range view.Connectors {
		byName[c.Name] = c
	}
	if view.Connectors[0].Name != "s1" {
		t.Fatalf("failed connector should be first, got %s", view.Connectors[0].Name)
	}
	s1 := byName["s1"]
	if s1.MsgRate != 5 || s1.Lag != 10 || s1.ByteRate != 50 || !s1.Measured || s1.GroupID != "connect-s1" {
		t.Fatalf("s1 load %+v", s1)
	}
	src := byName["src"]
	if src.MsgRate != 10 || src.ByteRate != 1000 || !src.Measured || src.TopicFrom != "api" {
		t.Fatalf("src load %+v", src)
	}
	custom := byName["custom"]
	if custom.GroupID != "my-group" || len(custom.Topics) != 1 || custom.Topics[0].Name != "ev" || custom.MsgRate != 5 || custom.Lag != 2 || custom.TopicFrom != "group" {
		t.Fatalf("custom %+v", custom)
	}
	rx := byName["rx"]
	if len(rx.Topics) != 1 || rx.Topics[0].Name != "orders" || rx.MsgRate != 8 || rx.ByteRate != 400 || rx.TopicFrom != "config" {
		t.Fatalf("rx %+v", rx)
	}
	if _, err := buildConnectView(context.Background(), "ftp://nope", nil); err == nil {
		t.Fatal("expected bad url error")
	}
	bare, err := buildConnectView(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range bare.Connectors {
		if c.Name == "s1" && c.GroupID != "connect-s1" {
			t.Fatalf("group id without snapshot: %+v", c)
		}
	}
}
