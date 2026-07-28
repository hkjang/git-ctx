# git-ctx

`git-ctx`는 사내 Bitbucket Server 6.9.1과 GitLab의 문서·코드 예제를 색인해
Context7과 같은 두 단계 MCP 흐름으로 제공하는 온프레미스 개발 지식 플랫폼입니다.

현재 저장소는 실행 가능한 기반 구현을 포함합니다.

- MCP Streamable HTTP `/mcp`: `initialize`, `tools/list`, `tools/call`, 세션 SSE와 종료
- Context7 호환 `resolve-library-id`, `query-docs`와 선택 가능한 Strict 모드
- Library ID 없이 ACL 범위에서 찾는 `search-repositories`, Bitbucket/GitLab Query API 기반 `search-source`
- 관리자 MCP 키 전용 `get-platform-status`, `list-index-jobs`, `reindex-repository`
- Go AST와 Java·TypeScript·Python·SQL 구조 분석 기반 `find-symbol`, `get-symbol-context`
- 언어·디렉터리·주요 파일·진입점을 요약하는 ACL 기반 `get-repository-map`
- import·호출·데이터 관계를 색인하는 `trace-dependencies`
- 두 ref의 심볼 변경과 의존 코드를 연결하는 `compare-refs`, `get-change-impact`
- 검색 후보 단계의 저장소 ACL 적용과 브랜치·태그별 조회
- 사용자 API 키 생성·목록·중지·폐기 및 HMAC 기반 비가역 저장
- SQLite와 PostgreSQL 공통 스키마
- PostgreSQL 기동 실패 시 SQLite 복구 모드와 관리자 연결 시험·논리 데이터 이전
- 암호화된 동적 관리자 설정 및 불변 설정 이력·감사 로그
- 암호화 DB 또는 Vault KV v2 기반 관리 Secret 등록·회전·중지와 `secret://` 설정 참조
- 네 항목으로 단순화한 Keycloak OIDC 설정, 자동 Issuer·Redirect, Discovery/JWKS 검증과 동일 이름 역할 매핑
- Keycloak Authorization Code+PKCE 사용자 로그인과 HttpOnly 서버 세션
- SSO 로그인과 최초 관리자 복구 진입 분리, 최고관리자용 사용자 CRUD·상태·역할 관리
- Bitbucket Server 6.9.1 및 GitLab API v4 소스 어댑터
- 저장소 ACL 동기화, commit diff 증분 수집, 문서·심볼 원자적 staging, content hash 기반 embedding 재사용
- 서명 검증 Webhook, 이벤트 멱등 처리 및 ref별 작업 큐
- 우측 상단 프로필 메뉴, Ctrl/Cmd+K 빠른 이동, 개인 MCP 키 관리와 분리된 관리자 웹 화면
- 키 회전·중지·재활성화와 CIDR·저장소·도구·분/시/일 호출 제한
- 동적 소스 설정 기반 Worker, 재시도·지수 백오프와 polling 무결성 보정
- 저장소 발견·등록·재색인 및 작업 운영 화면
- Readiness, Prometheus 지표와 hardened Kubernetes Kustomize 배포
- 관리자 설정 기반 OTLP HTTP tracing과 W3C trace context 전파
- 인앱 알림과 Webhook·사내 메신저·SMTP 외부 전달 Outbox, 연결 시험·재시도·관리 이력
- SQLite/PostgreSQL 공통 암호화 예약 백업, 보존 및 트랜잭션 복원 UI/API
- BM25와 256차원 로컬 벡터 결합 검색, 색인 전 Secret 차단·마스킹
- ACL 필터 이후 사내 `/v1/rerank` 재순위화와 장애 시 하이브리드 점수 fallback
- 모델 미설정 시 ACL 선검사 후 Bitbucket/GitLab 서버측 Query Search API 모드
- 선택적 OpenSearch BM25 증분 projection, 질의 단계 ACL filter와 DB 원문 재검증
- ACL 적용 정답 데이터셋 기반 Recall@K·MRR·nDCG@K 검색 품질 회귀 게이트
- 버전형 DB migration 및 PostgreSQL 다중 Worker `SKIP LOCKED`
- Docker 및 PostgreSQL Compose 배포

## 로컬 실행

SQLite는 개발·평가용이며 운영 기본은 PostgreSQL입니다.

