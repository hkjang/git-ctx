# git-ctx v0.58.0

이번 릴리스는 지난 열두 번의 릴리스에서 **손으로 돌리던 전 구간 검증을 저장소에 남는 시험으로** 바꿉니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 배경

최근 릴리스에서 실제로 잡힌 심각한 결함들은 모두 단위 시험이 아니라 **인스턴스를 띄워 전 구간을 돌려서** 나왔습니다 — 호출마다 15초 멈추던 도구, 자기 색인을 읽지 않던 검색, 아무것도 색인하지 않던 정책 변경, `/v1` 이 중복되던 임베딩 URL, 조용히 실패하던 재순위. 어느 것도 한 패키지 안에서는 보이지 않습니다.

## 추가

- `TestPlatformChainIntegration` — 소스 서버·임베딩/재순위 모델·알림 수신기를 **같은 프로세스 안에** 세우고, 실제 HTTP 핸들러와 실제 백그라운드 워커를 통해 다음을 순서대로 확인합니다.

```text
설정 저장(GitLab·모델·알림)  →  저장소 등록  →  워커가 색인
색인 결과   config/app.yaml 의 비밀번호가 마스킹되어 저장됨
            package-lock.json 의 18.3.1 이 선언 범위를 이기고 인벤토리에 들어감
임베딩      /v1 로 끝나는 base URL 로 요청이 나가고 전 청크가 임베딩됨
도구        search-code 가 색인된 문서를 찾음
            find-code-owner 가 CODEOWNERS 의 @ops-team 을 답함
            find-dependency-usage 가 log4j 2.14.1 을 AFFECTED 로 판정
            query-docs 가 재순위 결과와 그 사실을 함께 냄
알림        발송 행이 생기고 수신기까지 도달함
```

- 외부 프로세스나 컨테이너가 필요 없고 10초 안에 끝나므로 기본 시험에 포함됩니다. CI 의 통합 시험 단계(`-run Integration`)에서도 함께 실행됩니다.
- 마스킹을 제거해 보고 이 시험이 실패하는 것을 확인했습니다 — 통과가 우연이 아닙니다.

## 검증

- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트
- 새 시험 단독 10.3초, `internal/app` 전체 20초

## 업그레이드 참고

- 제품 동작 변경은 없습니다. 마이그레이션이나 재색인도 필요하지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.58.0.tar.gz`
- `git-ctx-v0.58.0.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.58.0.tar.gz.sha256
gzip -dc git-ctx-v0.58.0.tar.gz | docker load
docker image inspect git-ctx:v0.58.0 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.58.0`과 `git-ctx:0.58.0` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.57.3...v0.58.0
