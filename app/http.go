// HTTP 라우트 조립: Wails(API only) 와 -listen(정적+API) 이 공유한다.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"kvconsole/app/web"
)

var errManagementTarget = errors.New("활성 연결이 바뀌었습니다. 화면을 새로고침한 뒤 다시 확인하세요")

// registerAPI 는 JSON API 경로를 mux 에 한 번만 등록한다.
func (s *session) registerAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cluster", s.handleCluster)
	mux.HandleFunc("GET /api/connect", s.handleConnect)
	mux.HandleFunc("POST /api/connect/connectors", managementRequest(s.handleCreateConnector))
	mux.HandleFunc("DELETE /api/connect/connectors/{name}", managementRequest(s.handleChangeConnector))
	mux.HandleFunc("POST /api/connect/connectors/{name}/restart", managementRequest(s.handleChangeConnector))
	mux.HandleFunc("GET /api/topics/{name}/config", s.handleTopicConfig)
	mux.HandleFunc("PATCH /api/topics/{name}/config", managementRequest(s.handleChangeTopicConfig))
	mux.HandleFunc("GET /api/topics/{name}/messages", s.handleMessages)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
}

// managementRequest 는 교차 출처 폼 등에서 보낼 수 없는 헤더를 요구한다. 인증을 대신하지 않는다.
func managementRequest(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-KVC-Action") != "manage" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeErr(w, http.StatusForbidden, errors.New("관리 요청은 V-Console에서 실행하세요"))
			return
		}
		next(w, r)
	}
}

// decodeManagementJSON 은 최대 1 MiB의 단일 JSON 값만 받는다. 오류에 입력 내용을 싣지 않는다.
func decodeManagementJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil || dec.Decode(new(any)) != io.EOF {
		writeErr(w, http.StatusBadRequest, errors.New("1 MiB 이하의 올바른 JSON을 입력하세요"))
		return false
	}
	return true
}

// apiHandler 는 JSON API 만 제공한다. Wails 는 정적 파일을 먼저 찾고 나머지는 여기로 보낸다.
func (s *session) apiHandler() http.Handler {
	mux := http.NewServeMux()
	s.registerAPI(mux)
	return mux
}

// httpHandler 는 외부 브라우저용 정적 UI + API 이다 (-listen 모드).
func (s *session) httpHandler() http.Handler {
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mux := http.NewServeMux()
	s.registerAPI(mux)
	static := http.FileServerFS(web.Files)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})
	return mux
}
