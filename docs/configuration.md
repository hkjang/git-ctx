# 관리자 설정 레퍼런스

설정은 `system_settings`에 AES-256-GCM 암호문으로 저장되며 변경마다 불변
`setting_versions` 레코드를 만든다. `secret`, `password`, `token`, `apiKey`,
`pat` 이름의 필드는 조회 시 마스킹된다.

## Keycloak

```json
{
  "issuerUrl": "https://sso.company/realms/company",
  "clientId": "git-ctx",
  "clientSecret": "secret",
  "redirectUrl": "https://git-ctx.company/auth/callback",
  "scopes": ["openid", "profile", "email", "groups"],
  "usernameClaim": "preferred_username",
  "groupsClaim": "groups",
  "bitbucketUserSlugClaim": "bitbucket_user_slug",
  "gitlabUserIdClaim": "gitlab_user_id",
  "realmRoleMappings": {"git-ctx-admin": "platform-admin"},
  "clientRoleMappings": {"audit": "auditor"},
  "bitbucketGroupMappings": {"/engineering": "engineering"},
  "postLogoutRedirectUrl": "https://git-ctx.company/",
  "tlsVerify": true,
  "caCertificate": "-----BEGIN CERTIFICATE-----\n...\n",
  "proxyUrl": "",
  "timeoutSeconds": 15
}
```

## Bitbucket Server 6.9.1

```json
{
  "baseUrl": "https://bitbucket.company",
  "apiPrefix": "/rest/api/1.0",
  "pat": "secret",
  "webhookSecret": "secret",
  "tlsVerify": true,
  "caCertificate": "",
  "proxyUrl": "",
  "timeoutSeconds": 30
}
```

PAT 대신 `username`과 `password`를 설정할 수 있다. 서비스 계정에는 프로젝트,
저장소, ref, 파일과 권한을 읽고 저장소 webhook을 관리하는 최소 권한만 부여한다.

## GitLab

```json
{
  "baseUrl": "https://gitlab.company",
  "token": "secret",
  "webhookSecret": "secret",
  "tlsVerify": true,
  "caCertificate": "",
  "proxyUrl": "",
  "timeoutSeconds": 30
}
```

## MCP와 색인

```json
{
  "allowedOrigins": ["https://git-ctx.company"],
  "maxRequestBytes": 1048576
}
```

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
embedding 요청으로 연결을 시험하며 모델 차원 변경 후에는 전체 재색인이 필요하다.

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
외부 연결 검사를 통과해야 저장되며, rollback도 대상 버전 연결을 다시 검증한다.

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

DB DSN을 제외한 운영 설정은 관리자 화면에서 입력·시험·저장·버전 복구한다. Keycloak,
Bitbucket, GitLab과 모델 영역은 URL, Client/Token/API Key, 모델, TLS, 사내 CA,
Proxy와 Timeout 전용 필드를 제공하고 알 수 없는 확장 필드는 고급 JSON 편집기에
보존한다. Secret/PAT/Token/API Key는 암호화 저장되고 재조회 시 `********`로
마스킹되며, 마스킹 값을 그대로 저장하거나 시험하면 이전 암호문 값을 재사용한다.

`연결 테스트·검증`은 저장하지 않고 실제 호출을 수행한다.

- Keycloak: OIDC Discovery와 JWKS/endpoint/issuer 계약
- Bitbucket 6.9.1: 인증된 프로젝트 REST 조회
- GitLab: 인증된 Group API 조회
- Embedding: OpenAI 호환 `/v1/embeddings` 실제 벡터 응답
- Reranker: `/v1/rerank` 문서 index와 score 완전성
- OpenSearch: 인증된 cluster 조회, 청크 index 존재 확인 또는 mapping 생성
- OpenTelemetry: 실제 시험 span export
- Backup: 전용 경로 생성·쓰기와 정책 검증

저장과 rollback도 동일 검증을 다시 통과해야 한다. 시험 결과는 성공·실패 모두 감사
로그에 남되 입력 Secret과 응답 원문은 기록하지 않는다.

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

활성화 뒤 저장소를 재색인하면 Worker가 해당 ref의 기존 projection을 삭제하고 Bulk
API로 현재 청크와 ACL principal을 함께 기록한다. Projection 실패는 색인 작업 실패로
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
