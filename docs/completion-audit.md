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

2026-08-26 v0.70.0 릴리스 전 검증 결과:

```text
저장소 제한 키가 금지된 저장소를 못 봄           PASS
경계를 시험하지 않는 도구는 통과가 아니라 보고    PASS
scope·CIDR·호출량·만료·폐기·비활성화 강제        PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.70.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.70.0 빌드        PASS
```

API 키가 내세우는 제약이 실제로 강제되는지 하나씩 재 봤다. 일곱 중 여섯은 강제되고 있었고,
저장소 제한만 도구 두 개에서 새고 있었다 — 둘 다 libraryId 가 선택이라 인자 검사가 필터가
되지 못하는 도구였다. 처음 시도한 검사는 데이터가 없어 공허하게 통과했는데, 그것을 알아채고
"경계를 시험하지 않으면 실패"를 시험에 넣은 것이 이번 검증의 요점이다.

2026-08-26 v0.69.1 릴리스 전 검증 결과:

```text
안내가 부를 수 있는 도구 29개를 모두 이름        PASS
안내의 도구 이름이 모두 실재                     PASS
안내 4,000바이트 상한 유지(3,829)                PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험·콘솔 시험                    PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.69.1 렌더링        PASS
Docker linux/amd64·UID 10001·v0.69.1 빌드        PASS
```

지난 릴리스에서 매개변수 설명을 채웠으니 그 위층 — 도구를 고르는 안내 — 을 봤다. 29개 중
13개가 한 번도 나오지 않았고, 그중 하나는 다른 모든 도구가 요구하는 식별자를 만드는 도구였다.
길이 상한은 기존 시험이 이유와 함께 걸어 둔 것이라 늘리지 않고 문장을 줄여 맞췄다.

2026-08-26 v0.69.0 릴리스 전 검증 결과:

```text
MCP 핸드셰이크·오류코드·세션 발급 조사           PASS
도구 매개변수 43개 설명 채움                     PASS
스키마 구조 검사(설명·items·enum·required)       PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험·콘솔 시험                    PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.69.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.69.0 빌드        PASS
```

이 제품은 MCP 서버인데 시험이 핸드셰이크를 건너뛰고 tools/call 을 직접 부르고 있었다. 실제
클라이언트처럼 접속해 보니 프로토콜은 규격대로였고, 대신 도구 스키마의 매개변수 절반 가까이가
자기에 대해 아무 말도 하지 않았다 — 에이전트가 인자를 고르는 유일한 근거인데.

2026-08-26 v0.68.1 릴리스 전 검증 결과:

```text
내용이 있는 설치에서 전체 화면 렌더링            PASS
렌더 텍스트에 undefined·NaN 등 없음              PASS
discovered 백업 행이 목록에 그려짐               PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험·릴리스 업그레이드 시험        PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.68.1 렌더링        PASS
Docker linux/amd64·UID 10001·v0.68.1 빌드        PASS
```

콘솔 렌더 스윕이 언제나 빈 데이터베이스를 상대로 돌고 있었다. 내용을 채워 돌려 보니 화면
자체는 멀쩡했고, 대신 화면이 하는 말이 틀려 있었다 — 지난 세 릴리스가 바꾼 키 관리 방식을
콘솔은 여전히 예전대로 설명하고 있었다. 문서를 고칠 때 화면을 같이 보지 않은 내 누락이다.

2026-08-26 v0.68.0 릴리스 전 검증 결과:

```text
릴리스 이미지 기동·응답(읽기 전용 루트)         PASS
스모크 검사가 기동 실패 이미지를 거부            PASS
콘솔 중복 사본 제거 후 정상 서빙                PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험·릴리스 업그레이드 시험        PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.68.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.68.0 빌드        PASS
```

릴리스 검증이 이미지를 띄워 본 적이 없다는 것을 발견했다. 오늘 이미지는 띄워도 정상이었지만,
`-version` 한 줄만 보고 내보내는 것 자체가 결함이다. 배포 매니페스트가 선언한 보안 설정으로
띄워 보다가, 이미지가 아무도 읽지 않는 콘솔 사본을 담고 있다는 것도 함께 나왔다.

2026-08-26 v0.67.1 릴리스 전 검증 결과:

```text
DSN 변경 후 복구 키 회전·회전 전 백업 복원      PASS
회전 완료 후 새 키 단독 기동                    PASS
문서와 실제 동작 정합(전수 검색)                PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험·릴리스 업그레이드 시험        PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.67.1 렌더링        PASS
Docker linux/amd64·UID 10001·v0.67.1 빌드        PASS
```

