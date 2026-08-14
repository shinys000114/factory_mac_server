#ifndef MAC_CLIENT_H
#define MAC_CLIENT_H

#ifdef __cplusplus
extern "C" {
#endif

#include <stddef.h>

#define MAC_CLIENT_MAC_SIZE  12
#define MAC_CLIENT_UUID_SIZE 36

enum mac_client_status {
	MAC_CLIENT_OK = 0,
	MAC_CLIENT_ERROR_ARGUMENT = -1,
	MAC_CLIENT_ERROR_NETWORK = -2,
	MAC_CLIENT_ERROR_PROTOCOL = -3,
	MAC_CLIENT_ERROR_SERVER = -4,
};

struct mac_client {
	char server_url[256];
	long timeout_ms;
	char error[256];
};

struct mac_client_result {
	char mac[MAC_CLIENT_MAC_SIZE + 1];
	char uuid[MAC_CLIENT_UUID_SIZE + 1];
};

int mac_client_global_init(void);
void mac_client_global_cleanup(void);

int mac_client_init(struct mac_client *client, const char *server_url,
		    long timeout_ms);
const char *mac_client_error(const struct mac_client *client);

int mac_client_request(struct mac_client *client, const char *board,
		       struct mac_client_result *result);
int mac_client_confirm(struct mac_client *client, const char *board,
		       const struct mac_client_result *result);
int mac_client_release(struct mac_client *client, const char *board,
		       const char *mac);
int mac_client_is_available(struct mac_client *client, const char *board,
			    const char *mac, int *available);

#ifdef __cplusplus
}
#endif

#endif
