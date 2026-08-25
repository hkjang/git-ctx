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
