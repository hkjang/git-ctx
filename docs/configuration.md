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

## 검색 권한 모델

검색과 MCP 도구는 저장소 ACL을 Fail-Closed로 적용한다. 예외적으로
`platform-admin`, `source-admin`, `search-admin` 역할은 카탈로그를 운영하는
역할이므로 Bitbucket·GitLab 계정이 매핑되지 않아도 등록된 모든 저장소와 원격 소스
검색 결과를 볼 수 있다. 이때 원격 저장소 권한 API 호출도 생략하므로 검색이 빨라진다.

우회가 적용되면 `search-code` 응답 `Diagnostics`에 `repository ACL checks are
bypassed` 문구가 남고, `GET /api/v1/me/access`는 `unrestrictedSearch: true`를
반환한다. 세 역할은 실제 소스 접근 권한과 무관하게 색인된 내용을 볼 수 있으므로 꼭
필요한 담당자에게만 부여한다. 그 외 역할은 종전과 동일하게 claim 기반으로 제한된다.

`GET /api/v1/admin/setup-status`는 Keycloak, ACL claim 매핑, 소스 연결, 저장소 등록,
초기 색인, 백업 예약 여섯 단계의 진행 상태를 판정해 관리자 화면 상단 카드에 표시한다.

`POST /api/v1/admin/search-diagnostics`는 `search-admin` 이상 권한으로 다른 사용자의
ACL Principal을 사용해 같은 질의를 재현한다. 응답에는 저장소별 결과 수와 판정 근거만
담기고 코드 조각·파일 경로는 포함하지 않으며, 실행은 `search.diagnostics` 감사 로그로
남는다.

`GET /api/v1/admin/settings/{category}/versions`는 버전·변경자·변경 시각만 반환한다.
저장된 값은 암호문으로만 보관되며 이력 조회로도 복호화되지 않는다.

`/api/` 응답에는 `Cache-Control: no-store`가 적용되어 공용 PC의 브라우저·프록시
캐시에 ACL 결과가 남지 않는다. 최초 관리자 토큰과 일회용 복구 토큰 로그인은 같은
클라이언트 주소에서 5분당 10회로 제한되고, 초과 시 429와 감사 로그가 남는다.

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

`autoRegisterWebhook`가 켜져 있으면 저장소 등록 시 Push/Tag Webhook을 자동 등록하며
이때 `webhookSecret`이 필요하다. Webhook을 사용하지 않는 환경은 관리자 화면에서
“저장소 등록 시 Webhook 자동 등록”을 끄면 Secret 없이 등록할 수 있다.

프로젝트 식별자는 `namespace.full_path`로 구성한다. 서브그룹 프로젝트에서 직계 그룹
경로만 사용하면 `/projects/sub%2Fproject`가 되어 검색·ACL·파일 API가 모두 404가 된다.

코드 검색은 두 경로를 사용한다. Advanced Search가 켜진 인스턴스는 `/search?scope=blobs`
전역 검색 한 번으로 저장소 이름과 무관한 코드까지 찾고, 꺼진 인스턴스는 저장소 이름
검색(`/projects?search=`)으로 후보를 좁힌 뒤 프로젝트별 `scope=blobs` 검색으로
전환한다. 어느 경로가 실행됐는지와 ACL로 걸러진 건수는 `search-code` 응답의
`Diagnostics`에 남으므로, 결과가 0건일 때 원인을 구분할 수 있다.

## 색인 동작

색인 대상 파일은 저장소별 정책으로 결정한다. 기본 정책은 문서·설정과 주요 언어
확장자를 포함하고 `Dockerfile`, `Makefile`처럼 확장자가 없는 빌드 파일도 이름으로
인식한다. 정책에 없는 확장자만 있는 저장소는 작업이 “완료”로 끝나면서 색인이 0건이
되므로, 이 경우 작업 오류란에 “N file(s) listed but none matched the index policy”
경고가 남는다. 관리자 화면의 등록 저장소 목록에서 [색인 정책] 으로 저장소별 확장자와
제외 경로를 조정한 뒤 즉시 재색인할 수 있다.

작업은 클레임 후 15분(리스)이 지나도 `running`이면 자동으로 다시 큐에 들어간다.
서비스 재시작이나 응답 없는 원격 호출로 중단된 작업이 영구히 `running`으로 남아 저장소가
계속 미색인 상태가 되던 문제를 막는다. 한 작업의 실행 시간은 30분으로 제한되어 하나의
저장소가 전체 색인 큐를 막지 못한다.

