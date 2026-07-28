# 관리자 설정 레퍼런스

설정은 `system_settings`에 AES-256-GCM 암호문으로 저장되며 변경마다 불변
`setting_versions` 레코드를 만든다. `secret`, `password`, `token`, `apiKey`,
`pat` 이름의 필드는 조회 시 마스킹된다.

## Keycloak

Keycloak 관리자 화면에는 Base URL, Realm, Client ID, Client Secret 네 항목만 표시한다.
Issuer는 `{baseUrl}/realms/{realm}`, callback과 logout URL은 현재 `ui.publicUrl`에서
서버가 자동 계산한다. Scope와 표준 Claim 이름도 서버 기본값을 사용한다. 기본 요청
Scope는 별도 Client Scope 생성이 필요 없는 `openid profile email`이다. 그룹 Claim이
설정된 환경에서는 이를 자동 인식하지만, 그룹 매퍼가 없어도 로그인은 정상 동작한다.
Keycloak 26처럼 역할을 기본 ID Token이 아닌 Access Token에만 넣는 환경도 서명과
발급 대상을 검증한 뒤 역할을 자동 반영한다. Keycloak의
Realm 또는 Client Role 이름을 `platform-admin`, `security-admin`, `mcp-admin`,
`source-admin`, `search-admin`, `auditor`, `developer`, `service-account`,
`readonly-operator` 중 하나로 만들면 별도 매핑 없이 같은 플랫폼 역할로 적용된다.
`platform-admin`은 모든 설정과 사용자 CRUD를 수행하는 최고관리자다. 사용자 관리에서
Keycloak Subject를 사전 등록하고 활성/비활성 상태와 역할을 변경할 수 있으며, 사용자
삭제는 감사·참조 무결성을 위해 Soft Delete하고 활성 세션과 API 키를 즉시 폐기한다.

저장 전 Discovery, Authorization/Token endpoint와 OAuth client 설정을 검증하며 저장
응답은 `applied=true`, 적용 버전, 최종 Issuer와 Redirect를 반환한다. 저장 후 관리자
화면의 “OIDC 적용됨” 상태는 저장된 암호문을 다시 동적 로드해서 Discovery/JWKS/Token
endpoint를 표시하므로 입력값 시험과 실제 적용 상태를 구분할 수 있다. TLS 인증서
검증은 항상 사용하며 OS 또는 컨테이너의 신뢰 저장소를 사용한다.

```json
{
  "baseUrl": "https://sso.company.local",
  "realm": "company",
  "clientId": "git-ctx",
  "clientSecret": "secret"
}
```

사내 역할 명명 규칙 때문에 Keycloak 역할 이름을 플랫폼 역할과 같게 만들 수 없는
환경은 관리자 화면 Keycloak 탭의 “Keycloak 역할·Claim 매핑 (ACL)” 카드에서 매핑을
등록한다. 같은 카드에서 소스 ACL Principal을 만드는 Claim 이름도 지정한다. 저장되는
값은 다음과 같으며, 매핑 대상은 플랫폼 역할만 허용한다.

```json
{
  "realmRoleMappings": { "git-ctx-admin": "platform-admin" },
  "clientRoleMappings": { "admin": "platform-admin" },
  "bitbucketUserSlugClaim": "bitbucket_user_slug",
  "gitlabUserIdClaim": "gitlab_user_id",
  "groupsClaim": "groups"
}
```

`GET /api/v1/me/access`는 로그인 사용자의 플랫폼 역할, 설정 카테고리별 수정 가능
여부와 소스 ACL Principal 매핑 상태를 반환한다. 관리자 화면의 “ACL 설정 가이드”
모달이 이 응답을 그대로 표시하므로, 403 `insufficient_role`이 발생하면 어떤 역할이
빠졌는지 화면에서 바로 확인할 수 있다.

브라우저 세션은 Keycloak Access Token 수명과 분리된 12시간이며 활동 중에는 자동
연장된다. 세션 쿠키의 `Secure` 속성은 실제 요청 스킴(`X-Forwarded-Proto` 포함)을
따르므로 HTTP로 접속하는 내부망 배포에서도 새로고침에 로그인이 풀리지 않는다.

## Bitbucket Server 6.9.1

