package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"mac_server/internal/config"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

var (
	ErrNoAddress    = errors.New("no more available MAC addresses")
	ErrNotFound     = errors.New("MAC address was not found")
	ErrInvalidState = errors.New("MAC address is not reserved")
	ErrUUIDMismatch = errors.New("UUID does not match the reserved address")
	ErrPoolMismatch = errors.New("MAC address does not belong to the board")
)

type Store struct {
	db *sql.DB
}

type Allocation struct {
	MAC        string
	Board      string
	Prefix     string
	Suffix     int
	State      string
	UUID       string
	ReservedAt string
	AssignedAt string
	UpdatedAt  string
}

type PoolStats struct {
	Board      string
	Prefix     string
	RangeStart string
	RangeEnd   string
	Total      int
	Free       int
	Reserved   int
	Assigned   int
}

type AuditEvent struct {
	ID        int64
	Event     string
	Board     string
	MAC       string
	UUID      string
	Detail    string
	CreatedAt string
}

type ListFilter struct {
	Query  string
	Board  string
	Prefix string
	State  string
	Limit  int
}

func Open(ctx context.Context, path string, busyTimeoutMS int) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS),
		"PRAGMA synchronous=FULL",
	}
	for _, statement := range pragmas {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply %q: %w", statement, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS pools (
			prefix TEXT PRIMARY KEY CHECK(length(prefix) = 8),
			board TEXT NOT NULL,
			priority INTEGER NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0, 1)),
			next_suffix INTEGER NOT NULL DEFAULT 1 CHECK(next_suffix BETWEEN 1 AND 65536),
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS allocations (
			mac_addr TEXT PRIMARY KEY CHECK(length(mac_addr) = 12),
			board TEXT NOT NULL,
			prefix TEXT NOT NULL,
			suffix INTEGER NOT NULL CHECK(suffix BETWEEN 1 AND 65535),
			state TEXT NOT NULL CHECK(state IN ('reserved', 'assigned', 'released')),
			uuid TEXT UNIQUE,
			reserved_at TEXT,
			assigned_at TEXT,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(prefix) REFERENCES pools(prefix),
			UNIQUE(prefix, suffix)
		)`,
		`CREATE INDEX IF NOT EXISTS allocations_board_state_idx ON allocations(board, state)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event TEXT NOT NULL,
			board TEXT,
			mac_addr TEXT,
			uuid TEXT,
			detail TEXT,
			created_at TEXT NOT NULL
		)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(?, ?)`,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()
	for i, statement := range statements {
		var execErr error
		if i == len(statements)-1 {
			_, execErr = tx.ExecContext(ctx, statement, schemaVersion, now())
		} else {
			_, execErr = tx.ExecContext(ctx, statement)
		}
		if execErr != nil {
			return fmt.Errorf("apply schema migration: %w", execErr)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

func (s *Store) SyncConfig(ctx context.Context, pools []config.Pool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin config sync: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE pools SET enabled = 0, updated_at = ?`, now()); err != nil {
		return fmt.Errorf("disable old pools: %w", err)
	}
	for _, pool := range pools {
		var oldBoard string
		err := tx.QueryRowContext(ctx, `SELECT board FROM pools WHERE prefix = ?`, pool.Prefix).Scan(&oldBoard)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read pool %s: %w", pool.Prefix, err)
		}
		if err == nil && oldBoard != pool.Board {
			return fmt.Errorf("prefix %s is already owned by board %s", pool.Prefix, oldBoard)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO pools(prefix, board, priority, enabled, next_suffix, updated_at)
			VALUES(?, ?, ?, 1, 1, ?)
			ON CONFLICT(prefix) DO UPDATE SET
				priority = excluded.priority,
				enabled = 1,
				updated_at = excluded.updated_at`,
			pool.Prefix, pool.Board, pool.Priority, now()); err != nil {
			return fmt.Errorf("sync pool %s: %w", pool.Prefix, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log(event, detail, created_at)
		VALUES('config-sync', ?, ?)`, fmt.Sprintf("enabled pools: %d", len(pools)), now()); err != nil {
		return fmt.Errorf("write config audit log: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit config sync: %w", err)
	}
	return nil
}

