# git-ctx v0.53.5

이번 릴리스는 MCP 클라이언트 **인증 헤더 호환**을 넓힙니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 개선

- **API 키를 `Authorization: Bearer` 로 보낼 수 있습니다.** 헤더를 하나만 설정할 수 있는 MCP 클라이언트와 게이트웨이가 적지 않은데, 지금까지는 그렇게 보낸 키가 Keycloak 액세스 토큰으로 검증되어 “Keycloak access token validation failed” 로 거절됐습니다. 키를 붙여넣은 사람 입장에서는 쓰지도 않은 로그인 시스템의 오류였습니다.
- 키 형식(`bctx_live_`)이 스스로를 식별하므로 판정에 모호함이 없습니다. 형식이 아닌 값은 이전과 같이 Keycloak 토큰으로만 검증하며, 형식만 흉내 낸 값은 API 키 오류로 거절합니다.
- 기존 `CONTEXT7_API_KEY`, `X-API-Key` 헤더는 그대로 동작합니다.

## 검증

- 실제 인스턴스에서 `Authorization: Bearer <API 키>` 만으로 `/mcp` initialize → `tools/list` → `tools/call` 을 수행했습니다.
  - `tools/list` 는 키 scope와 동일한 4개 도구만 노출
  - `find-dependency-usage` 는 공지 기준으로 영향·안전 저장소를 분류
  - `get-repository-map` 은 요약과 함께 선언 스택 3건을 출력
  - scope 밖 `read-file` 은 자격증명 기준으로 거부
- Go 전체 단위·통합·race 테스트, vet와 build
- 위조된 키 형식·일반 Bearer 토큰이 각각 올바른 경로로 거절되는 회귀 시험

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- 클라이언트 설정 변경도 필요하지 않습니다. 기존 헤더를 그대로 두어도 됩니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.53.5.tar.gz`
- `git-ctx-v0.53.5.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.53.5.tar.gz.sha256
gzip -dc git-ctx-v0.53.5.tar.gz | docker load
docker image inspect git-ctx:v0.53.5 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.53.5`과 `git-ctx:0.53.5` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.53.4...v0.53.5
