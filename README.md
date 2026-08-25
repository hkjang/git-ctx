# git-ctx

`git-ctx`는 사내 Bitbucket Server 6.9.1과 GitLab의 문서·코드 예제를 색인해
Context7과 같은 두 단계 MCP 흐름으로 제공하는 온프레미스 개발 지식 플랫폼입니다.

현재 저장소는 실행 가능한 기반 구현을 포함합니다.

- MCP Streamable HTTP `/mcp`: `initialize`, `tools/list`, `tools/call`, 세션 SSE와 종료
- Context7 호환 `resolve-library-id`, `query-docs`와 선택 가능한 Strict 모드
- Library ID 없이 ACL 범위에서 찾는 `search-repositories`, Bitbucket/GitLab Query API 기반 `search-source`
- 관리자 MCP 키 전용 `get-platform-status`, `list-index-jobs`, `reindex-repository`
- Go AST와 Java·TypeScript·Python·SQL 구조 분석 기반 `find-symbol`, `get-symbol-context`
- 파일명·경로 글롭으로 위치를 찾는 `find-file`, 색인 전 저장소는 원격 tree 조회로 보완
- 파일 본문·줄 범위를 반환하는 `read-file`, 미색인 파일은 소스 서버 즉시 조회와 Secret 마스킹
- 디렉터리 탐색 `list-directory`와 커밋 이력 `get-file-history` (GitLab·Bitbucket 공통)
- 변경 배경을 설명하는 `search-merge-requests` (GitLab MR·Bitbucket PR)
- 저장소 전 범위 사용처 역추적 `find-dependents`와 저장소 규약 파일 안내
- 색인 최종 실패 시 운영 역할 사용자에게 인앱 알림
- MCP `initialize` 응답의 도구 선택 지침으로 클라이언트 모델의 첫 호출 정확도 개선
- 언어·디렉터리·주요 파일·진입점을 요약하는 ACL 기반 `get-repository-map`
- import·호출·데이터 관계를 색인하는 `trace-dependencies`
- 두 ref의 심볼 변경과 의존 코드를 연결하는 `compare-refs`, `get-change-impact`
- 여러 저장소·ref를 업무 단위로 묶는 관리자 CRUD와 `get-context-pack`
- 운영 문서 전용 `find-runbook`, 비신뢰 데이터 경계·크기 제한을 적용한 `export-context`
- PostgreSQL `tsvector` 후보 선별, 원격 line 기반 인접 청크 hydration
- 검색어 일치 근거·retrieval 모드·embedding 버전을 보여주는 `explain-search-result`
- 질문 하나에서 코드·심볼·의존성·관련 파일을 응답 예산에 맞춰 조립하는 `build-context`
- 파일 기여자를 근거와 함께 찾는 `find-code-owner`, 실제 참조와 이름 후보를 구분하는 `find-tests`
- 매니페스트에서 만든 의존성 인벤토리 `find-dependency-usage` — 버전별 저장소 묶음과, 수정 버전 기준 영향·안전·판정 불가 분류(범위·부동 버전은 안전으로 접지 않음)
- 저장소 간 구조·변경 위험·색인 건강도를 근거 중심으로 설명하는 `get-architecture-map`, `assess-change-risk`, `get-repository-health`
- 검색 후보 단계의 저장소 ACL 적용과 브랜치·태그별 조회
- 카탈로그 운영 역할(platform-admin·source-admin·search-admin)의 ACL 무관 전체 검색
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
- 사이드바 기반 관리 콘솔, 라이트·다크 테마, Ctrl/Cmd+K 빠른 이동과 비차단 알림
- 모든 설정 탭의 상세 모달 가이드, 설정 화면 내 연결 테스트·설정 검증·원격 검색 즉시 확인
- Keycloak 역할·Claim 매핑 편집기와 `GET /api/v1/me/access` 권한 진단
- 초기 설정 진행 상황 대시보드, 저장소 일괄 등록과 설정 변경 이력 조회
- 새로고침·링크 공유에도 유지되는 화면 위치, 저장소별 색인 정책 편집
- 관리자 검색 실행 모드(`keyword-only`, `hybrid-fallback`, `hybrid-required`)와 MCP 전체 공통 적용
- 임베딩 장애 시 NULL 벡터로 완료되는 lexical-safe 색인, 자동 재색인·MCP 캐시 버전 분리
- 배치 임베딩과 재시도, 파일 단위 색인 내결함성, 실시간 색인 진행 표시
- 중단된 색인 작업 자동 재큐, 작업 실행 시간 제한, 임베딩 사전 probe
- 저장소별 색인 상태·원인·조치를 설명하는 색인 진단 화면과 API
- 다른 사용자의 ACL로 검색을 재현하는 감사 기록형 검색 진단(코드·경로 비노출)
- 키 회전·중지·재활성화와 CIDR·저장소·도구·분/시/일 호출 제한
- MCP 도구 카탈로그를 API 키 Scope, 생성·편집 UI와 OpenAPI가 공유하고 계약 시험으로 드리프트 차단
- 동적 소스 설정 기반 Worker, 재시도·지수 백오프와 polling 무결성 보정
- 저장소 발견·등록·재색인 및 작업 운영 화면
- Readiness, Prometheus 지표와 hardened Kubernetes Kustomize 배포
- 관리자 설정 기반 OTLP HTTP tracing과 W3C trace context 전파
- 인앱 알림과 Webhook·사내 메신저·SMTP 외부 전달 Outbox, 연결 시험·재시도·관리 이력
- SQLite/PostgreSQL 공통 암호화 예약 백업, 보존 및 트랜잭션 복원 UI/API
- BM25와 사내 OpenAI 호환 임베딩 결합 검색, 색인 전 Secret 차단·마스킹
- 선택적 pgvector·Milvus 연동, 미연동 시 메타 DB 저장 벡터로 동일 동작
- Library ID 없이 찾는 저장소 전 범위 `search-semantic` (임베딩 미사용·장애 시 키워드/소스 Query API 자동 폴백)
- 도구별 응답 예산으로 에이전트 컨텍스트 보호, 잘린 응답은 남은 결과 수와 좁히는 방법을 함께 반환
- MCP 호출 감사(세션·클라이언트·질의 요약·검색 경로·결과 수)와 기간별 통계·권장 조치·CSV 내보내기
- 호출 X-ray: 단계별 후보→통과 수와 소요 시간, 같은 세션의 호출 순서로 결과가 사라진 지점 추적
- 세션(대화) 단위 분석과 연동 자가 점검: 실제 검색 경로를 관리자 권한으로 실행해 단계별로 검증
- 설정 버전 비교·되돌리기, 동시 편집 충돌 차단, 비밀값 없는 전체 설정 내보내기·가져오기
- 소스 연동 회복력: 어댑터 재사용, Retry-After 존중 재시도, 오류 분류, 소스별 서킷 브레이커와 자동 복구
- 소스 장애 시 색인은 실패가 아니라 대기: 시도 횟수를 소모하지 않고 복구 후 자동 재개
- 관리자 화면의 벡터 DB 연결 시험, 상태 비교와 무중단 재적재(마이그레이션)
- ACL 필터 이후 사내 `/v1/rerank` 재순위화와 장애 시 하이브리드 점수 fallback
- 모델 미설정 시 ACL 선검사 후 Bitbucket/GitLab 서버측 Query Search API 모드
- 색인 전 저장소를 위한 `query-docs`의 소스 Code Search API failover와 병렬 원격 검색
- 선택적 OpenSearch BM25 증분 projection, 질의 단계 ACL filter와 DB 원문 재검증
- ACL 적용 정답 데이터셋 기반 Recall@K·MRR·nDCG@K 검색 품질 회귀 게이트
- 버전형 DB migration 및 PostgreSQL 다중 Worker `SKIP LOCKED`
- Docker 및 PostgreSQL Compose 배포
- 태그·소스 버전과 커밋을 고정하고 원격 재다운로드까지 검증한 GitHub 오프라인 이미지 릴리스

