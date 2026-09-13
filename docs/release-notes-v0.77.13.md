# git-ctx v0.77.13

이번 릴리스는 **프로젝트가 실제로 갖는 파이썬 락파일을 인벤토리가 읽지 않던 문제**를 고칩니다. 파이썬 해석기는 넷이 보통 쓰이는데 읽히는 락파일은 `poetry.lock` 하나였기 때문에, uv·PDM·pipenv로 해석하는 저장소는 `pyproject.toml`의 범위만 인벤토리에 들어갔습니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

> **권고 대응 중이라면 이 릴리스가 판정할 수 있는 저장소를 늘립니다.** `uv.lock`·`pdm.lock`·`Pipfile.lock`으로 버전을 고정한 저장소는 `find-dependency-usage`가 보기에 `>=2.31`·`~=4.2` 같은 범위만 말하는 저장소였고, 범위는 권고의 고정 버전과 비교할 수 없는 쪽입니다. 이번에도 판정이 아니라 **저장되는 내용**이 바뀌므로, 답이 달라지려면 **재색인이 필요합니다.**

## 수정

### `uv.lock`도 `pdm.lock`도 `Pipfile.lock`도 인벤토리에 없었습니다

- `RecognizeLock`은 파이썬 락파일로 `poetry.lock`만 알았습니다. Poetry는 보통 쓰이는 해석기 넷 중 하나이고, 나머지 셋이 쓰는 파일은 전혀 인식되지 않았습니다.
- 그래서 uv·PDM·pipenv로 해석하는 모든 저장소의 인벤토리에는 `pyproject.toml`의 범위 — `>=2.31`, `~=4.2` — 만 남았습니다. **범위는 권고 판정이 결정하지 못하는 바로 그것입니다.** 인벤토리는 질문만 들고 있었고 답은 한 번도 들어오지 않았습니다.
- uv와 PDM은 Cargo·Poetry와 같은 `[[package]]` 블록을 쓰므로 **기존 TOML 락 파서로 보냅니다.** 이름과 버전은 블록 머리의 0열에 서 있어, uv 블록 아래의 `requires-dist` 항목이나 `[package.metadata]` 표가 패키지로 새지 않습니다.
- `Pipfile.lock`은 JSON이라 **전용 판독기**를 둡니다. 최상위의 `_meta`는 패키지 집합이 아니고 값의 모양도 다르므로, 문서 전체를 하나의 맵으로 읽는 대신 `default`·`develop` 두 집합을 이름으로 짚습니다 — 통째로 읽었다면 `_meta`에서 실패해 파일 전체가 빠졌을 것입니다.

### pipenv는 `==2.31.0`이라고 적습니다

- 다른 모든 락파일은 숫자만 적는데, pipenv는 핀을 설치할 요구사항 그대로 `==2.31.0`으로 저장합니다. 적힌 대로 두면 다른 도구가 해석한 같은 릴리스와 **다른 묶음으로 갈리고**, 권고의 고정 버전과 어떤 비교도 성공하지 못합니다. 연산자를 떼고 숫자만 기록합니다.
- VCS ref에서 가져온 패키지는 커밋으로 고정되고 버전이 없으므로 빼둡니다.

### 두 섹션에 같이 있는 패키지가 두 번 나왔습니다

- pipenv는 한 패키지가 `default`와 `develop`에 모두 필요하면 같은 버전으로 양쪽에 적습니다. `parsePipfileLock`은 둘 다 덧붙여 **같은 패키지가 바이트 단위로 똑같이 두 번** 나왔습니다.
- 겉보기 중복이 아닙니다. 색인기는 패키지를 `INSERT ... ON CONFLICT (...) DO UPDATE` 한 문장으로 묶어 넣는데, PostgreSQL은 같은 행을 두 번 갱신하는 문장을 거부합니다 — `ON CONFLICT DO UPDATE command cannot affect row a second time`. **배치가 실패하고 저장소는 색인되지 않았습니다.**
- 이름과 버전이 같은 항목은 첫 번째만 남깁니다. 같은 이름의 다른 버전은 여전히 두 패키지입니다.

## 검증

- 새 표 `TestThePythonLockFilesAProjectActuallyHas` — `uv.lock`·`pdm.lock`·`Pipfile.lock`이 인식되고 각 파일에서 해석된 패키지가 읽히는지, uv 블록의 `requires-dist`와 `[package.metadata]`, Pipfile.lock의 `_meta`와 VCS 핀이 패키지로 새지 않는지. 수정 전에는 실패함
- 새 시험 `TestPipfileLockStatesTheVersionAlone` — `==` 연산자가 떨어지고 숫자만 남는지. 수정 전에는 실패함
- 새 시험 `TestAPackageInBothSectionsIsEmittedOnce`·`TestTheSameNameAtTwoVersionsStaysTwoPackages` — 두 섹션의 같은 항목은 한 번, 다른 버전은 둘로 나오는지. 수정 전에는 실패함
- `gofmt -l`, `go vet ./...`, FTS5 빌드·전체 테스트 통과, manifest는 `-race`도 통과
- 버전 메타데이터 정합성, Kubernetes Kustomize 렌더링, linux/amd64 Docker 이미지 빌드

## 업그레이드 참고

- 마이그레이션은 필요하지 않습니다.
- **재색인이 필요합니다.** 의존성 인벤토리는 색인할 때 만들어지므로, 이미 색인된 저장소의 저장된 선언은 다시 읽기 전까지 그대로입니다. `uv.lock`·`pdm.lock`·`Pipfile.lock`을 두는 저장소가 있다면 해당 ref를 다시 색인하세요.
- 재색인 뒤에는 **해석된 버전이 인벤토리에 들어옵니다.** 지금까지 범위만 보이던 저장소가 권고 조회에서 고정 버전으로 판정됩니다.
- `docs/configuration.md`의 지원 락파일 목록에 세 이름을 더했습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.77.13.tar.gz`
- `git-ctx-v0.77.13.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.77.13.tar.gz.sha256
gzip -dc git-ctx-v0.77.13.tar.gz | docker load
docker image inspect git-ctx:v0.77.13 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.77.13`과 `git-ctx:0.77.13` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.77.12...v0.77.13
