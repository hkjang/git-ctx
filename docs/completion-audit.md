# 구현 완료 감사

이 문서는 제품 요건의 “코드로 검증된 범위”와 사내 시스템이 있어야만 검증할 수 있는
“배포 승인 범위”를 구분한다. 외부 승인 항목은 시험 결과 없이 완료로 간주하지 않는다.

## 코드와 자동 시험으로 검증된 범위

| 영역 | 상태 | 구현·검증 증거 |
|---|---|---|
| Context7 MCP | 완료 | `/mcp` GET/POST/DELETE, initialize, 세션형 SSE heartbeat·DELETE 종료, tools/list, tools/call, 두 호환 도구의 통합 시험과 실제 Codex CLI·Claude Code 호출 |
| 확장·관리 MCP | 완료(계약 시험) | Library ID 없는 저장소·코드 통합 검색, 미등록 원격 저장소 발견+실시간 ACL 확인, Bitbucket/GitLab Query API 소스 검색, 관리자 키 역할+Scope 이중 인가, 상태·작업·멱등 재색인 도구, Strict 모드 |
| 인증과 API 키 | 완료 | OIDC/JWKS, PKCE, HMAC 키 저장, 1회 노출, 회전·중지·폐기, 기존 키 Scope 사용자·관리자 변경과 감사, CIDR·도구·저장소·호출량 제한 |
| 권한 | 완료 | 사용자/그룹/키 제한을 후보 SQL 단계에서 적용하고 미인가 저장소를 일반화된 오류로 처리, Bitbucket·GitLab 이중 사용자 매핑과 소스별 전체 사용자 범위 분리 |
| Bitbucket/GitLab 어댑터 | 완료 | Bitbucket Server REST 1.0 저장소·프로젝트·전역 상속 ACL, 관리자 설정형 Code Search 경로와 실제 검색 연결 시험, GitLab API v4 직접·상속 멤버·가시성·Project Search 계약 시험, webhook 검증·중복 제거 |
| Confluence/Jira 어댑터 | 완료(계약 시험) | Capability 기반 소스 어댑터, Bearer 및 Basic Auth, 문서·이슈 검색, 관리자 설정·연결 시험과 명시적 ACL Principal |
| 색인 | 완료 | ref별 작업, 저장소 정책, Markdown/코드 청킹, Secret 차단·마스킹, 재시도·polling |
| 검색 | 완료 | DB BM25·벡터와 선택적 OpenSearch projection, 후보 질의 ACL 필터, DB 원문 재검증, 사내 `/v1/rerank`와 장애 fallback |
| 임베딩 실행 정책 | 완료 | 관리자 `keyword-only`·`hybrid-fallback`·`hybrid-required`, Worker/MCP 공통 동적 적용, 실패 시 NULL 벡터 lexical-safe 세대, ACL/ref 커버리지 임계값, 모델 revision별 구형 벡터 격리·자동 복구 재색인, 모델 Circuit·쿼리 벡터 캐시·동시 요청 병합, 관리자 UI/API/MCP/Prometheus 진단 |
| 모델 미설정 검색 | 완료 | ACL 검증 뒤 Bitbucket/GitLab source query API, Context7 출력 조립과 안전한 BM25 fallback 계약 시험 |
| 사용자 기능 | 완료 | 우측 상단 프로필 메뉴와 Ctrl/Cmd+K 빠른 이동, 관리자와 분리된 내 공간, 저장소·키·제한·사용량·호출·알림·MCP 설정 UI/API |
| 관리자 기능 | 완료(구현 범위) | 설정 자동 조회·시험·저장·삭제 CRUD, 저장소·정책·작업, MCP 도구, 키·감사·보안·상태 UI/API와 역할별 메뉴·쓰기 통제 |
| 데이터베이스 | 완료 | SQLite 회귀 시험 및 빈 PostgreSQL 16에서 001~042 migration/readiness와 암호화 백업·복원 실검증 |
| 배포 | 완료 | 비루트 Docker 이미지 실행, Compose, Kubernetes Kustomize와 기본 NetworkPolicy 렌더링. 태그·소스·커밋 고정, 원자적 패키징, checksum과 원격 재다운로드 검증 후 공개하는 릴리스 게이트 |
| 관측성 | 완료 | JSON 요청 로그, 동적 로그 레벨, request ID, health/readiness, Prometheus와 동적 OTLP HTTP tracing |
| 백업·복구 | 완료(애플리케이션 범위) | SQLite/PostgreSQL 공통 암호화 아카이브, 주기·보존, 무결성 검증, 트랜잭션 복원, 세션 무효화와 관리자 UI/API |
| 검색 품질 평가 | 완료 | ACL 적용 정답 사례, Recall@K·MRR·nDCG@K, 임계값 회귀 판정, 이력·상세 UI/API |
| 관리자 연동 설정 | 완료 | Keycloak·Bitbucket·GitLab·Confluence·Jira·Embedding·Reranker 전용 필드, Bitbucket/GitLab 실제 Query Search 진단과 결과 표시, 일반값 자동 재조회, 버전·수정자·시각과 비밀 필드별 마스킹·암호화 저장 |
| OpenSearch | 완료(계약 시험) | 관리자 연결·index mapping 시험, ref별 delete/bulk projection, repository·ref·principal 선필터, DB 청크 hydration, Worker 재시도 |
| 최초·복구 관리자 세션 | 완료 | 최초 일회용 토큰을 30분 HttpOnly·SameSite 세션으로 교환하고 실제 `platform-admin` SSO 로그인 성공 시 전역 폐기. 이후 CLI 서명 복구 토큰의 1회 소비·만료·Origin 검증·영구 MCP 키 생성 차단 |
| 의존성 인벤토리 | 완료 | 색인 시 go.mod·package.json·pom.xml·build.gradle·requirements.txt·pyproject.toml·Cargo.toml 파싱, 내용 정책 제외 매니페스트 포함, ref 단위 교체, `find-dependency-usage` 버전 묶음과 저장소 제한 키 비노출 계약 시험 |
| 버전 표시 | 완료 | 공개 설정과 `/api/v1/me` 버전 제공, 로그인 전 상단·안내와 로그인 후 프로필 표시 |
| 비밀정보 관리 | 완료(계약 시험) | 암호화 DB/Vault KV v2 backend, 등록·회전·중지, 원문 비노출, `secret://` 동적 참조와 Fail Closed |
| 관리자 UI 구조 | 완료 | 개인화 영역과 분리된 권한 기반 관리자 진입, 역할별 대메뉴, 설정 종류별 탭, 저장 진행·오류 상태 |
| Keycloak 설정 안정성 | 완료 | 4개 필드 UI, 자동 Issuer·Redirect·표준 Scope/Claim·동일 이름 역할/그룹, 저장값 자동 재조회, Discovery/JWKS/token exchange, PKCE callback·세션 E2E, Access Token 역할 병합, 실제 platform-admin 로그인 뒤 Bootstrap 폐기 |
| DB 연결 관리 | 완료 | 공개 비민감 상태, 관리자 DB·pool·migration 진단, Prometheus up, SQLite 단일 Writer pool, PostgreSQL 실패 복구 기동·연결 시험·논리 이전·재시작 전환 |
| 운영 정책 | 완료(애플리케이션 범위) | 동적 점검 모드, 재기동형 수신 주소·HTTP Timeout, 인앱 키 알림, Webhook·메신저·SMTP Outbox와 재시도, 감사·호출·알림·작업·설정 이력 보존 정리 |

