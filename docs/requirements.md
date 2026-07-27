# git-ctx 개발 요건 및 추적표

## 제품 경계

git-ctx는 외부 Context7을 호출하지 않는다. 사용자의 Bitbucket/GitLab 접근 권한을
후보 생성 이전에 적용하고, 허용된 버전 스냅샷만 Markdown과 원문 출처로 반환한다.
Go 모듈형 모놀리스로 시작하되 API/MCP/indexer/scheduler 프로세스를 분리할 수 있는
포트·어댑터 구조를 사용한다.

## 필수 인터페이스

| 영역 | 계약 |
|---|---|
| MCP | `/mcp` GET/POST/DELETE, JSON-RPC 2.0, Streamable HTTP |
| Resolve | `resolve-library-id(libraryName: string, query: string)` |
| Query | `query-docs(libraryId: string, query: string)` |
| 인증 | Keycloak Bearer, `CONTEXT7_API_KEY`, `X-API-Key` |
| Library ID | `/project/repository[/branch-or-tag]`, 소문자 정규형 |
| DB | PostgreSQL 운영 기본, SQLite 개발·단일 노드 지원 |

## 구현 추적

상태 표기는 `완료`, `부분`, `예정`이며 코드가 존재하고 테스트 가능한 경우에만 완료다.

| 요구사항 | 상태 | 근거/남은 작업 |
|---|---|---|
| MCP initialize/list/call | 완료 | `internal/mcp`, 계약 테스트 |
| Context7 도구명·입력·텍스트 출력 | 완료 | 두 도구 스키마 및 `content[type=text]` |
| API 키 HMAC 저장·1회 표시·회전·중지·폐기 | 완료 | 중복 유효기간, CIDR·도구·저장소·분/시/일 제한 포함 |
| ACL 선필터와 버전별 문서 조회 | 완료 | `internal/search` SQL ACL join |
| PostgreSQL·SQLite | 완료 | 공통 migration 및 driver |
| 암호화 설정·버전·감사 | 완료 | AES-256-GCM 저장, 마스킹, 불변 버전, 연결 시험, 새 버전 방식 rollback |
| Keycloak OIDC/JWKS 검증과 역할 매핑 | 완료 | 동적 암호화 설정, issuer/audience/서명 검증, Realm/Client 역할 매핑 |
| 브라우저 Authorization Code+PKCE | 완료 | 일회성 state/verifier, HttpOnly 세션, Claim 미리보기, Single Logout |
| Bitbucket 6.9.1 수집/Webhook | 완료 | REST v1 어댑터, ref 색인, HMAC webhook, 멱등 작업, polling 보정 |
| GitLab 수집/Webhook | 완료 | API v4 어댑터, ref 색인, token webhook, 멱등 작업, polling 보정 |
| 하이브리드 검색 | 완료 | 동적 BM25+로컬/사내 임베딩, ACL 이후 사내 API Reranker, 장애 fallback 구현 |
| 사용자/관리자 UI | 완료 | SSO, 키·사용량, MCP 연결과 세분화 관리자 역할별 설정·소스·도구·보안·감사 화면 |
| 관리자 동적 설정 | 완료 | 전체 범주 암호화 저장·버전·마스킹·연결시험·명시적 rollback 및 즉시 조회 반영 |
| 저장소·색인 운영 UI | 완료 | 탐색, 등록, 초기/수동/주기 색인, 작업 조회·재시도 |
| Secret Scan | 완료 | 개인키 차단, 자격증명·클라우드 키 마스킹, 보안 이벤트 이력 |
| 그룹 ACL | 완료 | Keycloak→Bitbucket 그룹 매핑을 사용자·API 키 후보 검색에 적용 |
| 키 알림 | 완료 | 7일 이내 만료와 호출량 초과 이상 사용 알림 |
| Kubernetes/관측/백업 | 부분 | Kustomize, hardened deployment, JSON 로그·readiness·Prometheus 구현; OTel·자동 백업은 미구현 |

상세한 구현 증거와 실환경 승인 경계는
[completion-audit.md](completion-audit.md)에 기록한다.

## 승인 게이트

1. 세 MCP 클라이언트 중 최소 두 개로 호환 계약을 실환경 검증한다.
2. 권한 없는 저장소의 이름·ID·오류 차이를 포함한 비노출 테스트를 통과한다.
3. Keycloak 역할/그룹/사용자 속성과 Bitbucket user slug 매핑을 Fail Closed로 검증한다.
4. 저장소별 webhook 중복·역순·누락 및 polling 보정을 검증한다.
5. 50 동시 MCP 호출과 목표 데이터량에서 P95를 측정한다.
6. 설정 실패 rollback, DB 백업/복원, break-glass 절차를 훈련한다.
