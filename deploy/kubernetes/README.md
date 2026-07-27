# Kubernetes 배포

운영 Secret은 Git에 저장하지 않고 External Secrets, Vault 또는 사내 KMS 연동으로
`git-ctx-bootstrap`을 생성한다. `secret.example.yaml`은 필드 설명용이며 그대로
적용하지 않는다.

```bash
kubectl -n git-ctx create secret generic git-ctx-bootstrap \
  --from-literal=GIT_CTX_DB_DSN='postgres://...' \
  --from-literal=GIT_CTX_API_KEY_PEPPER='...' \
  --from-literal=GIT_CTX_MASTER_KEY='...' \
  --from-literal=GIT_CTX_BOOTSTRAP_ADMIN='...'
kubectl -n git-ctx apply -k deploy/kubernetes/base
```

기본 NetworkPolicy는 외부 사내 Keycloak·Bitbucket·GitLab과 PostgreSQL을 위해
TCP 443/5432 및 DNS만 허용한다. 운영 overlay에서는 실제 사내망과 DB CIDR로
`ipBlock`을 더 좁힌다.

Keycloak 설정을 완료하고 플랫폼 관리자 로그인을 검증한 뒤
`GIT_CTX_BOOTSTRAP_ADMIN`을 제거한다. Ingress, 사내 CA, NetworkPolicy CIDR와
이미지 digest는 환경 overlay에서 지정한다.

기본 backup PVC는 여러 replica의 scheduler가 같은 아카이브를 읽을 수 있도록 RWX를
요청한다. 클러스터의 StorageClass가 RWX를 지원하지 않으면 운영 overlay에서 NFS/CSI
공유 볼륨 또는 승인된 백업 스토리지로 교체한다. `GIT_CTX_MASTER_KEY`는 이 PVC와
분리된 Secret Store에 보관한다.