```json
{
  "baseUrl": "https://bitbucket.company",
  "apiPrefix": "/rest/api/1.0",
  "searchApiPath": "/rest/search/latest/search",
  "searchTestQuery": "README",
  "pat": "secret",
  "webhookSecret": "secret",
  "tlsVerify": true,
  "caCertificate": "",
  "proxyUrl": "",
  "timeoutSeconds": 30
}
```

PAT 대신 `username`과 `password`를 설정할 수 있다. Code Search 경로는 설치된
Bitbucket Search 모듈의 경로가 다른 경우 관리자 화면에서 변경할 수 있다. 연결 시험은
프로젝트와 첫 저장소를 탐색한 뒤 저장소·프로젝트 권한, 브랜치 API 및 실제 Code
Search POST 요청까지 확인하고 결과 수를 표시한다. Bitbucket 6.x의
권한 조회 및 Webhook 등록 API 자체가 관리자 권한을 요구하므로 서비스 계정에는 색인
대상 프로젝트의 `PROJECT_ADMIN`이 필요하다. 전역 `ADMIN`/`SYS_ADMIN`
사용자의 상속 권한은 서비스 계정이 전역 권한 API를 읽을 수 있을 때만 합성하며,
읽을 수 없으면 Fail Closed로 제외한다.

## GitLab

```json
{
  "baseUrl": "https://gitlab.company",
  "token": "secret",
  "searchTestQuery": "README",
  "webhookSecret": "secret",
  "tlsVerify": true,
  "caCertificate": "",
  "proxyUrl": "",
  "timeoutSeconds": 30
}
```

연결 시험은 그룹·프로젝트 탐색뿐 아니라 첫 프로젝트의 멤버 ACL, 브랜치 API 및
`scope=blobs` Project Search API까지 검증한다. `internal` 프로젝트의 전체 접근은 GitLab 사용자 ID가 매핑된 플랫폼
사용자에게만 적용하고, `public` 프로젝트만 모든 인증 사용자에게 허용한다.

프로젝트 식별자는 `namespace.full_path`로 구성한다. 서브그룹 프로젝트에서 직계 그룹
경로만 사용하면 `/projects/sub%2Fproject`가 되어 검색·ACL·파일 API가 모두 404가 된다.

코드 검색은 두 경로를 사용한다. Advanced Search가 켜진 인스턴스는 `/search?scope=blobs`
전역 검색 한 번으로 저장소 이름과 무관한 코드까지 찾고, 꺼진 인스턴스는 저장소 이름
검색(`/projects?search=`)으로 후보를 좁힌 뒤 프로젝트별 `scope=blobs` 검색으로
전환한다. 어느 경로가 실행됐는지와 ACL로 걸러진 건수는 `search-code` 응답의
`Diagnostics`에 남으므로, 결과가 0건일 때 원인을 구분할 수 있다.

## MCP와 색인

```json
{
  "strictCompatibility": false,
  "allowedOrigins": ["https://git-ctx.company"],
  "maxRequestBytes": 1048576
}
```

Strict Compatibility를 켜면 `resolve-library-id`, `query-docs`만 노출한다. 기본 확장
모드에서는 `search-repositories`, `search-source`, `search-code`가 추가된다.
`search-code`는 등록된 저장소뿐 아니라 연결된 원격 소스에서 이름이 일치하는 저장소를
발견하고 원격 ACL을 현재 사용자 Principal과 대조한 뒤에만 결과와 안전하게 마스킹된
스니펫을 반환한다. 관리자 역할의 개인 MCP
키에는 선택한 Scope에 따라 `get-platform-status`, `list-index-jobs`,
`reindex-repository`가 추가된다. 관리자 도구는 역할과 API 키 Scope를 동시에 검사하며
일반 사용자 키나 브라우저 세션에는 노출하지 않는다.

`security.trustedProxyCidrs`에 등록된 reverse proxy의 전달 헤더만 CIDR 제한과 감사
IP에 사용한다. 등록되지 않은 클라이언트의 `X-Forwarded-For`는 제거된다.

```json
{
  "trustedProxyCidrs": ["10.20.0.0/16"]
}
```

```json
{
  "pollingMinutes": 30
}
```

## 운영·로그·알림·보존

`operations`의 점검 모드와 안내 문구는 즉시 적용된다. 수신 주소와 HTTP Timeout은
다음 재기동부터 적용되며 저장 응답과 관리자 UI가 `restartRequired`를 표시한다.