지난 세 릴리스가 복구 키의 의미를 바꿨으니 그것이 실제로 회전 가능한지 물었다. DSN 이
그대로일 때만 우연히 되고 있었다. 같은 이유로 문서도 더 이상 존재하지 않는 시스템을
설명하고 있었는데, 그것을 그렇게 만든 것은 내가 낸 변경이다.

2026-08-26 v0.67.0 릴리스 전 검증 결과:

```text
새 기기에서 백업 목록·복원·설정·API 키          PASS
설치 키로 봉인된 구버전 아카이브 복원            PASS
복원 직후 키 재적재                             PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험·릴리스 업그레이드 시험        PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.67.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.67.0 빌드        PASS
```

키 → 데이터베이스 이전으로 이어진 사슬의 마지막 고리를 봤다. 백업은 만든 설치에서만 복원할
수 있었다 — 파일을 찾지 못하고, 기록 없이 복원하지 못하고, 잃어버린 데이터베이스의 연결
문자열에서 파생된 키로 봉인되어 있어 열지도 못했다. 셋 다 백업이 존재하는 바로 그 경우에만
드러나는 것들이었다.

2026-08-26 v0.66.1 릴리스 전 검증 결과:

```text
SQLite→PostgreSQL 이전 후 기동·설정·API 키     PASS
키 테이블 없는 구버전 아카이브 복원              PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험·릴리스 업그레이드 시험        PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.66.1 렌더링        PASS
Docker linux/amd64·UID 10001·v0.66.1 빌드        PASS
```

직전 릴리스에서 키를 데이터베이스에 저장하게 만들었으니, 그 바로 아래쪽 — 데이터베이스를
옮기는 기능 — 을 확인했다. 이전은 키가 보호하는 것을 전부 옮기면서 키를 옮기지 않았고,
성공을 보고한 뒤 재시작에서 실패했다. v0.66.0 이전에도 같았지만 그때는 알고리즘 이름만
말했다.

2026-08-26 v0.66.0 릴리스 전 검증 결과:

```text
릴리스 바이너리가 쓴 DB 열기(v0.1.0~현재)      PASS
서버가 업그레이드된 DB 로 기동·응답             PASS
마이그레이션 위험 패턴 전수 점검                PASS
DSN 변경 후 기동·설정 읽기·API 키 동작          PASS
복구 불가 시 원인을 말함                        PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.66.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.66.0 빌드        PASS
```

온프레미스 제품에서 가장 위험한 순간은 업그레이드인데, 그 시험이 손으로 만든 과거만 다루고
있었다. 실제 릴리스 바이너리로 바꿔 돌려 보니 스키마는 v0.1.0 부터 깨끗했다. 대신 그 과정에서
설정 키가 연결 문자열에서 파생된다는 것을 발견했다. 문자열 하나 바뀌면 기동이 거부되고 모든
API 키가 죽는데, 그것을 알려 주는 것은 알고리즘 이름뿐이었다.

2026-08-26 v0.65.1 릴리스 전 검증 결과:

```text
문서 소스 조사(Confluence 공간·Jira 프로젝트)   PASS
인용이 합성 토큰을 담지 않음                     PASS
커넥터 설정 진단 문구가 참                       PASS
한국어·영어 런북 모두 발견                       PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.65.1 렌더링        PASS
Docker linux/amd64·UID 10001·v0.65.1 빌드        PASS
```

지난 릴리스에서 read-file 을 소스 우선으로 바꿨으니, 그것이 문서 소스에 회귀를 냈는지부터
확인했다(GetFile 이 제대로 있어 문제없었다). 그 김에 가장 덜 실행된 경로를 실제 질문으로
훑었고, 답 안의 문장 셋이 각각 틀려 있었다.

2026-08-26 v0.65.0 릴리스 전 검증 결과:

```text
청크 줄 번호와 실제 파일 위치 대조              PASS
제목에만 있는 단어·doc 주석에만 있는 단어 검색   PASS
read-file 호출 팬아웃 없음(호출자 2곳 확인)      PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.65.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.65.0 빌드        PASS
```

지난 릴리스에서 read-file 이 파일의 일부만 돌려준다는 것을 알았으니, 이번에는 그 반대편을
봤다 — 저장은 되어 있는데 검색되지 않는 텍스트. 인덱스가 세 컬럼을 색인하는데 점수는 두
컬럼만 읽고 있었고, 추출해서 출력까지 하는 주석은 아무 검색어도 닿지 않았다.

