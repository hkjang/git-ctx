# git-ctx

`git-ctx`는 사내 Bitbucket Server 6.9.1과 GitLab의 문서·코드 예제를 색인해
Context7과 같은 두 단계 MCP 흐름으로 제공하는 온프레미스 개발 지식 플랫폼입니다.

현재 저장소는 실행 가능한 기반 구현을 포함합니다.

- MCP Streamable HTTP `/mcp`: `initialize`, `tools/list`, `tools/call`
- Context7 호환 `resolve-library-id`, `query-docs`
- 검색 후보 단계의 저장소 ACL 적용과 브랜치·태그별 조회
- 사용자 API 키 생성·목록·중지·폐기 및 HMAC 기반 비가역 저장
- SQLite와 PostgreSQL 공통 스키마
- 암호화된 동적 관리자 설정 및 불변 설정 이력·감사 로그
- Keycloak OIDC Discovery/JWKS 검증과 Realm·Client 역할 매핑
- Keycloak Authorization Code+PKCE 사용자 로그인과 HttpOnly 서버 세션
- Bitbucket Server 6.9.1 및 GitLab API v4 소스 어댑터
- 저장소 ACL 동기화, 파일 정책, Markdown 청킹, 버전별 색인 작업
- 서명 검증 Webhook, 이벤트 멱등 처리 및 ref별 작업 큐
- 개인 MCP 키 관리와 전체 범주 관리자 설정 웹 화면
- 키 회전·중지·재활성화와 CIDR·저장소·도구·분/시/일 호출 제한
- 동적 소스 설정 기반 Worker, 재시도·지수 백오프와 polling 무결성 보정
- 저장소 발견·등록·재색인 및 작업 운영 화면
- Readiness, Prometheus 지표와 hardened Kubernetes Kustomize 배포
- 관리자 설정 기반 OTLP HTTP tracing과 W3C trace context 전파
- SQLite/PostgreSQL 공통 암호화 예약 백업, 보존 및 트랜잭션 복원 UI/API
- BM25와 256차원 로컬 벡터 결합 검색, 색인 전 Secret 차단·마스킹
- ACL 필터 이후 사내 `/v1/rerank` 재순위화와 장애 시 하이브리드 점수 fallback
- 모델 미설정 시 ACL 선검사 후 Bitbucket/GitLab 서버측 Query Search API 모드
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
직후 서버가 토큰과 파일을 폐기합니다.

```bash
curl -H "Authorization: Bearer $(cat backups/bootstrap-admin.token)" \
  http://localhost:4747/api/v1/admin/settings
```

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

PostgreSQL은 `GIT_CTX_DB_DSN` 하나만으로 연결할 수 있습니다.

```bash
GIT_CTX_DB_DSN='postgres://gitctx:password@db:5432/gitctx?sslmode=require'
```

마이그레이션은 시작할 때 멱등 실행됩니다. 설정 암호화 키와 API-key pepper는 DSN을
도메인 분리해 파생하므로 별도 Bootstrap 환경변수가 필요하지 않습니다. 암호화된 DB와
백업을 복원할 때는 원래 DSN 문자열이 필요하므로 DSN은 Secret Store에서 별도 보관합니다.

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
