# UUID / MAC 서버

MAC prefix는 TOML 파일로 관리하며, SQLite 스키마 생성, migration, 할당 상태,
설정 동기화와 백업은 서버가 처리합니다. 관리자가 SQLite DB를 직접 수정할
필요는 없습니다.

각 prefix에서 할당 가능한 suffix는 `0001`부터 `ffff`까지로 고정됩니다.

## 설정

개발용 예제 설정은 [configs/mac-server.example.toml](configs/mac-server.example.toml)입니다.
DB와 백업 경로가 각각 `./data/mac-server.db`, `./data/backups`로 지정되어
있으므로 저장소 루트에서 일반 사용자 권한으로 실행할 수 있습니다. 필요한
디렉터리와 DB 파일은 서버가 자동 생성합니다.

보드와 MAC 대역은 다음과 같이 설정합니다.

```toml
[boards.odroidr1]
aliases = ["r1", "odroid-r1"]
prefixes = ["001e065e", "001e065f"]
```

`prefixes` 배열 순서가 할당 우선순위입니다. 위 설정에서는
`001e065e0001`부터 `001e065effff`까지 사용한 후 다음 prefix로 넘어갑니다.

prefix 하나는 65,535개 주소를 나타냅니다.

```text
001e065e0001 ... 001e065effff
```

설정에 prefix를 추가하면 소스 코드를 수정하지 않고 새 대역을 활성화할 수
있습니다. prefix를 제거하면 해당 대역의 신규 할당만 중지하며 기존 할당 및
감사 기록은 삭제하지 않습니다. 이미 DB에 등록된 prefix를 다른 보드로
이동하는 설정은 거부합니다.

운영 서비스에서는 서비스 계정이 소유한 디렉터리에 절대 경로를 지정할 수
있습니다.

```toml
[database]
path = "/var/lib/mac-server/mac-server.db"
backup_dir = "/var/lib/mac-server/backups"
```

## 빌드 및 실행

서버 빌드:

```sh
go mod download
go mod tidy
go build -o mac-server ./cmd/mac-server
```

개발용 설정으로 실행:

```sh
./mac-server -config ./configs/mac-server.example.toml
```

예제 설정의 기본 주소:

```text
API:    http://127.0.0.1:8881
Web UI: http://127.0.0.1:8082/web/
```

## 기존 장비 호환 API

기존 `lib_mac` 호환을 위해 다음 경로를 제공합니다.

- `POST /restapi/login`
- `POST /restapi/request`
- `POST /restapi/verify`
- `POST /restapi/update`
- `POST /restapi/delete`

실제 인증은 사용하지 않습니다. `/restapi/login`은 기존 클라이언트를 위한
dummy 응답을 반환하며, `Authorization` 헤더는 전달되어도 무시합니다. 토큰은
DB에 저장하지 않습니다. 그 외 응답에도 기존 클라이언트 파싱을 위해
호환용 `auth` 객체를 포함합니다.

이 경로들은 기존 장비 전용입니다. 신규 `libmacclient`와 `macctl`은 아래의
`/api/v1/*`를 사용합니다.

## 신규 API v1

신규 클라이언트용 API는 다음 경로를 제공합니다.

- `POST /api/v1/allocations`: MAC/UUID 예약
- `POST /api/v1/allocations/{mac}/confirm`: 할당 확정
- `POST /api/v1/allocations/{mac}/release`: 예약 또는 할당 반환
- `POST /api/v1/allocations/{mac}/availability`: 사용 가능 여부 확인
- `GET /api/v1/allocations`: 할당 검색
- `GET /api/v1/pools`: 전체 prefix 통계
- `GET /api/v1/pools/{prefix}`: 단일 prefix 통계
- `GET /api/v1/version`: API 버전

v1 API는 HTTP 상태 코드와 `error.code`, `error.message`를 사용합니다. 예약
요청에는 UUID 형식의 `request_id`가 필요합니다. 같은 `request_id`를 다시
보내면 새 MAC을 소비하지 않고 기존 예약 결과를 반환합니다.

`libmacclient`는 request ID를 내부에서 생성하므로 일반 사용자가 직접 관리할
필요는 없습니다. confirm과 release도 같은 요청을 반복해도 성공하도록
멱등 처리합니다.

