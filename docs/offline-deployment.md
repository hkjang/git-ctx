# 오프라인 Docker 이미지 배포

v0.7.0 이후 GitHub Release의 `git-ctx-vX.Y.Z.tar.gz`는 네트워크가 없는 Linux
AMD64 환경으로 반입할 수 있는 Docker/OCI 이미지 보관 파일이다. 같은 릴리스의
`.sha256` 파일과 함께 내려받는다.

## 무결성 확인과 Docker 로드

```bash
VERSION=0.70.0
sha256sum -c "git-ctx-v${VERSION}.tar.gz.sha256"
gzip -dc "git-ctx-v${VERSION}.tar.gz" | docker load
docker image inspect "git-ctx:v${VERSION}" --format '{{.Id}} {{.Architecture}} {{.Os}}'
```

애플리케이션 이미지만 포함되므로 PostgreSQL, Keycloak과 사내 CA는 대상 망에서
별도로 준비한다. 기본 포트는 `4747`이다. PostgreSQL 연결 DSN과 별도로, 최소 32자의
고엔트로피 장기 복구 키가 필요하다. 최초 배포에서 한 번 생성한 뒤 같은 값을
Secret Store에서 계속 주입한다.

```bash
VERSION=0.70.0
# 최초 한 번만 생성하고 이후에는 Secret Store의 같은 값을 주입한다.
# 이 키는 복구 토큰 서명, 저장된 설정 암호화 키, 백업 아카이브 봉인의 뿌리다.
# 백업 볼륨과 분리해 보관한다.
export GIT_CTX_RECOVERY_KEY="$(openssl rand -base64 48)"
docker run -d --name git-ctx \
  --restart unless-stopped \
  -p 4747:4747 \
  -v git-ctx-backups:/var/lib/git-ctx/backups \
  -e GIT_CTX_RECOVERY_KEY \
  -e 'GIT_CTX_DB_DSN=postgres://gitctx:password@postgres.company:5432/gitctx?sslmode=require' \
  "git-ctx:v${VERSION}"

curl --fail http://127.0.0.1:4747/readyz
```

위 `openssl rand -base64 48`은 최초 생성 예시다. 운영에서는 시작할 때마다 새 키를
만들지 말고, DSN과 복구 키를 서로 독립된 Docker Secret, Kubernetes Secret 또는 사내
Secret Store 항목으로 주입한다. 두 값은 DB와 백업 볼륨 외부의 DR 절차로 별도
백업하고 shell history에 원문을 남기지 않는다. 최초 관리자 토큰은 컨테이너의
`/var/lib/git-ctx/backups/bootstrap-admin.token`에서 한 번 읽으며 Keycloak 설정 저장
후 실제 `platform-admin` Keycloak 로그인이 성공하면 자동 폐기된다.

## Kubernetes/containerd 반입

각 노드 또는 사내 registry에 이미지를 적재한다. containerd 노드의 예시는 다음과
같다.

```bash
VERSION=0.70.0
gzip -dc "git-ctx-v${VERSION}.tar.gz" \
  | sudo ctr --namespace k8s.io images import -
sudo ctr --namespace k8s.io images list | grep git-ctx
```

`deploy/kubernetes/base/deployment.yaml`의 이미지 이름을 해당 버전 또는
사내 registry 주소로 바꾸고 `imagePullPolicy: IfNotPresent`를 유지한다. Bootstrap
DSN과 `GIT_CTX_RECOVERY_KEY` Secret, 실제 egress CIDR과 Ingress는 환경 overlay에서
제공한다. 여러 Pod에는 같은 장기 복구 키를 주입한다. 사내 CA와 연동 URL은 관리자
화면에서 연결 시험 후 저장한다.

## 업그레이드와 복구

시작 시 DB migration이 자동 적용되므로 업그레이드 전에 PostgreSQL 백업을 생성한다.
문제가 생기면 이전 이미지 태그로 workload를 되돌리고, migration 호환 여부에 따라
백업 DB를 복원한다. 상세 점검 절차는 [operations.md](operations.md)를 따른다.

## 배포된 버전 확인

업그레이드 후 실제로 새 빌드가 도는지 확인하는 방법입니다. 태그만 바꾸고 컨테이너를
교체하지 않은 경우가 가장 흔한 원인입니다.

```bash
# 1) 실행 중인 컨테이너가 보고하는 버전과 빌드
curl -s http://localhost:4747/api/v1/public/config | jq -r '.version, .build'

# 2) 기동 로그 한 줄로도 확인됩니다
docker logs <container> | grep "git-ctx listening"

# 3) 이미지와 컨테이너가 같은지
docker inspect <container> --format '{{.Config.Image}}'
```

`build` 값에는 커밋과 빌드 시각이 포함되므로, 같은 버전 문자열이라도 다른 빌드인지
구분할 수 있습니다. 이미지 빌드 시 `--build-arg VERSION=v0.70.0` 을 주면 소스의
버전과 다를 때 빌드가 실패하므로, 태그와 코드가 어긋난 이미지가 만들어지지 않습니다.

릴리스 아카이브에는 정식 태그 `git-ctx:v0.70.0`와 호환 태그
`git-ctx:0.70.0`가 함께 들어 있습니다. 신규 배포 구성에는 정식 태그를 사용합니다.

## 릴리스 자산 보증

`vX.Y.Z` 태그가 푸시되면 릴리스 워크플로가 태그와 소스 버전의 일치 여부를 먼저
확인하고, 단위·race·PostgreSQL·pgvector·Vault·관리자 UI 시험을 통과한 동일 커밋의
Linux AMD64 이미지만 패키징합니다. 릴리스는 아래 두 파일을 GitHub에서 다시 내려받아
checksum, Docker archive, 버전, 커밋, 플랫폼, 비루트 UID를 모두 재검증한 뒤 공개됩니다.

- `git-ctx-vX.Y.Z.tar.gz`
- `git-ctx-vX.Y.Z.tar.gz.sha256`

운영 반입 전에도 저장소에 포함된 검증 스크립트로 같은 계약을 확인할 수 있습니다.

```bash
scripts/verify-offline-image.sh 0.70.0 \
  git-ctx-v0.70.0.tar.gz <릴리스-커밋-SHA>
```
