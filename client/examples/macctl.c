#include "mac_client.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void usage(const char *program)
{
	fprintf(stderr,
		"Usage:\n"
		"  %s -s URL request BOARD\n"
		"  %s -s URL confirm BOARD MAC UUID\n"
		"  %s -s URL release BOARD MAC\n"
		"  %s -s URL available BOARD MAC\n",
		program, program, program, program);
}

int main(int argc, char **argv)
{
	struct mac_client client;
	struct mac_client_result result;
	const char *server = NULL;
	const char *command;
	int available;
	int status;

	if (argc < 5 || strcmp(argv[1], "-s") != 0) {
		usage(argv[0]);
		return EXIT_FAILURE;
	}
	server = argv[2];
	command = argv[3];
	if (mac_client_global_init() != MAC_CLIENT_OK) {
		fprintf(stderr, "failed to initialize libcurl\n");
		return EXIT_FAILURE;
	}
	status = mac_client_init(&client, server, 5000);
	if (status != MAC_CLIENT_OK)
		goto error;

	if (strcmp(command, "request") == 0 && argc == 5) {
		status = mac_client_request(&client, argv[4], &result);
		if (status == MAC_CLIENT_OK)
			printf("mac=%s\nuuid=%s\n", result.mac, result.uuid);
	} else if (strcmp(command, "confirm") == 0 && argc == 7) {
		memset(&result, 0, sizeof(result));
		snprintf(result.mac, sizeof(result.mac), "%s", argv[5]);
		snprintf(result.uuid, sizeof(result.uuid), "%s", argv[6]);
		status = mac_client_confirm(&client, argv[4], &result);
		if (status == MAC_CLIENT_OK)
			puts("confirmed");
	} else if (strcmp(command, "release") == 0 && argc == 6) {
		status = mac_client_release(&client, argv[4], argv[5]);
		if (status == MAC_CLIENT_OK)
			puts("released");
	} else if (strcmp(command, "available") == 0 && argc == 6) {
		status = mac_client_is_available(&client, argv[4], argv[5],
					 &available);
		if (status == MAC_CLIENT_OK)
			printf("available=%s\n", available ? "true" : "false");
	} else {
		usage(argv[0]);
		mac_client_global_cleanup();
		return EXIT_FAILURE;
	}

	if (status != MAC_CLIENT_OK)
		goto error;
	mac_client_global_cleanup();
	return EXIT_SUCCESS;

error:
	fprintf(stderr, "error: %s\n", mac_client_error(&client));
	mac_client_global_cleanup();
	return EXIT_FAILURE;
}
