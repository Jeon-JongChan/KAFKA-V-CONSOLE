// Package main 은 Kafka V-Console 단일 바이너리 진입점이다.
// Windows 기본은 데스크톱 창, Linux 기본은 텍스트 콘솔, -listen 은 HTTP 웹 UI.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"kvconsole/app/mode"
)

// version 은 빌드 시 -ldflags "-X main.version=..." 로 주입된다.
var version = "dev"

// envOr 는 환경변수 값이 있으면 그 값을, 없으면 기본값을 돌려준다.
func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// config 는 CLI 플래그/환경변수/로컬 파일로 받은 실행 설정이다.
type config struct {
	listen, brokers, connect          string
	tls, tlsSkipVerify                bool
	tlsCA, tlsCert, tlsKey            string
	saslMechanism, saslUser, saslPass string

	console, status, watch, all bool
	interval                    time.Duration
	lagWarn                     int64
}

// profile 은 실행 설정에서 연결 프로필만 뽑는다.
func (c config) profile() connectionProfile {
	return connectionProfile{
		Brokers:       c.brokers,
		TLS:           c.tls,
		TLSSkipVerify: c.tlsSkipVerify,
		TLSCA:         c.tlsCA,
		TLSCert:       c.tlsCert,
		TLSKey:        c.tlsKey,
		SASLMechanism: c.saslMechanism,
		SASLUser:      c.saslUser,
		SASLPass:      c.saslPass,
		ConnectURL:    c.connect,
	}
}

// parseConfig 우선순위: CLI > 환경변수 > kv-console.json > 기본값.
func parseConfig() (config, string) {
	var c config
	if len(os.Args) > 1 && os.Args[1] == "status" {
		c.status = true
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	path := defaultConfigPath()
	file, err := loadConnectionProfile(path)
	if err != nil {
		log.Printf("경고: 설정 파일 읽기 실패 (%s): %v", path, err)
	}

	flag.BoolVar(&c.console, "console", false, "텍스트 콘솔 모드 (데스크톱 창 대신)")
	flag.BoolVar(&c.watch, "watch", false, "[console] Ctrl+C 전까지 -interval 마다 화면 갱신")
	flag.DurationVar(&c.interval, "interval", 5*time.Second, "[console] 처리량 측정/갱신 간격 (최소 3s)")
	flag.BoolVar(&c.all, "all", false, "[console] 내부 토픽(__*, _*) 포함")
	flag.Int64Var(&c.lagWarn, "lag-warn", 1000, "[console] 이 값 이상의 lag 에 ! 표시")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `사용법:
  kv-console [플래그]              Windows: 데스크톱 창 / Linux: 텍스트 콘솔
  kv-console -console [플래그]     텍스트 상태 출력
  kv-console status [플래그]       -console 과 동일
  kv-console -listen :8080         브라우저용 HTTP 서버

연결 설정은 UI 설정 또는 %s (CLI/환경변수 우선).

플래그:
`, path)
		flag.PrintDefaults()
	}
	flag.StringVar(&c.listen, "listen", envOr("KVC_LISTEN", ""), "HTTP 리슨 주소 (설정 시 데스크톱 대신 브라우저 서버, KVC_LISTEN)")
	flag.StringVar(&c.brokers, "brokers", envOr("KVC_BROKERS", firstNonEmpty(file.Brokers, "localhost:9092")), "쉼표로 구분한 브로커 목록 (KVC_BROKERS)")
	flag.BoolVar(&c.tls, "tls", envBool("KVC_TLS", file.TLS), "TLS 사용 (KVC_TLS=true)")
	flag.BoolVar(&c.tlsSkipVerify, "tls-skip-verify", envBool("KVC_TLS_SKIP_VERIFY", file.TLSSkipVerify), "서버 인증서 검증 생략 (KVC_TLS_SKIP_VERIFY=true)")
	flag.StringVar(&c.tlsCA, "tls-ca", envOr("KVC_TLS_CA", file.TLSCA), "CA 인증서 PEM 경로 (KVC_TLS_CA)")
	flag.StringVar(&c.tlsCert, "tls-cert", envOr("KVC_TLS_CERT", file.TLSCert), "mTLS 클라이언트 인증서 PEM (KVC_TLS_CERT)")
	flag.StringVar(&c.tlsKey, "tls-key", envOr("KVC_TLS_KEY", file.TLSKey), "mTLS 클라이언트 키 PEM (KVC_TLS_KEY)")
	flag.StringVar(&c.saslMechanism, "sasl-mechanism", envOr("KVC_SASL_MECHANISM", file.SASLMechanism), "PLAIN | SCRAM-SHA-256 | SCRAM-SHA-512 (KVC_SASL_MECHANISM)")
	flag.StringVar(&c.saslUser, "sasl-user", envOr("KVC_SASL_USER", file.SASLUser), "SASL 사용자 (KVC_SASL_USER)")
	flag.StringVar(&c.saslPass, "sasl-pass", envOr("KVC_SASL_PASS", file.SASLPass), "SASL 비밀번호 (KVC_SASL_PASS)")
	flag.StringVar(&c.connect, "connect", envOr("KVC_CONNECT", file.ConnectURL), "Kafka Connect REST 주소 (KVC_CONNECT). 비우면 Connect 감시 안 함")
	showVersion := flag.Bool("version", false, "버전 출력")
	flag.Parse()
	if *showVersion {
		mode.AttachConsole()
		fmt.Println("kv-console", version)
		os.Exit(0)
	}
	if c.console {
		c.status = true
	}
	return c, path
}

// main 은 설정을 읽고 런처에 넘긴다.
func main() {
	c, cfgPath := parseConfig()
	if err := run(c, cfgPath); err != nil {
		log.Fatal(err)
	}
}

// displayAddr 는 ":8080" 같은 주소를 브라우저에서 열 수 있는 형태로 바꾼다.
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}
