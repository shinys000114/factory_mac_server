#include "mac_client.h"

#include <curl/curl.h>

#include <ctype.h>
#include <errno.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/types.h>
#include <sys/random.h>

#define RESPONSE_LIMIT (64U * 1024U)
#define REQUEST_ID_SIZE 36
#define REQUEST_RETRIES 3

struct response_buffer {
	char *data;
	size_t length;
	int overflow;
};

static void set_error(struct mac_client *client, const char *format, ...)
{
	va_list args;

	if (client == NULL)
		return;
	va_start(args, format);
	vsnprintf(client->error, sizeof(client->error), format, args);
	va_end(args);
}

static int valid_board(const char *board)
{
	size_t i, length;

	if (board == NULL)
		return 0;
	length = strlen(board);
	if (length == 0 || length > 63)
		return 0;
	for (i = 0; i < length; ++i) {
		unsigned char c = (unsigned char)board[i];

		if (!isalnum(c) && c != '-' && c != '_')
			return 0;
	}
	return 1;
}

static int valid_mac(const char *mac)
{
	size_t i;

	if (mac == NULL || strlen(mac) != MAC_CLIENT_MAC_SIZE)
		return 0;
	for (i = 0; i < MAC_CLIENT_MAC_SIZE; ++i) {
		if (!isxdigit((unsigned char)mac[i]))
			return 0;
	}
	return strcasecmp(mac + 8, "0000") != 0;
}

static int valid_uuid(const char *uuid)
{
	size_t i;

	if (uuid == NULL || strlen(uuid) != MAC_CLIENT_UUID_SIZE)
		return 0;
	for (i = 0; i < MAC_CLIENT_UUID_SIZE; ++i) {
		if (i == 8 || i == 13 || i == 18 || i == 23) {
			if (uuid[i] != '-')
				return 0;
		} else if (!isxdigit((unsigned char)uuid[i])) {
			return 0;
		}
	}
	return 1;
}

static int generate_request_id(struct mac_client *client,
			       char request_id[REQUEST_ID_SIZE + 1])
{
	unsigned char bytes[16];
	size_t offset = 0;

	while (offset < sizeof(bytes)) {
		ssize_t count = getrandom(bytes + offset, sizeof(bytes) - offset, 0);

		if (count < 0) {
			if (errno == EINTR)
				continue;
			set_error(client, "failed to generate request ID: %s",
				  strerror(errno));
			return MAC_CLIENT_ERROR_PROTOCOL;
		}
		if (count == 0) {
			set_error(client, "failed to generate request ID");
			return MAC_CLIENT_ERROR_PROTOCOL;
		}
		offset += (size_t)count;
	}
	bytes[6] = (bytes[6] & 0x0fU) | 0x40U;
	bytes[8] = (bytes[8] & 0x3fU) | 0x80U;
	snprintf(request_id, REQUEST_ID_SIZE + 1,
		 "%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-"
		 "%02x%02x%02x%02x%02x%02x",
		 (unsigned int)bytes[0], (unsigned int)bytes[1],
		 (unsigned int)bytes[2], (unsigned int)bytes[3],
		 (unsigned int)bytes[4], (unsigned int)bytes[5],
		 (unsigned int)bytes[6], (unsigned int)bytes[7],
		 (unsigned int)bytes[8], (unsigned int)bytes[9],
		 (unsigned int)bytes[10], (unsigned int)bytes[11],
		 (unsigned int)bytes[12], (unsigned int)bytes[13],
		 (unsigned int)bytes[14], (unsigned int)bytes[15]);
	return MAC_CLIENT_OK;
}

static size_t receive_response(void *contents, size_t size, size_t nmemb,
			       void *user_data)
{
	struct response_buffer *buffer = user_data;
	size_t bytes = size * nmemb;
	char *new_data;

	if (bytes > RESPONSE_LIMIT || buffer->length > RESPONSE_LIMIT - bytes) {
		buffer->overflow = 1;
		return 0;
	}
	new_data = realloc(buffer->data, buffer->length + bytes + 1);
	if (new_data == NULL)
		return 0;
	buffer->data = new_data;
	memcpy(buffer->data + buffer->length, contents, bytes);
	buffer->length += bytes;
	buffer->data[buffer->length] = '\0';
	return bytes;
}

static int json_string(const char *json, const char *key, char *output,
		       size_t output_size)
{
	char pattern[80];
	const char *cursor;
	size_t used = 0;

	if (snprintf(pattern, sizeof(pattern), "\"%s\"", key) >=
	    (int)sizeof(pattern))
		return 0;
	cursor = strstr(json, pattern);
	if (cursor == NULL)
		return 0;
	cursor += strlen(pattern);
	while (isspace((unsigned char)*cursor))
		++cursor;
	if (*cursor++ != ':')
		return 0;
	while (isspace((unsigned char)*cursor))
		++cursor;
	if (*cursor++ != '"')
		return 0;

	while (*cursor != '\0' && *cursor != '"') {
		char value = *cursor++;

		if (value == '\\') {
			value = *cursor++;
			if (value == '\0')
				return 0;
		}
		if (used + 1 >= output_size)
			return 0;
		output[used++] = value;
	}
	if (*cursor != '"')
		return 0;
	output[used] = '\0';
	return 1;
}

