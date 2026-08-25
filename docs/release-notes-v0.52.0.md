# git-ctx v0.52.0

이번 릴리스는 **의존성 인벤토리**를 추가한 기능 릴리스입니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 주요 개선

- 색인할 때 저장소의 의존성 매니페스트를 함께 읽어 **어느 저장소가 어떤 서드파티 패키지를 어떤 버전으로 쓰는지** 인벤토리로 적재합니다. 지원 매니페스트는 `go.mod`, `package.json`, `pom.xml`, `build.gradle(.kts)`, `requirements.txt`, `pyproject.toml`, `Cargo.toml`입니다.
- MCP 도구 `find-dependency-usage`를 추가했습니다. 응답은 **버전별 저장소 묶음**을 먼저 제시하고 이어서 선언 위치(매니페스트 경로·범위·생태계)를 제시합니다. 보안 공지 대응("영향 버전을 쓰는 저장소가 어디인가")과 업그레이드 계획("몇 개 버전이 공존하는가")이 주 용도입니다.
- 기존 `find-dependents`는 import·호출 위치를 찾는 도구로, 버전 정보가 없고 전이 의존성은 import 자체가 없어 이 질문에 답할 수 없습니다. `initialize` 지침에 두 도구의 구분을 명시했습니다.
- 내용 색인 정책이 `.xml`·`.json`을 제외한 저장소에서도 매니페스트는 읽습니다. 정책 때문에 특정 저장소만 인벤토리에서 빠지면 "사용처 없음"이 사실과 달라지기 때문입니다. 한 ref당 매니페스트 60개로 제한합니다.
- 매니페스트를 읽지 못한 경우 색인을 실패시키지 않고 작업 경고로 남깁니다. 조용한 누락은 보안 공지 대응에서 잘못된 안심으로 이어집니다.
- Maven의 `${property}` 버전은 같은 파일의 properties로 해석하고, 해석되지 않으면 플레이스홀더 대신 "미지정"으로 남깁니다. `go.mod`의 `// indirect`는 전이 의존성으로 구분합니다.
- 아직 아무 저장소도 인벤토리에 없는 상태와 실제로 사용처가 없는 상태를 응답에서 구분합니다. 전자는 "재색인 후 확인"을 안내하며 사용처 없음의 근거가 되지 못한다고 명시합니다.
- 콘솔의 코드 지식 화면에 **Dependency Usage** 카드를 추가했습니다. 저장소 제한이 걸린 API 키는 버전 묶음과 저장소 수까지 재계산해 허용된 저장소 외의 정보를 노출하지 않습니다.
- 재색인 시 해당 ref의 인벤토리를 교체합니다. 매니페스트에서 빠진 패키지는 즉시 보고에서도 사라집니다.

## 업그레이드 참고

- 마이그레이션 `043_dependency_inventory.sql`이 `repository_packages`와 staging 테이블을 만들고 `find-dependency-usage` 도구 정책 행을 추가합니다. 기존 설정, 사용자, API 키와 검색 인덱스의 변경은 필요하지 않습니다.
- 인벤토리는 **재색인 시점에** 채워집니다. 업그레이드 직후에는 비어 있으므로, 저장소를 재색인한 뒤 조회하세요. 비어 있는 상태는 응답에서 명시적으로 구분됩니다.
- 기존 API 키 Scope는 변경되지 않습니다. 새 도구를 쓰려면 키 Scope에 `find-dependency-usage`를 추가하세요.
- 백업 대상 테이블에 `repository_packages`가 포함됩니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.52.0.tar.gz`
- `git-ctx-v0.52.0.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.52.0.tar.gz.sha256
gzip -dc git-ctx-v0.52.0.tar.gz | docker load
docker image inspect git-ctx:v0.52.0 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.52.0`과 `git-ctx:0.52.0` 태그가 포함됩니다.

## 검증

- Go 전체 단위·통합·race 테스트, vet와 build
- 매니페스트 파서 회귀 시험(go.mod 전이 표시, Maven property 해석, npm scope, Gradle·requirements·Cargo·pyproject)
- 색인 통합 시험(정책 제외 매니페스트 포함, 재색인 시 교체, staging 정리)
- MCP 종단 시험(버전 묶음, 저장소 제한 키의 정보 비노출)
- 관리자·사용자 UI JavaScript 구문 및 계약 시험

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.51.1...v0.52.0
