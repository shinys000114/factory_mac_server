package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"mac_server/internal/store"
)

var v1PrefixPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

type v1ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type v1ErrorResponse struct {
	Error v1ErrorBody `json:"error"`
}

func (a *API) V1Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/allocations", a.v1Request)
	mux.HandleFunc("GET /api/v1/allocations", a.v1ListAllocations)
	mux.HandleFunc("POST /api/v1/allocations/{mac}/confirm", a.v1Confirm)
	mux.HandleFunc("POST /api/v1/allocations/{mac}/release", a.v1Release)
	mux.HandleFunc("POST /api/v1/allocations/{mac}/availability", a.v1Availability)
	mux.HandleFunc("GET /api/v1/pools", a.v1Pools)
	mux.HandleFunc("GET /api/v1/pools/{prefix}", a.v1Pool)
	mux.HandleFunc("GET /api/v1/version", a.v1Version)
	return v1RecoverMiddleware(mux)
}

func (a *API) v1Request(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Board     string `json:"board"`
		RequestID string `json:"request_id"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeV1ClientError(w, "invalid_request", err)
		return
	}
	board, ok := a.configs.ResolveBoard(body.Board)
	if !ok {
		writeV1Error(w, http.StatusBadRequest, "unsupported_board",
			fmt.Sprintf("board %q is not configured", body.Board))
		return
	}
	requestID := strings.ToLower(strings.TrimSpace(body.RequestID))
	if !uuidPattern.MatchString(requestID) {
		writeV1Error(w, http.StatusBadRequest, "invalid_request_id",
			"request_id must be a 36-character UUID")
		return
	}
	allocation, err := a.store.AllocateWithRequestID(r.Context(), board, requestID)
	if err != nil {
		writeV1StoreError(w, err)
		return
	}
	writeV1JSON(w, http.StatusOK, allocation)
}

func (a *API) v1Confirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Board string `json:"board"`
		UUID  string `json:"uuid"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeV1ClientError(w, "invalid_request", err)
		return
	}
	board, mac, err := a.v1Address(body.Board, r.PathValue("mac"))
	if err != nil {
		writeV1ClientError(w, "invalid_address", err)
		return
	}
	uuid := strings.ToLower(strings.TrimSpace(body.UUID))
	if !uuidPattern.MatchString(uuid) {
		writeV1Error(w, http.StatusBadRequest, "invalid_uuid",
			"uuid must be a 36-character UUID")
		return
	}
	if !strings.EqualFold(uuid[24:], mac) {
		writeV1Error(w, http.StatusBadRequest, "uuid_mac_mismatch",
			"the final 12 UUID characters must match the MAC address")
		return
	}
	if err := a.store.Confirm(r.Context(), board, mac, uuid); err != nil {
		writeV1StoreError(w, err)
		return
	}
	writeV1JSON(w, http.StatusOK, map[string]any{
		"board": board, "mac": mac, "uuid": uuid, "state": "assigned",
	})
}

func (a *API) v1Release(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Board string `json:"board"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeV1ClientError(w, "invalid_request", err)
		return
	}
	board, mac, err := a.v1Address(body.Board, r.PathValue("mac"))
	if err != nil {
		writeV1ClientError(w, "invalid_address", err)
		return
	}
	if err := a.store.Release(r.Context(), board, mac); err != nil {
		writeV1StoreError(w, err)
		return
	}
	writeV1JSON(w, http.StatusOK, map[string]any{
		"board": board, "mac": mac, "state": "released",
	})
}

func (a *API) v1Availability(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Board string `json:"board"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeV1ClientError(w, "invalid_request", err)
		return
	}
	board, mac, err := a.v1Address(body.Board, r.PathValue("mac"))
	if err != nil {
		writeV1ClientError(w, "invalid_address", err)
		return
	}
	available, err := a.store.Available(r.Context(), board, mac)
	if err != nil {
		writeV1StoreError(w, err)
		return
	}
	writeV1JSON(w, http.StatusOK, map[string]any{
		"board": board, "mac": mac, "available": available,
	})
}

