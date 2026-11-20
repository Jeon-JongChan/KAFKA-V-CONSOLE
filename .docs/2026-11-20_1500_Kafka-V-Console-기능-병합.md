# Kafka V-Console 기능 병합

## 원인

독립 저장소를 분리한 뒤에도 `Google-Jules-Temp-Repo/KAFKA-V-CONSOLE`에서 개발이 이어져, 최신 콘솔 기능이 독립 저장소에 반영되지 않았다.

## 조치

- 원본의 Kafka 하위 경로가 마지막으로 바뀐 `05df600` 기준으로 `app/`과 `.tools/` 구조, Windows 데스크톱, Linux 콘솔, 브라우저 모드, 연결 프로필, Kafka Connect 감시, 로컬 데모를 이관했다. 상위 저장소의 다른 프로젝트와 로컬 바이너리·캐시는 포함하지 않았다.
- 독립 저장소의 Go API·콘솔·번역 키 테스트와 JavaScript 그래프 회귀 검사를 보존했다. 빌드 시 포터블 Node가 있으면 그래프 검사도 실행한다.
- Linux 런처가 브로커 환경변수에 기본값을 강제로 넣지 않도록 수정했다. 이제 앱에서 `CLI > 환경변수 > 저장된 프로필 > 기본값` 우선순위를 유지한다.

## 사용

- `.\build.ps1`로 빌드하면 실행 파일이 프로젝트 루트에 생성된다. Go 도구로 직접 실행할 때는 `go run ./app`을 사용한다.
- Windows는 데스크톱, Linux는 텍스트 상태가 기본이다. 브라우저를 쓰려면 `-listen 127.0.0.1:8080`을 지정한다.
- UI 설정의 Kafka Connect 주소 또는 `KVC_CONNECT`/`-connect`로 Connect 감시를 켤 수 있다. 브라우저 모드의 설정 API에는 인증이 없으므로 외부 네트워크에 직접 노출하지 않는다.

## 검증

- `go test -mod=readonly ./...`와 Node 그래프 회귀 검사 2종을 통과했다. 원본에서 쓰던 Go SDK와 모듈 캐시로 Windows amd64, Linux amd64·arm64 바이너리를 빌드했다.
- WSL에서 Linux 바이너리를 실행해 `/api/settings`, Connect 미설정 시 `/api/connect` 응답과 내장 `connect.js` 제공을 확인했다.
- 가짜 Kafka 바이너리는 빌드했으나 데모 포트 `19092`가 이미 사용 중이어서 데모 클러스터 연결은 확인하지 못했다. 실제 Kafka/Connect와 Windows 데스크톱 창은 확인하지 않았다.
