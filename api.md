# UUID / MAC 서버 API

이 문서는 UUID/MAC 서버가 제공하는 HTTP API를 설명합니다.

서버는 두 종류의 API를 제공합니다.

| 경로 | 대상 | 규칙 |
|---|---|---|
| `/api/v1/*` | 신규 `libmacclient`, `macctl`, 신규 프로그램 | HTTP 상태 코드와 구조화된 오류 사용 |
| `/restapi/*` | 기존 `lib_mac` 및 기존 생산 장비 | 기존 요청·응답 형식 유지 |

기존 장비는 계속 `/restapi/*`를 사용하고, 새 프로그램은 `/api/v1/*`를
사용해야 합니다.

## 공통 사항

예제 설정의 API 주소는 다음과 같습니다.

```text
http://127.0.0.1:8881
```

- 별도 인증은 사용하지 않습니다.
- `Authorization`, `Token` 등 기존 클라이언트가 보내는 인증 헤더는 무시합니다.
- 요청과 응답 본문은 UTF-8 JSON입니다.
- JSON 요청 본문은 최대 16KiB이며 알 수 없는 필드와 복수 JSON 값은 거부합니다.
- MAC 주소는 콜론 없는 소문자 12자리 형식입니다.
- MAC suffix `0000`은 할당하거나 조작할 수 없습니다.
- UUID는 하이픈을 포함한 36자리이며 마지막 12자리는 MAC 주소와 같습니다.
- 요청의 보드명에는 TOML의 canonical 보드명 또는 alias를 사용할 수 있습니다.

```text
MAC:  001e065e0001
UUID: f42c901b-4cec-4a44-9272-001e065e0001
```

할당 상태는 다음 순서로 변경됩니다.

```text
free/released --request--> reserved --confirm--> assigned
                         \--release-----------> released
assigned -------------------release-----------> released
```

`request` 후에는 보드에 MAC/UUID를 기록하고 다시 읽어 검증한 다음 반드시
`confirm` 또는 `release`를 호출해야 합니다.

## 신규 API v1

### 오류 응답

v1 API는 오류에 맞는 HTTP 상태 코드를 반환합니다.

```json
{
  "error": {
    "code": "pool_exhausted",
    "message": "no more available MAC addresses"
  }
}
```

주요 오류 코드는 다음과 같습니다.

| HTTP | code | 의미 |
|---:|---|---|
| 400 | `invalid_request` | JSON 형식, 필드 또는 본문이 잘못됨 |
| 400 | `unsupported_board` | 설정에 없는 보드명 |
| 400 | `invalid_request_id` | request ID 형식이 잘못됨 |
| 400 | `invalid_address` | 보드명 또는 MAC 형식이 잘못됨 |
| 400 | `invalid_uuid` | UUID 형식이 잘못됨 |
| 400 | `uuid_mac_mismatch` | UUID 마지막 12자리와 MAC이 다름 |
| 400 | `invalid_prefix` | prefix 형식이 잘못됨 |
| 400 | `invalid_limit` | 조회 개수 범위가 잘못됨 |
| 404 | `allocation_not_found` | 해당 할당을 찾을 수 없음 |
| 409 | `pool_exhausted` | 사용할 수 있는 MAC이 없음 |
| 400 | `invalid_state` | 목록 조회의 상태 필터가 잘못됨 |
| 409 | `invalid_state` | 현재 상태에서 요청한 변경을 수행할 수 없음 |
| 409 | `uuid_mismatch` | 예약 당시 UUID와 confirm UUID가 다름 |
| 409 | `pool_mismatch` | MAC이 해당 보드의 활성 prefix에 속하지 않음 |
| 409 | `request_id_conflict` | request ID가 다른 할당에 사용됨 |
| 500 | `internal_error` | 서버 또는 DB 내부 오류 |

### MAC/UUID 예약

```text
POST /api/v1/allocations
```

요청:

```json
{
  "board": "r1",
  "request_id": "b893d5e7-55c7-4a7f-b013-b21bcc4ad230"
}
```

`request_id`는 호출 작업을 식별하는 UUID입니다. 같은 보드와 request ID를
다시 보내면 새 주소를 할당하지 않고 처음 생성한 MAC/UUID를 반환합니다.
신규 C 라이브러리는 이 값을 내부에서 생성하고 네트워크 재시도 동안
재사용하므로 일반 사용자가 직접 관리할 필요는 없습니다.
HTTP API를 직접 사용하는 프로그램은 요청 결과가 확정될 때까지 request ID를
보관하고, 응답 유실 시 반드시 같은 값을 재사용해야 합니다.

