# git-ctx v0.59.1

이번 릴리스는 마지막으로 남아 있던 선택적 백엔드 — **OpenSearch 투영** — 을 전 구간 시험으로 덮습니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 배경

색인기는 ref 하나를 마칠 때마다 그 내용을 OpenSearch 로 투영하고, 검색은 거기서 후보를 읽어 옵니다. 투영이 조용히 멈추면 **클러스터는 이미 사라진 내용으로 계속 답하고**, 색인은 최신이라고 믿습니다. 이 어긋남은 몇 주 뒤 오래된 결과 하나로만 드러납니다.

## 추가

- `TestOpenSearchProjectionChainIntegration` — OpenSearch 를 같은 프로세스에 세우고 확인합니다.

```text
최초 색인   인덱스가 만들어지고 청크가 모두 투영됨, 투영 상태가 기록됨
push        service.go 교체 + legacy.go 삭제 → 웹훅
투영 갱신   새 내용이 클러스터에 들어오고, 삭제된 파일의 문서가 사라짐
```

- 투영을 꺼 보고 이 시험이 실패하는 것을 확인했습니다.

## 검증

- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트
- 체인 시험 7종(정상·부분장애·Bitbucket·증분push·권한·문서형 소스·OpenSearch)과 콘솔 렌더 스윕

## 업그레이드 참고

- 제품 동작 변경은 없습니다. 마이그레이션이나 재색인도 필요하지 않습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.59.1.tar.gz`
- `git-ctx-v0.59.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.59.1.tar.gz.sha256
gzip -dc git-ctx-v0.59.1.tar.gz | docker load
docker image inspect git-ctx:v0.59.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.59.1`과 `git-ctx:0.59.1` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.59.0...v0.59.1