2026-08-26 v0.64.0 릴리스 전 검증 결과:

```text
read-file 원본 대조(마크다운·Go·평문)           PASS
소스 장애 시 색인 대체가 자기를 밝힘             PASS
색인 나이 조회 비용 실측 0.05ms/호출            PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.64.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.64.0 빌드        PASS
```

지난 릴리스에서 추가한 색인 나이 조회 비용을 먼저 재 봤고(무시할 만했다), 그 김에 read-file 이
돌려주는 것을 원본과 글자 단위로 대조했다. 평문 파일만 맞았다. 청크는 검색용으로 만들어졌는데
그것을 파일이라고 내놓고 있었다.

2026-08-26 v0.63.0 릴리스 전 검증 결과:

```text
색인 30일 노후화 조사(읽기 도구 전체)           PASS
신선/노후 각각의 문구·빈 답 포함                PASS
libraryId 를 받는 모든 도구가 감사에 기록        PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.63.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.63.0 빌드        PASS
```

이 플랫폼은 색인 나이를 아는 도구를 따로 갖고 있으면서, 코드를 실제로 읽는 도구들에는 그것을
말하지 않았다. 조사하다 보니 감사 쪽 누락도 같은 자리에서 나왔다 — 나이를 붙이려면 어느
색인에서 온 답인지 알아야 하는데, 그걸 아는 표시가 여섯 도구에 빠져 있었다.

2026-08-26 v0.62.1 릴리스 전 검증 결과:

```text
빈 카탈로그 조사(읽기 도구 29개)                PASS
세 상황 구분·모호해야 할 것은 모호               PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.62.1 렌더링        PASS
Docker linux/amd64·UID 10001·v0.62.1 빌드        PASS
```

이번에는 두 구현을 견주는 대신, 아무것도 없을 때 각 도구가 무엇을 말하는지 물었다. 새 설치가
처음 마주하는 화면이 "권한이 거부되었다"였다. 답을 정확하게 만들되, 읽을 수 없는 저장소의
존재를 드러내지 않는 선은 지켰다.

2026-08-26 v0.62.0 릴리스 전 검증 결과:

```text
플랫폼 차분 시험 21개 호출                      PASS
Bitbucket 거절 4종 대체 경로·500 은 실패 유지    PASS
내용 매치 저장소 목록화                         PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.62.0 렌더링        PASS
Docker linux/amd64·UID 10001·v0.62.0 빌드        PASS
```

두 데이터베이스에 쓴 방법을 두 플랫폼에 적용했다. 비교가 제품을 말하게 하려면 픽스처가 실제로
동등해야 했고, 가짜 서버를 맞추는 일 자체가 검증의 절반이었다. 남은 차이 둘은 진짜였다.

2026-08-26 v0.61.1 릴리스 전 검증 결과:

```text
도구 차분 시험 28개 호출(3회 연속)              PASS
find-symbol PostgreSQL 동작                     PASS
정렬 로케일 비의존·동점 결정성                   PASS
go test -race (FTS5/태그 없음/PostgreSQL)       PASS
빌드 모드 교차 시험                             PASS
go vet ./... && go build ./...                 PASS
버전 메타데이터·GitHub Actions 정합성            PASS
Kubernetes Kustomize·:4747·v0.61.1 렌더링        PASS
Docker linux/amd64·UID 10001·v0.61.1 빌드        PASS
```

지난번 검색 한 곳에서 통했던 방법을 도구 표면 전체로 넓혔다. 같은 저장소를 두 데이터베이스에
평소 경로로 색인하고 읽기 도구 28개를 같은 인자로 불러 답을 비교했다. 8개가 갈렸고 하나는
아예 실패였는데, 그중 어느 것도 기존 시험이 볼 수 있는 것이 아니었다. 각 주장이 그때 도는
드라이버를 상대로만 확인되기 때문이다.

2026-08-26 v0.61.0 릴리스 전 검증 결과:

```text
두 드라이버 차분 시험(같은 질문 15개)           PASS
PostgreSQL 색인 표현식 교체·GIN 복구            PASS
플랫폼 체인 7개 PostgreSQL 실행                 PASS
go test -race (FTS5/태그 없음/PostgreSQL)      PASS
빌드 모드 교차 시험                            PASS
go vet ./... && go build ./...                PASS
버전 메타데이터·GitHub Actions 정합성           PASS
Kubernetes Kustomize·:4747·v0.61.0 렌더링       PASS
Docker linux/amd64·UID 10001·v0.61.0 빌드       PASS
```