func (a *API) v1ListAllocations(w http.ResponseWriter, r *http.Request) {
	filter := store.ListFilter{
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Prefix: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("prefix"))),
		State:  strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state"))),
		Limit:  100,
	}
	if rawBoard := strings.TrimSpace(r.URL.Query().Get("board")); rawBoard != "" {
		board, ok := a.configs.ResolveBoard(rawBoard)
		if !ok {
			writeV1Error(w, http.StatusBadRequest, "unsupported_board",
				fmt.Sprintf("board %q is not configured", rawBoard))
			return
		}
		filter.Board = board
	}
	if filter.Prefix != "" && !v1PrefixPattern.MatchString(filter.Prefix) {
		writeV1Error(w, http.StatusBadRequest, "invalid_prefix",
			"prefix must be exactly 8 hexadecimal characters")
		return
	}
	if filter.State != "" && filter.State != "reserved" &&
		filter.State != "assigned" && filter.State != "released" {
		writeV1Error(w, http.StatusBadRequest, "invalid_state",
			"state must be reserved, assigned, or released")
		return
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 500 {
			writeV1Error(w, http.StatusBadRequest, "invalid_limit",
				"limit must be between 1 and 500")
			return
		}
		filter.Limit = limit
	}
	allocations, err := a.store.List(r.Context(), filter)
	if err != nil {
		writeV1StoreError(w, err)
		return
	}
	if allocations == nil {
		allocations = []store.Allocation{}
	}
	writeV1JSON(w, http.StatusOK, map[string]any{"allocations": allocations})
}

func (a *API) v1Pools(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.PoolStats(r.Context())
	if err != nil {
		writeV1StoreError(w, err)
		return
	}
	if stats == nil {
		stats = []store.PoolStats{}
	}
	writeV1JSON(w, http.StatusOK, map[string]any{"pools": stats})
}

func (a *API) v1Pool(w http.ResponseWriter, r *http.Request) {
	prefix := strings.ToLower(strings.TrimSpace(r.PathValue("prefix")))
	if !v1PrefixPattern.MatchString(prefix) {
		writeV1Error(w, http.StatusBadRequest, "invalid_prefix",
			"prefix must be exactly 8 hexadecimal characters")
		return
	}
	stat, err := a.store.PoolStat(r.Context(), prefix)
	if err != nil {
		writeV1StoreError(w, err)
		return
	}
	writeV1JSON(w, http.StatusOK, stat)
}

func (a *API) v1Version(w http.ResponseWriter, _ *http.Request) {
	writeV1JSON(w, http.StatusOK, map[string]any{"api": "v1"})
}

func (a *API) v1Address(rawBoard, rawMAC string) (string, string, error) {
	board, ok := a.configs.ResolveBoard(rawBoard)
	if !ok {
		return "", "", fmt.Errorf("board %q is not configured", rawBoard)
	}
	mac := strings.ToLower(strings.TrimSpace(rawMAC))
	if !macPattern.MatchString(mac) {
		return "", "", fmt.Errorf("MAC must be exactly 12 hexadecimal characters")
	}
	if mac[8:] == "0000" {
		return "", "", fmt.Errorf("MAC suffix 0000 is not assignable")
	}
	return board, mac, nil
}

func writeV1StoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoAddress):
		writeV1Error(w, http.StatusConflict, "pool_exhausted", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeV1Error(w, http.StatusNotFound, "allocation_not_found", err.Error())
	case errors.Is(err, store.ErrInvalidState):
		writeV1Error(w, http.StatusConflict, "invalid_state", err.Error())
	case errors.Is(err, store.ErrUUIDMismatch):
		writeV1Error(w, http.StatusConflict, "uuid_mismatch", err.Error())
	case errors.Is(err, store.ErrPoolMismatch):
		writeV1Error(w, http.StatusConflict, "pool_mismatch", err.Error())
	case errors.Is(err, store.ErrRequestConflict):
		writeV1Error(w, http.StatusConflict, "request_id_conflict", err.Error())
	default:
		writeV1Error(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func writeV1ClientError(w http.ResponseWriter, code string, err error) {
	writeV1Error(w, http.StatusBadRequest, code, err.Error())
}

func writeV1Error(w http.ResponseWriter, status int, code, message string) {
	writeV1JSON(w, status, v1ErrorResponse{Error: v1ErrorBody{Code: code, Message: message}})
}

func writeV1JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func v1RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				writeV1Error(w, http.StatusInternalServerError,
					"internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