할당 상태 전환은 다음과 같습니다.

```text
request: free/released          -> reserved
update:  reserved               -> assigned
delete:  reserved 또는 assigned -> released
```

MAC 할당은 SQLite `BEGIN IMMEDIATE` transaction으로 직렬화합니다. 반환된
주소는 새 suffix를 사용하기 전에 우선 재사용합니다.

## Web UI

Web UI는 `/web/`에서 제공합니다. `web.listen`이 비어 있으면 API listener를
공유하고, 값이 있으면 별도 주소에서 실행합니다. 예제 설정은 인증 없는 관리
화면이 외부에 노출되지 않도록 localhost에 bind합니다.

대시보드는 활성화된 MAC prefix마다 다음 통계를 표시합니다.

- 보드 이름
- MAC prefix
- 전체 `0001-ffff` 범위
- Total
- Free
- Reserved
- Assigned

prefix를 선택하면 해당 대역 상세 화면으로 이동합니다. 상세 화면에서는
MAC/UUID 검색, 상태 필터와 수동 반환을 사용할 수 있습니다.
`/web/allocations`에서는 모든 prefix를 대상으로 보드, prefix, MAC/UUID와
상태를 함께 검색할 수 있습니다.

`/web/config`에서 서버가 현재 사용 중인 `-config` TOML 파일을 편집할 수
있습니다. 새 설정은 전체 parsing과 검증을 통과한 뒤 임시 파일과 rename을
이용해 원자적으로 저장합니다. 보드와 prefix 변경은 SQLite에도 즉시
반영하며, DB 동기화가 실패하면 이전 TOML 파일을 복구합니다.

listen 주소, Web listen 주소, DB 경로와 SQLite busy timeout은 프로세스
재시작이 필요한 값이므로 Web 편집기에서 실행 중 변경할 수 없습니다. Web
저장을 사용하려면 서버 계정에 설정 파일과 상위 디렉터리 쓰기 권한이 있어야
합니다.

HTML 템플릿과 CSS는 다음 경로에서 별도로 관리합니다.

```text
internal/webui/templates/
internal/webui/static/
```

파일은 빌드 시 실행 파일에 포함되므로 운영 환경에 별도 Web asset 디렉터리를
배포할 필요는 없습니다.

## C 클라이언트와 macctl

`client/`에는 libcurl 기반 C 라이브러리와 `macctl` 예제가 있습니다. 신규
클라이언트는 `/api/v1/*`만 사용하며 호환용 login API를 호출하지 않습니다.
예약 요청 중 응답이 유실되면 라이브러리가 동일한 내부 request ID를 사용해
최대 3회 재시도하므로 중복 MAC 할당을 방지할 수 있습니다.

일반적인 생산 흐름:

```text
mac_client_request()
  -> 보드에 MAC/UUID 기록
  -> 성공 시 mac_client_confirm()
  -> 실패 시 mac_client_release()
```

저장소 루트에서 클라이언트 빌드:

```sh
make
```

또는 다음 타깃을 사용할 수 있습니다.

```sh
make client
make -C client library
make -C client example
```

결과물:

```text
client/libmacclient.a
client/libmacclient.so
client/macctl
```

간단한 `macctl` 실행 예:

```sh
./client/macctl -s http://127.0.0.1:8881 request r1
./client/macctl -s http://127.0.0.1:8881 confirm r1 001e065e0001 12345678-1234-4234-8234-001e065e0001
./client/macctl -s http://127.0.0.1:8881 release r1 001e065e0001
./client/macctl -s http://127.0.0.1:8881 available r1 001e065e0001
```

C 라이브러리와 `macctl`의 자세한 사용법은
[client/README.md](client/README.md)를 참고하십시오.

## 운영 주의사항

- 같은 SQLite 파일을 사용하는 서버 프로세스를 두 개 이상 실행하지 마십시오.
- SQLite 파일을 NFS 등 네트워크 파일시스템에 두지 마십시오.
- `request` 성공 후에는 반드시 작업 결과에 따라 `confirm` 또는 `release`를
  호출하십시오.
- 인증 없는 Web UI는 localhost 또는 접근이 통제된 내부 관리망에만
  노출하십시오.
