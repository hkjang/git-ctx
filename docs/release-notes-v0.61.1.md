# git-ctx v0.61.1

이번 릴리스는 **같은 질문에 두 데이터베이스가 다른 답을 주던 것**을 없앱니다. `find-symbol` 은 PostgreSQL 에서 아예 실패하고 있었습니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

### find-symbol 이 PostgreSQL 에서 실패했습니다

- `SELECT DISTINCT` 결과를 선택 목록에 없는 식으로 정렬하고 있었습니다. 답이 없는 질문입니다 — PostgreSQL 은 그렇게 말하고 거절하고, SQLite 는 아무 행이나 골라 계속합니다.
- PostgreSQL 설치에서 `find-symbol` 은 `ORDER BY expressions must appear in select list` 로 매번 실패했습니다. 순위 식을 선택 목록에 넣었습니다.

### 결과 순서가 데이터베이스 로케일에 좌우됐습니다

- SQLite 는 텍스트를 바이트로 비교합니다. PostgreSQL 은 데이터베이스가 만들어질 때의 로케일로 비교하고, 흔한 기본값인 `en_US.utf8` 은 대소문자를 무시합니다. `README.md` 가 한쪽에서는 `package.json` 앞에, 다른 쪽에서는 뒤에 옵니다.
- 첫 결과만 읽는 에이전트는 **어느 데이터베이스인지에 따라 다른 답**을 받았습니다. 로케일이 다른 두 PostgreSQL 설치끼리도 갈릴 수 있었습니다.
- 정렬에 쓰는 텍스트는 이제 `store.SortText` 를 거쳐 두 곳 모두 바이트 순서로 비교합니다. 더 옳아서가 아니라, 답의 순위를 정하는 정렬은 어디서나 같아야 하기 때문입니다.
- 점수가 같은 결과들도 데이터베이스가 준 순서 그대로 남아 있었습니다. 이제 위치로 동점을 가릅니다 — 점수가 이미 정한 것은 아무것도 바꾸지 않습니다.
- 저장소 지도는 색인할 때 만들어져 JSON 으로 보관됩니다. 즉 **저장된 값 자체가** 드라이버마다 달랐습니다. 정렬을 SQL 밖으로 옮겼습니다.
- 한 저장소가 매니페스트와 락 파일 양쪽에 같은 패키지를 선언하는 흔한 경우, 그 두 줄의 순서가 정해져 있지 않았습니다.

### explain-search-result 가 실제로 한 일을 말하지 않았습니다

- 검색 경로를 드라이버 이름으로 설명했습니다. SQLite 도 v0.59.0 부터 자기 인덱스를 쓰는데 `application lexical` 이라고 말했고, 인덱스 구축에 실패한 PostgreSQL 설치에도 `PostgreSQL FTS candidates` 라고 말했을 것입니다.
- 이제 읽는 사람에게 중요한 것을 말합니다 — 후보가 인덱스에서 왔는지, 모든 청크를 읽어서 왔는지.

## 검증

- **도구 차분 시험**을 새로 추가했습니다(`TestBothDatabasesAnswerTheSameToolsIntegration`). 같은 저장소를 두 데이터베이스에 평소 경로로 색인하고, 에이전트가 쓰는 읽기 도구 28개를 같은 인자로 부르고, **답이 같기를** 요구합니다.
- 이 시험이 위 결함을 전부 찾았습니다. 28개 중 8개가 갈렸고, 하나는 아예 실패였습니다.
- 3회 연속 실행으로 순서가 우연히 맞은 게 아님을 확인했습니다.
- FTS5 빌드·태그 없는 빌드·PostgreSQL 3가지 조합 전체 단위·통합·race 테스트

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다. 다만 저장소 지도의 `keyFiles`·`entryPoints` 순서는 해당 ref 가 다음에 색인될 때 정리됩니다.
- 결과 순서가 바뀔 수 있습니다. 같은 점수끼리의 순서이며, 이제 어느 데이터베이스에서나 같습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.61.1.tar.gz`
- `git-ctx-v0.61.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.61.1.tar.gz.sha256
gzip -dc git-ctx-v0.61.1.tar.gz | docker load
docker image inspect git-ctx:v0.61.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.61.1`과 `git-ctx:0.61.1` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.61.0...v0.61.1
