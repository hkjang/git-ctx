# git-ctx v0.52.7

이번 릴리스는 의존성 조회 답변에 **색인 최신성**을 붙입니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 주요 개선

- `find-dependency-usage` 응답이 답변에 사용된 저장소의 **색인 시점**을 확인해, 오래된 색인이 섞여 있으면 어느 저장소가 얼마나 오래됐는지 진단에 남깁니다.
- 보안 공지 대응에서 특히 중요한 방향입니다. 한 달 전에 색인된 저장소는 그 사이 **영향 버전을 새로 받았을 수도** 있는데, 그 사실을 모른 채 "안전"으로 읽히면 잘못된 안심이 됩니다.
- 모든 저장소가 최근에 색인된 경우에는 아무 메모도 붙지 않습니다.

## 감사 결과 (수정 없음)

- 콘솔 REST 경로(`/api/v1/tools/*/test`) 20종을 감사했고, API 키 저장소 제한이 모두 적용되어 있음을 확인했습니다(`compare-refs`·`get-change-impact` 는 공용 `refAnalysis` 경유). v0.52.6 에서 고친 MCP 쪽 누락과 같은 문제는 이 표면에 없습니다.

## 업그레이드 참고

- 마이그레이션이나 재색인은 필요하지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.52.7.tar.gz`
- `git-ctx-v0.52.7.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.52.7.tar.gz.sha256
gzip -dc git-ctx-v0.52.7.tar.gz | docker load
docker image inspect git-ctx:v0.52.7 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.52.7`과 `git-ctx:0.52.7` 태그가 포함됩니다.

## 검증

- Go 전체 단위·통합·race 테스트, vet와 build
- 최신성 시험: 오래된 색인 저장소만 지목되고 최근 색인 저장소는 언급되지 않으며, 전부 최근이면 메모가 붙지 않음
- 전 도구 허용 목록 누출 가드 유지
- 관리자·사용자 UI JavaScript 구문 및 계약 시험

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.52.6...v0.52.7
