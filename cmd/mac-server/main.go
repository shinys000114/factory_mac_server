package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mac_server/internal/config"
	"mac_server/internal/server"
	"mac_server/internal/store"
	"mac_server/internal/webui"
)

func main() {
	configPath := flag.String("config", "mac-server.toml", "path to the TOML configuration file")
	flag.Parse()

	if err := run(*configPath); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := config.EnsureDirectories(cfg); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	database, err := store.Open(ctx, cfg.Database.Path, cfg.Database.BusyTimeoutMS)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.SyncConfig(ctx, cfg.Pools()); err != nil {
		return err
	}

	configs := config.NewManager(configPath, cfg)
	api := server.NewAPI(configs, database)
	web, err := webui.New(configs, database)
	if err != nil {
		return err
	}

	apiMux := http.NewServeMux()
	apiMux.Handle("/restapi/", api.Handler())
	apiMux.Handle("/healthz", api.Handler())
	if cfg.Web.Listen == "" {
		apiMux.Handle("/", web.Handler())
	}

	servers := []*http.Server{newHTTPServer(cfg.Server.Listen, apiMux, cfg)}
	if cfg.Web.Listen != "" {
		servers = append(servers, newHTTPServer(cfg.Web.Listen, web.Handler(), cfg))
	}

	errCh := make(chan error, len(servers))
	for _, httpServer := range servers {
		httpServer := httpServer
		go func() {
			log.Printf("listening on %s", httpServer.Addr)
			if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-errCh:
		stop()
		return fmt.Errorf("HTTP server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, httpServer := range servers {
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
	}
	return nil
}

func newHTTPServer(address string, handler http.Handler, cfg *config.Config) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeoutSeconds) * time.Second,
		WriteTimeout:      time.Duration(cfg.Server.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
