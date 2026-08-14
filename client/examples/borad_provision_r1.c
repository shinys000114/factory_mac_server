/*
 * ODROID-R1 UUID/MAC 생산 기록 예제
 *
 * 실행 예:
 *   sudo ./borad_provision_r1 -s http://127.0.0.1:8881
 *
 * 프로그램 내부에서 sudo를 실행하지 않는다. root 권한으로 실행한 프로세스가
 * /sys/class/efuse/{uuid,mac,clear}를 직접 읽고 쓴다.
 *
 * 주의: efuse/clear는 ODROID efuse 드라이버가 선택한 SPI NOR user OTP
 * region을 erase한다. 기록 실패 복구 외의 목적으로 호출하면 안 된다.
 *
 * 이 파일은 client/Makefile의 기본 컴파일 대상에 포함되지 않는다.
 */

#include "mac_client.h"

#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define EFUSE_UUID_PATH  "/sys/class/efuse/uuid"
#define EFUSE_MAC_PATH   "/sys/class/efuse/mac"
#define EFUSE_CLEAR_PATH "/sys/class/efuse/clear"

#define R1_BOARD "r1"
#define DEFAULT_TIMEOUT_MS 5000

#define UNPROVISIONED_UUID "ffffffff-ffff-ffff-ffff-ffffffffffff"
#define UNPROVISIONED_MAC  "ffffffffffff"

static int read_text_file(const char *path, char *buffer, size_t buffer_size)
{
	ssize_t count;
	size_t used = 0;
	int fd;

	if (buffer == NULL || buffer_size < 2) {
		errno = EINVAL;
		return -1;
	}

	fd = open(path, O_RDONLY | O_CLOEXEC);
	if (fd < 0)
		return -1;

	while (used + 1 < buffer_size) {
		count = read(fd, buffer + used, buffer_size - used - 1);
		if (count < 0) {
			if (errno == EINTR)
				continue;
			close(fd);
			return -1;
		}
		if (count == 0)
			break;
		used += (size_t)count;
	}
	if (close(fd) != 0)
		return -1;

	buffer[used] = '\0';
	while (used > 0 && isspace((unsigned char)buffer[used - 1]))
		buffer[--used] = '\0';
	if (used == 0) {
		errno = EIO;
		return -1;
	}
	return 0;
}

static int write_text_file(const char *path, const char *value)
{
	char buffer[MAC_CLIENT_UUID_SIZE + 2];
	ssize_t count;
	int length;
	int fd;

	length = snprintf(buffer, sizeof(buffer), "%s\n", value);
	if (length < 0 || (size_t)length >= sizeof(buffer)) {
		errno = EINVAL;
		return -1;
	}

	fd = open(path, O_WRONLY | O_CLOEXEC);
	if (fd < 0)
		return -1;
	do {
		count = write(fd, buffer, (size_t)length);
	} while (count < 0 && errno == EINTR);
	if (count != length) {
		if (count >= 0)
			errno = EIO;
		close(fd);
		return -1;
	}
	if (close(fd) != 0)
		return -1;
	return 0;
}

static int normalize_uuid(const char *input,
			  char output[MAC_CLIENT_UUID_SIZE + 1])
{
	size_t i;

	if (strlen(input) != MAC_CLIENT_UUID_SIZE)
		return -1;
	for (i = 0; i < MAC_CLIENT_UUID_SIZE; ++i) {
		if (i == 8 || i == 13 || i == 18 || i == 23) {
			if (input[i] != '-')
				return -1;
			output[i] = '-';
		} else {
			if (!isxdigit((unsigned char)input[i]))
				return -1;
			output[i] = (char)tolower((unsigned char)input[i]);
		}
	}
	output[MAC_CLIENT_UUID_SIZE] = '\0';
	return 0;
}

static int normalize_mac(const char *input,
			 char output[MAC_CLIENT_MAC_SIZE + 1])
{
	size_t input_index;
	size_t output_index = 0;

	for (input_index = 0; input[input_index] != '\0'; ++input_index) {
		unsigned char value = (unsigned char)input[input_index];

		if (value == ':' || value == '-')
			continue;
		if (!isxdigit(value) || output_index >= MAC_CLIENT_MAC_SIZE)
			return -1;
		output[output_index++] = (char)tolower(value);
	}
	if (output_index != MAC_CLIENT_MAC_SIZE)
		return -1;
	output[output_index] = '\0';
	return 0;
}

static int read_efuse_identity(char uuid[MAC_CLIENT_UUID_SIZE + 1],
			       char mac[MAC_CLIENT_MAC_SIZE + 1])
{
	char raw_uuid[128];
	char raw_mac[128];

	if (read_text_file(EFUSE_UUID_PATH, raw_uuid, sizeof(raw_uuid)) != 0) {
		perror("read " EFUSE_UUID_PATH);
		return -1;
	}
	if (read_text_file(EFUSE_MAC_PATH, raw_mac, sizeof(raw_mac)) != 0) {
		perror("read " EFUSE_MAC_PATH);
		return -1;
	}
	if (normalize_uuid(raw_uuid, uuid) != 0) {
		fprintf(stderr, "invalid UUID readback: %s\n", raw_uuid);
		return -1;
	}
	if (normalize_mac(raw_mac, mac) != 0) {
		fprintf(stderr, "invalid MAC readback: %s\n", raw_mac);
		return -1;
	}
	return 0;
}

static int efuse_is_unprovisioned(void)
{
	char uuid[MAC_CLIENT_UUID_SIZE + 1];
	char mac[MAC_CLIENT_MAC_SIZE + 1];

	if (read_efuse_identity(uuid, mac) != 0)
		return -1;
	if (strcmp(uuid, UNPROVISIONED_UUID) != 0 ||
	    strcmp(mac, UNPROVISIONED_MAC) != 0) {
		fprintf(stderr, "efuse is already provisioned: UUID=%s MAC=%s\n",
			uuid, mac);
		return 0;
	}
	return 1;
}

