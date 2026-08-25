# git-ctx v0.58.3

이번 릴리스는 전 구간 시험을 **push → 웹훅 → 증분 색인** 경로까지 넓힙니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 배경

이 플랫폼이 낸 가장 나쁜 결함이 이 경로에 있었습니다(v0.52.0). 파일 하나만 바뀐 동기화가 **ref 전체의 의존성 인벤토리를 비워서**, 실제로 그 라이브러리를 쓰는 저장소에 대해 공지 검색이 “사용처 없음”을 답했습니다. 지금까지의 체인 시험은 최초 색인만 덮고 있어, 같은 실수를 다시 해도 잡히지 않았습니다.

## 추가

- `TestIncrementalPushChainIntegration` — 변경 가능한 가짜 소스 저장소를 두고 실제 push 순서를 재현합니다.

```text
최초 색인   파일 4건 색인, pom.xml 인벤토리 생성
push        service.go 내용 교체 + legacy.go 삭제 (pom.xml 은 diff 에 없음)
웹훅        서명 검증 통과 → 202, 같은 이벤트 재전송은 200 + duplicate
증분 색인   새 내용이 검색됨, 이전 내용은 검색되지 않음
            삭제된 파일은 색인에서 사라짐
            건드리지 않은 파일은 그대로 남음
            diff 에 없던 매니페스트의 인벤토리가 그대로 유지됨
도구        find-dependency-usage 가 여전히 AFFECTED 로 판정
```

- 매니페스트 보존 로직을 되돌려 보고 이 시험이 인벤토리 1건 → 0건을 잡아내는 것을 확인했습니다. v0.52.0 의 결함을 그대로 재현합니다.

## 검증

- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트
- 증분 체인 4.3초, `internal/app` 전체 38초

## 업그레이드 참고

- 제품 동작 변경은 없습니다. 마이그레이션이나 재색인도 필요하지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.58.3.tar.gz`
- `git-ctx-v0.58.3.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.58.3.tar.gz.sha256
gzip -dc git-ctx-v0.58.3.tar.gz | docker load
docker image inspect git-ctx:v0.58.3 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.58.3`과 `git-ctx:0.58.3` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.58.2...v0.58.3
