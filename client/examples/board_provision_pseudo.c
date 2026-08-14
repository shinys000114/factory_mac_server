/*
 * UUID/MAC 생산 기록 의사 코드
 *
 * 이 파일은 실제 하드웨어 접근 함수를 구현하지 않은 참고 자료이며
 * client/Makefile의 컴파일 대상이 아니다. board_* 함수는 사용하는 보드의
 * EEPROM, OTP, U-Boot 환경 또는 별도 설정 저장 방식에 맞게 구현해야 한다.
 */

#include "mac_client.h"

#include <stdio.h>
#include <string.h>

/* 아래 함수들은 제품별 코드에서 구현한다. */
int board_write_identity(const char *mac, const char *uuid);
int board_read_identity(char *mac, size_t mac_size,
			char *uuid, size_t uuid_size);
int board_erase_and_verify_identity(void);
void save_for_manual_review(const char *board,
			    const struct mac_client_result *result);

/*
 * 반환값 예:
 *   0  생산 기록 및 서버 confirm 완료
 *  -1  MAC 서버 요청 실패
 *  -2  보드 기록/검증 실패 후 예약 반환 완료
 *  -3  보드 정리 또는 서버 release가 완료되지 않아 수동 확인 필요
 *  -4  보드 기록은 성공했으나 서버 confirm 실패, 수동 confirm 필요
 */
int provision_board_identity(const char *server_url, const char *board)
{
	struct mac_client_result result;
	struct mac_client client;
	char read_mac[MAC_CLIENT_MAC_SIZE + 1];
	char read_uuid[MAC_CLIENT_UUID_SIZE + 1];
	int global_initialized = 0;
	int status;
	int ret = -1;

	status = mac_client_global_init();
	if (status != MAC_CLIENT_OK) {
		fprintf(stderr, "libcurl global initialization failed\n");
		return -1;
	}
	global_initialized = 1;

	status = mac_client_init(&client, server_url, 5000);
	if (status != MAC_CLIENT_OK) {
		fprintf(stderr, "client initialization failed: %s\n",
			mac_client_error(&client));
		goto out;
	}

	/* 서버 DB 상태: free/released -> reserved */
	status = mac_client_request(&client, board, &result);
	if (status != MAC_CLIENT_OK) {
		fprintf(stderr, "MAC/UUID request failed: %s\n",
			mac_client_error(&client));
		goto out;
	}

	printf("reserved MAC=%s UUID=%s\n", result.mac, result.uuid);

	/* 실제 보드의 비휘발성 저장 영역에 MAC과 UUID를 기록한다. */
	if (board_write_identity(result.mac, result.uuid) != 0)
		goto board_record_failed;

	/* 기록한 값을 다시 읽어 원본과 정확히 일치하는지 검증한다. */
	memset(read_mac, 0, sizeof(read_mac));
	memset(read_uuid, 0, sizeof(read_uuid));
	if (board_read_identity(read_mac, sizeof(read_mac),
				read_uuid, sizeof(read_uuid)) != 0 ||
	    strcmp(read_mac, result.mac) != 0 ||
	    strcmp(read_uuid, result.uuid) != 0)
		goto board_record_failed;

	/* 서버 DB 상태: reserved -> assigned */
	status = mac_client_confirm(&client, board, &result);
	if (status != MAC_CLIENT_OK) {
		/*
		 * 보드에는 이미 MAC/UUID가 정상 기록되어 있다. 여기서 release하면
		 * 같은 MAC이 다른 보드에 재할당될 수 있으므로 절대 release하지
		 * 않는다. MAC/UUID를 보존한 뒤 같은 값으로 confirm을 재시도하거나
		 * 관리자가 Web UI에서 상태를 확인한다.
		 */
		fprintf(stderr, "confirm failed: %s\n",
			mac_client_error(&client));
		save_for_manual_review(board, &result);
		ret = -4;
		goto out;
	}

	printf("assigned MAC=%s UUID=%s\n", result.mac, result.uuid);
	ret = 0;
	goto out;

board_record_failed:
	/*
	 * write가 오류를 반환했더라도 일부 데이터가 기록됐을 수 있다.
	 * 보드에서 MAC/UUID가 완전히 제거됐음을 검증한 경우에만 서버 예약을
	 * release한다.
	 */
	if (board_erase_and_verify_identity() != 0) {
		fprintf(stderr,
			"board cleanup failed; keep reservation for manual review\n");
		save_for_manual_review(board, &result);
		ret = -3;
		goto out;
	}

	/* 서버 DB 상태: reserved -> released */
	status = mac_client_release(&client, board, result.mac);
	if (status != MAC_CLIENT_OK) {
		fprintf(stderr, "release failed: %s\n",
			mac_client_error(&client));
		save_for_manual_review(board, &result);
		ret = -3;
		goto out;
	}
	ret = -2;

out:
	if (global_initialized)
		mac_client_global_cleanup();
	return ret;
}