임베딩 모델이 설정된 경우 작업 시작 시 1건 probe 요청을 보낸다. URL·모델명·API Key
오류는 파일을 내려받기 전에 즉시 실패로 기록되며, 오류 메시지에 확인할 설정을 함께
남긴다. 커넥터·브랜치 조회·색인·projection 실패도 각각 어느 단계인지 명시한다.

`GET /api/v1/admin/index-diagnostics`는 저장소별 상태(`indexed`, `partial`, `indexing`,
`queued`, `stalled`, `failed`, `empty`, `never-run`)와 원인·조치 문구, 청크·심볼 수,
마지막 작업 정보를 반환한다. 관리자 화면 “색인 진단” 표가 이 응답을 그대로 표시한다.

개별 파일 다운로드 실패는 저장소 전체 색인을 중단시키지 않는다. 건너뛴 파일 수와
사유는 작업 행에 남고, 모든 파일이 실패한 경우에만 작업이 실패로 처리된다. 진행 중인
작업은 처리 파일 수를 주기적으로 갱신하며 화면도 자동으로 새로고침된다.

임베딩은 청크 단위 개별 호출 대신 최대 32개씩 묶어 요청하고, 429나 5xx 응답은 지수
백오프로 재시도한다. 잘못된 모델 이름 같은 4xx 응답은 재시도하지 않고 즉시 실패로
보고한다.

## 파일명 검색

색인은 정책에 맞는 파일의 *본문*만 청크로 저장하지만, `repository_files` 테이블에는
ref의 **모든 경로**를 기록한다(migration 030). 따라서 lockfile, 이미지, 정책에서
제외된 소스도 `find-file`로 찾을 수 있고, 결과에는 본문 색인 여부가 함께 표시되어
에이전트가 다음 도구(`query-docs`, `get-symbol-context`, `search-code`)를 고를 수 있다.

패턴 규칙은 개발자가 입력하는 방식을 따른다.

| 입력 | 의미 |
| --- | --- |
| `README` | 파일명에 대한 대소문자 무시 부분 일치, 정확한 이름이 상위 |
| `*.tf`, `auth*.py` | 파일명 글롭 (`*`는 디렉터리를 넘지 않음) |
| `db/migrations/` | 전체 경로 부분 일치 |
| `**/migrations/*.sql` | 임의 깊이 경로 글롭 |

아직 파일 목록이 없는 저장소(등록 직후)는 최대 5개까지 원격 tree를 즉시 조회해
보완하고, 그 사실을 응답 `Diagnostics`에 남긴다.

## 파일 본문 조회

`read-file`은 파일 전체 또는 `startLine`~`endLine` 범위를 반환한다. 색인된 파일은
저장된 청크를 재조립하고(이미 Secret 마스킹된 내용), 정책에서 제외된 파일은 소스
서버에서 즉시 읽어 Sanitize한 뒤 반환한다. 응답은 1,200줄 또는 192KiB로 제한되며
잘린 경우 남은 범위를 안내한다. 같은 경로가 여러 저장소에 있으면 임의로 고르지 않고
후보 Library ID 목록과 함께 `libraryId` 지정을 요구한다.

MCP 호출 기록은 결과가 0건인 성공 호출을 `empty` outcome으로 남긴다. 개인 사용량
화면과 관리자 통계에서 "실패"와 "결과 없음"을 구분할 수 있고, 빈 결과는 캐시하지
않으므로 색인이 끝나면 다음 호출에서 바로 반영된다.

## 이력·디렉터리 도구

`get-file-history`는 GitLab `/repository/commits?path=`, Bitbucket
`/commits?path=` 를 사용해 해당 경로를 바꾼 커밋을 최신순으로 반환한다(migration
032). `list-directory`는 저장된 파일 목록으로 하위 항목을 폴더 우선 정렬해 보여
주므로 원격 호출이 없다. 두 도구 모두 `read-file` 과 같은 ACL·모호성 규칙을 쓴다.

MCP `initialize` 응답에는 `instructions` 가 포함된다. 어떤 질문에 어떤 도구를 먼저
써야 하는지, 응답의 `Notes` 를 어떻게 읽어야 하는지를 클라이언트 모델에 한 번만
알려 주어 도구 선택 오류를 줄인다.

## 변경 요청 검색

