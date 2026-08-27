<div align="center">

# push

**Drop a file, get a link. Nothing sticks around.**

파일을 던지면 링크가 나옵니다. 하루 뒤엔 전부 사라집니다.

[![License: MIT](https://img.shields.io/badge/License-MIT-009B97.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-1.24-009B97)
![Next.js](https://img.shields.io/badge/Next.js-15-009B97)
![Garage](https://img.shields.io/badge/Storage-Garage-009B97)

[English](#english) · [한국어](#한국어)

</div>

---

## English

`push` is a zero-account file drop. You upload with one `curl` command (or by dragging a file onto the page), you get back an 8 character link, and 24 hours later the file deletes itself. There is no admin panel, no login, and no user database — because there is nothing to administer.

```bash
curl -T file.jpg localhost:3234
# https://push.example.com/aB3xK9pQ
```

### Why it exists

Most "quick upload" tools either buffer the whole file in RAM, write it to a temp disk first, or fall over on multi-gigabyte transfers. `push` streams the request body straight into object storage in parallel chunks, so a 30 GB upload uses roughly the same memory as a 30 MB one.

### How uploads actually work

The request body is read once, sequentially, and sliced into 16 MiB parts. Each part is handed to a worker pool that uploads to Garage concurrently. Memory stays bounded at `part_size × (concurrency + 1)` no matter how large the file is.

```
                    ┌──────────── worker 1 ──┐
 client ──stream──▶ ├──────────── worker 2 ──┤──▶ Garage (S3 multipart)
   body             ├──────────── worker … ──┤
                    └──────────── worker 8 ──┘
                             │
                    any failure ──▶ AbortMultipartUpload (no orphaned parts)
```

Durability details that matter:

| Concern | How it is handled |
| --- | --- |
| Partial upload | Multipart is aborted on any error, so Garage never keeps half a file |
| Client disconnect | Request context cancels every in-flight part, then aborts |
| Crashed process | A sweeper aborts multipart uploads older than 12h |
| Corruption | SHA-256 is computed inline and returned as `X-Checksum-Sha256` |
| Metadata loss | Metadata is a sidecar object in the same bucket — no database to lose |

### Quick start

```bash
git clone https://github.com/aroxu/push.git
cd push

cp .env.example .env
./scripts/gen-secrets.sh   # prints values to paste into .env

docker compose up -d
```

That is the whole deployment: Garage, the app, Caddy (TLS) and Dozzle (logs) in one `docker-compose.yml`.

The application image is published publicly as `ghcr.io/aroxu/push:latest`. Uploaded objects and Garage metadata are kept in the `garage-data` volume, entirely below `/data` inside the Garage container.

Just trying it locally, without TLS or a domain?

```bash
docker compose -f docker-compose.yml -f docker-compose.local.yml up -d
# open http://localhost:3234
```

### Using it

```bash
# upload (any of these work)
curl -T file.jpg           https://push.example.com
curl -F file=@file.jpg     https://push.example.com
curl --data-binary @f.bin  https://push.example.com

# machine readable response
curl -T file.jpg -H 'Accept: application/json' https://push.example.com

# download
curl -O https://push.example.com/aB3xK9pQ

# metadata only
curl https://push.example.com/aB3xK9pQ/info

# delete early (token comes back in the X-Delete-Token header)
curl -X DELETE 'https://push.example.com/aB3xK9pQ?token=YOUR_TOKEN'
```

Because the URL carries the original filename as an optional suffix, `https://push.example.com/aB3xK9pQ/photo.jpg` also works and downloads with the right name.

### Security posture

This is built assuming it is exposed directly to the internet with **no Cloudflare in front of it**, so the protections have to be in the application itself:

- **Unguessable URLs** — 8 chars from `[A-Za-z0-9]` via `crypto/rand` = 62⁸ ≈ 2.18 × 10¹⁴ combinations, uniformly distributed.
- **Nothing renders inline that could execute.** HTML, SVG and anything unrecognised are forced to `application/octet-stream` with `Content-Disposition: attachment`. Only a small allow-list (images, video, audio, PDF, plain text) renders in the browser.
- **Every download is sandboxed** with `Content-Security-Policy: default-src 'none'; sandbox` and `X-Content-Type-Options: nosniff`, so a stored file cannot become stored XSS against your domain.
- **Path traversal is impossible** — IDs are validated against a strict character class before they are ever turned into an object key, and filenames are stripped of directory components and control characters.
- **Spoof-proof rate limiting** — `X-Forwarded-For` is only trusted from explicitly configured proxy ranges, so a client cannot fake its IP to reset its own token bucket.
- **Timing-safe delete tokens** compared with `crypto/subtle`.
- **Hardened container** — read-only root filesystem, all capabilities dropped, `no-new-privileges`, non-root user, and no shell in the final image beyond busybox.
- **Bounded concurrency** so a flood of large uploads degrades gracefully instead of exhausting the host.

Only Caddy is published to the host; Garage, the app and Dozzle live on an internal network.

### Logs and monitoring

Dozzle is available at `/logs`, protected by basic auth (set `DOZZLE_USER` and `DOZZLE_PASSWORD_HASH` in `.env`).

Every upload, download, delete and expiry is emitted as a structured JSON line — to stdout so Dozzle can stream it live, and to a daily-rotated file kept for **7 days**:

```json
{"ts":"2026-08-26T10:04:11Z","event":"upload","id":"aB3xK9pQ","filename":"photo.jpg","size":48213,"ip":"203.0.113.9","status":201,"ms":142.8}
```

### Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `PUSH_PUBLIC_URL` | `http://localhost:3234` | Base URL returned to uploaders |
| `PUSH_MAX_UPLOAD` | `32GB` | Hard size ceiling |
| `PUSH_PART_SIZE` | `16MB` | Multipart chunk size (min 5 MiB) |
| `PUSH_PART_CONCURRENCY` | `8` | Parallel part uploads per file |
| `PUSH_UPLOAD_SLOTS` | `32` | Max concurrent uploads server-wide |
| `PUSH_RETENTION` | `24h` | Lifetime of every file |
| `PUSH_LOG_RETENTION` | `168h` | Audit log retention (7 days) |
| `PUSH_RATE_PER_MIN` | `60` | Requests per minute per IP |
| `PUSH_TRUSTED_PROXIES` | — | CIDRs allowed to set `X-Forwarded-For` |

Tuning note: throughput is roughly `part_size × concurrency` in flight per upload. Raising `PUSH_PART_CONCURRENCY` to 16 helps on fast links; lowering `PUSH_PART_SIZE` reduces memory at the cost of more round trips.

### Development

```bash
cd web && npm install --legacy-peer-deps && npm run build   # static export -> web/out
cd .. && go test ./server/ && go build ./...
```

The frontend is a Next.js static export that gets embedded into the Go binary with `go:embed`, so production runs as a single container with no Node runtime.

### License

MIT — see [LICENSE](LICENSE).

---

## 한국어

`push`는 계정이 필요 없는 파일 공유 도구입니다. `curl` 명령 한 줄로 (또는 페이지에 파일을 끌어다 놓아서) 업로드하면 8글자짜리 링크가 나오고, 24시간 뒤에 파일은 스스로 삭제됩니다. 관리자 페이지도, 로그인도, 사용자 DB도 없습니다. 관리할 것 자체가 없기 때문입니다.

```bash
curl -T file.jpg localhost:3234
# https://push.example.com/aB3xK9pQ
```

### 왜 만들었나

"빠른 업로드" 도구 대부분은 파일 전체를 메모리에 담거나, 임시 디스크에 먼저 쓰거나, 수 GB짜리 전송에서 그냥 죽어버립니다. `push`는 요청 본문을 병렬 청크로 쪼개 오브젝트 스토리지에 곧바로 흘려보냅니다. 그래서 30GB 업로드가 쓰는 메모리는 30MB 업로드와 거의 같습니다.

### 업로드가 실제로 동작하는 방식

요청 본문은 순차적으로 한 번만 읽으면서 16 MiB 단위로 잘립니다. 잘린 각 조각은 워커 풀에 넘겨져 Garage로 **동시에** 업로드됩니다. 파일이 아무리 커도 메모리는 `파트 크기 × (동시성 + 1)` 안에서 유지됩니다.

```
                    ┌──────────── 워커 1 ──┐
 클라이언트 ─스트림─▶ ├──────────── 워커 2 ──┤──▶ Garage (S3 멀티파트)
    본문             ├──────────── 워커 … ──┤
                    └──────────── 워커 8 ──┘
                             │
                    실패 시 ──▶ AbortMultipartUpload (찌꺼기 없음)
```

안전한 저장을 위해 신경 쓴 부분:

| 상황 | 처리 방식 |
| --- | --- |
| 업로드 중단 | 오류가 나면 멀티파트를 abort — 반쪽짜리 파일이 남지 않음 |
| 클라이언트 연결 끊김 | 컨텍스트 취소로 진행 중인 모든 파트를 정리한 뒤 abort |
| 프로세스 비정상 종료 | 청소 작업이 12시간 이상 방치된 멀티파트를 정리 |
| 무결성 | 업로드와 동시에 SHA-256 계산, `X-Checksum-Sha256` 헤더로 반환 |
| 메타데이터 유실 | 메타데이터를 같은 버킷의 사이드카 객체로 저장 — 잃어버릴 DB가 없음 |

### 빠른 시작

```bash
git clone https://github.com/aroxu/push.git
cd push

cp .env.example .env
./scripts/gen-secrets.sh   # .env에 붙여넣을 값을 출력합니다

docker compose up -d
```

배포는 이게 전부입니다. Garage, 앱, Caddy(TLS), Dozzle(로그)이 `docker-compose.yml` 하나로 뜹니다.

앱 이미지는 `ghcr.io/aroxu/push:latest`에 공개됩니다. 업로드 객체와 Garage 메타데이터는 `garage-data` 볼륨에 보존되며, Garage 컨테이너 내부에서는 전부 `/data` 아래에 저장됩니다.

도메인이나 TLS 없이 로컬에서만 먼저 써보고 싶다면:

```bash
docker compose -f docker-compose.yml -f docker-compose.local.yml up -d
# http://localhost:3234 접속
```

### 사용법

```bash
# 업로드 (아래 전부 동작합니다)
curl -T file.jpg           https://push.example.com
curl -F file=@file.jpg     https://push.example.com
curl --data-binary @f.bin  https://push.example.com

# JSON 응답이 필요할 때
curl -T file.jpg -H 'Accept: application/json' https://push.example.com

# 다운로드
curl -O https://push.example.com/aB3xK9pQ

# 메타데이터만 조회
curl https://push.example.com/aB3xK9pQ/info

# 만료 전에 직접 삭제 (토큰은 업로드 응답의 X-Delete-Token 헤더에 있습니다)
curl -X DELETE 'https://push.example.com/aB3xK9pQ?token=발급받은_토큰'
```

URL 뒤에 원래 파일명을 덧붙일 수도 있어서 `https://push.example.com/aB3xK9pQ/photo.jpg` 형태로도 받아집니다.

### 보안 설계

**Cloudflare 같은 프록시 없이 인터넷에 그대로 노출된다**는 전제로 만들었기 때문에, 방어는 전부 애플리케이션 안에 들어 있습니다.

- **추측 불가능한 URL** — `crypto/rand` 기반 `[A-Za-z0-9]` 8글자, 62⁸ ≈ 2.18 × 10¹⁴ 가지 조합이 균등 분포로 생성됩니다.
- **브라우저에서 실행될 수 있는 건 인라인으로 렌더링하지 않습니다.** HTML, SVG, 그리고 정체불명의 타입은 전부 `application/octet-stream` + `Content-Disposition: attachment`로 강제됩니다. 이미지·비디오·오디오·PDF·평문만 화이트리스트로 인라인 표시됩니다.
- **모든 다운로드는 샌드박스 처리** — `Content-Security-Policy: default-src 'none'; sandbox`와 `nosniff`를 적용해서, 업로드된 파일이 도메인에 대한 저장형 XSS로 바뀔 수 없습니다.
- **경로 조작 불가** — ID는 오브젝트 키로 변환되기 전에 엄격한 문자 집합으로 검증하고, 파일명에서는 디렉터리 성분과 제어문자를 제거합니다.
- **위조 불가능한 레이트 리밋** — `X-Forwarded-For`는 명시적으로 신뢰하도록 설정된 프록시 대역에서 온 경우에만 인정합니다. 클라이언트가 IP를 위조해 자기 버킷을 초기화할 수 없습니다.
- **삭제 토큰은 타이밍 공격에 안전하게** `crypto/subtle`로 비교합니다.
- **컨테이너 하드닝** — 읽기 전용 루트 파일시스템, 모든 capability 제거, `no-new-privileges`, 비root 사용자로 실행.
- **동시성 상한** — 대용량 업로드가 몰려도 호스트가 고갈되지 않고 완만하게 성능만 떨어집니다.

호스트에 열리는 건 Caddy뿐이고, Garage·앱·Dozzle은 내부 네트워크에만 존재합니다.

### 로그와 모니터링

Dozzle은 `/logs` 경로에서 볼 수 있고 basic auth로 보호됩니다 (`.env`의 `DOZZLE_USER`, `DOZZLE_PASSWORD_HASH`).

업로드·다운로드·삭제·만료는 모두 구조화된 JSON 한 줄로 남습니다. Dozzle이 실시간으로 볼 수 있도록 stdout으로, 그리고 **7일간** 보관되는 일 단위 파일로 동시에 기록됩니다.

```json
{"ts":"2026-08-26T10:04:11Z","event":"upload","id":"aB3xK9pQ","filename":"photo.jpg","size":48213,"ip":"203.0.113.9","status":201,"ms":142.8}
```

### 설정

| 환경 변수 | 기본값 | 설명 |
| --- | --- | --- |
| `PUSH_PUBLIC_URL` | `http://localhost:3234` | 업로더에게 돌려줄 기본 URL |
| `PUSH_MAX_UPLOAD` | `32GB` | 업로드 최대 크기 |
| `PUSH_PART_SIZE` | `16MB` | 멀티파트 청크 크기 (최소 5 MiB) |
| `PUSH_PART_CONCURRENCY` | `8` | 파일 하나당 동시 파트 업로드 수 |
| `PUSH_UPLOAD_SLOTS` | `32` | 서버 전체 동시 업로드 상한 |
| `PUSH_RETENTION` | `24h` | 파일 보관 기간 |
| `PUSH_LOG_RETENTION` | `168h` | 감사 로그 보관 기간 (7일) |
| `PUSH_RATE_PER_MIN` | `60` | IP당 분당 요청 수 |
| `PUSH_TRUSTED_PROXIES` | — | `X-Forwarded-For`를 신뢰할 CIDR |

튜닝 팁: 업로드 하나당 실제 처리량은 대략 `파트 크기 × 동시성`만큼 동시에 흐릅니다. 회선이 빠르면 `PUSH_PART_CONCURRENCY`를 16까지 올리면 도움이 되고, 메모리를 아끼려면 `PUSH_PART_SIZE`를 줄이는 대신 왕복 횟수를 감수하면 됩니다.

### 개발

```bash
cd web && npm install --legacy-peer-deps && npm run build   # 정적 export -> web/out
cd .. && go test ./server/ && go build ./...
```

프론트엔드는 Next.js 정적 export이고 `go:embed`로 Go 바이너리에 그대로 박히기 때문에, 운영 환경에서는 Node 런타임 없이 컨테이너 하나로 돌아갑니다.

### 라이선스

MIT — [LICENSE](LICENSE) 참고.