이번 검증은 체인 시험 7개가 전부 SQLite 에서만 돌고 있다는 데서 출발했다. PostgreSQL 로
돌려 보려면 시험 쪽 결함 3개를 먼저 고쳐야 했고, 그러고 나서 두 드라이버에 같은 질문을
던져 보니 답이 갈렸다. 통과와 동일은 다른 것이다.

2026-08-26 v0.60.2 릴리스 전 검증 결과:

```text
빌드 모드 교차 시험(태그 유/무 같은 DB)         PASS
방치된 색인 재구축·도장이 재색인하지 않음        PASS
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
커밋 도장 20k 청크 137ms → 56ms                PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.60.2 렌더링      PASS
Docker linux/amd64·UID 10001·v0.60.2 빌드      PASS
```

이번 검증은 데이터베이스가 그것을 만든 바이너리보다 오래 산다는 사실에서 출발했다. 이 저장소는
FTS5 있는 빌드와 없는 빌드를 둘 다 지원한다고 적어 두었는데, 그 두 빌드가 같은 데이터베이스를
나눠 쓰는 경우를 아무도 실행해 본 적이 없었다. 실행해 보니 색인이 아니라 색인 작업 전체가 멈췄다.

2026-08-26 v0.60.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
드문 단어 지연 실측 357ms → 6ms                PASS
상한·안쪽 생략 문구 분리 시험                  PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.60.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.60.1 빌드      PASS
```

이번 검증은 직접 바꾼 핫패스를 규모에서 다시 재 본 것이다. 인덱스가 이미 찾은 뒤에도
단어 안쪽 일치를 위해 전체를 훑고 있어, 일치가 한 건인 질의가 전체 훑기 비용을 냈다.

2026-08-26 v0.60.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
공지 소유자 조회 시험 신규                     PASS
실인스턴스 공지 응답에 소유자 표시             PASS
콘솔 CVE 대응 흐름 브라우저 확인               PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.60.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.60.0 빌드      PASS
```

이번 릴리스는 공지 답변을 목록에서 조치로 바꿨다. 영향받는 저장소마다 바꿔야 할
매니페스트를 덮는 CODEOWNERS 소유자를 함께 답하고, 선언이 없으면 없다고 밝힌다.

2026-08-26 v0.59.3 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
검색 경로 상태 보고 시험 신규                  PASS
콘솔 렌더 스윕 재실행(화면 11종)               PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.59.3 렌더링      PASS
Docker linux/amd64·UID 10001·v0.59.3 빌드      PASS
```

이번 릴리스는 에이전트만 알고 있던 사실을 운영 화면으로 옮겼다. 전문 인덱스 사용
여부, 재순위 설정 상태, 벡터 데이터베이스 연결이 콘솔의 운영 상태에 함께 나온다.

2026-08-26 v0.59.2 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
안내문·도구 등록 일치 시험 신규                PASS
안내문 길이 상한 시험(2,954/4,000바이트)       PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.59.2 렌더링      PASS
Docker linux/amd64·UID 10001·v0.59.2 빌드      PASS
```

이번 릴리스는 MCP 안내문을 실제 동작에 맞췄다. 문서형 소스·소유자 도구·색인 기준
답변 표시가 빠져 있어, 에이전트가 그 기능들을 찾아볼 근거가 없었다. 안내문이 부르는
도구가 실제로 등록되어 있는지도 시험으로 붙잡았다.

2026-08-26 v0.59.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
OpenSearch 투영 체인 시험 신규                 PASS(수정 없음)
투영 비활성화 시 실패 확인                     PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.59.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.59.1 빌드      PASS
```

이번 릴리스로 선택적 백엔드가 모두 전 구간 시험을 갖는다. 투영은 최초 색인과 push
이후 모두 정확했고 삭제된 파일의 문서도 사라졌다. 고칠 결함은 없었다.

2026-08-26 v0.59.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
Confluence·Jira 전 구간 체인 시험 신규         PASS(결함 1건 수정)
이전 동작으로 되돌릴 때 실패 확인              PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.59.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.59.0 빌드      PASS
```

이번 검증은 문서형 소스를 전 구간으로 처음 돌렸다. 그 과정에서 실시간 질의에 답하는
소스가 하나라도 있으면 색인을 보지 않아, 색인에만 존재하는 위키·이슈 내용이 답에서
사라지는 것을 발견해 고쳤다.

