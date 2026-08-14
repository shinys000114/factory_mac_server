package webui

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"mac_server/internal/config"
	"mac_server/internal/store"
)

const maxConfigSize = 256 * 1024

var (
	//go:embed templates/*.html static/*
	assets embed.FS

	macPattern    = regexp.MustCompile(`^[0-9a-f]{12}$`)
	prefixPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

type UI struct {
	configs *config.Manager
	store   *store.Store
	tmpl    *template.Template
	static  http.Handler
}

type pageData struct {
	Title       string
	CSRF        string
	Message     string
	Error       string
	Query       string
	Board       string
	Prefix      string
	State       string
	PoolStats   []store.PoolStats
	Pool        *store.PoolStats
	Allocations []store.Allocation
	Audit       []store.AuditEvent
	ConfigPath  string
	ConfigText  string
	BackupDir   string
}

func New(configs *config.Manager, database *store.Store) (*UI, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse web templates: %w", err)
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("open web static files: %w", err)
	}
	return &UI{
		configs: configs,
		store:   database,
		tmpl:    tmpl,
		static:  http.FileServer(http.FS(staticFS)),
	}, nil
}

func (ui *UI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/", http.StatusFound)
	})
	mux.Handle("GET /favicon.ico", ui.static)
	mux.Handle("GET /web/static/", http.StripPrefix("/web/static/", ui.static))
	mux.HandleFunc("GET /web/", ui.dashboard)
	mux.HandleFunc("GET /web/allocations", ui.allocations)
	mux.HandleFunc("GET /web/pools/{prefix}", ui.poolDetails)
	mux.HandleFunc("POST /web/release", ui.release)
	mux.HandleFunc("GET /web/config", ui.configEditor)
	mux.HandleFunc("POST /web/config", ui.saveConfig)
	mux.HandleFunc("POST /web/backup", ui.backup)
	return mux
}

func (ui *UI) allocations(w http.ResponseWriter, r *http.Request) {
	data, err := ui.baseData(w, r, "Allocation search")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	data.Board = strings.TrimSpace(r.URL.Query().Get("board"))
	data.Prefix = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("prefix")))
	data.State = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state")))

	filter := store.ListFilter{
		Query: data.Query, Prefix: data.Prefix, State: data.State, Limit: 500,
	}
	if data.Board != "" {
		board, ok := ui.configs.ResolveBoard(data.Board)
		if !ok {
			data.Error = "unknown board: " + data.Board
		} else {
			filter.Board = board
		}
	}
	if data.Prefix != "" && !prefixPattern.MatchString(data.Prefix) {
		data.Error = "prefix must be exactly 8 hexadecimal characters"
	}
	if data.State != "" && data.State != "reserved" &&
		data.State != "assigned" && data.State != "released" {
		data.Error = "unknown allocation state: " + data.State
	}
	if data.Error == "" {
		data.Allocations, err = ui.store.List(r.Context(), filter)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	ui.render(w, http.StatusOK, "allocations.html", data)
}

