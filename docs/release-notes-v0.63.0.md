# git-ctx v0.63.0

이번 릴리스는 **답이 자기 나이를 말하게** 합니다. 한 달 묵은 색인에서 나온 코드가 오늘 코드처럼 읽히던 문제입니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

### 답이 자기 나이를 말하지 않았습니다

- 이 플랫폼은 모든 답 뒤의 색인이 얼마나 오래됐는지 정확히 압니다 — 그것만 보고하는 도구가 따로 있습니다. 그리고 도구 4개에만 그 사실을 말하게 하고 있었습니다.
- `read-file` 은 한 달 전 함수 본문을 돌려주면서 "색인에서 재조립했다"고만 했습니다. `find-symbol`·`query-docs`·`list-directory`·`get-symbol-context`·`find-tests`·`trace-dependencies`·`get-repository-map`·`explain-search-result`·`get-file-history` 는 **아무 말도 하지 않았습니다.**
- 자기 나이를 달고 오지 않는 답은 현재 것으로 읽힙니다.
- 이제 색인 내용을 돌려주는 도구는 색인이 오래됐을 때 그 사실을 답에 답니다. 나이는 **답이 나가는 시점에** 읽습니다 — 캐시된 답이 처음 만들어졌을 때의 나이를 달고 다니지 않도록.
- **빈 답에도 답니다.** 나이가 가장 중요한 경우입니다: "이 디렉터리는 비어 있다"·"이 파일에는 이력이 없다"가 저장소에 대한 사실인지 한 달 전 색인에 대한 사실인지 구분되어야 합니다.
- `search-code` 의 "마지막 색인 시점만큼 최신입니다" 는 그 시점이 언제인지 말하지 않았습니다. 한 달 전 답과 1분 전 답이 자기에 대해 같은 말을 하고 있었습니다.

### libraryId 를 받고도 감사에 남기지 않았습니다

- 도구 6개가 `libraryId` 인자를 받으면서 MCP 호출 감사에 기록하지 않았습니다 — `read-file`·`get-file-history`·`list-directory`·`find-file`·`search-semantic`·`search-merge-requests`.
- **파일 내용을 돌려주는 도구의 감사 기록에 어느 저장소였는지가 비어 있었습니다.** 한 저장소로 좁힌 호출이 전체에 대한 호출처럼 남았습니다.
- 이미 `find-symbol`·`find-runbook` 에서 한 번 고쳐진 누락이고, 나머지가 남아 있었습니다. 이제 없습니다.

## 검증

- 저장소를 색인한 뒤 색인 시각을 30일 뒤로 돌리고 읽기 도구 전체를 부르는 조사로 시작했습니다.
- 신선한 색인에는 문구가 붙지 않고 한 달 된 색인에는 붙는지 회귀 시험(`TestAnAnswerFromAnOldIndexSaysHowOldItIs`), `libraryId` 를 받는 모든 도구가 그것을 기록하는지 시험(`TestEveryToolThatTakesALibraryRecordsIt`). 수정을 되돌리면 18건이 걸립니다.
- 레지스트리 전수 점검으로 `libraryId` 를 받으면서 기록하지 않는 항목이 더 없음을 확인
- FTS5 빌드·태그 없는 빌드·PostgreSQL 3가지 조합 전체 단위·통합·race 테스트, 빌드 모드 교차 시험

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- 색인이 일주일 넘게 오래된 저장소에 대한 답에 문구가 한 줄 붙습니다. 색인이 최신이면 아무 변화가 없습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.63.0.tar.gz`
- `git-ctx-v0.63.0.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.63.0.tar.gz.sha256
gzip -dc git-ctx-v0.63.0.tar.gz | docker load
docker image inspect git-ctx:v0.63.0 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.63.0`과 `git-ctx:0.63.0` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.62.1...v0.63.0