## 로컬 실행

SQLite는 개발·평가용이며 운영 기본은 PostgreSQL입니다.

```bash
# 최초 한 번만 생성하고 이후에는 Secret Store의 같은 값을 주입합니다.
export GIT_CTX_RECOVERY_KEY="$(openssl rand -base64 48)"
export GIT_CTX_DB_DSN='file:git-ctx.db?_foreign_keys=on&_busy_timeout=5000'
go run ./cmd/git-ctx
```

`GIT_CTX_RECOVERY_KEY`는 최초 한 번만 생성해 이후에도 같은 값을 주입하는 32자 이상의
고엔트로피 장기 비밀이며, DB DSN과 독립적으로 보관해야 합니다. 필수 Bootstrap 입력은
이 키와 `GIT_CTX_DB_DSN` 두 개이고 driver는 DSN에서 자동 판별됩니다.
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
GIT_CTX_DB_DSN='postgres://...' \
GIT_CTX_RECOVERY_KEY='<Secret Store의 기존 장기 복구 키>' \
./git-ctx recovery-token --ttl 15m
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

PostgreSQL 연결 정보는 `GIT_CTX_DB_DSN`으로 제공하고, 프로세스 기동에는 별도의
`GIT_CTX_RECOVERY_KEY`도 필요합니다.

