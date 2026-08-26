# git-ctx v0.69.1

이번 릴리스는 **에이전트가 받는 도구 안내에서 빠져 있던 도구 13개**를 채웁니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

### 절반 가까운 도구가 안내에 등장하지 않았습니다

- `serverInstructions` 는 에이전트가 무엇을 묻기 전에 받는 지도입니다. 거기에 이름이 없는 도구는 **손이 가지 않는 도구**입니다.
- 광고되는 29개 중 **13개가 한 번도 나오지 않았습니다** — `resolve-library-id`·`trace-dependencies`·`find-tests`·`find-runbook`·`compare-refs`·`get-change-impact`·`assess-change-risk`·`get-repository-health`·`get-architecture-map`·`explain-search-result`·`export-context`·`get-context-pack`·`search-source`.
- 그중 `resolve-library-id` 는 **다른 모든 도구가 요구하는 library id 를 만들어 내는 도구**입니다. 문서는 library id 를 계속 언급하면서 그것을 어디서 얻는지는 말하지 않았습니다.
- 전부 넣었습니다. 안내는 모든 세션에 실려 가므로 **매뉴얼이 아니라 브리핑이어야 한다**는 기존 제약(4,000바이트)을 지켰습니다 — 늘리는 대신 문장을 줄여 2,923 → 3,829바이트에 29개를 모두 담았습니다.

## 개선

- 안내와 도구 목록이 서로 어긋나지 못하게 시험으로 못박았습니다.
  - 에이전트가 부를 수 있는 모든 도구는 안내에 이름이 있어야 합니다. 관리자 도구는 API 키를 가진 호출자에게만 답하므로 제외합니다.
  - 안내에 적힌 도구 이름은 실재해야 합니다 — 없는 도구를 부르라고 보내지 않도록.

## 검증

- 안내 되돌리면 "13 tools an agent can call are not named in the instructions it is given" 으로 실패하는 것을 확인
- 도구 설명과 안내에 등장하는 도구 형태의 이름이 전부 실재하는지 전수 확인
- FTS5 빌드·태그 없는 빌드·PostgreSQL 3가지 조합 전체 단위·통합·race 테스트, 빌드 모드 교차 시험, 콘솔 시험

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다. `initialize` 응답의 instructions 가 길어지며 동작은 바뀌지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.69.1.tar.gz`
- `git-ctx-v0.69.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.69.1.tar.gz.sha256
gzip -dc git-ctx-v0.69.1.tar.gz | docker load
docker image inspect git-ctx:v0.69.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.69.1`과 `git-ctx:0.69.1` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.69.0...v0.69.1
