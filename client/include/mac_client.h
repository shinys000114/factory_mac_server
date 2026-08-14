#ifndef MAC_CLIENT_H
#define MAC_CLIENT_H

/**
 * @file mac_client.h
 * @brief UUID/MAC 할당 서버용 C 클라이언트 API
 *
 * 일반적인 사용 순서는 다음과 같다.
 *
 * @code{.c}
 * #include <stdio.h>
 * #include "mac_client.h"
 *
 * struct mac_client client;
 * struct mac_client_result result;
 * int ret;
 *
 * ret = mac_client_global_init();
 * if (ret != MAC_CLIENT_OK)
 *     return 1;
 *
 * ret = mac_client_init(&client, "http://127.0.0.1:8881", 5000);
 * if (ret == MAC_CLIENT_OK)
 *     ret = mac_client_request(&client, "r1", &result);
 *
 * if (ret == MAC_CLIENT_OK) {
 *     // result.mac과 result.uuid를 보드에 기록한다.
 *     if (board_write_succeeded)
 *         ret = mac_client_confirm(&client, "r1", &result);
 *     else
 *         ret = mac_client_release(&client, "r1", result.mac);
 * }
 *
 * if (ret != MAC_CLIENT_OK)
 *     fprintf(stderr, "%s\n", mac_client_error(&client));
 *
 * mac_client_global_cleanup();
 * @endcode
 *
 * 위 예제의 board_write_succeeded는 사용자가 보드 기록 결과에 따라 설정하는
 * 응용 프로그램의 조건값이다.
 *
 * mac_client_request()는 주소를 예약만 한다. 보드 기록 성공 시에는 반드시
 * mac_client_confirm()을 호출하고, 실패 시에는 mac_client_release()를 호출해
 * 예약을 반환해야 한다.
 */