2026-08-26 v0.58.7 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
콘솔 렌더 스윕 신규(관리자 화면 11종)          PASS(수정 없음)
빈 화면 주입 시 실패 확인                      PASS
콘솔 계약 시험 5종                             PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.58.7 렌더링      PASS
Docker linux/amd64·UID 10001·v0.58.7 빌드      PASS
```

이번 검증은 콘솔을 실제 브라우저에서 열어 관리자 화면을 모두 클릭했다. 화면 11종이
오류 없이 렌더링됐고, 라우팅 누락으로 비어 있던 두 카드가 실데이터를 보여주는 것도
확인했다. 텍스트로만 읽던 시험으로는 볼 수 없던 층이다.

2026-08-26 v0.58.6 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
릴리스 도구 시험(정합성·노트)                  PASS
감사 블록의 옛 버전 언급 허용 시험             PASS
산출물 줄 버전 불일치 시 실패 확인             PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.58.6 렌더링      PASS
Docker linux/amd64·UID 10001·v0.58.6 빌드      PASS
```

이번 릴리스는 절차 자체의 결함을 고쳤다. 정합성 검사가 감사 블록의 모든 버전을
검사해, 업그레이드 확인 같은 사실을 적으면 릴리스가 깨졌다. 이제 산출물 줄만
검사하고, 릴리스 스크립트가 커밋 확인 뒤에만 태그를 만든다.

2026-08-26 v0.58.5 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
구버전(0.50 계열) → 현행 실제 업그레이드       PASS(수정 없음)
업그레이드 시험 신규(마이그레이션·backfill)    PASS
backfill 제거 시 실패 확인                     PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.58.5 렌더링      PASS
Docker linux/amd64·UID 10001·v0.58.5 빌드      PASS
```

이번 검증은 구버전 바이너리를 빌드해 실제 업그레이드를 수행했다. 마이그레이션 42→46,
데이터 보존, 그리고 그 시점에 없던 전문 인덱스가 기존 청크로 채워지는 것까지 확인했다.
수정할 결함은 없었고, 같은 경로를 CI 가 확인하도록 시험으로 남겼다.

2026-08-26 v0.58.4 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
권한 격리 전 구간 체인 시험 신규(2.6초)        PASS
ACL 조건 무력화 시 실패 확인                   PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.58.4 렌더링      PASS
Docker linux/amd64·UID 10001·v0.58.4 빌드      PASS
```

이번 릴리스로 체인 시험이 다섯 종이 되었다. 그동안 모든 체인 시험이 ACL 을 우회하는
자격으로 돌아 핵심 보장 자체는 전 구간에서 실행되지 않고 있었다. 신원에서 도구
출력까지의 경로를 두 개발자·두 저장소로 확인한다.

2026-08-26 v0.58.3 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
push→웹훅→증분 색인 체인 시험 신규(4.3초)      PASS
매니페스트 보존 로직 제거 시 실패 확인         PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.58.3 렌더링      PASS
Docker linux/amd64·UID 10001·v0.58.3 빌드      PASS
```

이번 릴리스는 v0.52.0 에서 인벤토리를 비웠던 증분 경로를 시험으로 고정했다. 보존
로직을 되돌리면 인벤토리 1건이 0건이 되는 것을 이 시험이 그대로 잡아낸다.

2026-08-26 v0.58.2 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
Bitbucket 전 구간 체인 시험 신규(2.4초)        PASS
원문 엔드포인트 훼손 시 실패 확인              PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.58.2 렌더링      PASS
Docker linux/amd64·UID 10001·v0.58.2 빌드      PASS
```

이번 릴리스로 두 소스 모두 전 구간 시험을 갖는다. Bitbucket 은 페이지 envelope,
경로 이스케이프, 원문 엔드포인트, 사용자·그룹 권한이 GitLab 과 전혀 다르므로 같은
보장을 별도로 확인해야 한다.

2026-08-26 v0.58.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
부분 장애 체인 시험 신규(10.3초)               PASS
색인 대체 경로 제거 시 실패 확인               PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.58.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.58.1 빌드      PASS
```

이번 릴리스는 체인 시험을 부분 장애까지 넓혔다. 소스 검색·재순위·임베딩·알림 수신기를
하나씩 떼어내고, 각각의 답이 무슨 일이 있었는지 말하는지 확인한다.

2026-08-26 v0.58.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
전 구간 체인 통합 시험 신규(10.3초)            PASS
마스킹 제거 시 체인 시험 실패 확인             PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.58.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.58.0 빌드      PASS
```