`search-merge-requests`는 GitLab 병합 요청(`/merge_requests?search=`)과 Bitbucket
풀 리퀘스트(`/pull-requests`)를 함께 검색한다(migration 033). Bitbucket 6.x는 PR
텍스트 검색 API가 없으므로 제목·설명을 서버에서 받아 로컬로 필터링한다. 범위를 지정
하지 않으면 접근 가능한 저장소 중 최대 8곳을 병렬 조회하며, 설명은 Secret 마스킹 후
4,000자로 제한한다.

색인 작업이 재시도 한도를 모두 소진해 최종 실패하면 `platform-admin`·`source-admin`
사용자에게 인앱 알림을 생성한다. 같은 저장소·ref는 하나의 알림으로 갱신되므로 재시도
루프가 알림을 반복 생성하지 않는다.

## 영향 범위와 프로젝트 규약

`find-dependents`는 색인된 의존 관계(`code_dependencies`)를 저장소 경계 없이 조회해
공유 심볼·모듈·테이블을 사용하는 모든 위치를 반환한다(migration 034). 정확히 일치하는
대상이 부분 일치보다 먼저 정렬되고, 색인된 ref만 포함된다는 사실을 응답에 명시한다.

`get-repository-map`은 `AGENTS.md`, `CONTRIBUTING.md`, `CODEOWNERS`, ADR 디렉터리
같은 규약 파일 목록을 함께 반환한다. 에이전트가 코드를 쓰기 전에 프로젝트 관례를 읽을
수 있어 결과물이 저장소 스타일에 맞는다.

## MCP 코드 검색 동작

`search-code`는 저장소 이름 검색과 파일 내용 검색을 한 번에 수행하는 기본 진입점이다.
등록된 저장소는 소스 Code Search API로 **병렬** 질의하고(최대 6개 동시), 카탈로그에
없는 저장소는 원격 발견 후 ACL 검증을 거쳐 함께 반환한다. 응답에는 실행된 검색 경로,
ACL 판정, 타임아웃 여부가 `Notes`로 항상 포함되므로 코딩 에이전트가 "코드가 없다"와
"이번에는 못 찾았다"를 구분할 수 있다.

`query-docs`는 색인이 아직 없으면 소스 Code Search API로 failover 한다. 임베딩 모델이
설정되어 색인이 진행 중인 저장소도 즉시 답을 돌려주며, 응답 머리말에 라이브 조회임을
표시한다. `resolve-library-id`는 색인 전 라이브러리에 "not indexed yet" 주석을 붙인다.

`search-repositories`는 이름만 검색하는 도구임을 설명과 결과에 명시해, 에이전트가 코드
질문에 이 도구만 호출하고 끝내지 않도록 한다.

