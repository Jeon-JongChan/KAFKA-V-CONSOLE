// 세션: 열린 Kafka 연결과 스냅샷 캐시를 한 모듈 뒤에 둔다.
// HTTP·데스크톱·콘솔은 이 심만 건너 동작한다.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// session 은 현재 연결 프로필과 Kafka 클라이언트·스냅샷 서비스다.
type session struct {
	mu      sync.Mutex
	cl      *kgo.Client
	opts    []kgo.Opt
	adm     *kadm.Client
	snaps   *snapshotService
	profile connectionProfile
	path    string
}

// openSession 은 프로필로 Kafka 에 붙고 스냅샷 서비스를 만든다.
func openSession(profile connectionProfile, path string) (*session, error) {
	opts, cl, adm, err := openKafka(profile)
	if err != nil {
		return nil, err
	}
	return &session{
		cl:      cl,
		opts:    opts,
		adm:     adm,
		snaps:   newSnapshotService(kafkaSnapshotCollector{adm: adm, now: time.Now}, time.Now),
		profile: profile,
		path:    path,
	}, nil
}

// Close 는 Kafka 클라이언트를 닫는다. 여러 번 호출해도 안전하다.
func (s *session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cl != nil {
		s.cl.Close()
		s.cl = nil
	}
}

// Snapshot 은 캐시된(또는 새로 수집한) 클러스터 스냅샷을 돌려준다.
func (s *session) Snapshot(ctx context.Context) (*Snapshot, error) {
	s.mu.Lock()
	snaps := s.snaps
	s.mu.Unlock()
	return snaps.Snapshot(ctx)
}

// ConsumerOpts 는 메시지 뷰어용 일회 컨슈머에 쓸 옵션 사본이다.
func (s *session) ConsumerOpts() []kgo.Opt {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]kgo.Opt, len(s.opts))
	copy(out, s.opts)
	return out
}

// Admin 은 현재 관리 클라이언트다 (재연결 시 교체됨).
func (s *session) Admin() *kadm.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adm
}

// Probe 는 브로커 메타데이터로 연결을 한번 확인한다.
func (s *session) Probe(ctx context.Context) error {
	_, err := s.Admin().BrokerMetadata(ctx)
	return err
}

// Brokers 는 현재 프로필의 브로커 목록 문자열이다.
func (s *session) Brokers() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile.Brokers
}

// SettingsView 는 UI 용 연결 설정 뷰다.
func (s *session) SettingsView() settingsDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile.settingsView(s.path)
}

// ApplyAndSave 는 프로필을 적용(재연결)하고 디스크에 저장한다.
// 스냅샷 캐시·처리량 카운터는 유지하고 수집기만 갈아끼운다.
func (s *session) ApplyAndSave(next connectionProfile) error {
	opts, cl, adm, err := openKafka(next)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.cl
	s.cl = cl
	s.opts = opts
	s.adm = adm
	s.profile = next
	s.snaps.replaceCollector(kafkaSnapshotCollector{adm: adm, now: time.Now})
	path := s.path
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return saveConnectionProfile(path, next)
}

// handleGetSettings 는 현재 연결 설정을 돌려준다.
func (s *session) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.SettingsView())
}

// handlePutSettings 는 설정을 저장하고 Kafka 에 다시 붙인다.
func (s *session) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	cur := s.profile
	s.mu.Unlock()
	next := mergeConnectionUpdate(cur, in)
	if err := s.ApplyAndSave(next); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	log.Printf("설정 저장·재연결: %s -> %s", s.path, next.Brokers)
	writeJSON(w, http.StatusOK, s.SettingsView())
}
