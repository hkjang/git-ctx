# 오프라인 Docker 이미지 배포

GitHub Release의 `git-ctx-v0.3.0-linux-amd64.tar.gz`는 네트워크가 없는 Linux
AMD64 환경으로 반입할 수 있는 Docker/OCI 이미지 보관 파일이다. 같은 릴리스의
`.sha256` 파일과 함께 내려받는다.

## 무결성 확인과 Docker 로드

```bash
sha256sum -c git-ctx-v0.3.0-linux-amd64.tar.gz.sha256
gzip -dc git-ctx-v0.3.0-linux-amd64.tar.gz | docker load
docker image inspect git-ctx:v0.3.0 --format '{{.Id}} {{.Architecture}} {{.Os}}'
```

애플리케이션 이미지만 포함되므로 PostgreSQL, Keycloak과 사내 CA는 대상 망에서
별도로 준비한다. 기본 포트는 `4747`이며 PostgreSQL은 DSN 하나로 연결한다.

```bash
docker run -d --name git-ctx \
  --restart unless-stopped \
  -p 4747:4747 \
  -e GIT_CTX_DB_DRIVER=postgres \
  -e 'GIT_CTX_DB_DSN=postgres://gitctx:password@postgres.company:5432/gitctx?sslmode=require' \
  -e GIT_CTX_API_KEY_PEPPER='32자 이상의 별도 난수' \
  -e GIT_CTX_MASTER_KEY='정확히 32자인 별도 암호화 키' \
  -e GIT_CTX_BOOTSTRAP_ADMIN='최초 설정 후 제거할 복구 토큰' \
  git-ctx:v0.3.0

curl --fail http://127.0.0.1:4747/readyz
```

운영 Secret을 shell history에 남기지 않도록 실제 배포에서는 `--env-file` 대신
Docker Secret, Kubernetes Secret 또는 사내 Vault/KMS를 사용한다. Bootstrap
관리자 값은 Keycloak 로그인을 검증한 뒤 컨테이너 설정에서 제거한다.

## Kubernetes/containerd 반입

각 노드 또는 사내 registry에 이미지를 적재한다. containerd 노드의 예시는 다음과
같다.

```bash
gzip -dc git-ctx-v0.3.0-linux-amd64.tar.gz \
  | sudo ctr --namespace k8s.io images import -
sudo ctr --namespace k8s.io images list | grep git-ctx
```

`deploy/kubernetes/base/deployment.yaml`의 이미지 이름을 `git-ctx:v0.3.0` 또는
사내 registry 주소로 바꾸고 `imagePullPolicy: IfNotPresent`를 유지한다. Bootstrap
Secret, 사내 CA, 실제 egress CIDR과 Ingress는 환경 overlay에서 제공한다.

## 업그레이드와 복구

시작 시 DB migration이 자동 적용되므로 업그레이드 전에 PostgreSQL 백업을 생성한다.
문제가 생기면 이전 이미지 태그로 workload를 되돌리고, migration 호환 여부에 따라
백업 DB를 복원한다. 상세 점검 절차는 [operations.md](operations.md)를 따른다.