응답:

```json
{
  "mac": "001e065e0001",
  "board": "odroidr1",
  "prefix": "001e065e",
  "suffix": 1,
  "state": "reserved",
  "uuid": "f42c901b-4cec-4a44-9272-001e065e0001",
  "reserved_at": "2026-08-14T03:00:00.000000000Z",
  "assigned_at": "",
  "updated_at": "2026-08-14T03:00:00.000000000Z"
}
```

curl 예제:

```sh
curl -sS -X POST http://127.0.0.1:8881/api/v1/allocations \
  -H 'Content-Type: application/json' \
  -d '{"board":"r1","request_id":"b893d5e7-55c7-4a7f-b013-b21bcc4ad230"}'
```

### 할당 확정

```text
POST /api/v1/allocations/{mac}/confirm
```

보드에 기록하고 readback으로 확인한 MAC/UUID만 확정해야 합니다.
서버에는 보드 readback 값이 별도로 전달되지 않으므로, 요청값과 readback
값의 비교는 confirm을 호출하기 전에 클라이언트 프로그램이 수행해야 합니다.

요청:

```json
{
  "board": "r1",
  "uuid": "f42c901b-4cec-4a44-9272-001e065e0001"
}
```

응답:

```json
{
  "board": "odroidr1",
  "mac": "001e065e0001",
  "uuid": "f42c901b-4cec-4a44-9272-001e065e0001",
  "state": "assigned"
}
```

동일한 MAC/UUID로 confirm을 반복해도 성공합니다. 다른 UUID를 보내면
`uuid_mismatch`로 거부합니다.

```sh
curl -sS -X POST \
  http://127.0.0.1:8881/api/v1/allocations/001e065e0001/confirm \
  -H 'Content-Type: application/json' \
  -d '{"board":"r1","uuid":"f42c901b-4cec-4a44-9272-001e065e0001"}'
```

### 예약 또는 할당 반환

```text
POST /api/v1/allocations/{mac}/release
```

요청:

```json
{
  "board": "r1"
}
```

응답:

```json
{
  "board": "odroidr1",
  "mac": "001e065e0001",
  "state": "released"
}
```

동일한 release를 반복해도 성공합니다. 해제된 MAC은 이후 request에서
재사용될 수 있습니다.

보드 기록과 readback이 성공했는데 confirm 통신만 실패한 경우에는 MAC을
release하면 안 됩니다. 보드에 이미 기록된 주소가 다른 보드에 재할당될 수
있으므로 동일한 MAC/UUID로 confirm을 다시 시도해야 합니다.

```sh
curl -sS -X POST \
  http://127.0.0.1:8881/api/v1/allocations/001e065e0001/release \
  -H 'Content-Type: application/json' \
  -d '{"board":"r1"}'
```

### 사용 가능 여부 확인

```text
POST /api/v1/allocations/{mac}/availability
```

요청:

```json
{
  "board": "r1"
}
```

응답:

```json
{
  "board": "odroidr1",
  "mac": "001e065e0001",
  "available": false
}
```

아직 사용된 적이 없거나 `released` 상태이면 `true`, `reserved` 또는
`assigned` 상태이면 `false`입니다.

```sh
curl -sS -X POST \
  http://127.0.0.1:8881/api/v1/allocations/001e065e0001/availability \
  -H 'Content-Type: application/json' \
  -d '{"board":"r1"}'
```

### 할당 목록 조회

```text
GET /api/v1/allocations
```

지원하는 query parameter:

| 이름 | 설명 | 기본값 |
|---|---|---|
| `q` | MAC 또는 UUID 부분 검색 | 없음 |
| `board` | canonical 보드명 또는 alias | 모든 보드 |
| `prefix` | 정확히 일치하는 8자리 prefix | 모든 prefix |
| `state` | `reserved`, `assigned`, `released` | 모든 상태 |
| `limit` | 1~500 | 100 |

```sh
curl -sS \
  'http://127.0.0.1:8881/api/v1/allocations?board=r1&state=assigned&limit=100'
```

응답:

