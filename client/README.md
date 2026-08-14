# MAC 클라이언트 사용법

`libmacclient`는 UUID/MAC 서버에서 주소를 예약하고 확정 또는 반환하기 위한
C 라이브러리입니다. `macctl`은 라이브러리 사용 예제이자 수동 확인용
명령행 유틸리티입니다.

라이브러리는 신규 `/api/v1/*` API를 사용합니다. 기존 장비용 `/restapi/*`
호환 API나 login API는 사용하지 않습니다.

## 빌드

필요 항목:

- C 컴파일러
- GNU Make
- `pkg-config`
- libcurl 개발 파일

저장소 루트에서 빌드:

```sh
make client
```

`client` 디렉터리에서 직접 빌드:

```sh
make             # 라이브러리와 macctl
make library     # 라이브러리만
make example     # macctl과 필요한 정적 라이브러리
make clean
```

결과물:

```text
libmacclient.a    정적 라이브러리
libmacclient.so   공유 라이브러리
macctl            예제 유틸리티
```

실제 생산 프로그램의 권장 흐름은
[`examples/board_provision_pseudo.c`](examples/board_provision_pseudo.c)에
의사 코드로 정리되어 있습니다. 이 파일의 보드 저장 함수는 구현되지 않았으며
`Makefile`의 컴파일 대상이 아닙니다.

## C 라이브러리

헤더:

```c
#include "mac_client.h"
```

일반적인 생산 흐름은 다음과 같습니다.

1. `mac_client_request()`로 MAC/UUID를 예약합니다.
2. 반환된 값을 보드에 기록합니다.
3. 기록 성공 시 `mac_client_confirm()`으로 할당을 확정합니다.
4. 기록 실패 시 `mac_client_release()`로 예약을 반환합니다.

간단한 사용 예:

```c
struct mac_client client;
struct mac_client_result result;
int ret;

ret = mac_client_global_init();
if (ret != MAC_CLIENT_OK)
    return 1;

ret = mac_client_init(&client, "http://127.0.0.1:8881", 5000);
if (ret == MAC_CLIENT_OK)
    ret = mac_client_request(&client, "r1", &result);

if (ret == MAC_CLIENT_OK) {
    printf("MAC: %s\n", result.mac);
    printf("UUID: %s\n", result.uuid);

    /* 보드 기록 성공 시 */
    ret = mac_client_confirm(&client, "r1", &result);

    /* 보드 기록 실패 시에는 confirm 대신 다음을 호출 */
    /* ret = mac_client_release(&client, "r1", result.mac); */
}

if (ret != MAC_CLIENT_OK)
    fprintf(stderr, "%s\n", mac_client_error(&client));

mac_client_global_cleanup();
```

주요 API:

- `mac_client_global_init()`: 프로그램 시작 시 한 번 호출
- `mac_client_init()`: 서버 URL과 timeout 설정
- `mac_client_request()`: MAC/UUID 예약
- `mac_client_confirm()`: 예약된 MAC/UUID 할당 확정
- `mac_client_release()`: 예약 또는 할당된 MAC 반환
- `mac_client_is_available()`: 특정 MAC의 사용 가능 여부 확인
- `mac_client_error()`: 마지막 오류 문자열 반환
- `mac_client_global_cleanup()`: 프로그램 종료 전 호출

반환값은 성공 시 `MAC_CLIENT_OK(0)`, 실패 시 음수입니다.

`mac_client_request()`는 UUID 형식의 request ID를 내부에서 생성합니다.
네트워크 응답이 유실되면 같은 request ID로 최대 3회 재시도하므로 서버는
새 MAC을 추가로 할당하지 않고 동일한 예약을 반환합니다. 사용자는 request
ID를 생성하거나 함수에 전달할 필요가 없습니다.

호출이 네트워크 오류로 끝난 경우 request ID는 `struct mac_client`에 남아
다음 `mac_client_request()` 호출에서 다시 사용됩니다. 해당 요청을 완전히
포기하고 다른 작업을 시작하려면 `mac_client_init()`으로 객체를 다시
초기화할 수 있습니다.

직접 링크하는 예:

```sh
cc -Iinclude app.c libmacclient.a $(pkg-config --libs libcurl) -o app
```

## macctl

기본 형식:

```text
macctl -s SERVER COMMAND BOARD [MAC] [UUID]
```

MAC/UUID 예약:

```sh
./macctl -s http://127.0.0.1:8881 request r1
```

출력 예:

```text
mac=001e065e0001
uuid=f42c901b-4cec-4a44-9272-001e065e0001
```

할당 확정은 `request`에서 받은 MAC과 UUID를 그대로 사용합니다.

```sh
./macctl -s http://127.0.0.1:8881 confirm r1 \
    001e065e0001 f42c901b-4cec-4a44-9272-001e065e0001
```

예약 또는 할당 반환:

```sh
./macctl -s http://127.0.0.1:8881 release r1 001e065e0001
```

사용 가능 여부 확인:

```sh
./macctl -s http://127.0.0.1:8881 available r1 001e065e0001
```

```text
available=true
```

`request`만 반복하면 주소는 `reserved` 상태로 계속 남습니다. 실제 생산
프로그램에서는 반드시 기록 결과에 따라 `confirm` 또는 `release`를 호출해야
합니다.
