# 시험 및 승인 계획

## 자동 회귀

```bash
go test -race ./...
go vet ./...
node --check web/app.js
node test/web/roles.test.js
node test/web/admin-ui.test.js
kubectl kustomize deploy/kubernetes/base
docker build -t git-ctx:verify .
```

자동 테스트는 API 키 원문 비저장, 키 제한·회전, OIDC 서명과 역할 매핑, Bitbucket
6.9.1/GitLab API 계약, ACL 비노출, MCP 세션·도구 계약, 50개 동시 호출, Webhook
중복, Worker 재시도, Secret Scan, SQLite migration, OTLP protobuf export와 W3C
trace context 전파, 모델 실호출 검증, source query API와 품질 지표를 포함한다.
MCP GET은 initialize로 발급된 session에서 SSE 연결을 유지하고 DELETE가 stream과 session을
함께 종료하는지도 검증한다.

오프라인 파일명과 archive 무결성은 다음처럼 검증한다. 산출물 이름에는 플랫폼 접미사를
붙이지 않는다.

```bash
scripts/package-offline-image.sh 0.14.0 git-ctx:v0.14.0
```

실제 PostgreSQL 백업·복원 계약 시험은 격리된 빈 시험 DB DSN을 명시해서 실행한다.
시험은 대상 DB의 데이터를 삭제하므로 운영 DB에는 절대 지정하지 않는다.

```bash
GIT_CTX_TEST_POSTGRES_DSN='postgres://gitctx:password@localhost:5432/gitctx_test?sslmode=disable' \
  go test -run TestPostgresBackupRestoreIntegration -v ./internal/backup
GIT_CTX_TEST_POSTGRES_DSN='postgres://gitctx:password@localhost:5432/gitctx_test?sslmode=disable' \
  go test -run TestPostgresQualityBenchmarkIntegration -v ./internal/quality
GIT_CTX_TEST_POSTGRES_DSN='postgres://gitctx:password@localhost:5432/gitctx_test?sslmode=disable' \
  go test -run TestPostgresDSNOnlyBootstrapIntegration -v ./internal/app
```

## 부하 시험

승인 환경의 실제 저장소와 PostgreSQL을 사용한다.

```bash
GIT_CTX_URL=https://git-ctx.staging.company \
GIT_CTX_API_KEY=... \
LIBRARY_ID=/kcb/demo/main \
k6 run test/load/mcp.js
```

기본 게이트는 50 VU, 오류율 1% 미만, MCP P95 3초 미만이다. 캐시 적중 P95 1초,
`resolve-library-id` P95 2초 목표는 별도 시나리오로 측정한다. 시험 보고서에는 DB
크기, 청크 수, CPU/메모리, PostgreSQL connection 수와 검색 후보 수를 기록한다.

## 실환경 승인

- 실제 클라이언트 로컬 호환 결과와 재현 절차는
  [mcp-client-compatibility.md](mcp-client-compatibility.md)를 기준으로 대조
- Keycloak Authorization Code+PKCE 로그인, 역할/Claim 미리보기와 Single Logout
- Codex CLI, Claude Code, Cursor 중 조직 승인 대상 2개 이상의 실제 연결
- Bitbucket Server 6.9.1 push/branch/tag webhook과 30분 polling 보정
- GitLab push/tag webhook, 중복·역순 이벤트
- 권한 회수 후 캐시 만료 이전/이후 Fail Closed 동작
- 사내 CA 교체, 잘못된 CA, 프록시 장애와 복구
- PostgreSQL 백업 복원 및 원래 DSN Secret 분리 복구
- 2개 Pod 동시 Worker의 `SKIP LOCKED` 중복 방지

외부 시스템이 필요한 항목은 배포 환경의 서명된 시험 보고서 없이는 완료로 판정하지
않는다.
