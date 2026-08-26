# git-ctx v0.74.2

이번 릴리스는 **실패한 호출이 에이전트가 실제로 묻는 것에 답하지 않던 문제** 두 가지를 고칩니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

### 파일을 못 찾으면 이유가 무엇이든 같은 한 문장이 돌아왔습니다

- `read-file`·`get-file-history` 계열이 후보를 하나도 못 찾으면 항상 이렇게 답했습니다 — `no accessible repository contains "<path>"; run find-file first or pass libraryId`.
- **`libraryId`를 이미 넘긴 호출에도 같은 문장이 돌아왔습니다.** 이미 한 일을 다시 하라는 조언을 받은 에이전트에게는 다음 수가 없습니다.
- 경로가 아니라 **저장소 쪽이 문제일 때도** 경로 탓을 했습니다. 그 경우 `find-file`로 보내봐야 거기서도 아무것도 못 찾습니다.
- 이제 제약을 하나씩 걷어내며, 걷어냈을 때 파일이 나오는 첫 제약을 지목합니다.
  - 경로가 다른 라이브러리에 있음 → `"shared.go" is not in /kcb/alpha; it is in /kcb/beta`
  - 경로가 다른 ref에만 있음 → `... is in /kcb/alpha but not on its default branch; it is on release: pass ref to read it`
  - 지정한 ref에 없음 → `... but not on ref "main"; it is on release`
  - 라이브러리를 읽을 수 없음 → `no repository /kcb/secret is registered here that this identity can read: run resolve-library-id ...`
  - 정말 어디에도 색인되지 않음 → 기존 문장 그대로
- **읽을 수 없는 저장소와 존재하지 않는 저장소는 일부러 같은 문장으로 답합니다.** 그 차이를 감추는 것이 ACL이 하는 일입니다.

### 모든 스키마가 `additionalProperties: false`라고 선언했지만 아무도 확인하지 않았습니다

- `limit` 대신 `maxResults`를 보낸 에이전트는 **그 인자 없이 계산된 답**을 받았습니다. 답은 완전히 정상으로 보였습니다.
- `libraryId` 대신 `library_id`를 보낸 에이전트는 **자기가 분명히 보낸 필수 인자가 없다는 오류**를 받았습니다.
- 어느 쪽 응답에도 인자가 버려졌다는 말은 없었습니다.
- 이제 호출은 여전히 처리하되(무해한 여분 인자를 보내는 에이전트를 깨뜨리지 않기 위해), 버려진 인자와 그 도구가 받는 인자 목록을 응답에 적습니다.
- `maxBytes`와 `_meta` 같은 프로토콜 필드는 도구가 거부할 것이 아니므로 보고하지 않습니다.
- `serverInstructions`에도 이 동작을 적어, 에이전트가 그 줄을 읽고 다시 호출할 수 있게 했습니다.

## 검증

- 파일 조회가 비는 여덟 가지 상황이 각각 다른 제약을 지목하는지 확인하고, **`libraryId`를 넘긴 호출의 답에 다시 `pass libraryId`가 나오면 실패**하도록 했습니다
- `read-file`에서도 같은 이유가 나오는지 확인 — 헬퍼만 고치고 도구는 그대로인 경우를 막습니다
- 오타 난 선택 인자·오타 난 필수 인자·정상 호출·`maxBytes`+`_meta` 네 경우를 각각 확인
- 두 수정 모두 되돌리면 시험이 실패합니다
- FTS5 빌드·태그 없는 빌드·PostgreSQL 3가지 조합 전체 단위·통합·race 테스트, 빌드 모드 교차 시험, 릴리스 데이터베이스 업그레이드 시험, 콘솔 시험

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- 성공하는 호출의 응답은 바뀌지 않습니다. 바뀐 것은 실패했을 때 돌아오는 문장과, 도구에 없는 인자를 보냈을 때 덧붙는 한 줄입니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.74.2.tar.gz`
- `git-ctx-v0.74.2.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.74.2.tar.gz.sha256
gzip -dc git-ctx-v0.74.2.tar.gz | docker load
docker image inspect git-ctx:v0.74.2 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.74.2`와 `git-ctx:0.74.2` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.74.1...v0.74.2
