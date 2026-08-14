package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mac_server/internal/config"
	"mac_server/internal/store"
)

const compatExpiresIn = 315360000

var (
	macPattern  = regexp.MustCompile(`^[0-9a-f]{12}$`)
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type API struct {
	configs *config.Manager
	store   *store.Store
}

func NewAPI(configs *config.Manager, store *store.Store) *API {
	return &API{configs: configs, store: store}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /restapi/login", a.login)
	mux.HandleFunc("POST /restapi/request", a.request)
	mux.HandleFunc("POST /restapi/verify", a.verify)
	mux.HandleFunc("POST /restapi/update", a.update)
	mux.HandleFunc("POST /restapi/delete", a.delete)
	mux.HandleFunc("GET /healthz", a.health)
	return recoverMiddleware(mux)
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	// This endpoint intentionally ignores both credentials and Authorization.
	// It exists only for clients which still perform the legacy login flow.
	writeJSON(w, map[string]any{
		"user":         map[string]any{},
		"expires_in":   compatExpiresIn,
		"current_time": unixTime(),
		"token":        "compat",
	})
}

func (a *API) request(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Board string `json:"board"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeCompatError(w, err)
		return
	}
	board, ok := a.configs.ResolveBoard(body.Board)
	if !ok {
		writeCompatError(w, fmt.Errorf("Not supported board %q", body.Board))
		return
	}
	allocation, err := a.store.Allocate(r.Context(), board)
	if err != nil {
		writeCompatError(w, err)
		return
	}
	writeCompatJSON(w, map[string]any{
		"receivedData": map[string]any{"board": body.Board},
		"table":        allocation.Prefix,
		"mac_num":      allocation.Suffix,
		"mac_addr":     allocation.MAC,
		"uuid_mac":     allocation.UUID,
	})
}

func (a *API) verify(w http.ResponseWriter, r *http.Request) {
	var body addressRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeCompatError(w, err)
		return
	}
	board, mac, err := a.validateAddress(body.Board, body.MAC)
	if err != nil {
		writeCompatError(w, err)
		return
	}
	available, err := a.store.Available(r.Context(), board, mac)
	if err != nil {
		writeCompatError(w, err)
		return
	}
	writeCompatJSON(w, map[string]any{
		"receivedData": map[string]any{"board": body.Board, "mac_addr": body.MAC},
		"isAvailable":  available,
	})
}

func (a *API) update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Board string `json:"board"`
		MAC   string `json:"mac_addr"`
		UUID  string `json:"uuid"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeCompatError(w, err)
		return
	}
	board, mac, err := a.validateAddress(body.Board, body.MAC)
	if err != nil {
		writeCompatError(w, err)
		return
	}
	uuid := strings.ToLower(strings.TrimSpace(body.UUID))
	if uuid != "" && !uuidPattern.MatchString(uuid) {
		writeCompatError(w, fmt.Errorf("uuid must be a 36-character UUID"))
		return
	}
	if err := a.store.Confirm(r.Context(), board, mac, uuid); err != nil {
		writeCompatError(w, err)
		return
	}
	writeCompatJSON(w, map[string]any{
		"receivedData": map[string]any{"board": body.Board, "mac_addr": body.MAC, "uuid": body.UUID},
		"isUpdated":    true,
	})
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	var body addressRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeCompatError(w, err)
		return
	}
	board, mac, err := a.validateAddress(body.Board, body.MAC)
	if err != nil {
		writeCompatError(w, err)
		return
	}
	if err := a.store.Release(r.Context(), board, mac); err != nil {
		writeCompatError(w, err)
		return
	}
	writeCompatJSON(w, map[string]any{
		"receivedData": map[string]any{"board": body.Board, "mac_addr": body.MAC},
		"isDeleted":    true,
	})
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"status": "ok"})
}

type addressRequest struct {
	Board string `json:"board"`
	MAC   string `json:"mac_addr"`
}

func (a *API) validateAddress(rawBoard, rawMAC string) (string, string, error) {
	board, ok := a.configs.ResolveBoard(rawBoard)
	if !ok {
		return "", "", fmt.Errorf("Not supported board %q", rawBoard)
	}
	mac := strings.ToLower(strings.TrimSpace(rawMAC))
	if !macPattern.MatchString(mac) {
		return "", "", fmt.Errorf("mac_addr must be exactly 12 hexadecimal characters")
	}
	if mac[8:] == "0000" {
		return "", "", fmt.Errorf("MAC suffix 0000 is not assignable")
	}
	return board, mac, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid JSON request: multiple JSON values")
	}
	return nil
}

func writeCompatError(w http.ResponseWriter, err error) {
	writeCompatJSON(w, map[string]any{"error": err.Error()})
}

func writeCompatJSON(w http.ResponseWriter, response map[string]any) {
	response["auth"] = map[string]any{
		"expires_in":   compatExpiresIn,
		"current_time": unixTime(),
	}
	writeJSON(w, response)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(value)
}

func unixTime() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeCompatError(w, fmt.Errorf("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