이번 릴리스는 그동안 릴리스마다 손으로 돌리던 전 구간 검증을 저장소에 남겼다. 소스
서버·모델·알림 수신기를 같은 프로세스에 세우고 실제 핸들러와 워커로 설정→등록→색인→
검색→발송을 확인한다. 최근 릴리스의 중대한 결함들이 모두 이 경로에서만 보였다.

2026-08-26 v0.57.3 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
알림 발송·재시도·데드레터·관리자 재시도 실측    PASS(수정 없음)
미도달 알림의 상태 보고 시험                   PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.57.3 렌더링      PASS
Docker linux/amd64·UID 10001·v0.57.3 빌드      PASS
```

이번 검증은 알림 발송 경로를 실제 수신기로 끝까지 돌렸다. 발송·실패·지수 재시도·
데드레터·관리자 재시도가 모두 설계대로였고 고칠 것이 없었다. 다만 재시도를 소진한
알림이 있다는 사실을 알려주는 통로가 없어, 플랫폼 상태에 포함하도록 했다.

2026-08-26 v0.57.2 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
pgvector 연동 실인스턴스 확인                  PASS(결함 1건 수정)
벡터 투영 경로(페이지 단위) 실동작             PASS
조회 실패·기여·미설정 문구 시험                PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.57.2 렌더링      PASS
Docker linux/amd64·UID 10001·v0.57.2 빌드      PASS
```

이번 검증은 외부 벡터 데이터베이스를 실제 pgvector 위에서 처음 끝까지 돌렸다. 투영과
조회는 정상이었으나 조회 실패가 조용히 삼켜져, 키워드로 나오지 않는 후보가 빠진 답을
완전한 답과 구분할 수 없었다. 답과 상태 화면 모두에 드러내도록 고쳤다.

2026-08-26 v0.57.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
리랭커 실제 호출·중단 실인스턴스 확인          PASS(결함 1건 수정)
재순위 성공·실패·불일치 문구 시험              PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.57.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.57.1 빌드      PASS
```

이번 검증은 재순위 경로를 실제 모델 서버와 함께 돌렸다. 호출은 정상이었으나 실패가
조용히 삼켜져, 재순위되지 않은 순서를 재순위된 것으로 읽게 되어 있었다. 답과 상태
화면 모두에서 재순위 여부를 밝히도록 고쳤다.

2026-08-26 v0.57.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
모델 서버 연동 실인스턴스 확인                 PASS(결함 2건 수정)
모델 변경 시 자동 재임베딩(7/7)                PASS
임베딩 중단 시 키워드 경로·오류 문구           PASS
엔드포인트 URL 조합 시험                       PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.57.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.57.0 빌드      PASS
```

이번 검증은 임베딩·리랭커 경로를 실제 모델 서버와 함께 처음으로 끝까지 돌렸다.
운영자가 공급자 문서대로 넣은 `/v1` 이 중복되어 404 가 나고 장애처럼 보이던 것과,
임베딩 불가 상황의 오류가 마지막 실패만 전달하던 것을 고쳤다.

2026-08-26 v0.56.5 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
동시 8 호출 부하(청크 200,000)                 PASS(p95 117ms, 오류 0건)
백업 진행 중 동시 호출                         PASS(p95 159ms, 오류 0건)
백업·복원 왕복과 전문 인덱스 복구              PASS
백업 중 검색 완료 회귀 시험                    PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.56.5 렌더링      PASS
Docker linux/amd64·UID 10001·v0.56.5 빌드      PASS
```

이번 검증은 단일 연결 SQLite 에서 동시 부하와 장시간 읽기(백업)가 서로를 막지
않는지를 실측했다. 수정할 결함은 없었고, 같은 유형의 회귀를 잡는 시험을 남겼다.

2026-08-26 v0.56.4 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
제한 사용자 관점 ACL 격리 스윕(18개 호출)      PASS(누출 0건, 수정 없음)
런북 표식 두 경로 시험                         PASS
find-runbook 지연 실측 1,143ms → 6ms           PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.56.4 렌더링      PASS
Docker linux/amd64·UID 10001·v0.56.4 빌드      PASS
```

이번 검증은 처음으로 ACL 을 우회하지 않는 자격으로 규모 인스턴스를 훑었다. 400개
저장소 중 20개만 볼 수 있는 개발자 관점에서 목록형 9종에 누출이 없었고 저장소를
직접 지정한 9종은 모두 거부됐다. find-runbook 의 전수 훑기는 인덱스 조회로 옮겼다.