```json
{
  "allocations": [
    {
      "mac": "001e065e0001",
      "board": "odroidr1",
      "prefix": "001e065e",
      "suffix": 1,
      "state": "assigned",
      "uuid": "f42c901b-4cec-4a44-9272-001e065e0001",
      "reserved_at": "2026-08-14T03:00:00Z",
      "assigned_at": "2026-08-14T03:01:00Z",
      "updated_at": "2026-08-14T03:01:00Z"
    }
  ]
}
```

현재 목록은 `updated_at` 내림차순이며 최대 500개까지 반환합니다.

### 전체 prefix 통계

```text
GET /api/v1/pools
```

```sh
curl -sS http://127.0.0.1:8881/api/v1/pools
```

응답:

```json
{
  "pools": [
    {
      "board": "odroidr1",
      "prefix": "001e065e",
      "range_start": "001e065e0001",
      "range_end": "001e065effff",
      "total": 65535,
      "free": 65532,
      "reserved": 3,
      "assigned": 0
    }
  ]
}
```

### 단일 prefix 통계

```text
GET /api/v1/pools/{prefix}
```

```sh
curl -sS http://127.0.0.1:8881/api/v1/pools/001e065e
```

응답은 전체 prefix 통계의 단일 항목과 같은 형식입니다.

### API 버전

```text
GET /api/v1/version
```

```json
{
  "api": "v1"
}
```

## 기존 장비 호환 API

`/restapi/*`는 기존 `lib_mac` 동작을 유지하기 위한 API입니다. 신규 프로그램은
이 API 대신 `/api/v1/*`를 사용해야 합니다.

호환 API는 기존 클라이언트 파싱 규칙 때문에 오류가 발생해도 HTTP 200을
반환할 수 있습니다. 오류 여부는 응답의 `error` 필드로 판단해야 합니다.
login을 제외한 응답에는 호환용 `auth` 객체가 포함됩니다.

### 로그인 호환 응답

```text
POST /restapi/login
```

요청의 계정 정보와 인증 헤더는 검사하지 않습니다.

```json
{
  "user": {},
  "expires_in": 315360000,
  "current_time": 1786665600.0,
  "token": "compat"
}
```

### MAC/UUID 예약

```text
POST /restapi/request
```

```json
{
  "board": "m1"
}
```

주요 응답 필드:

```json
{
  "table": "001e064e",
  "mac_num": 1,
  "mac_addr": "001e064e0001",
  "uuid_mac": "f42c901b-4cec-4a44-9272-001e064e0001",
  "auth": {
    "expires_in": 315360000,
    "current_time": 1786665600.0
  }
}
```

### 사용 가능 여부 확인

```text
POST /restapi/verify
```

```json
{
  "board": "m1",
  "mac_addr": "001e064e0001"
}
```

응답의 `isAvailable`이 `true`이면 사용할 수 있습니다.

### 할당 확정

```text
POST /restapi/update
```

```json
{
  "board": "m1",
  "mac_addr": "001e064e0001",
  "uuid": "f42c901b-4cec-4a44-9272-001e064e0001"
}
```

성공 시 `isUpdated`가 `true`입니다.

### 예약 또는 할당 반환

```text
POST /restapi/delete
```

```json
{
  "board": "m1",
  "mac_addr": "001e064e0001"
}
```

성공 시 `isDeleted`가 `true`입니다.

### 호환 API 오류 예

```json
{
  "error": "Not supported board \"unknown\"",
  "auth": {
    "expires_in": 315360000,
    "current_time": 1786665600.0
  }
}
```

## 상태 확인

```text
GET /healthz
```

```json
{
  "status": "ok"
}
```

이 응답은 프로세스의 HTTP 처리 여부를 확인합니다. DB 쓰기나 prefix 잔여량을
검사하는 readiness 응답은 아닙니다.

## 운영 주의사항

- 인증이 없으므로 API listener는 생산 내부망에만 노출해야 합니다.
- Web UI listener는 localhost 또는 접근 통제된 관리망에만 노출해야 합니다.
- 요청 성공 후 보드 기록 결과가 불명확하면 즉시 release하지 말고 할당 상태와
  실제 보드 readback 값을 확인해야 합니다.
- DB 파일은 서버가 관리하므로 외부 프로그램이 직접 수정하면 안 됩니다.
