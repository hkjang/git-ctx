# git-ctx v0.54.0

이번 릴리스는 **소스 검색 API가 죽어도 `search-code` 가 답하도록** 만듭니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 개선

- **색인해 둔 내용을 두고도 빈 답을 주던 경로를 없앴습니다.** `search-code` 의 파일 내용 검색은 Bitbucket/GitLab 검색 API로만 나갔습니다. 연동이 일시 중단됐거나, Code Search 가 켜져 있지 않거나, 인스턴스가 응답하지 않으면 — 저장소 전체가 이 플랫폼에 색인되어 있어도 — “일치하는 파일 내용이 없습니다”가 돌아왔습니다. 에이전트는 그걸 “그 코드는 없다”로 읽습니다. 이 제품이 막으려는 결론이 정확히 그것입니다.
- 이제 소스 질의가 아무것도 돌려주지 못하면 색인된 청크에서 답하고, 두 가지를 함께 밝힙니다. 답이 색인 기준이라는 것과, 실시간 경로가 왜 건너뛰어졌는지입니다. ACL·저장소·ref 필터는 동일하게 적용되며, 저장된 청크는 이미 비밀정보가 마스킹된 상태입니다.
- 저장소 경로가 다른 프로젝트로 넘어간 경우(소스에서의 rename·transfer) 색인 작업이 `UNIQUE constraint failed: repositories...` 라는 원본 오류로 끝나며 재시도만 반복했습니다. 이제 어떤 저장소가 그 경로를 쥐고 있는지와 무엇을 해야 하는지를 문장으로 답합니다.

## 검증

- 가짜 GitLab 인스턴스를 세워 **등록 → 색인 → 검색까지 실제로 통과**시켰습니다.
  - 6개 파일 색인, `config/app.yaml` 의 비밀번호는 `[REDACTED]` 로 저장
  - `pom.xml`·`package.json`·`package-lock.json` 에서 인벤토리 5건 생성, `react` 는 선언 범위가 아니라 락파일의 `18.3.1` 로 집계
  - 검색 API가 응답하지 못하는 상태에서 `search-code` 가 색인된 README 를 찾아내고 근거 ref·commit 을 인용
- 경로 충돌 오류 메시지 회귀 시험, 색인 대체 경로의 ACL 시험
- Go 전체 단위·통합·race 테스트, vet와 build

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- 지금까지 빈 결과를 받던 질의가 결과를 돌려주기 시작할 수 있습니다. 결과에 `index:` 진단이 붙어 있으면 그 답은 마지막 색인 시점 기준입니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.54.0.tar.gz`
- `git-ctx-v0.54.0.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.54.0.tar.gz.sha256
gzip -dc git-ctx-v0.54.0.tar.gz | docker load
docker image inspect git-ctx:v0.54.0 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.54.0`과 `git-ctx:0.54.0` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.53.7...v0.54.0