2026-08-26 v0.56.3 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
단일 연결 자기 잠금 회귀 시험                  PASS
32개 도구 규모 스윕(청크 200,000)              PASS
커서 보유 중 재질의 지점 전수 조사             PASS(1건 수정, 5건 예방)
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.56.3 렌더링      PASS
Docker linux/amd64·UID 10001·v0.56.3 빌드      PASS
```

이번 검증은 규모 스윕에서 explain-search-result 가 호출마다 15초씩 멈추는 것을 찾아
원인을 goroutine 덤프로 확정했다. SQLite 단일 연결에서 커서를 쥔 채 설정을 다시
읽어 자기 자신을 기다리고 있었고, 설정 읽기가 실패해 검색 모드 설명도 기본값으로
대체되고 있었다. 같은 유형을 전수 조사해 벡터 동기화의 연결 점유를 함께 없앴다.

2026-08-26 v0.56.2 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
어휘 경로 동등성 시험(색인·훑기)               PASS
20만 청크 지연 실측                            PASS
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.56.2 렌더링      PASS
Docker linux/amd64·UID 10001·v0.56.2 빌드      PASS
```

이번 검증에서 search-semantic 이 같은 코퍼스를 두 번 훑고 있던 것을 실측으로
발견해 없앴다. 어휘 대체 경로와 query-docs 후보 선별도 인덱스를 쓰도록 옮겼다.

2026-08-26 v0.56.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5/태그 없음)  PASS
PostgreSQL 16 통합 시험(전문 인덱스)           PASS
SQLite→PostgreSQL 논리 이전 시험               PASS(회귀 1건 수정)
200,000행 질의 계획 확인                        PASS(GIN 사용)
go vet ./... && go build ./...               PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.56.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.56.1 빌드      PASS
```

이번 검증으로 두 데이터베이스의 검색 경로가 같아졌다. 생성 열이 백업의 열 대조를
깨뜨려 논리 이전이 거부되던 회귀를 기존 통합 시험이 잡았고, 생성 열을 데이터가 아닌
색인으로 취급하도록 고쳤다.

2026-08-26 v0.56.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./... (FTS5 빌드)      PASS
go test -race -count=1 ./... (태그 없음)      PASS
go vet ./... && go build ./...               PASS
전문 인덱스 트리거 동기화 시험                 PASS
두 검색 경로 동등성 시험                       PASS
20만 청크 지연·분포 실측                       PASS
오프라인 이미지 빌드(태그 적용) 전체 시험       PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.56.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.56.0 빌드      PASS
```

이번 검증은 색인 내용 검색을 훑기에서 조회로 바꿨다. 인덱스는 선택 사항으로 두어
FTS5 없이 빌드한 바이너리와 PostgreSQL 은 기존 경로로 답하고, 단어 안쪽 일치는
보충 훑기로 회수율을 유지한다.

2026-08-25 v0.55.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
규모 시험(저장소 400·청크 200,000)             PASS
ACL 제한 질의 계획 점검                        PASS(전체 스캔 없음)
부분 훑기 진단 회귀 시험                       PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.55.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.55.1 빌드      PASS
```

이번 검증은 규모를 키워 성능과 정확도를 함께 봤다. 지연은 모든 도구에서 60ms 이하였고
질의 계획에도 문제가 없었으나, 색인 훑기가 2,000건에서 멈춘다는 사실이 답에 드러나지
않아 표본이 전수처럼 읽히고 있었다.

2026-08-25 v0.55.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
CODEOWNERS 선언 소유자 실인스턴스 확인          PASS
정책 변경 후 동일 커밋 재색인 실측              PASS
CODEOWNERS 패턴·섹션 파싱 시험                 PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.55.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.55.0 빌드      PASS
```

이번 검증은 소유권 질문과 색인 정책을 실인스턴스에서 확인했다. 저장소가 CODEOWNERS 로
이미 답을 적어 두었는데도 커밋 이력만 추정하고 있었고, 색인 정책을 바꿔도 커밋이
움직이지 않으면 재색인이 0건으로 끝나 새 확장자가 반영되지 않았다.

