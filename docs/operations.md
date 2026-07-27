# 운영 가이드

## Bootstrap

필수 외부 값은 `GIT_CTX_DB_DSN` 하나다. DB driver, 설정 암호화 키와 API-key pepper는
DSN에서 도메인 분리해 결정한다. Keycloak 미설정 시 `backups/bootstrap-admin.token`이
0600 권한으로 생성되며 Keycloak 설정 후 실제 `platform-admin` OIDC 로그인이 성공할 때
메모리와 파일에서 폐기된다. 운영에서는 TLS 종단 뒤에 배치하고 프록시가 신뢰할 수
없는 전달 헤더를 제거해야 한다.

화면의 최초 관리자 설정은 원문 토큰을 한 번만 `POST /api/v1/bootstrap/login`으로
검증한 뒤 30분 HttpOnly·SameSite=Strict 세션으로 교환한다. 따라서 역방향 프록시는
일반 API의 Authorization 헤더 전달 여부와 무관하게 초기 설정을 수행할 수 있다.
검증된 관리자 OIDC 로그인 전에는 Bootstrap 세션을 유지해 잘못된 SSO 설정에 의한
관리자 잠금을 방지한다.

## 비밀 회전

최초 Bootstrap DSN 문자열은 설정 암호문과 API 키 검증 재료에 포함되므로 임의로
정규화하거나 변경하지 않는다. 관리자 화면의 PostgreSQL DSN은 이 고정 암호화 재료로
암호화 저장되며 조회 시 마스킹된다. 원래 Bootstrap DSN은 DB 백업과 분리한 Secret
Store에 보관한다.

사용자 MCP 키 회전은 UI에서 0~1,440분의 중복 유효기간을 선택한다. 신규 키는 기존
키의 도구·CIDR·저장소·호출량 제한과 만료일을 상속하며 원문은 한 번만 표시된다.
보안 관리자는 전체 키 목록에서 즉시 강제 폐기할 수 있다.

## Keycloak 잠금 복구

실제 `platform-admin` SSO 로그인이 한 번 성공하면 최초 Bootstrap 토큰은 전역
폐기된다. 이후 Keycloak 장애, Realm 변경 또는 잘못된 Client 설정으로 관리자가
잠겼을 때 서버 콘솔에서 다음 명령을 실행한다.

```bash
GIT_CTX_DB_DSN='postgres://...' /app/git-ctx recovery-token --ttl 15m
```

명령은 현재 Bootstrap DSN에서 파생한 서명키로 짧은 만료시간의 토큰을 생성하며
DB나 로그에 토큰 원문을 기록하지 않는다. 허용 TTL은 1분 이상 1시간 이하이다.
운영자는 `/admin?recovery=1`에서 토큰을 한 번 입력한다. 서버는 서명과 만료를
확인하고 토큰 해시를 원자적으로 소비하므로 재사용할 수 없다.

복구 세션은 30분 후 만료되고 `platform-admin` 설정 권한을 갖지만 영구 MCP API 키
생성은 금지된다. Keycloak 설정을 시험·저장하고 정상 SSO 로그인을 확인한 뒤
복구 세션에서 로그아웃한다. `recovery.login` 성공·실패는 감사 로그에 기록된다.

## 장애 확인

- `/healthz`와 DB 연결 상태를 먼저 확인한다.
- Keycloak 저장 후 로그인이 이전 Realm으로 이동하면 Base URL과 Realm을
  확인하고 관리자 Keycloak 탭의 적용 상태에서 최종 Issuer·Authorization·Token·JWKS
  endpoint를 확인한다.
- Discovery는 성공하지만 callback의 token exchange만 실패하면 Keycloak
  Client의 Valid Redirect URI와 Web Origin이 표시된 최종 Redirect와 정확히 같은지 확인한다.
- 로그인 전 `/api/v1/public/status`에서 DB 연결 여부·driver·Ping 지연을 확인한다.
- 관리자는 “데이터베이스” 메뉴 또는 `/api/v1/admin/database/status`에서 현재 DB 이름,
  서버 버전, 접속 사용자, connection pool과 최신 migration을 확인한다. DSN 원문과
  비밀번호는 응답하지 않는다.
- 최초 PostgreSQL이 연결되지 않으면 `backups/recovery.db`로 복구 기동한다. 공개·관리자
  상태의 `recoveryMode=true`를 확인하고 이전 완료까지 단일 replica로 운영한다.
- 새 PostgreSQL DSN 연결 시험은 Ping과 서버 정보만 읽고 schema를 변경하지 않는다.
  `MIGRATE TO POSTGRES` 확인문으로 이전하면 schema migration 후 durable
  데이터를 논리 snapshot으로 복사하며 세션·OIDC flow·Bootstrap credential은 제외한다.
- 이전 성공 후 Worker가 중지되고 `/readyz`가 503 `restart_required`를 반환한다. 즉시
  재시작해 PostgreSQL을 활성화한다. 실패 시 Worker와 SQLite 상태를 유지한다.