2026-08-25 v0.53.3 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
인증 last_used 쓰기 병합 시험                  PASS
API 키 인증 경로 감사                          PASS(수정 없음)
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.53.3 렌더링      PASS
Docker linux/amd64·UID 10001·v0.53.3 빌드      PASS
```

이번 검증은 인증 경로의 요청당 쓰기를 제거한 것을 고정한다. 키당 1분에 한 번만
기록하고, 창 안에서는 문장 자체가 발생하지 않으며, 복제본이 값을 뒤로 되돌리지
않도록 SQL 조건을 둔다. 인증 경로의 상태·CIDR·호출량 검사와 비활성 사용자 거부는
이미 갖춰져 있어 수정이 필요 없었다.

2026-08-25 v0.53.2 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
pnpm 락파일 3종 형식 시험                      PASS
yarn 락파일 두 형식 회귀 시험                  PASS(수정 없음)
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.53.2 렌더링      PASS
Docker linux/amd64·UID 10001·v0.53.2 빌드      PASS
```

이번 검증은 락파일 형식 누락을 고정한다. pnpm 의 슬래시 형식(v5~v7)을 전혀 읽지
못해 해당 저장소가 인벤토리에서 조용히 빠졌고, v9 의 scope 패키지와 peer 접미사도
잘못 처리됐다. 세 형식과 접미사를 모두 회귀 시험으로 고정했다. yarn 은 클래식과
berry 두 형식 모두 이미 정확히 읽고 있어 수정이 필요 없었다.

