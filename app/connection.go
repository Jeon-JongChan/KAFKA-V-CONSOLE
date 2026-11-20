// 연결 프로필: Kafka 접속 정보의 단일 소스.
// 디스크(json)·UI·CLI 가 이 모듈의 심을 통해 읽고 쓴다.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// connectionProfile 은 Kafka 에 붙는 데 필요한 접속 정보다. listen/콘솔 모드는 포함하지 않는다.
type connectionProfile struct {
	Brokers       string `json:"brokers"`
	TLS           bool   `json:"tls"`
	TLSSkipVerify bool   `json:"tlsSkipVerify"`
	TLSCA         string `json:"tlsCA"`
	TLSCert       string `json:"tlsCert"`
	TLSKey        string `json:"tlsKey"`
	SASLMechanism string `json:"saslMechanism"`
	SASLUser      string `json:"saslUser"`
	SASLPass      string `json:"saslPass,omitempty"`
	// ConnectURL 은 Kafka Connect REST 루트다. 비어 있으면 Connect 감시를 하지 않는다.
	ConnectURL string `json:"connectUrl"`
}

// defaultConfigPath 는 KVC_CONFIG 또는 실행 파일 옆 kv-console.json 경로다.
func defaultConfigPath() string {
	if p := os.Getenv("KVC_CONFIG"); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return "kv-console.json"
	}
	return filepath.Join(filepath.Dir(exe), "kv-console.json")
}

// loadConnectionProfile 은 설정 파일을 읽는다. 없으면 빈 프로필을 돌려준다.
func loadConnectionProfile(path string) (connectionProfile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return connectionProfile{}, nil
		}
		return connectionProfile{}, err
	}
	var p connectionProfile
	if err := json.Unmarshal(b, &p); err != nil {
		return connectionProfile{}, err
	}
	return p, nil
}

// saveConnectionProfile 은 연결 프로필을 JSON 으로 저장한다 (권한 0600).
func saveConnectionProfile(path string, p connectionProfile) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// settingsDTO 는 UI 설정 폼용 JSON 이다. 비밀번호 본문 대신 saslPassSet 만 노출한다.
type settingsDTO struct {
	Brokers       string `json:"brokers"`
	TLS           bool   `json:"tls"`
	TLSSkipVerify bool   `json:"tlsSkipVerify"`
	TLSCA         string `json:"tlsCA"`
	TLSCert       string `json:"tlsCert"`
	TLSKey        string `json:"tlsKey"`
	SASLMechanism string `json:"saslMechanism"`
	SASLUser      string `json:"saslUser"`
	SASLPass      string `json:"saslPass"`
	SASLPassSet   bool   `json:"saslPassSet"`
	ConnectURL    string `json:"connectUrl"`
	ConfigPath    string `json:"configPath"`
}

// mergeConnectionUpdate 는 UI/DTO 갱신을 반영한다. 빈 비밀번호는 기존 값을 유지한다.
func mergeConnectionUpdate(cur connectionProfile, in settingsDTO) connectionProfile {
	next := cur
	next.Brokers = in.Brokers
	next.TLS = in.TLS
	next.TLSSkipVerify = in.TLSSkipVerify
	next.TLSCA = in.TLSCA
	next.TLSCert = in.TLSCert
	next.TLSKey = in.TLSKey
	next.SASLMechanism = in.SASLMechanism
	next.SASLUser = in.SASLUser
	if in.SASLPass != "" {
		next.SASLPass = in.SASLPass
	}
	next.ConnectURL = strings.TrimSpace(in.ConnectURL)
	return next
}

// settingsView 는 UI 에 내려줄 안전 뷰다 (비밀번호 본문 대신 설정 여부만).
func (p connectionProfile) settingsView(path string) settingsDTO {
	return settingsDTO{
		Brokers:       p.Brokers,
		TLS:           p.TLS,
		TLSSkipVerify: p.TLSSkipVerify,
		TLSCA:         p.TLSCA,
		TLSCert:       p.TLSCert,
		TLSKey:        p.TLSKey,
		SASLMechanism: p.SASLMechanism,
		SASLUser:      p.SASLUser,
		SASLPassSet:   p.SASLPass != "",
		ConnectURL:    p.ConnectURL,
		ConfigPath:    path,
	}
}

// openKafka 는 프로필로 franz-go 클라이언트·관리 클라이언트를 연다.
func openKafka(p connectionProfile) (opts []kgo.Opt, cl *kgo.Client, adm *kadm.Client, err error) {
	opts, err = kafkaOptsFromProfile(p)
	if err != nil {
		return nil, nil, nil, err
	}
	cl, err = kgo.NewClient(opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	return opts, cl, kadm.NewClient(cl), nil
}

// kafkaOptsFromProfile 은 연결 프로필로부터 클라이언트 옵션을 만든다.
func kafkaOptsFromProfile(p connectionProfile) ([]kgo.Opt, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(strings.Split(p.Brokers, ",")...),
		kgo.ClientID("kv-console"),
		kgo.DialTimeout(5 * time.Second),
	}
	if p.TLS || p.TLSCA != "" || p.TLSCert != "" || p.TLSSkipVerify {
		tc := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: p.TLSSkipVerify}
		if p.TLSCA != "" {
			pem, err := os.ReadFile(p.TLSCA)
			if err != nil {
				return nil, fmt.Errorf("CA 읽기 실패: %w", err)
			}
			tc.RootCAs = x509.NewCertPool()
			if !tc.RootCAs.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("CA PEM 파싱 실패: %s", p.TLSCA)
			}
		}
		if p.TLSCert != "" {
			cert, err := tls.LoadX509KeyPair(p.TLSCert, p.TLSKey)
			if err != nil {
				return nil, fmt.Errorf("클라이언트 인증서 로드 실패: %w", err)
			}
			tc.Certificates = []tls.Certificate{cert}
		}
		opts = append(opts, kgo.DialTLSConfig(tc))
	}
	switch strings.ToUpper(p.SASLMechanism) {
	case "":
	case "PLAIN":
		opts = append(opts, kgo.SASL(plain.Auth{User: p.SASLUser, Pass: p.SASLPass}.AsMechanism()))
	case "SCRAM-SHA-256":
		opts = append(opts, kgo.SASL(scram.Auth{User: p.SASLUser, Pass: p.SASLPass}.AsSha256Mechanism()))
	case "SCRAM-SHA-512":
		opts = append(opts, kgo.SASL(scram.Auth{User: p.SASLUser, Pass: p.SASLPass}.AsSha512Mechanism()))
	default:
		return nil, fmt.Errorf("지원하지 않는 SASL 메커니즘: %s", p.SASLMechanism)
	}
	return opts, nil
}

// firstNonEmpty 는 빈 문자열이 아닌 첫 값을 돌려준다.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// envBool 은 환경변수가 있으면 "true" 여부, 없으면 def 를 쓴다.
func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	return v == "true"
}
