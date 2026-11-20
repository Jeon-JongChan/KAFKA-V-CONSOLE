package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// connectorRegistration 은 Kafka Connect의 POST /connectors 요청 형식이다.
type connectorRegistration struct {
	Name   string            `json:"name"`
	Config map[string]string `json:"config"`
}

// validConnectorName 은 공백뿐인 이름과 REST 경로에서 의미가 바뀌는 점 세그먼트를 거른다.
func validConnectorName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && name != "." && name != ".." && !strings.ContainsAny(name, "\r\n\x00")
}

// parseConnectorRegistration 은 REST 요청 형식과 name을 포함한 평면 설정을 모두 받는다.
func parseConnectorRegistration(input map[string]any) (connectorRegistration, error) {
	name, _ := input["name"].(string)
	if !validConnectorName(name) {
		return connectorRegistration{}, errors.New("유효한 커넥터 name이 필요합니다")
	}
	raw := input
	if wrapped, ok := input["config"]; ok {
		raw, ok = wrapped.(map[string]any)
		if !ok || len(input) != 2 {
			return connectorRegistration{}, errors.New("중첩 형식은 name과 config 객체만 포함해야 합니다")
		}
	}
	if inner, exists := raw["name"]; exists && inner != name {
		return connectorRegistration{}, errors.New("name과 config.name이 일치해야 합니다")
	}
	out := connectorRegistration{Name: name, Config: make(map[string]string, len(raw))}
	for key, value := range raw {
		if strings.TrimSpace(key) == "" {
			return connectorRegistration{}, errors.New("빈 커넥터 설정 키는 사용할 수 없습니다")
		}
		switch v := value.(type) {
		case string:
			out.Config[key] = v
		case json.Number:
			out.Config[key] = v.String()
		case bool:
			out.Config[key] = strconv.FormatBool(v)
		default:
			return connectorRegistration{}, errors.New("커넥터 설정값에는 문자열, 숫자 또는 불리언만 사용할 수 있습니다")
		}
	}
	if strings.TrimSpace(out.Config["connector.class"]) == "" {
		return connectorRegistration{}, errors.New("connector.class 설정이 필요합니다")
	}
	return out, nil
}

// handleCreateConnector 는 기존 커넥터를 덮어쓰지 않는 POST로 등록한다.
func (s *session) handleCreateConnector(w http.ResponseWriter, r *http.Request) {
	var input map[string]any
	if !decodeManagementJSON(w, r, &input) {
		return
	}
	registration, err := parseConnectorRegistration(input)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := json.Marshal(registration)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("커넥터 설정을 변환할 수 없습니다"))
		return
	}
	s.forwardConnectorAction(w, r, "connectors", body)
}

// handleChangeConnector 는 지정한 커넥터를 삭제하거나 해당 태스크까지 재시작한다.
func (s *session) handleChangeConnector(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validConnectorName(name) {
		writeErr(w, http.StatusBadRequest, errors.New("유효한 커넥터 이름이 필요합니다"))
		return
	}
	path := "connectors/" + url.PathEscape(name)
	if r.Method == http.MethodPost {
		path += "/restart?includeTasks=true&onlyFailed=false"
	}
	s.forwardConnectorAction(w, r, path, nil)
}

// forwardConnectorAction 은 활성 프로필의 Connect에만 쓰고, 비밀이 들어갈 수 있는 응답 본문은 노출하지 않는다.
func (s *session) forwardConnectorAction(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	s.mu.Lock()
	raw := s.profile.ConnectURL
	matches := r.Header.Get("X-KVC-Target") == s.targetLocked()
	s.mu.Unlock()
	if !matches {
		writeErr(w, http.StatusConflict, errManagementTarget)
		return
	}
	base, err := parseConnectURL(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ref, err := url.Parse(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("잘못된 커넥터 요청 경로입니다"))
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, base.ResolveReference(ref).String(), bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("커넥터 요청을 만들 수 없습니다"))
		return
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := *connectClient
	// 쓰기가 다른 경로로 재전송되거나 POST가 GET으로 바뀌어 성공으로 오인되는 것을 막는다.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, errors.New("Kafka Connect 관리 요청의 결과를 확인할 수 없습니다. 목록을 새로고침한 뒤 다시 시도하세요"))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	code := resp.StatusCode
	if code < 400 || code >= 500 {
		code = http.StatusBadGateway
	}
	writeErr(w, code, fmt.Errorf("Kafka Connect가 요청을 거부했습니다 (HTTP %d %s)", resp.StatusCode, http.StatusText(resp.StatusCode)))
}