func (s *Store) Allocate(ctx context.Context, board string) (Allocation, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Allocation{}, fmt.Errorf("get sqlite connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Allocation{}, fmt.Errorf("begin allocation transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	allocation, err := allocateReleased(ctx, conn, board)
	if errors.Is(err, sql.ErrNoRows) {
		allocation, err = allocateNew(ctx, conn, board)
	}
	if err != nil {
		return Allocation{}, err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO audit_log(event, board, mac_addr, uuid, created_at)
		VALUES('reserve', ?, ?, ?, ?)`, board, allocation.MAC, allocation.UUID, now()); err != nil {
		return Allocation{}, fmt.Errorf("write allocation audit log: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Allocation{}, fmt.Errorf("commit allocation: %w", err)
	}
	committed = true
	return allocation, nil
}

func allocateReleased(ctx context.Context, conn *sql.Conn, board string) (Allocation, error) {
	var allocation Allocation
	err := conn.QueryRowContext(ctx, `
		SELECT a.mac_addr, a.board, a.prefix, a.suffix
		FROM allocations a
		JOIN pools p ON p.prefix = a.prefix
		WHERE a.board = ? AND a.state = 'released' AND p.enabled = 1
		ORDER BY p.priority, a.suffix
		LIMIT 1`, board).Scan(&allocation.MAC, &allocation.Board, &allocation.Prefix, &allocation.Suffix)
	if err != nil {
		return Allocation{}, err
	}

	uuid, err := uuidForMAC(allocation.MAC)
	if err != nil {
		return Allocation{}, err
	}
	timestamp := now()
	result, err := conn.ExecContext(ctx, `
		UPDATE allocations
		SET state = 'reserved', uuid = ?, reserved_at = ?, assigned_at = NULL, updated_at = ?
		WHERE mac_addr = ? AND state = 'released'`, uuid, timestamp, timestamp, allocation.MAC)
	if err != nil {
		return Allocation{}, fmt.Errorf("reserve released MAC address: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Allocation{}, ErrNoAddress
	}
	allocation.State = "reserved"
	allocation.UUID = uuid
	allocation.ReservedAt = timestamp
	allocation.UpdatedAt = timestamp
	return allocation, nil
}

func allocateNew(ctx context.Context, conn *sql.Conn, board string) (Allocation, error) {
	var prefix string
	var suffix int
	err := conn.QueryRowContext(ctx, `
		SELECT prefix, next_suffix
		FROM pools
		WHERE board = ? AND enabled = 1 AND next_suffix <= ?
		ORDER BY priority
		LIMIT 1`, board, config.LastSuffix).Scan(&prefix, &suffix)
	if errors.Is(err, sql.ErrNoRows) {
		return Allocation{}, ErrNoAddress
	}
	if err != nil {
		return Allocation{}, fmt.Errorf("select available pool: %w", err)
	}
	if suffix < config.FirstSuffix || suffix > config.LastSuffix {
		return Allocation{}, fmt.Errorf("pool %s contains invalid next suffix %d", prefix, suffix)
	}

	mac := fmt.Sprintf("%s%04x", prefix, suffix)
	uuid, err := uuidForMAC(mac)
	if err != nil {
		return Allocation{}, err
	}
	timestamp := now()
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO allocations(mac_addr, board, prefix, suffix, state, uuid, reserved_at, updated_at)
		VALUES(?, ?, ?, ?, 'reserved', ?, ?, ?)`,
		mac, board, prefix, suffix, uuid, timestamp, timestamp); err != nil {
		return Allocation{}, fmt.Errorf("insert allocation: %w", err)
	}
	result, err := conn.ExecContext(ctx, `
		UPDATE pools SET next_suffix = ?, updated_at = ?
		WHERE prefix = ? AND next_suffix = ?`, suffix+1, timestamp, prefix, suffix)
	if err != nil {
		return Allocation{}, fmt.Errorf("advance pool: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Allocation{}, fmt.Errorf("pool %s allocation cursor changed unexpectedly", prefix)
	}
	return Allocation{
		MAC: mac, Board: board, Prefix: prefix, Suffix: suffix,
		State: "reserved", UUID: uuid, ReservedAt: timestamp, UpdatedAt: timestamp,
	}, nil
}

func (s *Store) Confirm(ctx context.Context, board, mac, uuid string) error {
	return s.changeState(ctx, board, mac, uuid, "assigned")
}

func (s *Store) Release(ctx context.Context, board, mac string) error {
	return s.changeState(ctx, board, mac, "", "released")
}

func (s *Store) changeState(ctx context.Context, board, mac, uuid, target string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get sqlite connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin state transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var currentState, currentUUID string
	err = conn.QueryRowContext(ctx, `SELECT state, COALESCE(uuid, '') FROM allocations WHERE mac_addr = ? AND board = ?`, mac, board).Scan(&currentState, &currentUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read allocation: %w", err)
	}

	timestamp := now()
	switch target {
	case "assigned":
		if currentState != "reserved" && currentState != "assigned" {
			return ErrInvalidState
		}
		if uuid != "" && !strings.EqualFold(uuid, currentUUID) {
			return ErrUUIDMismatch
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE allocations SET state = 'assigned', assigned_at = COALESCE(assigned_at, ?), updated_at = ?
			WHERE mac_addr = ? AND board = ?`, timestamp, timestamp, mac, board); err != nil {
			return fmt.Errorf("confirm allocation: %w", err)
		}
	case "released":
		if _, err := conn.ExecContext(ctx, `
			UPDATE allocations
			SET state = 'released', uuid = NULL, reserved_at = NULL, assigned_at = NULL, updated_at = ?
			WHERE mac_addr = ? AND board = ?`, timestamp, mac, board); err != nil {
			return fmt.Errorf("release allocation: %w", err)
		}
	default:
		return fmt.Errorf("unsupported target state %q", target)
	}

	event := "release"
	if target == "assigned" {
		event = "confirm"
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO audit_log(event, board, mac_addr, uuid, created_at)
		VALUES(?, ?, ?, ?, ?)`, event, board, mac, currentUUID, timestamp); err != nil {
		return fmt.Errorf("write state audit log: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit state transaction: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) Available(ctx context.Context, board, mac string) (bool, error) {
	if len(mac) != 12 || mac[8:] == "0000" {
		return false, ErrPoolMismatch
	}
	var poolBoard string
	if err := s.db.QueryRowContext(ctx, `SELECT board FROM pools WHERE prefix = ? AND enabled = 1`, mac[:8]).Scan(&poolBoard); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrPoolMismatch
		}
		return false, fmt.Errorf("read pool: %w", err)
	}
	if poolBoard != board {
		return false, ErrPoolMismatch
	}
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM allocations WHERE mac_addr = ?`, mac).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read MAC state: %w", err)
	}
	return state == "released", nil
}

func (s *Store) PoolStats(ctx context.Context) ([]PoolStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.board, p.prefix,
		       65535 AS total,
		       65535 - COALESCE(r.reserved, 0) - COALESCE(r.assigned, 0) AS free,
		       COALESCE(r.reserved, 0) AS reserved,
		       COALESCE(r.assigned, 0) AS assigned
		FROM pools p
		LEFT JOIN (
			SELECT prefix,
			       SUM(CASE WHEN state = 'reserved' THEN 1 ELSE 0 END) AS reserved,
			       SUM(CASE WHEN state = 'assigned' THEN 1 ELSE 0 END) AS assigned
			FROM allocations GROUP BY prefix
		) r ON r.prefix = p.prefix
		WHERE p.enabled = 1
		ORDER BY p.board, p.priority, p.prefix`)
	if err != nil {
		return nil, fmt.Errorf("query pool statistics: %w", err)
	}
	defer rows.Close()

	var result []PoolStats
	for rows.Next() {
		var stat PoolStats
		if err := rows.Scan(&stat.Board, &stat.Prefix, &stat.Total, &stat.Free,
			&stat.Reserved, &stat.Assigned); err != nil {
			return nil, fmt.Errorf("scan pool statistics: %w", err)
		}
		stat.RangeStart = stat.Prefix + "0001"
		stat.RangeEnd = stat.Prefix + "ffff"
		result = append(result, stat)
	}
	return result, rows.Err()
}

func (s *Store) PoolStat(ctx context.Context, prefix string) (PoolStats, error) {
	stats, err := s.PoolStats(ctx)
	if err != nil {
		return PoolStats{}, err
	}
	for _, stat := range stats {
		if stat.Prefix == prefix {
			return stat, nil
		}
	}
	return PoolStats{}, ErrNotFound
}

func (s *Store) List(ctx context.Context, filter ListFilter) ([]Allocation, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	query := `SELECT mac_addr, board, prefix, suffix, state, COALESCE(uuid, ''),
		COALESCE(reserved_at, ''), COALESCE(assigned_at, ''), updated_at
		FROM allocations WHERE 1 = 1`
	var args []any
	if filter.Query != "" {
		query += ` AND (mac_addr LIKE ? OR COALESCE(uuid, '') LIKE ?)`
		value := "%" + strings.ToLower(filter.Query) + "%"
		args = append(args, value, value)
	}
	if filter.Board != "" {
		query += ` AND board = ?`
		args = append(args, filter.Board)
	}
	if filter.Prefix != "" {
		query += ` AND prefix = ?`
		args = append(args, filter.Prefix)
	}
	if filter.State != "" {
		query += ` AND state = ?`
		args = append(args, filter.State)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, filter.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list allocations: %w", err)
	}
	defer rows.Close()
	var result []Allocation
	for rows.Next() {
		var allocation Allocation
		if err := rows.Scan(&allocation.MAC, &allocation.Board, &allocation.Prefix, &allocation.Suffix,
			&allocation.State, &allocation.UUID, &allocation.ReservedAt, &allocation.AssignedAt,
			&allocation.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan allocation: %w", err)
		}
		result = append(result, allocation)
	}
	return result, rows.Err()
}

func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event, COALESCE(board, ''), COALESCE(mac_addr, ''),
		       COALESCE(uuid, ''), COALESCE(detail, ''), created_at
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit log: %w", err)
	}
	defer rows.Close()

	var result []AuditEvent
	for rows.Next() {
		var event AuditEvent
		if err := rows.Scan(&event.ID, &event.Event, &event.Board, &event.MAC,
			&event.UUID, &event.Detail, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit log: %w", err)
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func uuidForMAC(mac string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	uuid := encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + mac
	return uuid, nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