원격 API 왕복이 필요한 도구의 기본 Timeout은 `search-code`·`search-source` 90초,
`query-docs` 60초다(migration 029). 이전 30초 기본값에서는 저장소 수가 많을 때 코드
검색이 시간 초과되어 저장소 목록만 반환되었다. 운영자가 MCP 도구 화면에서 이미 값을
바꿨다면 그 값은 유지된다.

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
  "retrievalMode": "hybrid-fallback",
  "keywordWeight": 1.0,
  "vectorWeight": 0.35,
  "candidateLimit": 5000,
  "finalK": 8,
  "rerankLimit": 30,
  "minimumEmbeddingCoveragePercent": 80,
  "embeddingFailureThreshold": 2,
  "embeddingCooldownSeconds": 60,
  "embeddingCacheSeconds": 120
}
```

`retrievalMode`은 색인 Worker와 모든 MCP 검색 도구가 공유하는 실행 정책이다.

| 값 | 동작 |
|---|---|
| `keyword-only` | 임베딩 생성과 벡터 DB 조회를 하지 않고 로컬 키워드 및 Bitbucket/GitLab Query Search API만 사용 |
| `hybrid-fallback` | 임베딩이 정상이면 하이브리드 검색을 사용하고 모델·벡터 데이터 장애 시 키워드 검색으로 자동 전환 (기본값) |
| `hybrid-required` | 모델 또는 접근 가능한 임베딩 데이터가 없으면 오류를 반환해 품질 계약 위반을 숨기지 않음 |

접근 가능한 저장소/ref의 임베딩 커버리지가 `minimumEmbeddingCoveragePercent`보다
낮으면 벡터가 존재하는 일부 문서만 과대평가하지 않도록 해당 질의 전체를
키워드·Bitbucket/GitLab Query Search API로 전환한다. `hybrid-required`에서는 같은
상황을 오류로 반환한다. 커버리지는 ACL 적용 뒤 ref별 집계로 계산하며 색인 상태에는
`ready`, `partial`, `degraded`, `unavailable`, `disabled`가 기록된다.

임베딩 endpoint가 `embeddingFailureThreshold`회 연속 실패하면 프로세스별 Circuit이
열리고 `embeddingCooldownSeconds` 동안 느린 모델 재시도를 즉시 차단한다. 대기 후에는
한 요청만 시험해 성공 시 자동 복구한다. `embeddingCacheSeconds`는 동일한 질의와
색인 청크의 벡터를 짧게 재사용하며 `0`이면 캐시를 사용하지 않는다. Kubernetes에서는
Circuit과 캐시 지표가 Pod별 값이므로 Prometheus에서 instance label로 관찰한다.

실행 모드 또는 임베딩 모델 identity가 바뀌면 알려진 저장소 ref를 자동으로 재색인
큐에 등록한다. 새 staging 세대가 원자적으로 완료될 때까지 기존 검색 데이터는 유지된다.
`hybrid-fallback`에서 모델 probe나 배치 임베딩이 실패하면 청크·심볼·의존성은 NULL
벡터로 정상 커밋되고 색인 작업에 경고가 기록된다. MCP 캐시는 `search`, `model`,
`vector`, `opensearch` 설정 버전을 키에 포함하므로 설정 저장 즉시 이전 응답과 분리된다.
관리자 검색·벡터 DB 탭과 `/api/v1/admin/health`, 관리자 MCP
`get-platform-status`는 요청 정책, 실제 동작 모드, 커버리지, Circuit과 마지막 장애를
함께 표시한다.

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

`model.provider=local`은 이전 설정과 시험 호환용이며 운영 의미 검색 벡터로 사용하지
않는다. 모델이 없으면 `hybrid-fallback`은 키워드 전용으로 동작한다. 사내 vLLM 또는 AI
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

`retrievalMode=keyword-only`이거나 `hybrid-fallback`에서 모델이 없으면 벡터 점수를
사용하지 않는다. Reranker는 별도 설정이며 활성화된 경우 키워드 후보에도 적용할 수 있다. 저장소
ACL과 Library ID를 먼저 검증한 다음 GitLab은 프로젝트 `/search?scope=blobs`,
Bitbucket Server 6.9.1은 `/rest/search/latest/search`를 해당 저장소로 제한해 호출한다.
서버측 검색이 결과를 반환하면 Context7 형식 Markdown과 원문 출처로 조립한다. 검색
API 결과 본문은 Secret Scan과 크기 제한을 통과한 경우에만 사용하고, 승인된 색인이
있으면 해당 로컬 청크와 인접 문맥으로 대체한다. 검색 API가 비활성·장애이면 로컬
색인의 BM25 키워드 검색으로 Fail Safe fallback한다.
Bitbucket code search는 기본 브랜치 계약이므로 비기본 branch/tag 질의는 버전별 로컬
색인만 사용한다.

## 벡터 데이터베이스

기본값은 미사용이다. 하이브리드 모드의 임베딩은 `document_chunks.embedding`에 저장되고
애플리케이션이 코사인 점수를 직접 계산하므로 추가 인프라 없이 동작한다. 키워드 전용
모드에서는 이 컬럼을 NULL로 저장한다. 벡터 DB를 연동하면 키워드
후보에 걸리지 않는 의미 후보를 ANN으로 추가하고, 청크 수가 늘어도 성능이 유지된다.

```json
{
  "provider": "pgvector",
  "dsn": "",
  "collection": "git_ctx_chunk_vectors",
  "dimensions": 0,
  "timeoutSeconds": 10
}
```

```json
{
  "provider": "milvus",
  "baseUrl": "http://milvus:19530",
  "database": "default",
  "token": "secret://milvus-token",
  "collection": "git_ctx_chunk_vectors"
}
```

- pgvector: DSN을 비우면 플랫폼 PostgreSQL을 그대로 사용한다. 연결 시험에서
  서버 패키지 설치 여부와 실제 접속한 database/user를 확인한 뒤
  `CREATE EXTENSION IF NOT EXISTS vector` 와 테이블·HNSW 인덱스를 생성한다.
  확장은 PostgreSQL 서버 전체가 아니라 database별로 활성화되므로, 다른 database에
  설치된 경우에는 벡터 설정 DSN에 대상 database를 명시해야 한다. 확장이 별도
  스키마에 설치된 환경도 자동 감지하여 타입·연산자·opclass를 해당 스키마로 한정한다.
- Milvus: RESTful v2 API만 사용하므로 SDK 의존성이 없다. 컬렉션은 COSINE metric으로
  자동 생성된다.
- 색인 작업이 끝나면 해당 ref의 벡터가 자동으로 재적재된다. 기존 데이터를 옮기거나
  pgvector ↔ Milvus 를 전환할 때는 `POST /api/v1/admin/vector/rebuild` 또는 관리자
  화면의 [벡터 재적재]를 사용한다. 재색인은 필요 없다.
- 검색은 벡터 DB를 **후보 공급자**로만 사용한다. 장애가 나면 메타 DB 임베딩 경로로
  자동 fallback 하므로 검색이 중단되지 않는다. `GET /api/v1/admin/vector/status` 는
  벡터 DB 보유 수와 메타 DB 임베딩 수를 함께 보여 준다.

`search-semantic`은 Library ID 없이 저장소 전 범위에서 의미로 검색한다. 벡터 DB가
있으면 ANN 결과를 ACL·범위 조건으로 거른 뒤 반환하고, 없으면 저장된 임베딩을 최대
20,000개까지 스캔해 채점한 뒤 그 사실을 응답 `Diagnostics`에 남긴다. 어느 경로가
쓰였는지는 응답의 `Mode` 로 확인할 수 있다.

## 소스 연동 동작 (Bitbucket · GitLab)

- **연결 재사용**: 소스 어댑터는 소스별로 한 번만 만들어 재사용합니다. 설정 버전이
  바뀌면 다음 호출에서 자동으로 다시 만듭니다. 검색 한 번이 저장소 수만큼 TLS
  핸드셰이크를 하던 문제를 없앴습니다.
- **재시도**: 본문이 없는 요청(모든 조회)은 최대 3회까지 시도합니다. 429·5xx 와
  일시적 네트워크 오류가 대상이고, 서버가 보낸 `Retry-After` 를 우선합니다(최대 30초).
- **오류 분류**: 401·403(자격 증명), 404(없음), 429(제한), 5xx(일시)를 구분합니다.
  404 는 스캔 중 정상 결과로 취급해 연동 상태에 영향을 주지 않습니다.
- **서킷 브레이커**: 연속 실패가 임계치를 넘거나 인증 오류가 나면 그 소스 호출을
  30초간 멈추고 색인된 결과로만 답합니다. 응답 진단에 사유와 재시도 시각이 남고,
  복구 확인용 호출은 한 번만 통과시킵니다. 관리자는 소스·색인 화면에서
  [지금 재시도] 로 즉시 해제할 수 있습니다.
- **페이지 상한**: 한 번의 목록 조회는 GitLab 50페이지(5,000건), Bitbucket
  20페이지(20,000건)까지만 읽습니다.

### 색인과 연동 상태

색인 작업도 같은 서킷 브레이커를 봅니다. 소스가 중단 상태면 작업을 실행하지 않고
**시도 횟수를 소모하지 않은 채** 45초 뒤로 다시 예약하며, 색인 진단에
`source-paused`(연동 대기) 로 표시합니다. 실행 중 발생한 오류도 일시적 장애
(연결 거부·타임아웃·5xx)이면 시도 횟수를 되돌리고 더 긴 간격으로 재시도하므로,
10분짜리 소스 장애가 모든 저장소의 재시도 예산을 태워 `failed` 로 만들지 않습니다.
저장소 자체의 문제(404, 정책 불일치)는 기존대로 시도 횟수를 소모하고 실패로
보고됩니다. 색인 결과는 브레이커에도 반영되어, 색인이 먼저 장애를 감지하면 검색
경로도 즉시 그 사실을 반영합니다.

## 설정 저장 규칙

- **버전 충돌 방지**: 저장 요청에 화면이 불러온 `expectedVersion` 을 함께 보냅니다.
  그 사이 다른 관리자가 저장했으면 409 로 거부하고, 화면은 다시 불러올지 묻습니다.
- **삭제 후 재설정**: 설정을 삭제해도 버전 이력은 남습니다. 다음 저장은 이력의
  마지막 번호 다음부터 이어집니다.
- **검증 건너뛰기**: 대상 서버 점검 등으로 연결 검증이 실패할 때
  `PUT ...?force=true` 로 저장할 수 있습니다. 건너뛴 사유는 응답과 감사 로그에
  남습니다.
- **비밀값**: 조회 시 `********` 로 마스킹되고, 그대로 저장하면 기존 값이 유지됩니다.
  저장된 값이 없는데 마스크가 들어오면 그 항목은 저장하지 않고 응답의
  `missingSecrets` 로 알려 줍니다. 화면의 [저장된 값 지우기] 로 비밀값을 삭제할 수
  있습니다.
- **되돌리기**: `GET /api/v1/admin/settings/{category}/versions/{version}` 로 과거
  값과 현재 값의 차이를 확인하고, `.../restore` 로 되돌립니다. 되돌리기도 새 버전으로
  기록되므로 다시 되돌릴 수 있습니다.
- **이관**: `GET /api/v1/admin/settings-export` 는 전체 설정을 한 파일로 내보냅니다.
  비밀값은 포함되지 않습니다. `POST /api/v1/admin/settings-import` 는 기본이 미리보기
  (dry run) 이고 `?apply=true` 로 적용합니다.

## MCP 응답 예산과 호출 감사

MCP 응답 한 건의 크기는 도구별 예산으로 제한됩니다. 기본값은 24 KB이고,
`export-context`(64 KB), `read-file`(48 KB), `search-code`·`query-docs`·
`get-context-pack`(40 KB)은 더 큰 값이 미리 설정되어 있습니다. 예산을 넘으면
결과 경계에서 잘린 뒤 남은 결과 수와 좁히는 방법을 `### Truncated` 절에 적고,
검색 경로를 설명하는 `### Notes` 절은 잘리지 않고 유지됩니다. 클라이언트는
`maxBytes` 인자로 더 작은 응답만 요청할 수 있고, 관리자가 정한 예산을 넘길 수는
없습니다.

