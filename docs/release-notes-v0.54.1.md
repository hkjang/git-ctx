# git-ctx v0.54.1

이번 릴리스는 연동 오류 메시지에서 **Go 내부 타입이 새어 나가던 것**을 없앱니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

- 소스 서버가 예상과 다른 형태의 응답을 주면 `json: cannot unmarshal object into Go value of type []struct { ID string "json:\"id\"" ... }` 같은 문자열이 그대로 MCP 클라이언트와 운영 화면까지 전달됐습니다. 읽는 쪽이 사람이든 에이전트든, 이 문자열로는 아무것도 판단할 수 없습니다.
- 이제 어떤 서버의 어느 엔드포인트인지와, 무엇이 잘못됐는지를 JSON 용어로 말합니다 — “목록이 와야 할 자리에 객체가 왔습니다”. 확인할 곳(프록시나 로그인 페이지를 가리키는 base URL, 지원하지 않는 서버 버전)도 함께 제시합니다. 실제 원인은 대부분 이 둘입니다.
- GitLab·Bitbucket·Confluence·Jira 네 연동이 같은 공용 함수를 씁니다. 검색·커밋 이력·트리·페이지네이션 등 응답을 읽는 모든 경로에 적용됩니다.

## 검증

- 32개 MCP 도구를 실제 인스턴스에서 한 번씩 호출해 응답을 확인했습니다. 소스 API가 다른 형태를 돌려주는 상태에서 `get-file-history`·`find-code-owner`·`search-source` 가 내부 타입 대신 진단 문장을 반환합니다.
- 인자 검증 메시지(`distinct baseRef and headRef are required` 등)는 그대로 유지됩니다.
- 디코딩 실패 메시지 회귀 시험(타입 누출 금지, 비JSON 본문 구분)
- Go 전체 단위·통합·race 테스트, vet와 build

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.
- 연동 오류 문자열로 알림 규칙을 만들어 두었다면 문구가 바뀝니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.54.1.tar.gz`
- `git-ctx-v0.54.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.54.1.tar.gz.sha256
gzip -dc git-ctx-v0.54.1.tar.gz | docker load
docker image inspect git-ctx:v0.54.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.54.1`과 `git-ctx:0.54.1` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.54.0...v0.54.1
