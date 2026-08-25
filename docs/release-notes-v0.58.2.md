# git-ctx v0.58.2

이번 릴리스는 전 구간 시험을 **Bitbucket Server 경로까지** 넓힙니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 배경

이 플랫폼의 간판은 Bitbucket Server 와 GitLab 입니다. 그런데 전 구간 검증은 지금까지 GitLab 에서만 이뤄졌습니다. 두 어댑터는 색인기와 검색 계층을 공유할 뿐, **페이지네이션·경로 이스케이프·권한 모델·원문 파일 엔드포인트가 모두 다릅니다.** 이쪽 배선이 어긋나면 소스 하나가 통째로 비는데, 단위 시험은 그 사실을 알려주지 않습니다.

## 추가

- `TestBitbucketChainIntegration` — Bitbucket Server REST 1.0 을 같은 프로세스에 세우고 다음을 확인합니다.

```text
설정 저장  →  프로젝트 탐색(discover)이 저장소를 나열
저장소 등록  →  워커가 색인
색인 결과   config/app.yaml 의 비밀번호 마스킹
            pom.xml 이 인벤토리에 들어감
            사용자·그룹 권한이 ACL 로 들어옴
도구        search-code 가 색인된 문서를 찾음
            find-dependency-usage 가 log4j 2.14.1 을 AFFECTED 로 판정
```

- 원문 파일 엔드포인트의 경로 처리를 일부러 깨뜨려 이 시험이 실패하는 것을 확인했습니다.

## 검증

- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트
- Bitbucket 체인 2.4초, `internal/app` 전체 33초

## 업그레이드 참고

- 제품 동작 변경은 없습니다. 마이그레이션이나 재색인도 필요하지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.58.2.tar.gz`
- `git-ctx-v0.58.2.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.58.2.tar.gz.sha256
gzip -dc git-ctx-v0.58.2.tar.gz | docker load
docker image inspect git-ctx:v0.58.2 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.58.2`과 `git-ctx:0.58.2` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.58.1...v0.58.2
