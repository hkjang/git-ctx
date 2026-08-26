# git-ctx v0.61.0

이번 릴리스는 **PostgreSQL 에서 파일 경로에 든 단어로 검색하면 아무것도 찾지 못하던 문제**를 고치고, 플랫폼 체인 시험 전체를 두 데이터베이스에서 돌립니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 수정

- 이 플랫폼은 SQLite 와 PostgreSQL 을 모두 지원합니다. 두 색인은 다르게 만들어집니다 — SQLite 는 FTS5 외부 콘텐츠 테이블, PostgreSQL 은 생성 컬럼 tsvector.
- PostgreSQL 의 텍스트 검색 파서는 **구조를 알아봅니다**. `internal/settlement/Retry.go` 를 `file` 종류의 토큰 **하나**로 읽고 통째로 색인합니다. SQLite 의 `unicode61` 은 영숫자 아닌 모든 문자에서 쪼갭니다.
- 그래서 `settlement` 로 검색하면 SQLite 설치에서는 파일 4개를 찾고, **PostgreSQL 설치에서는 하나도 못 찾았습니다**. 저장소의 한 부분으로 범위를 좁히는 가장 흔한 방법입니다.
- 이제 텍스트가 파서에 닿기 전에 영숫자 단위로 나뉘므로 두 토크나이저가 같은 단어를 봅니다. URL 과 경로가 단일 토큰이기를 그만두는데, SQLite 는 이미 그렇게 하고 있었습니다.
- 이미 색인 컬럼이 있는 설치는 **표현식이 교체**됩니다. 생성 컬럼은 자리에서 다시 정의할 수 없어 테이블을 한 번 다시 씁니다. 그대로 두면 그 설치는 수명이 다할 때까지 틀린 단어를 색인합니다.

## 검증

- **차분 시험**을 새로 추가했습니다(`TestBothDatabasesAnswerTheSameSearchIntegration`). 같은 코퍼스에 같은 질문 15개를 두 데이터베이스에 던지고 **답이 같기를** 요구합니다. 드라이버마다 기대값을 따로 적는 대신, 갈라지면 차이로 드러납니다.
- 이 시험이 위 결함을 찾았습니다. 15개 중 2개가 갈라졌고, 둘 다 경로 단어 검색이었습니다.
- 오래된 표현식을 가진 데이터베이스가 열릴 때 교체되고 GIN 인덱스가 되돌아오는지 확인하는 업그레이드 시험
- FTS5 빌드·태그 없는 빌드·PostgreSQL 3가지 조합 전체 단위·통합·race 테스트

## 개선

- 플랫폼 체인 시험 7개가 **PostgreSQL 에서도 돕니다**. SQLite 를 상대로 쓰여 거기서만 돌았고, 노드가 둘 이상인 설치가 실제로 쓰는 드라이버는 질의 한 건 위로는 시험된 적이 없었습니다. `GIT_CTX_TEST_POSTGRES_DSN` 이 있으면 같은 체인이 한 번 더 돕니다 — CI 에서 매번.
- 돌려 보니 시험 쪽 결함 3개가 나왔습니다: `?` 자리표시자를 그대로 쓴 조회 20건(PostgreSQL 은 `$1`), 실패를 삼키던 대기 루프, SQLite 에서만 문자열로 읽히는 큰따옴표 리터럴.

## 업그레이드 참고

- PostgreSQL 설치는 첫 기동에서 `document_chunks` 를 한 번 다시 씁니다. 코퍼스 크기에 비례하며, 그동안 색인은 열려 있습니다.
- SQLite 설치는 영향이 없습니다.
- 재색인은 필요하지 않습니다. 컬럼이 기존 행에서 다시 계산됩니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.61.0.tar.gz`
- `git-ctx-v0.61.0.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.61.0.tar.gz.sha256
gzip -dc git-ctx-v0.61.0.tar.gz | docker load
docker image inspect git-ctx:v0.61.0 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.61.0`과 `git-ctx:0.61.0` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.60.2...v0.61.0
