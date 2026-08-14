package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  *Config
}

func NewManager(path string, cfg *Config) *Manager {
	return &Manager{path: path, cfg: cfg}
}

func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *Manager) ResolveBoard(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.ResolveBoard(name)
}

func (m *Manager) Text() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	contents, err := os.ReadFile(m.path)
	if err != nil {
		return "", fmt.Errorf("read config %q: %w", m.path, err)
	}
	return string(contents), nil
}

func (m *Manager) Path() string {
	return m.path
}

func (m *Manager) ApplyText(contents string, apply func(*Config) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := Parse(contents, m.path)
	if err != nil {
		return err
	}
	if err := cfg.ValidateReloadFrom(m.cfg); err != nil {
		return err
	}
	if err := EnsureDirectories(cfg); err != nil {
		return err
	}

	previous, err := os.ReadFile(m.path)
	if err != nil {
		return fmt.Errorf("read current config before saving: %w", err)
	}
	if err := atomicWrite(m.path, []byte(contents)); err != nil {
		return err
	}
	if err := apply(cfg); err != nil {
		if restoreErr := atomicWrite(m.path, previous); restoreErr != nil {
			return fmt.Errorf("apply config: %v; restore previous file: %w", err, restoreErr)
		}
		return fmt.Errorf("apply config: %w", err)
	}
	m.cfg = cfg
	return nil
}

func atomicWrite(path string, contents []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat config %q: %w", path, err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".mac-server-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config %q: %w", path, err)
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}