#ifdef __cplusplus
extern "C" {
#endif

#include <stddef.h>

/** 구분자와 문자열 종료 문자를 제외한 MAC 주소 길이이다. */
#define MAC_CLIENT_MAC_SIZE  12
/** 문자열 종료 문자를 제외한 하이픈 포함 UUID 길이이다. */
#define MAC_CLIENT_UUID_SIZE 36

/** 모든 공개 함수가 반환하는 상태 코드이다. */
enum mac_client_status {
	/** 요청이 성공했다. */
	MAC_CLIENT_OK = 0,
	/** NULL 포인터, 잘못된 보드명, MAC 또는 UUID 등 인자가 잘못됐다. */
	MAC_CLIENT_ERROR_ARGUMENT = -1,
	/** 서버 연결, 전송 또는 libcurl 초기화에 실패했다. */
	MAC_CLIENT_ERROR_NETWORK = -2,
	/** 서버 응답에 필수 필드가 없거나 값의 형식이 잘못됐다. */
	MAC_CLIENT_ERROR_PROTOCOL = -3,
	/** 서버가 HTTP 오류 또는 JSON의 error 필드를 반환했다. */
	MAC_CLIENT_ERROR_SERVER = -4,
};

/**
 * 서버 연결 설정과 마지막 오류를 보관하는 클라이언트 객체이다.
 *
 * 사용자가 필드를 직접 설정하지 말고 mac_client_init()으로 초기화한다.
 * 하나의 객체를 동시에 여러 스레드에서 사용하지 않는다.
 */
struct mac_client {
	/** 경로 끝의 '/'를 제거한 서버 기본 URL이다. */
	char server_url[256];
	/** 연결과 요청에 공통으로 적용되는 제한 시간(ms)이다. */
	long timeout_ms;
	/** 가장 최근 실패 원인을 담는 내부 버퍼이다. */
	char error[256];
};

/** 서버가 예약한 MAC 주소와 UUID 한 쌍이다. */
struct mac_client_result {
	/** 콜론 없는 12자리 16진수 MAC 문자열이다. */
	char mac[MAC_CLIENT_MAC_SIZE + 1];
	/** 마지막 12자리가 mac과 같은 36자리 UUID 문자열이다. */
	char uuid[MAC_CLIENT_UUID_SIZE + 1];
};

/**
 * libcurl 전역 상태를 초기화한다.
 *
 * 프로그램 시작 시, 다른 mac_client 함수보다 먼저 한 번 호출한다.
 * 멀티스레드 프로그램은 스레드를 만들기 전에 호출하는 것이 안전하다.
 *
 * @return MAC_CLIENT_OK 또는 MAC_CLIENT_ERROR_NETWORK
 */
int mac_client_global_init(void);

/**
 * libcurl 전역 상태를 정리한다.
 *
 * 모든 클라이언트 사용이 끝난 뒤 프로그램 종료 전에 한 번 호출한다.
 */
void mac_client_global_cleanup(void);

/**
 * 클라이언트 객체에 서버 주소와 제한 시간을 설정한다.
 *
 * @param client 초기화할 객체
 * @param server_url "http://host:port" 형식의 서버 URL
 * @param timeout_ms 제한 시간(ms). 0 이하이면 기본값 5000ms를 사용한다.
 * @return MAC_CLIENT_OK 또는 MAC_CLIENT_ERROR_ARGUMENT
 */
int mac_client_init(struct mac_client *client, const char *server_url,
		    long timeout_ms);

/**
 * 가장 최근 오류 메시지를 반환한다.
 *
 * 반환된 포인터는 해제하거나 수정하지 않는다. 다음 API 호출 시 내용이
 * 바뀔 수 있으므로 필요한 경우 사용자가 즉시 복사해야 한다.
 *
 * @param client 오류를 확인할 클라이언트 객체
 * @return 내부 오류 문자열. client가 NULL이면 "invalid client"
 */
const char *mac_client_error(const struct mac_client *client);

/**
 * 보드용 MAC 주소와 UUID를 서버에서 예약한다.
 *
 * board에는 서버 TOML 설정의 보드명 또는 alias(예: "r1")를 전달한다.
 * 성공한 result는 보드 기록 후 mac_client_confirm()에 그대로 전달한다.
 * 기록하지 못했다면 mac_client_release()로 반드시 예약을 반환한다.
 *
 * @param client mac_client_init()을 마친 객체
 * @param board 서버에 설정된 보드명 또는 alias
 * @param result 성공 시 예약된 MAC/UUID를 받을 객체
 * @return enum mac_client_status 값
 */
int mac_client_request(struct mac_client *client, const char *board,
		       struct mac_client_result *result);

/**
 * 예약된 MAC/UUID를 최종 할당 상태로 확정한다.
 *
 * request에서 반환된 board와 result를 변경하지 않고 전달해야 한다.
 * 이미 확정했거나 반환한 주소, 서로 일치하지 않는 MAC/UUID는 실패한다.
 *
 * @param client 초기화된 클라이언트 객체
 * @param board request에 사용한 보드명 또는 alias
 * @param result request에서 받은 MAC/UUID 한 쌍
 * @return enum mac_client_status 값
 */
int mac_client_confirm(struct mac_client *client, const char *board,
		       const struct mac_client_result *result);

/**
 * 예약 또는 할당된 MAC 주소를 서버의 가용 풀로 반환한다.
 *
 * 생산 중 보드 기록에 실패했을 때 예약을 취소하거나, 관리 목적으로 기존
 * 할당을 해제할 때 사용한다. 해제된 주소는 이후 요청에서 재사용될 수 있다.
 *
 * @param client 초기화된 클라이언트 객체
 * @param board 해당 MAC이 속한 보드명 또는 alias
 * @param mac 콜론 없는 12자리 16진수 MAC 문자열
 * @return enum mac_client_status 값
 */
int mac_client_release(struct mac_client *client, const char *board,
		       const char *mac);

/**
 * 특정 MAC 주소를 새로 할당할 수 있는지 확인한다.
 *
 * 아직 사용된 적이 없거나 released 상태이면 available에 1을 저장한다.
 * reserved 또는 assigned 상태이면 0을 저장한다. 함수 자체의 성공 여부는
 * 반환값으로 판단하고, MAC의 가용 여부는 available로 판단해야 한다.
 *
 * @param client 초기화된 클라이언트 객체
 * @param board 해당 MAC이 속한 보드명 또는 alias
 * @param mac 콜론 없는 12자리 16진수 MAC 문자열
 * @param available 성공 시 사용 가능하면 1, 아니면 0
 * @return enum mac_client_status 값
 */
int mac_client_is_available(struct mac_client *client, const char *board,
			    const char *mac, int *available);

#ifdef __cplusplus
}
#endif

#endif