2026-08-25 v0.53.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
저장소 지도 스택 시험                          PASS
전 도구 빈 응답 점검                           PASS(수정 없음)
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.53.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.53.1 빌드      PASS
```

이번 검증에는 저장소 지도의 의존성 스택이 포함된다. 직접 선언만 집계하고 전이·락파일
항목은 제외하며, 인벤토리가 없으면 절 자체를 붙이지 않아 빈 목록이 "의존성 없음"으로
읽히지 않는다. 비관리 도구 29종의 빈 응답은 모두 이유와 다음 행동을 제시하고 있어
수정이 필요 없었다.

2026-08-25 v0.53.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
웹훅 거부 기록·사유 회귀 시험                  PASS
웹훅 인증 경로 감사                            PASS(수정 없음)
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.53.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.53.0 빌드      PASS
```

이번 검증에는 웹훅 수신 가시성이 포함된다. 거부된 이벤트를 사유·발신 식별자와 함께
기록하고, 서명 검증 실패는 기록하지 않으며, 거부가 색인 작업을 만들지 않는 것을
회귀 시험으로 고정했다. 인증 경로 자체는 상수 시간 비교와 크기 제한이 이미 갖춰져
있어 수정이 필요 없었다.

2026-08-25 v0.52.7 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
의존성 답변 색인 최신성 시험                   PASS
콘솔 REST 20종 키 제한 감사                    PASS(수정 없음)
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.52.7 렌더링      PASS
Docker linux/amd64·UID 10001·v0.52.7 빌드      PASS
```

이번 검증에는 의존성 답변의 색인 최신성이 포함된다. 오래된 색인에 근거한 "안전"
판정이 잘못된 안심으로 읽히지 않도록, 답변이 참조한 저장소 중 오래된 것을 지목한다.
콘솔 REST 경로의 키 저장소 제한은 20종 모두 적용되어 있어 수정이 필요 없었다.

2026-08-25 v0.52.6 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
Context Pack 키 저장소 제한 회귀 시험          PASS
전 도구 허용 목록 누출 가드(29종)              PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.52.6 렌더링      PASS
Docker linux/amd64·UID 10001·v0.52.6 빌드      PASS
```

이번 검증은 API 키 저장소 제한이 모든 도구에서 지켜지는지를 고정한다.
`get-context-pack` 만 제한을 확인하지 않아 팩에 포함된 다른 저장소 내용을
반환했고, 이제 수집 이전 단계에서 구성원을 거른다. 표식 저장소를 이용한 전 도구
가드는 표에 없는 도구가 레지스트리에 추가되어도 실패하므로, 새 도구가 같은 검사를
건너뛸 수 없다.

2026-08-25 v0.52.5 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
대용량 락파일 색인 상한 시험                   PASS
증분 색인 인벤토리 보존 회귀 시험              PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.52.5 렌더링      PASS
Docker linux/amd64·UID 10001·v0.52.5 빌드      PASS
```

이번 검증은 락파일 색인의 자원 사용을 고정한다. 매니페스트·락파일 원문을 모아 두지
않고 읽는 즉시 파싱하며, 선언은 배치로 기록하고, ref 총량 상한을 넘으면 작업 경고로
남긴다.

2026-08-25 v0.52.4 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
락파일 파서(7종)·중첩 사본 보존 시험           PASS
락파일 우선 판정·범위 방향 회귀 시험           PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.52.4 렌더링      PASS
Docker linux/amd64·UID 10001·v0.52.4 빌드      PASS
```

이번 검증에는 락파일 기반 판정이 포함된다. 락파일이 있는 저장소는 해석된 버전으로
판정하고, 범위는 위로만 해석되므로 하한이 수정 버전 미만이면 판정 불가, 이상이면
안전으로 처리한다. 정확히 고정된 버전만 영향으로 보고하는 것을 회귀 시험으로
고정했다.