static int json_bool(const char *json, const char *key, int *value)
{
	char pattern[80];
	const char *cursor;

	if (snprintf(pattern, sizeof(pattern), "\"%s\"", key) >=
	    (int)sizeof(pattern))
		return 0;
	cursor = strstr(json, pattern);
	if (cursor == NULL)
		return 0;
	cursor += strlen(pattern);
	while (isspace((unsigned char)*cursor))
		++cursor;
	if (*cursor++ != ':')
		return 0;
	while (isspace((unsigned char)*cursor))
		++cursor;
	if (strncmp(cursor, "true", 4) == 0) {
		*value = 1;
		return 1;
	}
	if (strncmp(cursor, "false", 5) == 0) {
		*value = 0;
		return 1;
	}
	return 0;
}

static int post_json(struct mac_client *client, const char *path,
		     const char *request, struct response_buffer *response)
{
	CURL *curl;
	CURLcode curl_status;
	struct curl_slist *headers = NULL;
	char url[sizeof(client->server_url) + 64];
	long http_status = 0;

	if (snprintf(url, sizeof(url), "%s%s", client->server_url, path) >=
	    (int)sizeof(url)) {
		set_error(client, "request URL is too long");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	response->data = calloc(1, 1);
	if (response->data == NULL) {
		set_error(client, "out of memory");
		return MAC_CLIENT_ERROR_PROTOCOL;
	}

	curl = curl_easy_init();
	if (curl == NULL) {
		free(response->data);
		response->data = NULL;
		set_error(client, "curl_easy_init failed");
		return MAC_CLIENT_ERROR_NETWORK;
	}
	headers = curl_slist_append(headers, "Content-Type: application/json");
	curl_easy_setopt(curl, CURLOPT_URL, url);
	curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
	curl_easy_setopt(curl, CURLOPT_POSTFIELDS, request);
	curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE, (long)strlen(request));
	curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, receive_response);
	curl_easy_setopt(curl, CURLOPT_WRITEDATA, response);
	curl_easy_setopt(curl, CURLOPT_TIMEOUT_MS, client->timeout_ms);
	curl_easy_setopt(curl, CURLOPT_CONNECTTIMEOUT_MS, client->timeout_ms);
	curl_easy_setopt(curl, CURLOPT_NOSIGNAL, 1L);
	curl_easy_setopt(curl, CURLOPT_USERAGENT, "mac-client/1.1");

	curl_status = curl_easy_perform(curl);
	curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &http_status);
	curl_slist_free_all(headers);
	curl_easy_cleanup(curl);
	if (response->overflow) {
		set_error(client, "server response exceeds %u bytes", RESPONSE_LIMIT);
		free(response->data);
		response->data = NULL;
		return MAC_CLIENT_ERROR_PROTOCOL;
	}
	if (curl_status != CURLE_OK) {
		set_error(client, "HTTP request failed: %s",
			  curl_easy_strerror(curl_status));
		free(response->data);
		response->data = NULL;
		return MAC_CLIENT_ERROR_NETWORK;
	}
	if (http_status < 200 || http_status >= 300) {
		char message[sizeof(client->error)];

		if (json_string(response->data, "message", message,
				sizeof(message)))
			set_error(client, "server returned HTTP %ld: %s",
				  http_status, message);
		else
			set_error(client, "server returned HTTP %ld", http_status);
		free(response->data);
		response->data = NULL;
		return MAC_CLIENT_ERROR_SERVER;
	}
	return MAC_CLIENT_OK;
}

static int post_json_retry(struct mac_client *client, const char *path,
			   const char *request,
			   struct response_buffer *response)
{
	int attempt;
	int status = MAC_CLIENT_ERROR_NETWORK;

	for (attempt = 0; attempt < REQUEST_RETRIES; ++attempt) {
		memset(response, 0, sizeof(*response));
		status = post_json(client, path, request, response);
		if (status != MAC_CLIENT_ERROR_NETWORK)
			break;
	}
	return status;
}

int mac_client_global_init(void)
{
	return curl_global_init(CURL_GLOBAL_DEFAULT) == CURLE_OK ?
		MAC_CLIENT_OK : MAC_CLIENT_ERROR_NETWORK;
}

void mac_client_global_cleanup(void)
{
	curl_global_cleanup();
}

