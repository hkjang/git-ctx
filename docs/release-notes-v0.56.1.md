# git-ctx v0.56.1

이번 릴리스는 전문 검색 인덱스를 **PostgreSQL 에도** 넣어 두 데이터베이스의 검색 경로를 맞춥니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 개선

- PostgreSQL 은 `document_chunks` 에 생성 열(`search_vector`)과 GIN 인덱스를 갖습니다. 값은 데이터베이스가 행을 쓸 때마다 스스로 계산하므로 유지할 트리거가 없고, ref 교체 같은 대량 삽입도 그대로 따라갑니다.
- 이제 어느 데이터베이스로 설치하든 같은 질의가 같은 결과를 냅니다. 단어 안쪽 일치를 위한 보충 훑기도 양쪽에서 동일하게 동작합니다.
- 200,000행으로 확인한 질의 계획은 GIN 인덱스를 사용합니다(실행 9ms).

## 수정

- **백업·이전이 깨질 뻔한 것을 함께 고쳤습니다.** 백업은 테이블을 `SELECT *` 로 내보내고 복원 시 스키마를 대조하는데, 생성 열이 생기면서 SQLite 백업과 PostgreSQL 대상의 열 구성이 달라져 “backup schema for document_chunks does not match” 로 이전이 거부됐습니다. 생성 열은 데이터가 아니라 색인이므로 백업이 싣지도, 대조하지도 않습니다. PostgreSQL 은 생성 열에 값을 넣는 것을 거부하므로 복원 경로도 함께 막혔을 문제입니다.

## 검증

- PostgreSQL 16 컨테이너에서 통합 시험: 인덱스 생성, 정확 일치·접두사·경로 조각·단어 안쪽 일치, ACL 격리, 내용 수정 후 재검색
- SQLite→PostgreSQL 논리 이전 통합 시험 통과(이번 회귀를 잡은 시험)
- FTS5 빌드·태그 없는 빌드 양쪽 전체 단위·통합·race 테스트
- 200,000행 질의 계획 확인(Bitmap Index Scan 사용)

## 업그레이드 참고

- PostgreSQL 인스턴스는 첫 기동에서 생성 열과 GIN 인덱스를 만듭니다. 큰 테이블에서는 그만큼 시간이 걸리고, 저장 공간도 늘어납니다.
- 이전 버전에서 만든 백업 아카이브는 그대로 복원됩니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.56.1.tar.gz`
- `git-ctx-v0.56.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.56.1.tar.gz.sha256
gzip -dc git-ctx-v0.56.1.tar.gz | docker load
docker image inspect git-ctx:v0.56.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.56.1`과 `git-ctx:0.56.1` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.56.0...v0.56.1
