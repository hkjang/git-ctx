# git-ctx v0.58.1

이번 릴리스는 전 구간 시험을 **부분 장애 경로까지** 넓힙니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 배경

앞선 릴리스에서 새로 넣은 체인 시험은 정상 경로를 덮습니다. 그런데 실제로 잡힌 결함은 모두 **일부가 빠졌을 때**에 있었습니다 — 색인이 답을 쥐고 있는데 빈 결과를 주던 검색, 임베딩 장애가 소스 오류로 보고되던 문제, 조용히 실패하던 재순위, 아무도 모르게 포기하던 알림.

## 추가

- `TestPlatformDegradationIntegration` — 같은 체인을 돌리되 구성요소를 하나씩 떼어냅니다.

```text
소스 검색 API 차단   search-code 가 색인에서 답하고 "index:" 로 그 사실을 밝힘
재순위 모델 차단     query-docs 가 "재순위 모델을 호출하지 못해…" 를 함께 냄
임베딩 모델 차단     search-semantic 이 어휘 경로로 답을 냄
알림 수신기 거부     재시도 소진 → dead → 플랫폼 상태에 "gave up after their retries"
```

- 관리자 도구는 설계상 MCP API 키를 요구하므로, 상태 확인은 키를 발급해 운영자의 에이전트와 같은 방식으로 호출합니다.
- 색인 대체 경로를 꺼 보고 이 시험이 실패하는 것을 확인했습니다.

## 검증

- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트
- 두 체인 시험 각각 10.3초, `internal/app` 전체 31초

## 업그레이드 참고

- 제품 동작 변경은 없습니다. 마이그레이션이나 재색인도 필요하지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.58.1.tar.gz`
- `git-ctx-v0.58.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.58.1.tar.gz.sha256
gzip -dc git-ctx-v0.58.1.tar.gz | docker load
docker image inspect git-ctx:v0.58.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.58.1`과 `git-ctx:0.58.1` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.58.0...v0.58.1
