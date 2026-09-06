# git-ctx v0.77.8

이번 릴리스는 **의존성 인벤토리가 조용히 불완전해지던 네 곳**을 말하게 만듭니다. 매니페스트가 너무 크거나 락파일 항목이 너무 많으면 색인은 그 선언들을 버렸고, 아무 데도 기록하지 않았습니다. 기본 서비스 포트는 계속 `4747`이며 기존 API·MCP 호환성을 유지합니다.

> **권고 대응 중이라면 색인 작업의 경고를 확인하세요.** 인벤토리에 없는 저장소와 그 라이브러리를 쓰지 않는 저장소는 `find-dependency-usage`의 답에서 똑같이 생겼습니다. 이번 릴리스부터는 전자가 색인 작업 경고로 남습니다. 한계 자체는 그대로이므로 저장된 내용은 달라지지 않습니다 — 다음 색인부터 경고가 보입니다.

## 수정

### 인벤토리가 조용히 버리던 네 곳

- **`MaxManifestBytes`(1MB) 초과** — `Parse`가 `nil`을 돌려줬습니다. 생성된 `package.json` 하나가 1MB를 넘기면 그 저장소는 인벤토리에서 통째로 빠졌고, 권고 질의는 그 빈자리를 "이 저장소는 그 라이브러리를 쓰지 않는다"로 읽었습니다.
- **`MaxLockBytes`(8MB) 초과** — `ParseLock`이 마찬가지로 `nil`을 돌려줬습니다. 없어진 것은 해석된 버전, 즉 권고를 즉답할 수 있는 유일한 근거입니다.
- **`MaxLockPackages`(4000) 초과** — 앞쪽 4000개만 남았습니다. `go.sum`은 모듈 경로 순으로 정렬돼 있으므로, 잘려 나간 것은 **알파벳 뒤쪽의 연속된 덩어리**입니다. 그 모듈들은 전부 무사한 것처럼 보였습니다.
- **ref당 매니페스트 60개 상한** — 상한에 닿으면 `break`로 빠져나가, "60개를 다 읽었다"와 "60개를 읽고 40개를 버렸다"를 구분할 수 없었습니다.

### 한계는 그대로, 다만 말합니다

- `manifest.ParseNoted` / `manifest.ParseLockNoted`가 읽지 못한 사실을 note로 함께 돌려줍니다. 파일 이름, 실제 크기 또는 개수, 적용된 한계, 그래서 무엇이 인벤토리에 없는지를 적습니다.
- 색인은 그 note를 이미 쓰고 있던 **매니페스트 경고 경로**로 올립니다. 읽지 못한 매니페스트가 작업의 `error_message`에 남고, 생성 자체는 정상 완료됩니다 — 온전한 부분은 그대로 쓸 수 있어야 하기 때문입니다.
- ref당 상한은 `break` 대신 남은 매니페스트를 세도록 바꿔, 몇 개를 읽지 않았는지 정확히 보고합니다.
- `manifest.Parse`와 `manifest.ParseLock`의 서명과 동작은 그대로입니다. 한계값도 바뀌지 않았습니다.

## 검증

- 새 표 `TestOversizedManifestSaysWhatItDropped`, `TestOversizedLockSaysWhatItDropped`, `TestTruncatedLockSaysHowManyItDropped`, `TestAFileReadWholeCarriesNoNote` — 버려진 사실이 note에 파일명·크기·한계와 함께 남는지, 온전히 읽은 파일은 note를 만들지 않는지
- 새 표 `TestAppendPackagesReportsWhatItCouldNotRead` — 색인이 파서의 note를 실제로 실어 나르는지
- `gofmt -l`, `go vet ./...`, FTS5 빌드·전체 테스트 통과, manifest·indexer·search는 `-race`도 통과

## 업그레이드 참고

- 마이그레이션은 필요하지 않습니다.
- **재색인은 선택입니다.** 저장된 선언과 한계값은 그대로이고, 다음 색인부터 경고가 채워집니다. 다만 인벤토리에 구멍이 있는지 알고 싶다면 재색인 후 작업 경고를 보는 것이 유일한 방법입니다.
- 경고가 뜬 저장소는 인벤토리가 부분적이라는 뜻이며, 그 저장소에 대한 권고 판정은 "안전"이 아니라 "확인 필요"로 다뤄야 합니다.

## 오프라인 Docker 이미지

릴리스 자산은 아키텍처 접미사가 없는 다음 두 파일입니다.

- `git-ctx-v0.77.8.tar.gz`
- `git-ctx-v0.77.8.tar.gz.sha256`

```bash
sha256sum -c git-ctx-v0.77.8.tar.gz.sha256
gzip -dc git-ctx-v0.77.8.tar.gz | docker load
docker image inspect git-ctx:v0.77.8 --format '{{.Os}}/{{.Architecture}} {{.Config.User}}'
```

기대 결과는 `linux/amd64 10001`입니다. 아카이브에는 `git-ctx:v0.77.8`과 `git-ctx:0.77.8` 태그가 포함됩니다.

**전체 변경 내역**: https://github.com/hkjang/git-ctx/compare/v0.77.7...v0.77.8