```json
{
  "listenAddress": ":4747",
  "readHeaderTimeoutSeconds": 10,
  "readTimeoutSeconds": 30,
  "writeTimeoutSeconds": 60,
  "idleTimeoutSeconds": 90,
  "shutdownTimeoutSeconds": 15,
  "maintenanceMode": false,
  "maintenanceMessage": ""
}
```

```json
{ "level": "info" }
```

`logging.level`은 `debug`, `info`, `warn`, `error` 중 하나이며 재기동 없이 프로세스의
구조화 JSON 로그 레벨에 적용된다. Secret과 문서 원문은 로그 레벨과 무관하게 기록하지
않는다.

```json
{
  "inAppEnabled": true,
  "apiKeyExpiryWarningDays": 7,
  "rateLimitAlertsEnabled": true,
  "externalEnabled": true,
  "webhookUrl": "https://ops.company.local/git-ctx",
  "webhookAuthorization": "secret://notification-webhook-token",
  "messengerWebhookUrl": "https://messenger.company.local/hooks/git-ctx",
  "messengerAuthorization": "secret://messenger-webhook-token",
  "smtpEnabled": true,
  "smtpHost": "smtp.company.local",
  "smtpPort": 587,
  "smtpUsername": "git-ctx",
  "smtpPassword": "secret://smtp-password",
  "smtpFrom": "git-ctx@company.local",
  "smtpTlsMode": "starttls",
  "testRecipient": "operator@company.local",
  "timeoutSeconds": 10,
  "maxAttempts": 5
}
```

알림 정책은 API 키 만료와 호출량 초과 인앱 알림 생성에 동적으로 적용된다. 외부 전달을
켜면 저장 시점 이후의 알림을 Webhook, 사내 메신저 Webhook 및 사용자 이메일로 전송한다.
전달 이력은 멱등 Outbox에 저장되며 지수 Backoff 후 Dead Letter로 전환된다. 보안
관리자는 목적지나 인증정보가 노출되지 않는 전달 이력을 조회하고 실패 건을 수동
재시도할 수 있다. 설정 탭의 `연결 시험`은 저장 전에 설정된 모든 채널에 실제 시험
메시지를 보낸다. 운영망 SMTP는 `tls` 또는 `starttls`를 사용해야 하며 `none`은
localhost 시험 서버에만 허용된다.

```json
{
  "auditLogDays": 365,
  "mcpCallDays": 90,
  "notificationDays": 90,
  "webhookEventDays": 30,
  "indexJobDays": 30,
  "securityEventDays": 180,
  "settingVersionDays": 365
}
```

보존일 `0`은 영구 보존이다. 현재 활성 설정 버전과 실행 대기·실행 중 색인 작업은 보존
정리에서 제외한다.

## 검색과 임베딩

```json
{
  "keywordWeight": 1.0,
  "vectorWeight": 0.35,
  "candidateLimit": 5000,
  "finalK": 8,
  "rerankLimit": 30
}
```

선택적으로 사내 AI Gateway가 `/v1/rerank` 계약(`query`, `documents`, `model`,
`top_n`)을 제공하면 같은 `model` 설정에 다음 필드를 추가한다. Reranker에는 ACL을
통과한 후보만 전달되며 장애나 불완전 응답 시 기존 하이브리드 순서를 사용한다.

```json
{
  "rerankerEnabled": true,
  "rerankerProvider": "openai-compatible",
  "rerankerBaseUrl": "https://ai-gateway.company",
  "rerankerModel": "internal-reranker",
  "rerankerApiKey": "secret",
  "rerankerTimeoutSeconds": 15
}
```

기본 `model.provider`는 외부 호출이 없는 256차원 `local`이다. 사내 vLLM 또는 AI
Gateway가 OpenAI embeddings API를 제공하면 다음처럼 변경한다. 저장 전에 실제
embedding 요청으로 연결을 시험한다. 색인 청크에는 provider, model, dimension,
revision이 함께 저장되며 모델 identity가 변경되면 동일 commit이라도 전체 재색인한다.

```json
{
  "provider": "openai-compatible",
  "baseUrl": "https://ai-gateway.company",
  "model": "internal-embedding",
  "apiKey": "secret",
  "timeoutSeconds": 30,
  "tlsVerify": true,
  "caCertificate": "",
  "proxyUrl": ""
}
```

