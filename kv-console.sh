#!/bin/sh
# Kafka V-Console 실행 스크립트 (Linux). 서버 아키텍처에 맞는 바이너리를 골라 실행한다.
#
#   ./kv-console.sh                텍스트 상태 1회 (기본)
#   ./kv-console.sh -console       동일
#   ./kv-console.sh watch          텍스트 상태를 5초마다 갱신 (Ctrl+C 로 종료)
#   ./kv-console.sh -listen :8080  브라우저용 HTTP 서버
#   ./kv-console.sh status -all -interval 10s -lag-warn 5000
#
# 접속 정보는 저장된 연결 프로필을 사용하거나 실행 시 환경변수로 덮어쓴다.
#   KVC_BROKERS=10.0.0.1:9092,10.0.0.2:9092 ./kv-console.sh watch
#   (TLS/SASL: KVC_TLS, KVC_TLS_CA, KVC_SASL_MECHANISM, KVC_SASL_USER, KVC_SASL_PASS ...)
#   (Connect 감시: KVC_CONNECT=http://connect:8083)

dir=$(cd "$(dirname "$0")" && pwd)
case "$(uname -m)" in
  x86_64|amd64)  bin="$dir/kv-console-linux-amd64" ;;
  aarch64|arm64) bin="$dir/kv-console-linux-arm64" ;;
  *) echo "지원하지 않는 아키텍처: $(uname -m)" >&2; exit 1 ;;
esac
[ -f "$bin" ] || { echo "바이너리 없음: $bin" >&2; exit 1; }
# Windows 에서 복사해 오면 실행 권한이 빠지는 경우가 많다
[ -x "$bin" ] || chmod +x "$bin"

case "$1" in
  status|-console) shift; exec "$bin" -console "$@" ;;
  watch)  shift; exec "$bin" -console -watch "$@" ;;
  *)      exec "$bin" "$@" ;;
esac
