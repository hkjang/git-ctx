# git-ctx v0.62.1

이번 릴리스는 **아무것도 등록되지 않은 플랫폼이 "권한이 거부되었다"고 답하던 문제**를 고칩니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

- `library is unavailable or access is denied` 한 문장이 **세 가지 다른 상황**을 덮고 있었습니다.
  1. 이 플랫폼에 저장소가 하나도 등록되지 않았다
  2. 이 신원으로 읽을 수 있는 저장소가 하나도 없다
  3. 이름이 가리키는 것이 없거나, 있어도 이 caller 에게 허용되지 않는다
- 앞의 둘은 **새 설치**와 **신원 매핑이 끊긴 계정**의 모습입니다. 가장 흔한 두 경우가 "권한 거부"로 보고됐고, 그 답을 읽은 사람은 등록이나 계정 매핑이 답인데 권한 모델을 뒤지게 됐습니다.
- 이제 각각을 말합니다 — 등록된 저장소가 없으니 관리 콘솔에서 등록하고 첫 색인을 기다리라고, 또는 이 계정의 Bitbucket·GitLab 신원 매핑을 확인하라고.
- 세 번째는 **일부러 모호하게 둡니다**. "없다"와 "당신 것이 아니다"를 구분하면 읽을 수 없는 저장소의 존재를 알려 주는 셈이기 때문입니다.
- 등록됐지만 아직 색인되지 않은 저장소 — 새 설치가 처음 몇 분을 보내는 상태 — 는 여전히 "이 ref 를 다시 색인하라"고 답합니다. 빈 플랫폼도 거부도 아닙니다.
- 경로로 무언가를 찾는 도구들(`read-file`·`list-directory`·`find-code-owner`·`get-file-history`)도 같은 설명을 거칩니다. 빈 플랫폼에서 "find-file 을 먼저 실행하라"는 조언은 갈 곳이 없습니다.

## 검증

- 읽기 도구 29개를 **빈 카탈로그**에 대고 부르는 조사로 시작했습니다. 8개가 "access is denied" 로 답했고, 4개는 갈 곳 없는 조언을 했습니다.
- 세 상황이 각각 다르게 답하는지, 모호해야 할 것은 모호한지 회귀 시험(`TestAnEmptyPlatformSaysItIsEmpty`·`TestARegisteredRepositoryThatIsNotIndexedSaysSo`·`TestARestrictedCallerIsNotToldWhatExists`). 수정을 되돌리면 조회 도구 14개 전부 실패합니다.
- FTS5 빌드·태그 없는 빌드·PostgreSQL 3가지 조합 전체 단위·통합·race 테스트, 빌드 모드 교차 시험

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- 실패 메시지 문구가 바뀝니다. 문구로 분기하는 클라이언트가 있다면 확인이 필요합니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.62.1.tar.gz`
- `git-ctx-v0.62.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.62.1.tar.gz.sha256
gzip -dc git-ctx-v0.62.1.tar.gz | docker load
docker image inspect git-ctx:v0.62.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.62.1`과 `git-ctx:0.62.1` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.62.0...v0.62.1
