# git-ctx v0.56.4

이번 릴리스는 `find-runbook` 을 인덱스로 옮기고, **제한된 사용자 관점의 격리를 규모에서 실증**합니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 개선

- `find-runbook` 은 `runbook`·`playbook`·`operations` 표식을 **부분 문자열로** 찾느라 카탈로그의 모든 청크를 읽고 나서야 순위를 매겼습니다. 청크 200,000개에서 1,143ms.
- 표식은 단어이므로 이제 인덱스로 조회합니다. 런북이 있는 카탈로그에서 **1,143ms → 6ms**. 인덱스가 아무것도 찾지 못하면 기존 부분 문자열 방식으로 한 번 더 찾습니다 — `myrunbooks.md` 처럼 표식이 긴 단어 안에 묻힌 경우를 놓치지 않기 위해서입니다. 런북이 하나도 없는 카탈로그는 그 보충 탐색 비용(약 150~230ms)만 남습니다.

## 검증 (제한된 사용자 관점)

지금까지의 라이브 검증은 ACL 을 우회하는 관리자 자격으로 했습니다. 이번에는 **저장소 400개 중 20개만 볼 수 있는 개발자** 자격으로 18개 호출을 실행했습니다.

```text
목록형 도구 9종      누출 0건 — 결과에 다른 팀 저장소가 한 건도 나타나지 않음
직접 지정 9종        전부 거부 — 저장소를 특정해도 일반화된 거부 메시지
                     (query-docs, read-file, get-repository-map, list-directory,
                      explain-search-result, get-repository-health, export-context,
                      find-code-owner, reindex-repository)
```

- `find-dependency-usage` 는 볼 수 있는 20개 저장소 범위에서만 집계했습니다.
- `reindex-repository` 는 자격증명 자체에 그 도구가 없어 거부됐습니다.
- 수정할 결함은 없었습니다. 이 릴리스는 그 사실을 기록으로 남깁니다.

## 검증 (그 외)

- 표식이 단어일 때와 긴 단어 안에 묻혀 있을 때 모두 찾는지, 런북이 아닌 문서는 제외되는지, 두 경로 모두 ACL 이 적용되는지 시험
- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.56.4.tar.gz`
- `git-ctx-v0.56.4.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.56.4.tar.gz.sha256
gzip -dc git-ctx-v0.56.4.tar.gz | docker load
docker image inspect git-ctx:v0.56.4 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.56.4`과 `git-ctx:0.56.4` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.56.3...v0.56.4