static int clear_and_verify_efuse(void)
{
	char uuid[MAC_CLIENT_UUID_SIZE + 1];
	char mac[MAC_CLIENT_MAC_SIZE + 1];

	if (write_text_file(EFUSE_CLEAR_PATH, "c") != 0) {
		perror("write " EFUSE_CLEAR_PATH);
		return -1;
	}
	if (read_efuse_identity(uuid, mac) != 0)
		return -1;
	if (strcmp(uuid, UNPROVISIONED_UUID) != 0 ||
	    strcmp(mac, UNPROVISIONED_MAC) != 0) {
		fprintf(stderr, "efuse clear verification failed: UUID=%s MAC=%s\n",
			uuid, mac);
		return -1;
	}
	return 0;
}

static void print_manual_action(const char *server_url,
				const struct mac_client_result *result,
				const char *command)
{
	fprintf(stderr, "manual action required:\n");
	fprintf(stderr, "  ./macctl -s %s %s %s %s",
		server_url, command, R1_BOARD, result->mac);
	if (strcmp(command, "confirm") == 0)
		fprintf(stderr, " %s", result->uuid);
	fputc('\n', stderr);
}

static void print_manual_review(const struct mac_client_result *result)
{
	fprintf(stderr,
		"manual review required; do not confirm or release automatically:\n"
		"  reserved MAC=%s UUID=%s\n",
		result->mac, result->uuid);
}

static int provision_r1(const char *server_url)
{
	struct mac_client_result result;
	struct mac_client client;
	char read_uuid[MAC_CLIENT_UUID_SIZE + 1];
	char read_mac[MAC_CLIENT_MAC_SIZE + 1];
	int status;
	int ret = EXIT_FAILURE;

	status = efuse_is_unprovisioned();
	if (status <= 0)
		return EXIT_FAILURE;

	status = mac_client_global_init();
	if (status != MAC_CLIENT_OK) {
		fprintf(stderr, "failed to initialize libcurl\n");
		return EXIT_FAILURE;
	}

	status = mac_client_init(&client, server_url, DEFAULT_TIMEOUT_MS);
	if (status != MAC_CLIENT_OK) {
		fprintf(stderr, "client initialization failed: %s\n",
			mac_client_error(&client));
		goto out;
	}

	/* 서버 DB: free/released -> reserved */
	status = mac_client_request(&client, R1_BOARD, &result);
	if (status != MAC_CLIENT_OK) {
		fprintf(stderr, "MAC/UUID request failed: %s\n",
			mac_client_error(&client));
		goto out;
	}
	printf("reserved MAC=%s UUID=%s\n", result.mac, result.uuid);

	/* UUID 기록 시 드라이버도 내부 readback 검증을 수행한다. */
	if (write_text_file(EFUSE_UUID_PATH, result.uuid) != 0) {
		perror("write " EFUSE_UUID_PATH);
		goto record_failed;
	}

	/* UUID와 UUID에서 파생된 MAC을 사용자 공간에서도 다시 검증한다. */
	memset(read_uuid, 0, sizeof(read_uuid));
	memset(read_mac, 0, sizeof(read_mac));
	if (read_efuse_identity(read_uuid, read_mac) != 0) {
		fprintf(stderr, "efuse readback failed\n");
		goto record_failed;
	}
	if (strcmp(read_uuid, result.uuid) != 0 ||
	    strcmp(read_mac, result.mac) != 0) {
		fprintf(stderr,
			"efuse readback mismatch: expected UUID=%s MAC=%s, "
			"read UUID=%s MAC=%s\n",
			result.uuid, result.mac, read_uuid, read_mac);
		goto record_failed;
	}

	/* 서버 DB: reserved -> assigned */
	status = mac_client_confirm(&client, R1_BOARD, &result);
	if (status != MAC_CLIENT_OK) {
		/*
		 * OTP에는 이미 올바른 값이 기록됐다. release하면 같은 MAC이 다른
		 * 보드에 재할당될 수 있으므로 UUID를 지우거나 release하지 않는다.
		 */
		fprintf(stderr, "confirm failed: %s\n",
			mac_client_error(&client));
		print_manual_action(server_url, &result, "confirm");
		goto out;
	}

	printf("assigned MAC=%s UUID=%s\n", result.mac, result.uuid);
	ret = EXIT_SUCCESS;
	goto out;

record_failed:
	/* clear와 readback이 모두 성공한 경우에만 서버 예약을 반환한다. */
	if (clear_and_verify_efuse() != 0) {
		fprintf(stderr,
			"efuse cleanup failed; keep server reservation\n");
		print_manual_review(&result);
		goto out;
	}

	status = mac_client_release(&client, R1_BOARD, result.mac);
	if (status != MAC_CLIENT_OK) {
		fprintf(stderr, "release failed: %s\n",
			mac_client_error(&client));
		print_manual_action(server_url, &result, "release");
		goto out;
	}
	fprintf(stderr, "efuse write failed; server reservation released\n");

out:
	mac_client_global_cleanup();
	return ret;
}

static void usage(const char *program)
{
	fprintf(stderr, "Usage: sudo %s -s SERVER_URL\n", program);
}

int main(int argc, char **argv)
{
	if (geteuid() != 0) {
		fprintf(stderr, "this program must be run as root\n");
		return EXIT_FAILURE;
	}
	if (argc != 3 || strcmp(argv[1], "-s") != 0) {
		usage(argv[0]);
		return EXIT_FAILURE;
	}
	return provision_r1(argv[2]);
}
