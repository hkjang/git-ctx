# 구현 완료 감사

이 문서는 제품 요건의 “코드로 검증된 범위”와 사내 시스템이 있어야만 검증할 수 있는
“배포 승인 범위”를 구분한다. 외부 승인 항목은 시험 결과 없이 완료로 간주하지 않는다.

## 코드와 자동 시험으로 검증된 범위

| 영역 | 상태 | 구현·검증 증거 |
|---|---|---|
| Context7 MCP | 완료 | `/mcp` GET/POST/DELETE, initialize, session, tools/list, tools/call 및 두 호환 도구의 통합 시험 |
| 인증과 API 키 | 완료 | OIDC/JWKS, PKCE, HMAC 키 저장, 1회 노출, 회전·중지·폐기, CIDR·도구·저장소·호출량 제한 |
| 권한 | 완료 | 사용자/그룹/키 제한을 후보 SQL 단계에서 적용하고 미인가 저장소를 일반화된 오류로 처리 |
| Bitbucket/GitLab 어댑터 | 완료 | Bitbucket Server REST 1.0 및 GitLab API v4 계약 시험, webhook 검증·중복 제거 |
| 색인 | 완료 | ref별 작업, 저장소 정책, Markdown/코드 청킹, Secret 차단·마스킹, 재시도·polling |
| 검색 | 완료 | DB BM25·벡터와 선택적 OpenSearch projection, 후보 질의 ACL 필터, DB 원문 재검증, 사내 `/v1/rerank`와 장애 fallback |
| 모델 미설정 검색 | 완료 | ACL 검증 뒤 Bitbucket/GitLab source query API, Context7 출력 조립과 안전한 BM25 fallback 계약 시험 |
| 사용자 기능 | 완료 | 저장소·키·제한·사용량·호출·알림·MCP 설정·도구 시험 UI/API |
| 관리자 기능 | 완료(구현 범위) | 설정·연결시험·버전·rollback, 저장소·정책·작업, MCP 도구, 키·감사·보안·상태 UI/API와 역할별 메뉴·쓰기 통제 |
| 데이터베이스 | 완료 | SQLite 회귀 시험 및 빈 PostgreSQL 16에서 001~013 migration/readiness와 암호화 백업·복원 실검증 |
| 배포 | 완료 | 비루트 Docker 이미지 실행, Compose, Kubernetes Kustomize와 기본 NetworkPolicy 렌더링 |
| 관측성 | 완료 | JSON 요청 로그, request ID, health/readiness, Prometheus와 동적 OTLP HTTP tracing |
| 백업·복구 | 완료(애플리케이션 범위) | SQLite/PostgreSQL 공통 암호화 아카이브, 주기·보존, 무결성 검증, 트랜잭션 복원, 세션 무효화와 관리자 UI/API |
| 검색 품질 평가 | 완료 | ACL 적용 정답 사례, Recall@K·MRR·nDCG@K, 임계값 회귀 판정, 이력·상세 UI/API |
| 관리자 연동 설정 | 완료 | Keycloak·Bitbucket·GitLab·Embedding·Reranker 전용 필드, 실제 호출 시험, 마스킹·암호화·버전·rollback |
| OpenSearch | 완료(계약 시험) | 관리자 연결·index mapping 시험, ref별 delete/bulk projection, repository·ref·principal 선필터, DB 청크 hydration, Worker 재시도 |
| 최초 관리자 세션 | 완료 | 일회용 토큰을 30분 HttpOnly·SameSite 세션으로 교환, Origin 검증, Keycloak 저장 시 세션·키 전역 폐기 |
| 버전 표시 | 완료 | 공개 설정과 `/api/v1/me` 버전 제공, 로그인 전 상단·안내와 로그인 후 프로필 표시 |
| 비밀정보 관리 | 완료(계약 시험) | 암호화 DB/Vault KV v2 backend, 등록·회전·중지, 원문 비노출, `secret://` 동적 참조와 Fail Closed |
| 관리자 UI 구조 | 완료 | 역할별 대메뉴, 설정 종류별 탭, 고급 JSON 접기, TLS 토글·CA 조건부 표시, 저장 진행·오류 상태 |
| Keycloak 설정 안정성 | 완료 | Base URL+Realm 정규화, Redirect 기본값, OAuth 저장 전 검증, 실제 platform-admin 로그인 뒤 Bootstrap 폐기 |
| DB 연결 관리 | 완료 | 공개 비민감 상태, 관리자 DB·pool·migration 진단, Prometheus up, SQLite 단일 Writer pool, PostgreSQL 실패 복구 기동·연결 시험·논리 이전·재시작 전환 |

2026-07-27 로컬 검증 결과:

```text
go test -race ./...                         PASS
go vet ./...                                PASS
node --check web/app.js                     PASS
node test/web/roles.test.js                 PASS
kubectl kustomize deploy/kubernetes/base    PASS
PostgreSQL 16 migration 001..013            PASS
PostgreSQL 16 backup/restore round trip     PASS
PostgreSQL 16 quality benchmark contract    PASS
PostgreSQL 16 DSN-only application startup PASS
PostgreSQL 실패→SQLite 복구→PostgreSQL 논리 이전·재기동 PASS
node test/web/admin-ui.test.js              PASS
Docker build + UID 10001 readiness/UI       PASS
OpenSearch auth/mapping/bulk/ACL contract   PASS
Default listen address :4747 readiness      PASS
```

## 구현되지 않았거나 후속 단계인 항목

| 항목 | 현재 상태 | 완료에 필요한 작업 |
|---|---|---|
| 레거시 MCP SSE endpoint | 미구현(선택) | 승인 대상 구형 클라이언트가 요구할 때 추가 |
| Confluence/PDF 등 확장 소스 | 미구현(3단계) | SourceRepository 플러그인과 파서 구현 |

## 사내 배포 환경 승인 게이트

다음 항목은 로컬 모의 서버나 단위 시험만으로 최종 완료를 선언할 수 없다.

1. 실제 Keycloak에서 PKCE 로그인, 역할·그룹·사용자 속성 매핑, 로그아웃, 잘못된
   issuer/audience와 키 회전 시험
2. 실제 Bitbucket Server 6.9.1에서 프로젝트·저장소·ACL·branch/tag 수집과
   저장소별 push webhook, 누락 polling 시험
3. 승인된 Codex CLI, Claude Code, Cursor 중 최소 2개에서 MCP 연결·도구 호출 시험
4. 권한 부여·회수 전후 저장소 이름, ID, 캐시, 오류 내용의 완전 비노출 시험
5. 목표 데이터량과 50개 동시 호출에서 P95 및 오류율 측정
6. PostgreSQL 백업/복원, 원래 DSN Secret 분리 복구, Keycloak 설정 rollback과
   break-glass 운영 훈련
7. 운영 NetworkPolicy를 실제 Keycloak·Bitbucket·GitLab·DB CIDR로 제한하고
   사내 CA·프록시 장애/복구 시험
8. 실제 운영 대상 OpenSearch 버전에서 mapping, Bulk 재색인, 장애 복구와 목표 규모
   repository/ref/principal 필터 성능 시험
9. 실제 사내 Vault에서 최소 권한 KV v2 정책, Token TTL·회전·폐기, HA standby,
   seal/unseal 장애와 Vault backup/DR 복구 시험

따라서 현재 산출물은 실행 가능한 MVP 기반과 2단계 일부 기능까지 구현됐지만,
요건서 전체의 최종 제품 완료 판정은 위 미구현 항목의 범위 결정과 사내 승인 시험 후에
가능하다.
