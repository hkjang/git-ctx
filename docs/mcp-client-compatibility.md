# MCP 실제 클라이언트 호환 시험

## 2026-07-27 로컬 Docker 승인 시험

`git-ctx:v0.7.0`을 기본 포트 4747로 실행하고, 사용자 API 키를
`CONTEXT7_API_KEY` 헤더로 전달해 실제 클라이언트 프로세스에서 시험했다. 키 원문과
세션 ID는 보고서에 기록하지 않았다.

| 클라이언트 | 버전 | 연결 | 도구 검색 | 실제 도구 호출 | 결과 |
|---|---:|---:|---:|---:|---|
| Codex CLI | 0.145.0 | 통과 | 통과 | `resolve-library-id` | 통과 |
| Claude Code | 2.1.218 | 통과 | 통과 | `resolve-library-id` | 통과 |

두 클라이언트 모두 Streamable HTTP initialize, initialized notification,
`tools/list`, 세션 SSE와 세션 DELETE 요청을 수행했다. `resolve-library-id`는 빈 시험
DB에서 `No accessible libraries matched` 텍스트 콘텐츠를 정상 반환했다. 이는 오류가
아니며 `content[].type=text` 응답 계약까지 처리됐음을 뜻한다.

이 시험은 클라이언트 구현과 로컬 서버의 프로토콜 호환 증거다. 사내 승인 완료에는
조직이 승인한 동일 클라이언트 버전에서 실제 Bitbucket/GitLab 라이브러리를 대상으로
`resolve-library-id`와 `query-docs`를 연속 호출하고 출처 및 ACL 결과를 확인해야 한다.

## 재현 절차

1. 사용자 화면에서 두 도구 scope를 가진 단기 API 키를 생성한다.
2. Codex MCP 설정의 `http_headers`에 `CONTEXT7_API_KEY` 환경 참조를 등록한다.
3. Claude Code HTTP MCP 설정에 같은 헤더를 등록한다.
4. 각 클라이언트에서 `resolve-library-id`를 호출한다.
5. 반환된 ID로 `query-docs`를 호출하고 Markdown, 코드, 출처를 확인한다.
6. 서버 감사 로그에서 사용자, 키 prefix, 도구, 결과를 대조한다.
7. 시험 키를 즉시 폐기한다.


## 2026-08-25 인증 헤더 확장 실측

`v0.53.4` 바이너리를 4747 포트로 실행하고, 4개 scope를 가진 사용자 API 키를
`Authorization: Bearer` 헤더로만 전달해 `/mcp` 를 호출했다.

| 단계 | 결과 |
|---|---|
| initialize (Mcp-Session-Id 발급) | 통과 |
| `tools/list` | 키 scope와 동일한 4개 도구만 노출 |
| `find-dependency-usage` (fixedIn 포함) | 영향/안전 저장소 분류 및 근거 출력 |
| `get-repository-map` | 요약과 함께 Stack 3건 출력 |
| scope 밖 `read-file` | `This MCP tool is unavailable for this credential.` 거부 |

키 형식(`bctx_live_`)이 스스로를 식별하므로 `CONTEXT7_API_KEY`, `X-API-Key`,
`Authorization: Bearer` 어느 헤더로 와도 API 키로 인증한다. 헤더를 하나만 설정할 수
있는 MCP 클라이언트·게이트웨이를 위한 확장이며, 키 형식이 아닌 값은 이전과 같이
Keycloak 토큰으로만 검증한다.

## 키 없이 SSO 로 연결하기

관리자가 MCP 설정에서 “SSO(OAuth) 토큰으로 MCP 접속 허용” 을 켠 환경에서는 헤더 없이
`/mcp` 주소 하나만 등록한다. MCP 인가 규격(2025-06-18 이후)은 OAuth 2.1 이라, 클라이언트가
401 응답의 `WWW-Authenticate` 에서 `/.well-known/oauth-protected-resource/mcp` 를 읽고
거기 적힌 Keycloak 으로 브라우저 로그인 창을 띄운 뒤 토큰을 받아 온다. 이미 Keycloak 에
로그인돼 있으면 화면이 거의 뜨지 않는다.

```json
{
  "mcpServers": {
    "git-ctx": { "url": "https://git-ctx.company/mcp" }
  }
}
```

- 먼저 git-ctx 웹 화면에 한 번 로그인해 둔다. SSO 토큰은 **이미 등록된 활성 계정** 만
  열고, 없으면 “sign in to the web console once first” 로 거부된다.
- SSO 로 들어오면 관리자가 정한 Scope 상한 안의 도구만 보인다. 저장소 제한이나 더 넓은
  범위가 필요하면 개인 API 키를 그대로 쓴다 — 키 방식은 바뀌지 않았다.
- 토큰은 `/mcp` 에서만 통한다. REST API 나 스크립트 자동화에는 계속 키를 쓴다.
- 접속이 거부되면 응답 `detail` 이 무엇을 봤고 무엇을 고치면 되는지 말한다(예: 대상 검사
  실패 시 토큰의 `aud`/`azp` 와 관리자가 적을 값). 관리자용 표는
  `docs/configuration.md` 의 “MCP 를 SSO 로 연결하기” 절에 있다.
