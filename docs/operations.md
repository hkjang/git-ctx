# 운영 가이드

## Bootstrap

필수 외부 값은 DB DSN, 32바이트 API 키 pepper, 32바이트 설정 암호화 키다.
초기 관리자 토큰은 Keycloak 설정 직후 환경에서 제거하고 재시작한다. 운영에서는 TLS
종단 뒤에 배치하고 프록시가 신뢰할 수 없는 전달 헤더를 제거해야 한다.

## 비밀 회전

API 키 pepper를 바꾸면 기존 키를 검증할 수 없으므로 이중 pepper 검증 기간을 포함한
계획 회전이 필요하다. Master key 회전은 모든 `system_settings`와
`setting_versions` 값을 트랜잭션으로 재암호화한 뒤 수행한다. DB 백업과 복구 시험
없이 키를 교체하지 않는다.

사용자 MCP 키 회전은 UI에서 0~1,440분의 중복 유효기간을 선택한다. 신규 키는 기존
키의 도구·CIDR·저장소·호출량 제한과 만료일을 상속하며 원문은 한 번만 표시된다.
보안 관리자는 전체 키 목록에서 즉시 강제 폐기할 수 있다.

## 장애 확인

- `/healthz`와 DB 연결 상태를 먼저 확인한다.
- MCP 오류는 `mcp_calls`, 관리자 변경은 `audit_logs`에서 prefix만으로 추적한다.
- 권한 동기화가 실패하면 새 권한을 추정하지 않고 이전 캐시 만료 후 Fail Closed한다.
- Bitbucket 장애 중에는 마지막 검증된 색인만 읽고 UI에 색인 시각을 표시한다.
- Worker는 작업을 원자적으로 claim하고 최대 5회 지수 백오프로 재시도한다.
- 15분 이상 `running`인 작업은 Scheduler가 lease 만료로 판단해 다시 대기시킨다.
- `index.pollingMinutes` 기본값은 30분이며 Webhook 누락을 커밋/ref 재색인으로 보정한다.

## 상태와 지표

`/healthz`는 프로세스 생존, `/readyz`는 DB 연결을 확인한다. `/metrics`는 저장소,
청크, MCP 호출, 활성 키, 대기·실패 작업과 Go goroutine을 Prometheus 형식으로
제공한다. 관리자 `/api/v1/admin/health`는 같은 정보를 JSON으로 제공한다.

## 백업과 복구

PostgreSQL 운영 환경은 매일 `pg_dump --format=custom` 백업을 암호화 저장소로
전송하고 주기적으로 별도 DB에 `pg_restore` 검증한다. SQLite 단일 노드는 WAL
checkpoint 이후 SQLite online backup API 또는 스토리지 스냅샷을 사용한다.
DB에는 설정 암호문이 저장되므로 `GIT_CTX_MASTER_KEY`는 DB 백업과 분리하여 KMS에
보관한다. 목표는 RPO 24시간, RTO 4시간이며 분기별 복구 훈련으로 입증한다.

## 데이터 보호

API 키, Keycloak secret, Bitbucket PAT, 문서 전체 원문은 로그에 남기지 않는다.
제한·비밀 등급 파일과 `.env`, 인증서, 개인키는 파싱 전에 제외한다. 검색 문서는
비신뢰 데이터이며 문서 안의 명령문이 시스템 설정이나 권한을 변경할 수 없다.
개인키 블록은 파일 전체를 차단하고 자격증명 대입문과 클라우드 접근 키는 청크 저장
전에 `[REDACTED]`로 치환한다. 탐지 경로와 조치는 `index_security_events`에
기록되지만 탐지된 원문은 기록하지 않는다.

## DB migration

모든 migration은 `schema_migrations`에 버전이 기록된다. PostgreSQL에서는 시작 시
advisory lock을 획득하므로 여러 Pod가 동시에 배포되어도 DDL은 한 번만 실행된다.
배포 전 staging 백업 복원본에 새 이미지의 migration과 rollback 절차를 검증한다.
