# git-ctx v0.56.2

이번 릴리스는 전문 인덱스를 **나머지 검색 경로에도** 적용하고, 중복으로 코퍼스를 두 번 훑던 경로를 없앱니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 개선

- `search-semantic` 의 어휘 대체 경로와 `query-docs` 의 후보 선별이 인덱스를 씁니다. 임베딩을 끄고 운영하는 설치에서는 이 대체 경로가 사실상 기본 경로입니다.
- **`search-semantic` 이 코퍼스를 두 번 훑고 있었습니다.** 자기 경로에서 색인을 조회·훑은 뒤 `search-code` 를 호출해 같은 일을 다시 시켰습니다. 이제 그 자리에서는 실시간 소스 질의만 요청합니다 — 색인 몫은 이미 끝났기 때문입니다.
- 두 경로 모두 단어 안쪽 일치를 위한 보충 훑기를 유지하므로 회수율은 그대로입니다.

## 실측 (저장소 400 · 청크 200,000)

```text
호출                          이전      이후
search-semantic(흔한 단어)    46ms      2ms
search-semantic(없는 단어)   982ms*   404ms     * 중복 훑기가 있던 상태
search-code(흔한 단어)        53ms     63ms
search-code(없는 단어)       267ms    185ms
```

## 검증

- 인덱스 경로와 훑기 경로가 같은 내용을 찾는지 시험(정확 일치·접두사·경로 조각·단어 안쪽·한국어), `query-docs` 는 기존에 매칭되던 단어 기준을 유지하는지 확인
- ACL 격리, 없는 단어의 빈 결과 유지
- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트
- 20만 청크 인스턴스에서 지연 실측

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- `search-semantic` 의 진단에 `lexical index:` 문구가 그대로 나오며, 상한에 걸리면 그 사실도 함께 표시됩니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.56.2.tar.gz`
- `git-ctx-v0.56.2.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.56.2.tar.gz.sha256
gzip -dc git-ctx-v0.56.2.tar.gz | docker load
docker image inspect git-ctx:v0.56.2 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.56.2`과 `git-ctx:0.56.2` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.56.1...v0.56.2