- MCP 오류는 `mcp_calls`, 관리자 변경은 `audit_logs`에서 prefix만으로 추적한다.
- 권한 동기화가 실패하면 새 권한을 추정하지 않고 이전 캐시 만료 후 Fail Closed한다.
- Bitbucket 장애 중에는 마지막 검증된 색인만 읽고 UI에 색인 시각을 표시한다.
- Worker는 작업을 원자적으로 claim하고 최대 5회 지수 백오프로 재시도한다.
- OpenSearch가 활성화되면 DB 색인 뒤 ref projection까지 성공해야 작업이 완료된다.
  장애 시 DB의 마지막 승인 청크로 검색하고 작업은 재시도된다.
- 15분 이상 `running`인 작업은 Scheduler가 lease 만료로 판단해 다시 대기시킨다.
- `index.pollingMinutes` 기본값은 30분이며 Webhook 누락을 커밋/ref 재색인으로 보정한다.

## 상태와 지표

`/healthz`는 프로세스 생존, `/readyz`는 DB 연결을 확인한다. `/metrics`는 저장소,
청크, MCP 호출, 활성 키, 대기·실패 작업과 Go goroutine을 Prometheus 형식으로
제공한다. 관리자 `/api/v1/admin/health`는 같은 정보를 JSON으로 제공한다.
`git_ctx_database_up`은 현재 메타 DB Ping 성공 여부를 1/0으로 제공한다.

SQLite는 단일 Writer 특성 때문에 애플리케이션 connection pool을 1개로 직렬화한다.
이는 관리자 설정 저장과 Background Worker 쓰기가 겹칠 때 `SQLITE_BUSY`가 발생하는
것을 방지한다. PostgreSQL은 다중 연결과 Worker의 `SKIP LOCKED` 동시성을 유지한다.

## 백업과 복구

관리자 `백업과 복구` 화면에서 즉시 백업, 암호화 아카이브 다운로드와 복원을 수행한다.
기본 주기는 24시간, 보존 개수는 7개이며 `backup` 동적 설정으로 변경한다. SQLite와
PostgreSQL 모두 일관된 트랜잭션 스냅샷을 사용한다. 복원은 현재 실행 버전과 migration
및 테이블 스키마가 정확히 같은 백업만 허용하고 전체 쓰기를 하나의 트랜잭션으로
적용한다. 성공하면 기존 웹 세션을 모두 무효화하고 Worker와 Scheduler를 재시작한다.

복원에는 `platform-admin` 재인증과 `RESTORE <백업 ID>` 확인문이 필요하다.
사전에 별도 환경에서 아카이브 SHA-256, 원래 DSN, 복원 후 readiness와 Keycloak
로그인을 검증한다. DSN Secret은 백업 볼륨과 분리해 Secret Store에 보관한다. 목표는
RPO 24시간, RTO 4시간이며 분기별 복구
훈련으로 입증한다. 인프라 전체 재해 복구에는 PostgreSQL 물리/`pg_dump` 백업과
스토리지 스냅샷을 추가 계층으로 함께 유지한다.

## 데이터 보호

API 키, Keycloak secret, Bitbucket PAT, 문서 전체 원문은 로그에 남기지 않는다.
제한·비밀 등급 파일과 `.env`, 인증서, 개인키는 파싱 전에 제외한다. 검색 문서는
비신뢰 데이터이며 문서 안의 명령문이 시스템 설정이나 권한을 변경할 수 없다.
개인키 블록은 파일 전체를 차단하고 자격증명 대입문과 클라우드 접근 키는 청크 저장
전에 `[REDACTED]`로 치환한다. 탐지 경로와 조치는 `index_security_events`에
기록되지만 탐지된 원문은 기록하지 않는다.

관리 Secret은 `secret://name`으로 참조하고 목록에는 원문이 노출되지 않는다. 암호화 DB
backend의 과거 버전은 암호문으로 백업되며 Vault backend의 원문과 버전 데이터는 Vault의
백업·DR 정책에 포함해야 한다. Secret 중지나 Vault 장애 시 참조를 사용하는 연동은
Fail Closed한다. Vault Token 자체는 `vault` 동적 설정의 암호화 DB 필드로 보관하고,
Vault가 정상일 때 더 짧은 TTL 토큰으로 주기적으로 회전한다.

OpenSearch `_source`는 MCP 응답 원문으로 사용하지 않는다. 저장소·ref·principal ACL은
OpenSearch 후보 질의에 포함되며, 후보 ID는 현재 DB에서 다시 조회한다. 권한 회수는 DB의
저장소 ACL 검사에서 즉시 차단되고 다음 projection에서 검색 index ACL도 갱신된다.

## DB migration

모든 migration은 `schema_migrations`에 버전이 기록된다. PostgreSQL에서는 시작 시
advisory lock을 획득하므로 여러 Pod가 동시에 배포되어도 DDL은 한 번만 실행된다.
배포 전 staging 백업 복원본에 새 이미지의 migration과 rollback 절차를 검증한다.