운영 환경에서 `tlsVerify: false`는 연결 진단 외에는 사용하지 않는다. 설정 변경은
외부 연결 검사를 통과해야 저장된다.

## 사용자 화면 브랜드

`ui` 설정은 재배포 없이 공개 화면의 서비스명, 태그라인, 공지와 자산을 변경한다.
자산 URL은 루트 상대 경로 또는 HTTPS URL만 허용하며 공개 API에는 아래 비밀이 아닌
필드만 노출된다.

```json
{
  "publicUrl": "https://git-ctx.company.local",
  "serviceName": "git-ctx",
  "tagline": "사내 개발 지식 MCP",
  "logoUrl": "/logo.svg",
  "faviconUrl": "/favicon.svg",
  "notice": ""
}
```

## OpenTelemetry 추적

`observability` 설정을 저장할 때 임시 span을 실제 Collector로 전송해 endpoint, TLS,
사내 CA, proxy와 인증 헤더를 검증한다. 저장에 성공하면 새 provider가 즉시 적용되고
기존 provider는 flush 후 종료된다. HTTP 요청은 W3C `traceparent`를 이어받으며 응답의
`X-Trace-Id`로 장애 로그와 trace를 연결할 수 있다. MCP 질의 원문과 문서 원문은 span
attribute에 기록하지 않는다.

```json
{
  "enabled": true,
  "otlpEndpoint": "https://otel-collector.company.local/v1/traces",
  "serviceName": "git-ctx",
  "sampleRatio": 0.1,
  "headers": {
    "Authorization": "Bearer secret"
  },
  "timeoutSeconds": 10,
  "tlsVerify": true,
  "caCertificate": "-----BEGIN CERTIFICATE-----...",
  "proxyUrl": ""
}
```

`headers.Authorization`, Token, API Key 필드는 재조회 시 마스킹되며 원문은 암호화된
설정에만 보관된다. 평문 HTTP endpoint는 기본 거부한다. 로컬 Collector 시험에서만
`allowInsecureLocalhost: true`로 localhost HTTP를 허용할 수 있다.

## 관리자 연동 설정과 검증

관리자 설정 탭은 Keycloak, Bitbucket, GitLab, MCP, 검색, 모델, OpenSearch,
색인, 보안, Vault, 알림, 로깅, 관측성, 백업, 보존, 운영, UI의 실제 런타임 필드를
전용 폼으로 제공한다. 그 밖의 확장 정책은 같은 탭의 고급 JSON에서 편집하며, 전용
폼과 JSON은 양방향으로 동기화된다. Bootstrap 환경변수는 `GIT_CTX_DB_DSN` 하나뿐이다.

DB DSN을 제외한 운영 설정은 관리자 화면에서 자동 조회·시험·저장·삭제한다. Keycloak,
Bitbucket, GitLab과 모델 영역은 URL, Client/Token/API Key, 모델, TLS, 사내 CA,
Proxy와 Timeout 전용 필드를 제공하고 알 수 없는 확장 필드는 고급 JSON 편집기에
보존한다. Secret/PAT/Token/API Key는 암호화 저장되고 재조회 시 `********`로
마스킹되며, 마스킹 값을 그대로 저장하거나 시험하면 이전 암호문 값을 재사용한다.
탭에 다시 접근하면 일반 필드는 DB의 현재 버전 값으로 자동 복원되고 설정 버전,
마지막 수정자와 수정시각을 함께 표시한다. 각 비밀 필드는 `********`와 “저장된
비밀값” 상태를 표시하므로 값이 설정됐는지 확인할 수 있다. `secret://이름`은 비밀
원문이 아닌 참조이므로 참조 이름을 다시 표시한다.

`연결 테스트·검증`은 저장하지 않고 실제 호출을 수행한다.

- Keycloak: OIDC Discovery와 JWKS/endpoint/issuer 계약
- Bitbucket 6.9.1: 인증된 프로젝트 REST 조회
- GitLab: 인증된 Group API 조회
- Embedding: OpenAI 호환 `/v1/embeddings` 실제 벡터 응답
- Reranker: `/v1/rerank` 문서 index와 score 완전성
- OpenSearch: 인증된 cluster 조회, 청크 index 존재 확인 또는 mapping 생성
- OpenTelemetry: 실제 시험 span export
- Backup: 전용 경로 생성·쓰기와 정책 검증

