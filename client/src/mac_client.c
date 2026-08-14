#include "mac_client.h"

#include <curl/curl.h>

#include <ctype.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#define RESPONSE_LIMIT (64U * 1024U)

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
	curl_easy_setopt(curl, CURLOPT_USERAGENT, "mac-client/1.0");

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
		set_error(client, "server returned HTTP %ld", http_status);
		free(response->data);
		response->data = NULL;
		return MAC_CLIENT_ERROR_SERVER;
	}
	return MAC_CLIENT_OK;
}

static int check_server_error(struct mac_client *client, const char *json)
{
	char message[sizeof(client->error)];

	if (json_string(json, "error", message, sizeof(message))) {
		set_error(client, "%s", message);
		return MAC_CLIENT_ERROR_SERVER;
	}
	return MAC_CLIENT_OK;
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
	char request[128];
	int status;

	if (client == NULL || result == NULL || !valid_board(board)) {
		set_error(client, "invalid request arguments");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	memset(result, 0, sizeof(*result));
	snprintf(request, sizeof(request), "{\"board\":\"%s\"}", board);
	status = post_json(client, "/restapi/request", request, &response);
	if (status != MAC_CLIENT_OK)
		return status;
	status = check_server_error(client, response.data);
	if (status == MAC_CLIENT_OK &&
	    (!json_string(response.data, "mac_addr", result->mac,
			 sizeof(result->mac)) ||
	     !json_string(response.data, "uuid_mac", result->uuid,
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
	free(response.data);
	return status;
}

int mac_client_confirm(struct mac_client *client, const char *board,
		       const struct mac_client_result *result)
{
	struct response_buffer response = {0};
	char request[256];
	int updated = 0;
	int status;

	if (client == NULL || result == NULL || !valid_board(board) ||
	    !valid_mac(result->mac) || !valid_uuid(result->uuid) ||
	    strcasecmp(result->uuid + 24, result->mac) != 0) {
		set_error(client, "invalid confirm arguments");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	snprintf(request, sizeof(request),
		 "{\"board\":\"%s\",\"mac_addr\":\"%s\",\"uuid\":\"%s\"}",
		 board, result->mac, result->uuid);
	status = post_json(client, "/restapi/update", request, &response);
	if (status != MAC_CLIENT_OK)
		return status;
	status = check_server_error(client, response.data);
	if (status == MAC_CLIENT_OK &&
	    (!json_bool(response.data, "isUpdated", &updated) || !updated)) {
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
	char request[160];
	int released = 0;
	int status;

	if (client == NULL || !valid_board(board) || !valid_mac(mac)) {
		set_error(client, "invalid release arguments");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	snprintf(request, sizeof(request),
		 "{\"board\":\"%s\",\"mac_addr\":\"%s\"}", board, mac);
	status = post_json(client, "/restapi/delete", request, &response);
	if (status != MAC_CLIENT_OK)
		return status;
	status = check_server_error(client, response.data);
	if (status == MAC_CLIENT_OK &&
	    (!json_bool(response.data, "isDeleted", &released) || !released)) {
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
	char request[160];
	int status;

	if (client == NULL || available == NULL || !valid_board(board) ||
	    !valid_mac(mac)) {
		set_error(client, "invalid verify arguments");
		return MAC_CLIENT_ERROR_ARGUMENT;
	}
	*available = 0;
	snprintf(request, sizeof(request),
		 "{\"board\":\"%s\",\"mac_addr\":\"%s\"}", board, mac);
	status = post_json(client, "/restapi/verify", request, &response);
	if (status != MAC_CLIENT_OK)
		return status;
	status = check_server_error(client, response.data);
	if (status == MAC_CLIENT_OK &&
	    !json_bool(response.data, "isAvailable", available)) {
		set_error(client, "server response is missing isAvailable");
		status = MAC_CLIENT_ERROR_PROTOCOL;
	}
	free(response.data);
	return status;
}
