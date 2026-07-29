# 오프라인 Docker 이미지 배포

v0.7.0 이후 GitHub Release의 `git-ctx-vX.Y.Z.tar.gz`는 네트워크가 없는 Linux
AMD64 환경으로 반입할 수 있는 Docker/OCI 이미지 보관 파일이다. 같은 릴리스의
`.sha256` 파일과 함께 내려받는다.

## 무결성 확인과 Docker 로드

```bash
VERSION=0.35.5
sha256sum -c "git-ctx-v${VERSION}.tar.gz.sha256"
gzip -dc "git-ctx-v${VERSION}.tar.gz" | docker load
docker image inspect "git-ctx:v${VERSION}" --format '{{.Id}} {{.Architecture}} {{.Os}}'
```

애플리케이션 이미지만 포함되므로 PostgreSQL, Keycloak과 사내 CA는 대상 망에서
별도로 준비한다. 기본 포트는 `4747`이며 PostgreSQL은 DSN 하나로 연결한다.

```bash
VERSION=0.35.5
docker run -d --name git-ctx \
  --restart unless-stopped \
  -p 4747:4747 \
  -v git-ctx-backups:/var/lib/git-ctx/backups \
  -e 'GIT_CTX_DB_DSN=postgres://gitctx:password@postgres.company:5432/gitctx?sslmode=require' \
  "git-ctx:v${VERSION}"

curl --fail http://127.0.0.1:4747/readyz
```

운영 DSN을 shell history에 남기지 않도록 실제 배포에서는 Docker Secret,
Kubernetes Secret 또는 사내 Secret Store를 사용한다. 최초 관리자 토큰은 컨테이너의
`/var/lib/git-ctx/backups/bootstrap-admin.token`에서 한 번 읽으며 Keycloak 설정 저장
후 실제 `platform-admin` Keycloak 로그인이 성공하면 자동 폐기된다.

## Kubernetes/containerd 반입

각 노드 또는 사내 registry에 이미지를 적재한다. containerd 노드의 예시는 다음과
같다.

```bash
VERSION=0.35.5
gzip -dc "git-ctx-v${VERSION}.tar.gz" \
  | sudo ctr --namespace k8s.io images import -
sudo ctr --namespace k8s.io images list | grep git-ctx
```

`deploy/kubernetes/base/deployment.yaml`의 이미지 이름을 해당 버전 또는
사내 registry 주소로 바꾸고 `imagePullPolicy: IfNotPresent`를 유지한다. Bootstrap
DSN Secret, 실제 egress CIDR과 Ingress는 환경 overlay에서 제공한다. 사내 CA와
연동 URL은 관리자 화면에서 연결 시험 후 저장한다.

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
구분할 수 있습니다. 이미지 빌드 시 `--build-arg VERSION=v0.35.5` 을 주면 소스의
버전과 다를 때 빌드가 실패하므로, 태그와 코드가 어긋난 이미지가 만들어지지 않습니다.

릴리스 아카이브에는 정식 태그 `git-ctx:v0.35.5`와 호환 태그
`git-ctx:0.35.5`가 함께 들어 있습니다. 신규 배포 구성에는 정식 태그를 사용합니다.