저장도 동일 검증을 다시 통과해야 한다. 시험 결과는 성공·실패 모두 감사
로그에 남되 입력 Secret과 응답 원문은 기록하지 않는다.

관리자 화면은 `/admin`으로 직접 접근할 수 있으며 대메뉴(설정, MCP, 소스·색인, 검색 품질, 보안·Secret, 감사,
데이터베이스, 운영 상태, 백업·복구)와 설정 종류별 탭으로 구성된다. 각 연동의
Keycloak 탭은 고급 JSON과 수동 불러오기를 노출하지 않는다.
탭 진입과 새로고침 때 저장값을 자동 조회하고 Secret 원문만 마스킹한다.

Keycloak은 `baseUrl`과 `realm`을 입력하면 `issuerUrl`을
`{baseUrl}/realms/{realm}`으로 생성한다. Redirect가 비어 있으면 현재 공개 URL의
`/auth/callback`과 `/`를 각각 로그인·로그아웃 기본값으로 저장한다. 저장 전 Discovery뿐
아니라 브라우저 OAuth 구성까지 검증하므로 Redirect 누락 상태가 저장되는 것을 막는다.
설정 저장만으로 Bootstrap을 폐기하지 않으며, Keycloak에서 `platform-admin` 역할을 받은
사용자가 “Keycloak 로그인 시험”에 성공한 시점에 여러 Pod의 Bootstrap 토큰·세션을
전역 폐기한다. 로그인 실패나 잘못된 역할 매핑이면 복구 진입이 유지된다.

## 관리 Secret과 Vault KV v2

`security-admin`은 보안 화면에서 이름 기반 Secret을 등록·회전·중지할 수 있다. 기본
backend는 DSN 파생 AES-256-GCM으로 암호화하는 `database`이며, Vault 설정을 먼저
연결 시험·저장하면 `vault` KV v2 backend도 선택할 수 있다. 목록과 감사 로그에는
이름, backend, 버전, 상태만 기록하고 원문은 생성·회전 요청 뒤 반환하지 않는다.

```json
{
  "enabled": true,
  "baseUrl": "https://vault.company.local:8200",
  "token": "********",
  "namespace": "company/platform",
  "mount": "secret",
  "prefix": "git-ctx",
  "tlsVerify": true
}
```

Vault 연결 시험은 `GET /v1/auth/token/lookup-self`, 저장과 조회는 KV v2의
`/{mount}/data/{prefix}/{name}` 계약을 사용한다. Token은 암호화된 `vault` 관리자
설정에 보관하며 최소 권한 정책과 짧은 TTL의 전용 토큰을 사용한다. Vault를 사용하지
않아도 외부 Bootstrap 값은 계속 `GIT_CTX_DB_DSN` 하나뿐이다.

Keycloak Client Secret, Bitbucket PAT, GitLab Token, 모델 API Key 등 문자열 설정에는
원문 대신 다음처럼 참조를 입력할 수 있다.

```json
{
  "pat": "secret://bitbucket-readonly-pat"
}
```

참조는 외부 호출 직전에 해석된다. Secret이 없거나 중지됐거나 Vault가 장애이면 해당
연동은 Fail Closed하며 암호화 설정에 원문을 복사하지 않는다. 설정 조회 시 참조 이름은
보이지만 Secret 원문은 어떤 조회 API에서도 반환하지 않는다.

## 메타 데이터베이스 복구와 전환

Bootstrap PostgreSQL에 연결할 수 없으면 `backups/recovery.db` SQLite로 복구 기동한다.
관리자 “데이터베이스” 메뉴는 현재 driver, Ping 지연, pool, migration과 복구 모드를
표시한다. `platform-admin`만 PostgreSQL DSN 연결 시험과 데이터 이전을 실행할 수 있다.
연결 시험은 대상 DB를 변경하지 않으며 데이터 이전은 정확한 확인문을
요구한다. 성공한 DSN은 암호화 저장되고 API에서는 원문 대신 `********`로만 보인다.

전환 직후 서비스는 새 요청을 받지 않고 readiness를 실패시켜 재시작을 요구한다. 재시작
뒤 PostgreSQL 연결이 다시 실패하면 기존 recovery SQLite로 Fail Safe 복귀한다. 이 모드는
장기 운영 DB가 아니므로 Kubernetes에서는 단일 replica로 연결 복구와 이전만 수행한다.