int mac_client_init(struct mac_client *client, const char *server_url,
		    long timeout_ms)
{
	size_t length;

	if (client == NULL || server_url == NULL)
		return MAC_CLIENT_ERROR_ARGUMENT;
	memset(client, 0, sizeof(*client));
	length = strlen(server_url);
	while (length > 0 && server_url[length - 1] == '/')
		--length;
	if (length == 0 || length >= sizeof(client->server_url)) {
		set_error(client, "invalid server URL");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	memcpy(client->server_url, server_url, length);
	client->server_url[length] = '\0';
	client->timeout_ms = timeout_ms > 0 ? timeout_ms : 5000;
	return MAC_CLIENT_OK;
}

const char *mac_client_error(const struct mac_client *client)
{
	return client == NULL ? "invalid client" : client->error;
}

int mac_client_request(struct mac_client *client, const char *board,
		       struct mac_client_result *result)
{
	struct response_buffer response = {0};
	char request[192];
	int status;

	if (client == NULL || result == NULL || !valid_board(board)) {
		set_error(client, "invalid request arguments");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	memset(result, 0, sizeof(*result));
	if (client->request_id[0] == '\0') {
		status = generate_request_id(client, client->request_id);
		if (status != MAC_CLIENT_OK)
			return status;
	}
	snprintf(request, sizeof(request),
		 "{\"board\":\"%s\",\"request_id\":\"%s\"}",
		 board, client->request_id);
	status = post_json_retry(client, "/api/v1/allocations", request,
				 &response);
	if (status != MAC_CLIENT_OK)
		return status;
	if (status == MAC_CLIENT_OK &&
	    (!json_string(response.data, "mac", result->mac,
			 sizeof(result->mac)) ||
	     !json_string(response.data, "uuid", result->uuid,
			 sizeof(result->uuid)))) {
		set_error(client, "server response is missing MAC or UUID");
		status = MAC_CLIENT_ERROR_PROTOCOL;
	}
	if (status == MAC_CLIENT_OK &&
	    (!valid_mac(result->mac) || !valid_uuid(result->uuid) ||
	     strcasecmp(result->uuid + 24, result->mac) != 0)) {
		set_error(client, "server returned an invalid MAC/UUID pair");
		status = MAC_CLIENT_ERROR_PROTOCOL;
	}
	if (status == MAC_CLIENT_OK)
		client->request_id[0] = '\0';
	free(response.data);
	return status;
}

int mac_client_confirm(struct mac_client *client, const char *board,
		       const struct mac_client_result *result)
{
	struct response_buffer response = {0};
	char path[128];
	char request[160];
	char state[32];
	int status;

	if (client == NULL || result == NULL || !valid_board(board) ||
	    !valid_mac(result->mac) || !valid_uuid(result->uuid) ||
	    strcasecmp(result->uuid + 24, result->mac) != 0) {
		set_error(client, "invalid confirm arguments");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	snprintf(path, sizeof(path), "/api/v1/allocations/%s/confirm",
		 result->mac);
	snprintf(request, sizeof(request),
		 "{\"board\":\"%s\",\"uuid\":\"%s\"}", board, result->uuid);
	status = post_json_retry(client, path, request, &response);
	if (status != MAC_CLIENT_OK)
		return status;
	if (status == MAC_CLIENT_OK &&
	    (!json_string(response.data, "state", state, sizeof(state)) ||
	     strcmp(state, "assigned") != 0)) {
		set_error(client, "server did not confirm the allocation");
		status = MAC_CLIENT_ERROR_PROTOCOL;
	}
	free(response.data);
	return status;
}

int mac_client_release(struct mac_client *client, const char *board,
		       const char *mac)
{
	struct response_buffer response = {0};
	char path[128];
	char request[96];
	char state[32];
	int status;

	if (client == NULL || !valid_board(board) || !valid_mac(mac)) {
		set_error(client, "invalid release arguments");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	snprintf(path, sizeof(path), "/api/v1/allocations/%s/release", mac);
	snprintf(request, sizeof(request),
		 "{\"board\":\"%s\"}", board);
	status = post_json_retry(client, path, request, &response);
	if (status != MAC_CLIENT_OK)
		return status;
	if (status == MAC_CLIENT_OK &&
	    (!json_string(response.data, "state", state, sizeof(state)) ||
	     strcmp(state, "released") != 0)) {
		set_error(client, "server did not release the allocation");
		status = MAC_CLIENT_ERROR_PROTOCOL;
	}
	free(response.data);
	return status;
}

int mac_client_is_available(struct mac_client *client, const char *board,
			    const char *mac, int *available)
{
	struct response_buffer response = {0};
	char path[128];
	char request[96];
	int status;

	if (client == NULL || available == NULL || !valid_board(board) ||
	    !valid_mac(mac)) {
		set_error(client, "invalid verify arguments");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	*available = 0;
	snprintf(path, sizeof(path), "/api/v1/allocations/%s/availability",
		 mac);
	snprintf(request, sizeof(request),
		 "{\"board\":\"%s\"}", board);
	status = post_json_retry(client, path, request, &response);
	if (status != MAC_CLIENT_OK)
		return status;
	if (status == MAC_CLIENT_OK &&
	    !json_bool(response.data, "available", available)) {
		set_error(client, "server response is missing available");
		status = MAC_CLIENT_ERROR_PROTOCOL;
	}
	free(response.data);
	return status;
}
