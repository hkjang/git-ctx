# git-ctx v0.69.0

이번 릴리스는 **에이전트가 도구를 어떻게 부를지 배우는 유일한 근거인 매개변수 설명**을 채웁니다. 43개가 비어 있었습니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

### 매개변수 43개가 자기에 대해 아무 말도 하지 않았습니다

- 도구 스키마는 에이전트가 이 플랫폼을 부르는 법을 배우는 곳입니다. 설명 없는 매개변수는 **추측해야 하는 매개변수**입니다.
- `trace-dependencies`·`compare-refs`·`get-change-impact`·`find-runbook`·`explain-search-result` 는 **모든 인자가** 비어 있었습니다. 에이전트가 가장 정확히 부르기 어려운 도구들입니다.
- 그 밖에 `limit` 18곳, `sourceType` 5곳, `libraryId` 6곳, `query` 4곳, `ref` 3곳, `baseRef`·`headRef` 각 2곳, `state`·`status`·`symbol`·`libraryIds` 가 비어 있었습니다.
- 의도된 누락이 아니라 쌓인 것이었습니다. 전부 채웠고, 파일이 이미 쓰던 어조를 따랐습니다.

## 개선

- 스키마가 **클라이언트가 검증하는 대로** 검사됩니다. 매개변수에 설명이 있는지, 배열에 `items` 가 있는지, `enum` 이 비어 있지 않은지, `required` 의 이름이 실제 속성인지, `type` 이 `object` 인지, `additionalProperties` 가 불리언인지 — 시험으로 못박았습니다. 이 시험이 조사에서 빠졌던 한 곳(`list-index-jobs.status`, 관리자 도구라 `tools/list` 에 나오지 않습니다)을 추가로 잡았습니다.

## 검증

- 실제 MCP 핸드셰이크로 조사했습니다 — `initialize`, 지원하지 않는 프로토콜 버전(서버가 자기 버전을 돌려주는 것이 규격이며 그렇게 동작합니다), 알 수 없는 메서드(-32601), 깨진 JSON(-32700), id 없는 알림(202·본문 없음), `Mcp-Session-Id` 발급, `tools/list`.
- 광고되는 도구가 29개이고 빠진 3개가 설계대로 관리자 도구(`get-platform-status`·`list-index-jobs`·`reindex-repository`)임을 확인
- FTS5 빌드·태그 없는 빌드·PostgreSQL 3가지 조합 전체 단위·통합·race 테스트, 빌드 모드 교차 시험, 콘솔 시험

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다. `tools/list` 응답의 스키마에 설명이 추가되며 동작은 바뀌지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.69.0.tar.gz`
- `git-ctx-v0.69.0.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.69.0.tar.gz.sha256
gzip -dc git-ctx-v0.69.0.tar.gz | docker load
docker image inspect git-ctx:v0.69.0 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.69.0`과 `git-ctx:0.69.0` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.68.1...v0.69.0