2026-08-25 v0.52.3 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
증분 색인 인벤토리 보존 회귀 시험              PASS
패키지 이름 와일드카드 리터럴 시험             PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.52.3 렌더링      PASS
Docker linux/amd64·UID 10001·v0.52.3 빌드      PASS
```

이번 검증은 인벤토리 정확성 결함 2건을 고정한다. 증분 동기화는 변경된 파일만
받으므로 ref 단위 전체 교체가 손대지 않은 매니페스트를 지웠고, 패키지 이름의 SQL
와일드카드가 패턴으로 해석되어 카탈로그 전체를 사용처로 보고했다. 두 경로 모두
회귀 시험으로 고정했다.

2026-08-25 v0.52.2 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
인벤토리 집계·커버리지 회귀 시험               PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.52.2 렌더링      PASS
Docker linux/amd64·UID 10001·v0.52.2 빌드      PASS
```

이번 검증에는 카탈로그 전체 의존성 인벤토리가 포함된다. 버전이 갈린 패키지를 위로
올리고, 매니페스트가 색인된 저장소 비율을 함께 제시하며, 미색인 저장소가 늘면
커버리지가 내려가고 경고가 붙는 것을 회귀 시험으로 고정했다. 집계는 호출자의 ACL
범위 안에서만 이루어진다.

2026-08-25 v0.52.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
버전 비교·판정 불가 회귀 시험                  PASS
공지 판정(혼합 선언·판정 불가 분리) 시험        PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.52.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.52.1 빌드      PASS
```

이번 검증에는 보안 공지 판정이 포함된다. 범위·부동 버전은 안전으로 접지 않고
판정 불가로 남기며, 범위 하한이 수정 버전 이상이면 영향 없음으로 판단한다. 한
저장소가 영향 버전과 수정 버전을 함께 선언하면 더 엄격한 쪽인 영향으로 분류해,
일부만 고친 저장소가 완료로 보고되지 않는 것을 회귀 시험으로 고정했다.

2026-08-25 v0.52.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
매니페스트 파서 회귀(7종 생태계)               PASS
색인 인벤토리 통합(정책 제외·재색인 교체)       PASS
MCP 종단(버전 묶음·저장소 제한 키)             PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.52.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.52.0 빌드      PASS
```

이번 검증에는 의존성 매니페스트 인벤토리가 포함된다. 내용 색인 정책이 제외한
매니페스트도 읽어 인벤토리를 만들고, 재색인 시 ref 단위로 교체하며, staging 을
정리하는 것을 확인했다. `find-dependency-usage` 는 버전별 저장소 묶음을 먼저
제시하고, 저장소가 제한된 API 키에는 묶음과 저장소 수까지 재계산해 허용되지 않은
저장소를 노출하지 않는다. 아직 인벤토리가 비어 있는 상태와 실제 사용처가 없는
상태를 응답에서 구분하는 것도 회귀 시험으로 고정했다.

2026-08-25 v0.51.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
PostgreSQL 16 + pgvector + Vault Integration PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
설정 강제 저장 경계·오류 코드 회귀 시험         PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.51.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.51.1 빌드      PASS
```

이번 검증에는 개인 홈·빠른 이동, MCP 클라이언트별 설정 생성, 최소 권한 키 Preset,
일회성 키의 화면 이탈 즉시 DOM·앱 상태 제거, 도구 필터와 결과 내보내기,
설정 dirty/stale-load 보호, 명시적 임베딩 probe, PostgreSQL 동일 DSN migration
gate, 외부 연결 실패·502/503/504만 허용하고 인증·TLS 오류는 거부하는 typed
force-save, 상세 릴리스 본문 자동 반영과 trusted tooling sparse-checkout 계약 시험이 포함된다.

2026-08-25 v0.50.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
PostgreSQL 16 + pgvector + Vault Integration PASS
관리자 UI JavaScript parse·계약 시험 전체       PASS
GitHub Actions actionlint·shell syntax         PASS
govulncheck reachable symbols                  PASS (0)
Kubernetes Kustomize·:4747·v0.50.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.50.1 빌드      PASS
공개 설정·상단·로그인·프로필 버전 표시 계약       PASS
```

이번 검증에는 MCP registry·API 키 Scope·생성/편집 UI·OpenAPI와 관리자 도구 정책
테이블의 전수 정합성 시험, 캐시 10,000개 hard cap 동시성 시험, 저장소 건강도에서
동명이인 심볼과 ACL 비공개 consumer를 구분하는 회귀 시험이 포함된다.

