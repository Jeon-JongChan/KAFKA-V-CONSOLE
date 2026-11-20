// 런처: 실행 모드(콘솔 / HTTP / 데스크톱)를 고르고 세션을 붙인다.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	"kvconsole/app/mode"
	"kvconsole/app/web"
)

// runMode 는 프로세스 진입 모드다.
type runMode int

const (
	modeConsole runMode = iota
	modeHTTP
	modeDesktop
)

// resolveMode 는 OS·플래그로 실행 모드를 고른다.
func resolveMode(c config) runMode {
	if c.status || c.console {
		return modeConsole
	}
	if c.listen != "" {
		return modeHTTP
	}
	if runtime.GOOS != "windows" {
		// 포터블 Linux/mac 빌드는 데스크톱 창이 없다 → 기본 콘솔
		return modeConsole
	}
	return modeDesktop
}

// run 은 세션을 열고 모드에 맞는 어댑터를 실행한다.
func run(c config, cfgPath string) error {
	rm := resolveMode(c)
	if rm == modeConsole {
		mode.AttachConsole()
	}

	s, err := openSession(c.profile(), cfgPath)
	if err != nil {
		if rm == modeConsole {
			mode.AttachConsole()
		}
		return err
	}
	defer s.Close()

	switch rm {
	case modeConsole:
		return runStatus(s, c, os.Stdout)
	case modeHTTP:
		return runHTTP(s, c, cfgPath)
	default:
		return mode.RunDesktop(web.Files, s.apiHandler(), s.Brokers(), s.Probe)
	}
}

// runHTTP 는 -listen 주소에 브라우저용 HTTP 서버를 연다.
func runHTTP(s *session, c config, cfgPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := s.Probe(ctx); err != nil {
		log.Printf("경고: 브로커 연결 실패 (%s): %v — UI 설정에서 변경 가능", s.Brokers(), err)
	}
	cancel()
	log.Printf("kv-console %s  http://%s  -> kafka %s  (config %s)", version, displayAddr(c.listen), s.Brokers(), cfgPath)
	srv := &http.Server{Addr: c.listen, Handler: s.httpHandler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}
