# GLOSSARY

KAFKA-V-CONSOLE 도메인·아키텍처 용어.

## 연결 프로필 (Connection Profile)

Kafka 클러스터에 붙는 데 필요한 접속 정보(브로커, TLS, SASL).\
디스크(`kv-console.json`)·UI 설정·CLI 플래그가 같은 개념을 가리킨다.\
실행 모드(`-listen`, `-console`)는 포함하지 않는다.

## 세션 (Session)

열린 Kafka 연결과 스냅샷 캐시. 재연결 시 수집기만 갈아끼우고 처리량 카운터는 유지한다.\
HTTP·데스크톱·콘솔은 세션의 심만 사용한다.

## 런처 (Launcher)

프로세스 진입 시 모드를 고른다: 텍스트 콘솔 / HTTP 웹 UI / Windows 데스크톱 창.

## 스냅샷 (Snapshot)

UI·콘솔이 한 번에 받아가는 클러스터 상태(브로커·토픽·그룹·처리량).

## Connect 감시 (Connect watch)

Kafka Connect REST 에 등록된 커넥터·태스크·워커 상태.
싱크 부하는 컨슈머 그룹 `connect-<커넥터 이름>`(또는 `consumer.override.group.id`)의 소비율·Lag.
소스 부하는 그 커넥터가 쓰는 토픽의 유입률. 같은 토픽의 다른 프로듀서와 합산될 수 있다.