최초 Bootstrap이 폐기된 뒤 Keycloak 설정 장애로 관리자 로그인이 불가능하면
`git-ctx recovery-token --ttl 15m`으로 일회용 복구 토큰을 생성하고
`/admin?recovery=1`에서 소비한다. 복구 토큰은 DSN에서 파생한 키로 서명되고 원문을
저장하지 않으며, 1회 사용·최대 1시간 만료·영구 MCP 키 생성 금지를 적용한다.

## 모델 미설정 검색 모드

`model.provider`가 없거나 `local`이면 벡터와 Reranker 점수를 사용하지 않는다. 저장소
ACL과 Library ID를 먼저 검증한 다음 GitLab은 프로젝트 `/search?scope=blobs`,
Bitbucket Server 6.9.1은 `/rest/search/latest/search`를 해당 저장소로 제한해 호출한다.
서버측 검색이 결과를 반환하면 Context7 형식 Markdown과 원문 출처로 조립한다. 검색
API 결과 경로가 현재 ref의 승인된 색인에 존재할 때만 채택하고, 반환 본문은 Secret
Scan·경로 정책을 통과한 로컬 청크로 대체한다. 검색 API가 비활성·장애이면 로컬
색인의 BM25 키워드 검색으로 Fail Safe fallback한다.
Bitbucket code search는 기본 브랜치 계약이므로 비기본 branch/tag 질의는 버전별 로컬
색인만 사용한다.

## OpenSearch 키워드 검색

`opensearch` 설정은 선택 사항이다. 관리자 화면에서 사용 여부, Base URL, index,
Basic 인증 또는 인코딩된 API Key, TLS 검증, 사내 CA, Proxy와 Timeout을 입력하고
저장 전에 실제 연결과 index mapping 생성을 시험한다.

```json
{
  "enabled": true,
  "baseUrl": "https://opensearch.company.local:9200",
  "index": "git-ctx-chunks",
  "username": "git-ctx",
  "password": "********",
  "tlsVerify": true,
  "timeoutSeconds": 30
}
```

활성화 뒤 최초 색인은 해당 ref 전체를 Bulk API로 투영한다. 이후에는 source compare
API의 변경 journal과 projection cursor를 이용해 삭제된 chunk ID와 변경 파일만 반영한다.
commit 이력이 이어지지 않거나 cursor가 없으면 전체 projection으로 자동 복구한다.
ACL fingerprint가 변경되면 commit이 같아도 전체 ACL을 갱신한다. Projection 실패는 색인 작업 실패로
처리되어 지수 Backoff 재시도 대상이 된다. 검색 요청은 repository, ref와 호출자
principal을 OpenSearch `bool.filter`에 넣는다. OpenSearch에서는 청크 ID와 점수만
받으며 출력 본문·경로·출처는 PostgreSQL의 승인된 청크에서 다시 읽는다. 장애이거나
아직 projection이 없는 경우 기존 DB BM25 검색으로 안전하게 전환한다.

## 검색 품질 회귀 게이트

`search-admin`은 Library ID, 질의, 시험 ACL principal과 정답 파일 경로를 등록하고
현재 운영 검색을 실행한다. 문서 원문은 결과 DB에 저장하지 않고 순위화된 상대 경로,
Recall@K, reciprocal rank와 nDCG@K만 보존한다. 하나 이상의 질의 오류 또는 평균
지표가 실행 임계값보다 낮으면 상태는 `regressed`다.

## 백업

애플리케이션 백업은 SQLite와 PostgreSQL에서 동일한 논리 형식으로 생성되고 gzip 뒤
DSN에서 도메인 분리해 파생한 AES-256-GCM 키로 인증 암호화된다. 여러
Pod가 같은 DB와 RWX 볼륨을 사용할 때도 schedule slot 고유 제약으로 한 번만 실행된다.

```json
{
  "enabled": true,
  "directory": "/var/lib/git-ctx/backups",
  "intervalHours": 24,
  "retentionCount": 7,
  "maxBytes": 536870912
}
```

저장 시 전용 디렉터리의 생성·쓰기 가능 여부를 검사한다. `directory`는 애플리케이션
전용 경로여야 하며 파일시스템 루트는 거부한다. 백업 암호화 키와 아카이브를 같은
스토리지에 보관하지 않는다.