호출은 `mcp_calls`에 시각·사용자·API 키·세션·클라이언트·도구·Library·결과·오류
코드·검색 경로·지연·응답 크기·잘림 여부·결과 수·캐시 적중과 함께, 비밀값을
마스킹해 300자로 자른 질의 요약이 남습니다. 보관 기간은 운영 설정의 MCP 호출
보관일을 따릅니다.

- `GET /api/v1/admin/mcp/analytics?window=24h|7d|30d` — 도구별 p50/p95, 빈 응답률,
  잘림 비율, 검색 경로 분포, 답하지 못한 질문, 그리고 그로부터 도출한 설정 권장값
- `GET /api/v1/admin/mcp/calls?...&format=csv` — 개별 호출 감사 기록과 CSV 내보내기
- `GET /api/v1/admin/mcp/calls/{id}` — 호출 하나의 단계별 X-ray와 같은 세션의 호출 순서
  (`GET /api/v1/me/calls/{id}` 는 자기 호출만)

- `GET /api/v1/admin/mcp/sessions?window=` — 에이전트 대화 단위 집계. 마지막 호출이
  빈 응답이나 오류로 끝난 대화를 위로 올립니다
- `POST /api/v1/admin/mcp/selfcheck` — 호출자 본인의 ACL 주체로 실제 검색 경로를
  실행하고 단계 기록과 판정을 반환합니다

### 호출 X-ray

호출 한 건은 여러 단계를 거칩니다. ACL 해석, 색인 조회, 저장소별 원격 폴백,
임베딩, 스캔, 파일 읽기 경로 각각이 `mcp_call_steps` 에 남고, 단계마다 **후보
수(candidates)** 와 **통과 수(results)** 를 기록합니다. 둘의 차이가 결과가
사라진 지점입니다. 예를 들어 `source-query gitlab: 12 candidates, none passed`
는 "저장소는 12개 보였지만 그 안에서 매칭이 없었다"는 뜻이고, ACL 단계에서
0이면 권한 문제입니다.

단계 합계와 전체 소요 시간의 차이는 `untracedMs` 로 함께 반환합니다. 포맷팅,
응답 예산 적용, 네트워크 처리처럼 단계 밖에서 쓰인 시간을 감춰 합계가 맞는 것처럼
보이게 하지 않기 위해서입니다. 한 호출의 단계 기록은 60개로 제한하며, 초과하면
잘렸다는 사실 자체를 마지막 행으로 남깁니다.

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
