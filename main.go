// Package main 은 Kafka V-Console 단일 바이너리 진입점이다.
// 오프라인 환경에서 Kafka 클러스터 상태를 읽기 전용으로 시각화한다.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"kvconsole/web"
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

// config 는 CLI 플래그/환경변수로 받은 실행 설정이다.
type config struct {
	listen, brokers                  string
	tls, tlsSkipVerify               bool
	tlsCA, tlsCert, tlsKey           string
	saslMechanism, saslUser, saslPass string

	// status 하위 명령(텍스트 모드) 전용
	status, watch, all bool
	interval           time.Duration
	lagWarn            int64
}

// parseConfig 는 플래그를 파싱한다. 각 플래그는 KVC_* 환경변수로도 지정 가능하다.
// 첫 인자가 "status" 이면 웹 서버 대신 텍스트 상태 출력 모드로 동작한다.
func parseConfig() config {
	var c config
	if len(os.Args) > 1 && os.Args[1] == "status" {
		c.status = true
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	flag.BoolVar(&c.watch, "watch", false, "[status] Ctrl+C 전까지 -interval 마다 화면 갱신")
	flag.DurationVar(&c.interval, "interval", 5*time.Second, "[status] 처리량 측정/갱신 간격 (최소 3s)")
	flag.BoolVar(&c.all, "all", false, "[status] 내부 토픽(__*, _*) 포함")
	flag.Int64Var(&c.lagWarn, "lag-warn", 1000, "[status] 이 값 이상의 lag 에 ! 표시")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "사용법:\n  kv-console [플래그]          웹 UI 실행\n  kv-console status [플래그]   텍스트로 클러스터 상태 출력 (웹 포트를 열 수 없을 때)\n\n플래그:\n")
		flag.PrintDefaults()
	}
	flag.StringVar(&c.listen, "listen", envOr("KVC_LISTEN", ":8080"), "HTTP 리슨 주소 (KVC_LISTEN)")
	flag.StringVar(&c.brokers, "brokers", envOr("KVC_BROKERS", "localhost:9092"), "쉼표로 구분한 브로커 목록 (KVC_BROKERS)")
	flag.BoolVar(&c.tls, "tls", envOr("KVC_TLS", "") == "true", "TLS 사용 (KVC_TLS=true)")
	flag.BoolVar(&c.tlsSkipVerify, "tls-skip-verify", envOr("KVC_TLS_SKIP_VERIFY", "") == "true", "서버 인증서 검증 생략 (KVC_TLS_SKIP_VERIFY=true)")
	flag.StringVar(&c.tlsCA, "tls-ca", envOr("KVC_TLS_CA", ""), "CA 인증서 PEM 경로 (KVC_TLS_CA)")
	flag.StringVar(&c.tlsCert, "tls-cert", envOr("KVC_TLS_CERT", ""), "mTLS 클라이언트 인증서 PEM (KVC_TLS_CERT)")
	flag.StringVar(&c.tlsKey, "tls-key", envOr("KVC_TLS_KEY", ""), "mTLS 클라이언트 키 PEM (KVC_TLS_KEY)")
	flag.StringVar(&c.saslMechanism, "sasl-mechanism", envOr("KVC_SASL_MECHANISM", ""), "PLAIN | SCRAM-SHA-256 | SCRAM-SHA-512 (KVC_SASL_MECHANISM)")
	flag.StringVar(&c.saslUser, "sasl-user", envOr("KVC_SASL_USER", ""), "SASL 사용자 (KVC_SASL_USER)")
	flag.StringVar(&c.saslPass, "sasl-pass", envOr("KVC_SASL_PASS", ""), "SASL 비밀번호, 프로세스 목록 노출을 피하려면 환경변수 권장 (KVC_SASL_PASS)")
	showVersion := flag.Bool("version", false, "버전 출력")
	flag.Parse()
	if *showVersion {
		fmt.Println("kv-console", version)
		os.Exit(0)
	}
	return c
}

// kafkaOpts 는 설정으로부터 franz-go 클라이언트 옵션을 만든다.
func kafkaOpts(c config) ([]kgo.Opt, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(strings.Split(c.brokers, ",")...),
		kgo.ClientID("kv-console"),
		kgo.DialTimeout(5 * time.Second),
	}
	// TLS 관련 옵션 중 하나라도 있으면 TLS 를 켠다
	if c.tls || c.tlsCA != "" || c.tlsCert != "" || c.tlsSkipVerify {
		tc := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.tlsSkipVerify}
		if c.tlsCA != "" {
			pem, err := os.ReadFile(c.tlsCA)
			if err != nil {
				return nil, fmt.Errorf("CA 읽기 실패: %w", err)
			}
			tc.RootCAs = x509.NewCertPool()
			if !tc.RootCAs.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("CA PEM 파싱 실패: %s", c.tlsCA)
			}
		}
		if c.tlsCert != "" {
			cert, err := tls.LoadX509KeyPair(c.tlsCert, c.tlsKey)
			if err != nil {
				return nil, fmt.Errorf("클라이언트 인증서 로드 실패: %w", err)
			}
			tc.Certificates = []tls.Certificate{cert}
		}
		opts = append(opts, kgo.DialTLSConfig(tc))
	}
	switch strings.ToUpper(c.saslMechanism) {
	case "":
	case "PLAIN":
		opts = append(opts, kgo.SASL(plain.Auth{User: c.saslUser, Pass: c.saslPass}.AsMechanism()))
	case "SCRAM-SHA-256":
		opts = append(opts, kgo.SASL(scram.Auth{User: c.saslUser, Pass: c.saslPass}.AsSha256Mechanism()))
	case "SCRAM-SHA-512":
		opts = append(opts, kgo.SASL(scram.Auth{User: c.saslUser, Pass: c.saslPass}.AsSha512Mechanism()))
	default:
		return nil, fmt.Errorf("지원하지 않는 SASL 메커니즘: %s", c.saslMechanism)
	}
	return opts, nil
}

// main 은 Kafka 관리 클라이언트와 HTTP 서버를 띄운다.
func main() {
	c := parseConfig()
	opts, err := kafkaOpts(c)
	if err != nil {
		log.Fatal(err)
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer cl.Close()

	s := &server{opts: opts, adm: kadm.NewClient(cl)}
	if c.status {
		if err := runStatus(s, c, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Windows 레지스트리가 .js 를 text/plain 으로 덮어쓰면 브라우저가 ES 모듈 로드를 거부하므로 고정한다
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/cluster", s.handleCluster)
	mux.HandleFunc("GET /api/topics/{name}/config", s.handleTopicConfig)
	mux.HandleFunc("GET /api/topics/{name}/messages", s.handleMessages)
	// embed 파일은 수정 시각이 없어 브라우저가 바이너리 교체 후에도 옛 JS 를 쓰므로 매번 재검증시킨다
	static := http.FileServerFS(web.Files)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})

	// 연결 확인 로그만 남기고, 실패해도 UI 에서 오류를 보여주도록 계속 진행한다
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if _, err := s.adm.BrokerMetadata(ctx); err != nil {
		log.Printf("경고: 브로커 연결 실패 (%s): %v", c.brokers, err)
	}
	cancel()

	log.Printf("kv-console %s  http://%s  -> kafka %s", version, displayAddr(c.listen), c.brokers)
	srv := &http.Server{Addr: c.listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// displayAddr 는 ":8080" 같은 주소를 브라우저에서 열 수 있는 형태로 바꾼다.
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}
