# git-ctx v0.60.0

이번 릴리스는 보안 공지 답변에 **“누가 고쳐야 하는가”** 를 함께 넣습니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 개선

- `find-dependency-usage` 에 수정 버전을 주면 영향받는 저장소 목록이 나옵니다. 그런데 공지 대응에서 그 목록은 **절반**입니다. 나머지 절반은 각 저장소를 누가 책임지는가이고, 저장소 열두 개를 손으로 찾아보는 데 시간이 갑니다.
- 이제 영향받는 저장소마다 **바꿔야 할 매니페스트를 덮는 CODEOWNERS 규칙의 소유자**를 함께 답합니다. 파일 단위 규칙이 있으면 그것이, 없으면 catch-all 이 답이 됩니다.
- 선언이 없는 저장소는 비워 두지 않고 **없다고 밝히며**, `find-code-owner` 로 최근 기여자를 확인하도록 안내합니다.
- 콘솔의 Dependency Usage 카드도 같은 정보를 함께 보여 줍니다.
- 조회는 영향 저장소 25곳으로 제한합니다. 그보다 큰 공지는 조회가 아니라 계획 수립의 문제이고, 그때 필요한 것은 목록입니다.

## 실증

실제 인스턴스에서 확인했습니다.

```text
Advisory: fixed in 2.17.1 · affected 1 · safe 0 · undecided 0 repositories

### Owners of the affected repositories
- /gitlab~core/api — @platform-team
```

- 콘솔에서 CVE 대응 흐름을 브라우저로 끝까지 확인했습니다: MCP 화면 → 패키지·수정 버전 입력 → 영향 저장소·선언 위치·소유자.

## 검증

- 파일 단위 규칙이 catch-all 을 이기는지, 선언 없는 저장소가 그렇게 표시되는지, 안전한 저장소는 목록에 없는지, 수정 버전을 주지 않으면 조회 자체가 일어나지 않는지 시험
- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- CODEOWNERS 가 색인되어 있어야 소유자가 나옵니다. 색인 정책에서 확장자 없는 파일이 제외돼 있으면 재색인 후에 표시됩니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.60.0.tar.gz`
- `git-ctx-v0.60.0.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.60.0.tar.gz.sha256
gzip -dc git-ctx-v0.60.0.tar.gz | docker load
docker image inspect git-ctx:v0.60.0 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.60.0`과 `git-ctx:0.60.0` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.59.3...v0.60.0
