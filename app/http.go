// HTTP 라우트 조립: Wails(API only) 와 -listen(정적+API) 이 공유한다.
package main

import (
	"mime"
	"net/http"

	"kvconsole/app/web"
)

// registerAPI 는 JSON API 경로를 mux 에 한 번만 등록한다.
func (s *session) registerAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cluster", s.handleCluster)
	mux.HandleFunc("GET /api/connect", s.handleConnect)
	mux.HandleFunc("GET /api/topics/{name}/config", s.handleTopicConfig)
	mux.HandleFunc("GET /api/topics/{name}/messages", s.handleMessages)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
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
