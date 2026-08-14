package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Store) Backup(ctx context.Context, directory string) (string, error) {
	if directory == "" {
		return "", fmt.Errorf("database.backup_dir is not configured")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	path := filepath.Join(directory, "mac-server-"+time.Now().In(s.location).Format("20060102-150405")+".db")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("backup already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("check backup path: %w", err)
	}

	quotedPath := strings.ReplaceAll(path, "'", "''")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+quotedPath+"'"); err != nil {
		return "", fmt.Errorf("create sqlite backup: %w", err)
	}
	return path, nil
}