2026-08-25 v0.54.1 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
32개 MCP 도구 실인스턴스 일괄 호출             PASS
연동 디코딩 실패 메시지 시험                   PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.54.1 렌더링      PASS
Docker linux/amd64·UID 10001·v0.54.1 빌드      PASS
```

이번 검증은 32개 도구를 실제 인스턴스에서 모두 호출했다. 그 과정에서 소스 응답을
읽지 못했을 때 Go 타입 정의가 MCP 클라이언트까지 전달되던 것을 발견해, 네 연동이
공용 진단 문장을 쓰도록 고쳤다. 인자 검증 메시지는 이미 실행 가능한 문장이어서
수정하지 않았다.

2026-08-25 v0.54.0 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
가짜 GitLab 인스턴스 등록→색인→검색 통과       PASS
비밀정보 마스킹·매니페스트 인벤토리 실측       PASS
소스 질의 불가 시 색인 대체 경로 시험          PASS
저장소 경로 충돌 오류 메시지 시험              PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.54.0 렌더링      PASS
Docker linux/amd64·UID 10001·v0.54.0 빌드      PASS
```

이번 검증은 색인 파이프라인을 실제 소스 API 위에서 처음부터 끝까지 돌렸다. 그
과정에서 `search-code` 가 색인된 내용을 전혀 읽지 않아, 소스 검색 API가 응답하지
못하면 색인이 멀쩡해도 빈 답을 주던 경로가 드러났고 이를 고쳤다.

2026-08-25 v0.53.7 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
실제 GitLab 웹훅 수신 경로 실호출              PASS
수신기 상태 코드 구분 회귀 시험                PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.53.7 렌더링      PASS
Docker linux/amd64·UID 10001·v0.53.7 빌드      PASS
```

이번 검증은 수신기가 보내는 상태 코드를 바로잡았다. 이 플랫폼 쪽 장애가 4xx 로
응답되어 소스 서버가 재시도하지 않고 이벤트가 사라지던 경로가 있었고, 읽을 수 없는
payload 와 미등록 저장소가 같은 코드로 묶여 있었다. 서명 검증·중복 제거·색인 작업
등록은 실호출로 확인했고 수정할 결함이 없었다.

2026-08-25 v0.53.6 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
권한 밖 MCP 호출 감사 기록 실측                PASS
클라이언트 주소 확정·전달 시험                 PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.53.6 렌더링      PASS
Docker linux/amd64·UID 10001·v0.53.6 빌드      PASS
```

이번 검증은 감사 기록의 두 공백을 메웠다. 권한 밖 도구 호출은 기록 없이 끝나고
있었고, 남는 클라이언트 주소는 임시 포트가 붙어 CIDR 제한과 대조할 수 없었으며
MCP 계층이 신뢰하지 않은 전달 헤더를 그대로 믿고 있었다.

2026-08-25 v0.53.5 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
실제 인스턴스 MCP 세션 실호출                  PASS
Bearer 형식 API 키 인증·거절 경로 시험         PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.53.5 렌더링      PASS
Docker linux/amd64·UID 10001·v0.53.5 빌드      PASS
```

이번 검증은 에이전트가 실제로 쓰는 경로를 인스턴스에서 확인했다. 키를
Authorization 헤더로만 보내는 클라이언트가 Keycloak 오류로 막히던 것을 고쳤고,
scope 축소 노출·공지 판정·스택 출력·scope 밖 도구 거부를 실호출로 확인했다.

2026-08-25 v0.53.4 릴리스 전 검증 결과:

```text
go test -race -count=1 ./...                 PASS
go vet ./... && go build ./...               PASS
실제 인스턴스 기동·로그인·엔드포인트 실호출     PASS(라우팅 누락 2건 수정)
문서화된 엔드포인트 라우팅 시험                PASS
오프라인 이미지 빌드 내 전체 시험              PASS
락파일 우선 집계 시험                          PASS
사용자·관리자 UI JavaScript parse·계약 시험    PASS
버전 메타데이터·GitHub Actions 정합성          PASS
Kubernetes Kustomize·:4747·v0.53.4 렌더링      PASS
Docker linux/amd64·UID 10001·v0.53.4 빌드      PASS
```

이번 검증은 단위 시험만으로는 드러나지 않던 연결 누락을 실제 기동으로 확인했다.
웹훅 수신 현황과 의존성 인벤토리 API 는 구현·문서·화면이 모두 있었으나 라우터에
등록되지 않아 항상 404 였다. 정적 화면이 루트에서 서비스되므로 미등록 경로도
화면 핸들러로 흘러가며, 이를 실패로 판정하는 계약 시험을 함께 넣었다.

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
