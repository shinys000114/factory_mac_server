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

```sh
sudo apt install libcurl4-openssl-dev pkg-config build-essential

git clone https://github.com/shinys000114/factory_mac_server.git
cd factory_mac_server/client
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

ODROID-R1의 `/sys/class/efuse/` 인터페이스를 사용하는 구체적인 예제는
[`examples/borad_provision_r1.c`](examples/borad_provision_r1.c)에 있습니다.
요청한 파일명을 따라 `borad`로 작성했으며 이 파일도 기본 `Makefile`의
컴파일 대상에는 포함하지 않았습니다.

`client` 디렉터리에서 라이브러리를 먼저 빌드한 뒤 R1 예제만 수동으로
컴파일할 수 있습니다.

```sh
make library

cc -O2 -g -Wall -Wextra -Werror -Iinclude \
  $(pkg-config --cflags libcurl) \
  examples/borad_provision_r1.c libmacclient.a \
  $(pkg-config --libs libcurl) \
  -o borad_provision_r1
```

R1 대상 장치에서 실행하는 형식은 다음과 같습니다.

```sh
sudo ./borad_provision_r1 -s http://MAC_SERVER_ADDRESS:8881
```

이 프로그램은 `/sys/class/efuse/uuid`에 UUID를 기록하며 실패 복구 과정에서
`/sys/class/efuse/clear`를 사용할 수 있습니다. 반드시 R1 생산 대상 장치와
올바른 서버 주소를 확인한 후 실행해야 합니다.

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

빌드한 정적 라이브러리를 링크하는 예:

```sh
cc -Iinclude app.c libmacclient.a $(pkg-config --libs libcurl) -o app
```

### 소스 파일을 프로젝트에 직접 추가하는 경우

`src/mac_client.c`와 `include/mac_client.h`를 프로젝트에 복사하여
응용 프로그램과 함께 컴파일할 수도 있습니다. 이 경우 `libmacclient.a`나
`libmacclient.so`를 별도로 빌드할 필요는 없지만, libcurl 개발 파일과
`pkg-config`는 위의 빌드 방법과 동일하게 필요합니다.

다음처럼 세 파일을 같은 디렉터리에 두고 `app.c`에서
`#include "mac_client.h"`를 사용한다고 가정합니다.

```text
my_project/
├── app.c
├── mac_client.c
└── mac_client.h
```

프로젝트 디렉터리에서 다음 명령으로 컴파일합니다.

```sh
cc -O2 -g -Wall -Wextra \
  $(pkg-config --cflags libcurl) \
  app.c mac_client.c \
  $(pkg-config --libs libcurl) \
  -o app
```

Makefile을 사용하는 경우에는 다음처럼 작성할 수 있습니다. 명령행 앞의
들여쓰기는 탭 문자입니다.

```makefile
CC ?= cc
CFLAGS ?= -O2 -g
CFLAGS += -Wall -Wextra
CPPFLAGS += $(shell pkg-config --cflags libcurl)
LDLIBS += $(shell pkg-config --libs libcurl)

app: app.o mac_client.o
	$(CC) $(LDFLAGS) -o $@ $^ $(LDLIBS)

app.o mac_client.o: mac_client.h
```

위 Makefile은 기본 규칙으로 각 `.c` 파일을 `.o` 파일로 컴파일하며,
`make`를 실행하면 `app`이 생성됩니다. 기존 프로젝트에 통합할 때도
`mac_client.c`를 컴파일 대상에 포함하고 libcurl 링크 옵션을 소스나
오브젝트 파일 뒤에 지정합니다. 헤더를 별도 디렉터리에 두었다면
`-I헤더_디렉터리`도 컴파일 옵션에 추가합니다.

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