func (ui *UI) dashboard(w http.ResponseWriter, r *http.Request) {
	data, err := ui.baseData(w, r, "MAC pool status")
	if err == nil {
		data.PoolStats, err = ui.store.PoolStats(r.Context())
	}
	if err == nil {
		data.Audit, err = ui.store.RecentAudit(r.Context(), 100)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ui.render(w, http.StatusOK, "dashboard.html", data)
}

func (ui *UI) poolDetails(w http.ResponseWriter, r *http.Request) {
	prefix := strings.ToLower(strings.TrimSpace(r.PathValue("prefix")))
	if !prefixPattern.MatchString(prefix) {
		http.NotFound(w, r)
		return
	}
	stat, err := ui.store.PoolStat(r.Context(), prefix)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, err := ui.baseData(w, r, stat.Board+" / "+stat.Prefix)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.Pool = &stat
	data.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	data.State = strings.TrimSpace(r.URL.Query().Get("state"))
	data.Allocations, err = ui.store.List(r.Context(), store.ListFilter{
		Query: data.Query, Prefix: prefix, State: data.State, Limit: 500,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ui.render(w, http.StatusOK, "pool.html", data)
}

func (ui *UI) configEditor(w http.ResponseWriter, r *http.Request) {
	data, err := ui.baseData(w, r, "TOML configuration")
	if err == nil {
		data.ConfigText, err = ui.configs.Text()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.ConfigPath = ui.configs.Path()
	ui.render(w, http.StatusOK, "config.html", data)
}

func (ui *UI) saveConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxConfigSize)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid configuration form: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	contents := r.FormValue("toml")
	if contents == "" {
		ui.renderConfigError(w, r, contents, "configuration must not be empty")
		return
	}
	if !strings.HasSuffix(contents, "\n") {
		contents += "\n"
	}
	err := ui.configs.ApplyText(contents, func(cfg *config.Config) error {
		return ui.store.SyncConfig(r.Context(), cfg.Pools())
	})
	if err != nil {
		ui.renderConfigError(w, r, contents, err.Error())
		return
	}
	http.Redirect(w, r, "/web/config?message="+url.QueryEscape("configuration saved and applied"), http.StatusSeeOther)
}

func (ui *UI) renderConfigError(w http.ResponseWriter, r *http.Request, contents, message string) {
	data, err := ui.baseData(w, r, "TOML configuration")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.ConfigPath = ui.configs.Path()
	data.ConfigText = contents
	data.Error = message
	ui.render(w, http.StatusBadRequest, "config.html", data)
}

func (ui *UI) release(w http.ResponseWriter, r *http.Request) {
	if !validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	board, ok := ui.configs.ResolveBoard(r.FormValue("board"))
	if !ok {
		redirectError(w, r, safeReturnPath(r.FormValue("return_to")), "unknown board")
		return
	}
	mac := strings.ToLower(strings.TrimSpace(r.FormValue("mac_addr")))
	if !macPattern.MatchString(mac) || mac[8:] == "0000" {
		redirectError(w, r, safeReturnPath(r.FormValue("return_to")), "invalid MAC address")
		return
	}
	if err := ui.store.Release(r.Context(), board, mac); err != nil {
		redirectError(w, r, safeReturnPath(r.FormValue("return_to")), err.Error())
		return
	}
	redirectMessage(w, r, safeReturnPath(r.FormValue("return_to")), mac+" released")
}

func (ui *UI) backup(w http.ResponseWriter, r *http.Request) {
	if !validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	path, err := ui.store.Backup(r.Context(), ui.configs.Get().Database.BackupDir)
	if err != nil {
		redirectError(w, r, "/web/", err.Error())
		return
	}
	redirectMessage(w, r, "/web/", "backup created: "+filepath.Base(path))
}

func (ui *UI) baseData(w http.ResponseWriter, r *http.Request, title string) (pageData, error) {
	csrf, err := csrfToken(w, r)
	if err != nil {
		return pageData{}, fmt.Errorf("create CSRF token: %w", err)
	}
	return pageData{
		Title:     title,
		CSRF:      csrf,
		Message:   r.URL.Query().Get("message"),
		Error:     r.URL.Query().Get("error"),
		BackupDir: ui.configs.Get().Database.BackupDir,
	}, nil
}

func (ui *UI) render(w http.ResponseWriter, status int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = ui.tmpl.ExecuteTemplate(w, name, data)
}

func csrfToken(w http.ResponseWriter, r *http.Request) (string, error) {
	if cookie, err := r.Cookie("mac_server_csrf"); err == nil && len(cookie.Value) == 64 {
		return cookie.Value, nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	http.SetCookie(w, &http.Cookie{
		Name: "mac_server_csrf", Value: token, Path: "/web/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	return token, nil
}

func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("mac_server_csrf")
	if err != nil {
		return false
	}
	return len(cookie.Value) == 64 && cookie.Value == r.FormValue("csrf")
}

func safeReturnPath(value string) string {
	if strings.HasPrefix(value, "/web/pools/") ||
		strings.HasPrefix(value, "/web/allocations") || value == "/web/" {
		return value
	}
	return "/web/"
}

func redirectMessage(w http.ResponseWriter, r *http.Request, path, message string) {
	http.Redirect(w, r, addQuery(path, "message", message), http.StatusSeeOther)
}

func redirectError(w http.ResponseWriter, r *http.Request, path, message string) {
	http.Redirect(w, r, addQuery(path, "error", message), http.StatusSeeOther)
}

func addQuery(path, key, value string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + url.QueryEscape(key) + "=" + url.QueryEscape(value)
}