```bash
export GIT_CTX_DB_DSN='file:git-ctx.db?_foreign_keys=on&_busy_timeout=5000'
go run ./cmd/git-ctx
```

실제 Bootstrap 입력은 `GIT_CTX_DB_DSN` 하나뿐이며 driver는 DSN에서 자동 판별됩니다.
Keycloak이 설정되지 않은 최초 기동에는 `backups/bootstrap-admin.token`이 권한 0600으로
한 번 생성됩니다. 화면의 `최초 관리자 설정`에서 입력하면 되고 Keycloak 설정 저장
후 실제 `platform-admin` Keycloak 로그인이 성공하면 토큰, 30분 HttpOnly 초기 설정
세션과 파일을 폐기합니다. 서비스 버전은
로그인 전 상단과 로그인 후 내 계정 화면에 표시됩니다.

```bash
curl -H "Authorization: Bearer $(cat backups/bootstrap-admin.token)" \
  http://localhost:4747/api/v1/admin/settings
```

Keycloak 장애나 잘못된 설정으로 모든 관리자가 잠긴 경우 서버 콘솔에서 1회용 복구
토큰을 생성합니다. 토큰 원문은 저장되지 않고 기본 15분 뒤 만료되며 한 번 소비하면
재사용할 수 없습니다.

```bash
GIT_CTX_DB_DSN='postgres://...' ./git-ctx recovery-token --ttl 15m
```

출력된 토큰을 `/admin?recovery=1`에서 입력하면 30분짜리 제한된 최고관리자 세션이
생성됩니다. 이 세션은 영구 MCP API 키를 만들 수 없으며 Keycloak 설정 복구 후 즉시
로그아웃해야 합니다.

Keycloak 설정은 먼저 Discovery 연결을 시험한 뒤 저장됩니다.

```bash
curl -X POST -H "Authorization: Bearer $(cat backups/bootstrap-admin.token)" \
  -H 'Content-Type: application/json' \
  -d '{"issuerUrl":"https://sso.company/realms/company","clientId":"git-ctx",
       "bitbucketUserSlugClaim":"bitbucket_user_slug",
       "realmRoleMappings":{"git-ctx-admin":"platform-admin"}}' \
  http://localhost:4747/api/v1/admin/settings/keycloak/test
```

## 데이터베이스

PostgreSQL은 `GIT_CTX_DB_DSN` 하나만으로 최초 연결합니다.

```bash
GIT_CTX_DB_DSN='postgres://gitctx:password@db:5432/gitctx?sslmode=require'
```

마이그레이션은 시작할 때 멱등 실행됩니다. 연결에 실패하면
`backups/recovery.db` SQLite로 복구 기동됩니다. 관리자는 “데이터베이스” 메뉴에서 새
PostgreSQL DSN을 읽기 전용으로 시험한 뒤 명시적 확인과 사유를 입력해 스키마·데이터를
논리 이전할 수 있습니다. 성공 후 재시작하면 암호화 저장된 검증 DSN을 활성화합니다.

설정 암호화 키와 API-key pepper는 최초 Bootstrap DSN을 도메인 분리해 파생합니다.
따라서 관리자 전환 뒤에도 환경의 Bootstrap DSN 문자열을 임의 변경하지 말고 Secret
Store에 보관해야 합니다. 관리자 DSN 원문은 조회·로그·감사 기록에 남지 않습니다.

상세 설계와 구현 상태는 [docs/requirements.md](docs/requirements.md) 및
[docs/operations.md](docs/operations.md)를 참고하십시오. 구현 증거, 미구현 범위와
실환경 승인 게이트는 [docs/completion-audit.md](docs/completion-audit.md)에
분리해 기록했습니다.

REST 계약은 [docs/openapi.yaml](docs/openapi.yaml), 동적 설정 예시는
[docs/configuration.md](docs/configuration.md)에 있습니다.

## 운영 상태

- 생존 확인: `GET /healthz`
- DB 포함 readiness: `GET /readyz`
- Prometheus: `GET /metrics`
- 관리자 상태: `GET /api/v1/admin/health`

Kubernetes 배포는 [deploy/kubernetes/README.md](deploy/kubernetes/README.md)를
참고하십시오.

인터넷이 차단된 환경의 이미지 반입과 실행 절차는
[docs/offline-deployment.md](docs/offline-deployment.md)를 참고하십시오.
