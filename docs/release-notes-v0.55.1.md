# git-ctx v0.55.1

이번 릴리스는 **부분 결과를 부분이라고 말하게** 만듭니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

- 색인된 내용을 훑는 검색은 청크 2,000건에서 멈춥니다. 큰 카탈로그에서 흔한 단어는 그보다 훨씬 많이 일치하므로, 지금까지의 답은 **먼저 읽힌 일부에서 뽑은 표본**이었는데 그 사실이 어디에도 없었습니다. 20만 청크·400개 저장소로 실측한 결과, `payment` 는 199,343개 청크에 있는데 답은 8개 저장소만 보여줬습니다. 에이전트는 그것을 “이 코드는 여기에만 있다”로 읽습니다.
- 이제 훑기 상한에 도달하면 `search-code` 와 `search-semantic` 이 그 사실과 좁히는 방법(libraryId·저장소·경로)을 함께 말합니다. 상한에 닿지 않은 답에는 이 경고가 붙지 않습니다 — 모든 답에 달리는 경고는 아무도 읽지 않습니다.

## 검증 (규모 시험)

저장소 400개 · 청크 200,000개 · 파일 24,000개 · 심볼 2,000개 · 의존성 선언 1,600건을 넣고 실측했습니다.

```text
search-repositories       p50   1ms
search-code               p50  53ms   (색인 대체 경로)
find-file                 p50   2ms
search-semantic           p50   3ms
find-symbol               p50   2ms
find-dependency-usage     p50   1ms   (저장소 203개 집계)
get-architecture-map      p50   1ms
```

- 질의 계획을 확인한 결과, ACL 제한 경로는 모두 권한 인덱스에서 시작해 저장소별 인덱스로 들어갑니다. 전체 스캔은 없었고 추가할 인덱스도 없었습니다.
- 동일 질의를 반복해도 같은 결과가 나오는지 확인했습니다.
- 상한 도달·미도달 두 경우의 진단 문구 회귀 시험
- Go 전체 단위·통합·race 테스트, vet와 build

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- 큰 카탈로그에서 흔한 단어로 검색하면 새 경고가 붙습니다. 경고가 붙은 답은 표본이므로, 범위를 좁혀 다시 물어야 정확합니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.55.1.tar.gz`
- `git-ctx-v0.55.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.55.1.tar.gz.sha256
gzip -dc git-ctx-v0.55.1.tar.gz | docker load
docker image inspect git-ctx:v0.55.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.55.1`과 `git-ctx:0.55.1` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.55.0...v0.55.1