```bash
export GIT_CTX_RECOVERY_KEY='<Secret Store에서 주입>'
export GIT_CTX_DB_DSN='postgres://gitctx:password@db:5432/gitctx?sslmode=require'
```

마이그레이션은 시작할 때 멱등 실행됩니다. 연결에 실패하면
`backups/recovery.db` SQLite로 복구 기동됩니다. 관리자는 “데이터베이스” 메뉴에서 새
PostgreSQL DSN을 읽기 전용으로 시험한 뒤 명시적 확인과 사유를 입력해 스키마·데이터를
논리 이전할 수 있습니다. 성공 후 재시작하면 암호화 저장된 검증 DSN을 활성화합니다.

설정 암호화 키와 API-key pepper는 최초 Bootstrap DSN을 도메인 분리해 파생하지만,
복구 토큰 서명키는 DSN이 아니라 `GIT_CTX_RECOVERY_KEY`를 사용합니다. Bootstrap DSN
문자열과 복구 키를 서로 독립된 Secret 항목으로 보관하고, DB·애플리케이션 백업과
분리해 함께 복구할 수 있어야 합니다. 여러 Pod에는 동일한 복구 키를 주입하며 배포 때
임의로 재생성하지 않습니다. 관리자 DSN 원문은 조회·로그·감사 기록에 남지 않습니다.

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

### 원격 소스 검색

`search-code`는 Library ID 없이 접근 가능한 저장소 이름과 코드를 함께 검색합니다.
GitLab은 Advanced Search가 있으면 인스턴스 전역 `scope=blobs` 검색을, 없으면 저장소
이름 검색으로 후보를 좁힌 뒤 Project Search API를 사용합니다. Bitbucket Server는 Code
Search API를 전역·저장소 범위로 모두 사용하며, 로컬 색인이 아직 없는 파일도 ACL
검증과 Secret 마스킹 후 결과에 포함합니다. 저장소 이름과 무관한 코드 문자열도
검색되고, 어떤 경로가 실행됐는지와 ACL로 걸러진 건수는 응답 `Diagnostics`로
설명합니다. 소스 ACL Principal이 매핑되지 않은 계정은 결과가 0건인 이유를 함께
반환합니다. 아직 카탈로그에 등록되지 않은 저장소도 원격 발견 후 저장소 ACL이 현재
사용자 Principal과 일치할 때만 표시합니다. 다만 카탈로그를 운영하는
`platform-admin`, `source-admin`, `search-admin` 역할은 소스 계정 매핑 없이도 전체
저장소를 검색하며, 이때 응답 `Diagnostics`에 ACL 우회 사실이 기록됩니다. 예를 들어 `dify 소스 검색해`는 검색 명령
표현을 제거한 `dify`를 원격 API에 전달합니다. 기존 `search-source`와 `query-docs`도 같은 안전한 원격
검색 결과 경로를 사용합니다.

Bitbucket Server의 원격 Code Search는 기본 브랜치와 512 KiB 미만 파일만 검색합니다.
비기본 branch/tag 요청은 기본 브랜치 결과로 바꿔 표시하지 않고, 해당 ref의 로컬
색인을 사용하거나 지원 범위를 명시한 경고를 반환합니다. Bitbucket 자체 제한인
쿼리 250자·최대 9개 표현식도 그대로 적용됩니다.

발급된 MCP API 키의 도구 Scope는 사용자 키 관리 화면에서 변경할 수 있으며,
관리자는 API 키 관리 API를 통해 Scope를 수정할 수 있습니다.