2026-07-30 로컬 검증 결과:

- Keycloak 26.3.3에서 별도 `groups` Client Scope나 ID Token 역할 Mapper 없이
  Authorization Code+PKCE 로그인, `/admin` 복귀, `/api/v1/me` 200,
  `platform-admin` 역할 반영과 Bootstrap 자동 폐기를 검증했다.

```text
go test -race ./...                         PASS
go vet ./...                                PASS
node --check web/app.js                     PASS
node test/web/roles.test.js                 PASS
kubectl kustomize deploy/kubernetes/base    PASS
PostgreSQL 16 migration 001..039            PASS
PostgreSQL 16 backup/restore round trip     PASS
PostgreSQL 16 quality benchmark contract    PASS
PostgreSQL 16 notification outbox delivery  PASS
pgvector extension activation·model revision filtering PASS
SMTP protocol connection test               PASS
Notification outbox 1,205-event paging      PASS
PostgreSQL 16 application bootstrap          PASS
PostgreSQL 실패→SQLite 복구→PostgreSQL 논리 이전·재기동 PASS
node test/web/admin-ui.test.js              PASS
node test/web/guides.test.js                PASS
헤드리스 브라우저 관리자 화면 전 패널·설정 탭 순회 PASS (콘솔 오류 0)
scripts/package-offline-image.sh            PASS (`git-ctx-vX.Y.Z.tar.gz`)
Docker build + UID 10001 readiness/UI       PASS
OpenSearch auth/mapping/bulk/ACL contract   PASS
Default listen address :4747 readiness      PASS
Codex CLI 0.145.0 resolve-library-id         PASS
Claude Code 2.1.218 resolve-library-id       PASS
```

## 구현되지 않았거나 후속 단계인 항목

| 항목 | 현재 상태 | 완료에 필요한 작업 |
|---|---|---|
| 레거시 MCP SSE endpoint | 미구현(선택) | 승인 대상 구형 클라이언트가 요구할 때 추가 |
| PDF 바이너리 직접 추출 | 미구현(선택 확장) | 현재 OpenAPI·텍스트·코드와 Confluence/Jira는 지원하며, PDF OCR/텍스트 추출은 별도 격리 Worker가 필요 |

## 사내 배포 환경 승인 게이트

다음 항목은 로컬 모의 서버나 단위 시험만으로 최종 완료를 선언할 수 없다.

1. 실제 Keycloak에서 PKCE 로그인, 역할·그룹·사용자 속성 매핑, 로그아웃, 잘못된
   issuer/audience와 키 회전 시험
2. 실제 Bitbucket Server 6.9.1에서 프로젝트·저장소·ACL·branch/tag 수집과
   저장소별 push webhook, 누락 polling 및 설치된 Search 모듈의 Code Search 경로·응답 시험
3. Codex CLI와 Claude Code 실제 호출은 로컬 Docker에서 통과했다. 조직 승인 버전과
   실제 사내 저장소에서 두 단계 호출·출처 확인 필요
4. 권한 부여·회수 전후 저장소 이름, ID, 캐시, 오류 내용의 완전 비노출 시험
5. 목표 데이터량과 50개 동시 호출에서 P95 및 오류율 측정
6. PostgreSQL 백업/복원, 원래 DSN Secret 분리 복구, Keycloak 설정 삭제·재구성과
   `recovery-token` break-glass 운영 훈련
7. 운영 NetworkPolicy를 실제 Keycloak·Bitbucket·GitLab·DB CIDR로 제한하고
   사내 CA·프록시 장애/복구 시험
8. 실제 운영 대상 OpenSearch 버전에서 mapping, Bulk 재색인, 장애 복구와 목표 규모
   repository/ref/principal 필터 성능 시험
9. 실제 사내 Vault에서 최소 권한 KV v2 정책, Token TTL·회전·폐기, HA standby,
   seal/unseal 장애와 Vault backup/DR 복구 시험

따라서 현재 산출물은 실행 가능한 MVP 기반과 2단계 일부 기능까지 구현됐지만,
요건서 전체의 최종 제품 완료 판정은 위 미구현 항목의 범위 결정과 사내 승인 시험 후에
가능하다.
