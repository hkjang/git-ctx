# git-ctx v0.51.1

이번 릴리스는 사용자 작업 흐름과 관리자 설정 안전성을 함께 개선한 UI/UX 고도화 릴리스입니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

## 주요 개선

- 개인 홈을 추가해 접근 가능한 저장소, 활성 API 키, 최근 MCP 호출, 알림과 시작 체크리스트를 한 화면에서 확인할 수 있습니다.
- 프로필 메뉴, 좌측 메뉴와 `Ctrl/Cmd+K` 빠른 이동을 정비하고 누락된 개인·관리자·벡터 DB 메뉴를 모두 연결했습니다.
- Codex, Claude Code, Cursor, VS Code별 Streamable HTTP 설정을 정확한 형식으로 생성하고 주소·설정·환경변수 명령을 바로 복사할 수 있습니다. API 키 원문은 브라우저 저장소에 보관하지 않습니다.
- 새 MCP 키는 Context7 호환 2개 도구만 기본 허용하며, Context7·코드 탐색·전체 도구 Preset과 발급 직후 키/환경변수 복사를 제공합니다. 일회성 키는 키 화면을 벗어나는 즉시 앱 상태와 DOM에서 제거되어 다시 표시되지 않으며 기존 키 Scope 편집은 그대로 지원합니다.
- 코드 지식 도구를 검색·범주 필터링할 수 있고 실행 시간 표시, 결과 복사, Markdown 저장과 초기화를 추가했습니다.
- 관리자 설정은 저장하지 않은 변경을 표시하고 탭 이동·페이지 종료 전에 경고합니다. 병렬 요청과 조회 실패가 이전 탭 값을 새 카테고리에 남기지 않도록 격리했습니다.
- 외부 시스템 연결 불가와 일시적 502·503·504만 안정 오류 코드 `external_setting_unreachable`로 구분해 명시적으로 강제 저장할 수 있습니다. 형식, 범위, CIDR, 인증 실패와 TLS 인증서 검증은 `force=true`로도 우회되지 않습니다.
- 검색을 키워드 전용으로 설정하면 임베딩 전용 항목을 숨기고 모든 검색 도구가 lexical/원격 Query API 경로를 사용합니다. 모델 시험은 관리자가 명시적으로 실행할 때만 외부 endpoint를 호출합니다.
- PostgreSQL 데이터 이전은 현재 입력한 동일 DSN의 연결 시험을 통과하고 확인 문구를 입력한 경우에만 활성화됩니다.
- 모바일 메뉴, 키보드 탭 이동, 메뉴 역할, focus, live status와 좁은 화면 레이아웃을 보강했습니다.
- 버전 메타데이터 정합성을 CI와 릴리스 게이트에서 자동 검증하여 상단·로그인·프로필·OpenAPI·Kubernetes·오프라인 문서가 같은 버전을 표시합니다.
- 릴리스 검증용 trusted tooling checkout에 워크플로 계약 파일을 명시적으로 포함해, 상세 본문과 자산 계약 시험이 태그 빌드에서도 동일하게 실행됩니다.

## 업그레이드 참고

- 기존 설정, 사용자, API 키와 검색 인덱스의 마이그레이션은 필요하지 않습니다.
- 기존 API 키 Scope는 변경되지 않습니다. 최소 권한 기본값은 새 키를 만들 때만 적용됩니다.
- 일반 벡터 상태 새로고침은 임베딩 API를 호출하지 않습니다. 실제 모델 연결은 **임베딩 모델 시험** 버튼으로 확인하세요.
- 외부 연결 불가 상태에서 설정을 준비하려면 경고 내용을 확인한 뒤 강제 저장을 선택하세요. 자격 증명 오류나 잘못된 설정은 강제 저장할 수 없습니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.51.1.tar.gz`
- `git-ctx-v0.51.1.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.51.1.tar.gz.sha256
gzip -dc git-ctx-v0.51.1.tar.gz | docker load
docker image inspect git-ctx:v0.51.1 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.51.1`과 `git-ctx:0.51.1` 태그가 포함됩니다.

## 검증

- Go 전체 단위·통합·race 테스트, vet와 build
- PostgreSQL 16, pgvector, Vault 통합 시험
- 관리자·사용자 UI JavaScript 구문 및 계약 시험
- GitHub Actions, 버전 정합성, Kubernetes와 Docker 릴리스 검증
- 공개 릴리스 자산 재다운로드, SHA-256과 이미지 version/revision/platform/non-root 검증

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.50.1...v0.51.1
