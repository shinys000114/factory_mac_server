package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	FirstSuffix = 0x0001
	LastSuffix  = 0xffff
)

var (
	boardNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	prefixPattern    = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

type Config struct {
	Server   ServerConfig           `toml:"server"`
	Web      WebConfig              `toml:"web"`
	Database DatabaseConfig         `toml:"database"`
	Boards   map[string]BoardConfig `toml:"boards"`

	aliases map[string]string
}

type ServerConfig struct {
	Listen              string `toml:"listen"`
	Timezone            string `toml:"timezone"`
	ReadTimeoutSeconds  int    `toml:"read_timeout_seconds"`
	WriteTimeoutSeconds int    `toml:"write_timeout_seconds"`
}

type WebConfig struct {
	Listen string `toml:"listen"`
}

type DatabaseConfig struct {
	Path          string `toml:"path"`
	BusyTimeoutMS int    `toml:"busy_timeout_ms"`
	BackupDir     string `toml:"backup_dir"`
}

type BoardConfig struct {
	Aliases  []string `toml:"aliases"`
	Prefixes []string `toml:"prefixes"`
}

type Pool struct {
	Board    string
	Prefix   string
	Priority int
}

func Load(path string) (*Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(string(contents), path)
}

func Parse(contents, path string) (*Config, error) {
	var cfg Config

	metadata, err := toml.Decode(contents, &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return nil, fmt.Errorf("unknown config key %q", undecoded[0].String())
	}
	cfg.setDefaults(path)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) setDefaults(configPath string) {
	if c.Server.Listen == "" {
		c.Server.Listen = "0.0.0.0:8880"
	}
	if c.Server.Timezone == "" {
		c.Server.Timezone = "Asia/Seoul"
	}
	if c.Server.ReadTimeoutSeconds == 0 {
		c.Server.ReadTimeoutSeconds = 10
	}
	if c.Server.WriteTimeoutSeconds == 0 {
		c.Server.WriteTimeoutSeconds = 15
	}
	if c.Database.Path == "" {
		c.Database.Path = filepath.Join(filepath.Dir(configPath), "mac-server.db")
	}
	if c.Database.BusyTimeoutMS == 0 {
		c.Database.BusyTimeoutMS = 5000
	}
}

func (c *Config) validate() error {
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("server.listen: %w", err)
	}
	if c.Web.Listen != "" {
		if _, _, err := net.SplitHostPort(c.Web.Listen); err != nil {
			return fmt.Errorf("web.listen: %w", err)
		}
		if c.Web.Listen == c.Server.Listen {
			return fmt.Errorf("web.listen must differ from server.listen; leave it empty to use the API listener")
		}
	}
	if c.Server.ReadTimeoutSeconds < 1 || c.Server.WriteTimeoutSeconds < 1 {
		return fmt.Errorf("server timeouts must be positive")
	}
	if _, err := time.LoadLocation(c.Server.Timezone); err != nil {
		return fmt.Errorf("server.timezone: %w", err)
	}
	if c.Database.BusyTimeoutMS < 1 {
		return fmt.Errorf("database.busy_timeout_ms must be positive")
	}
	if len(c.Boards) == 0 {
		return fmt.Errorf("at least one [boards.<name>] section is required")
	}

	aliases := make(map[string]string)
	prefixOwner := make(map[string]string)
	normalizedBoards := make(map[string]BoardConfig, len(c.Boards))
	for rawName, board := range c.Boards {
		name := normalize(rawName)
		if !boardNamePattern.MatchString(name) {
			return fmt.Errorf("invalid board name %q", rawName)
		}
		if len(board.Prefixes) == 0 {
			return fmt.Errorf("boards.%s.prefixes must not be empty", rawName)
		}
		if _, exists := normalizedBoards[name]; exists {
			return fmt.Errorf("duplicate board name %q", name)
		}

		seenPrefix := make(map[string]struct{})
		for i, rawPrefix := range board.Prefixes {
			prefix := normalize(rawPrefix)
			if !prefixPattern.MatchString(prefix) {
				return fmt.Errorf("boards.%s.prefixes[%d] must be exactly 8 hexadecimal characters", rawName, i)
			}
			if _, exists := seenPrefix[prefix]; exists {
				return fmt.Errorf("duplicate prefix %q in boards.%s", prefix, rawName)
			}
			if owner, exists := prefixOwner[prefix]; exists {
				return fmt.Errorf("prefix %q is assigned to both %s and %s", prefix, owner, name)
			}
			seenPrefix[prefix] = struct{}{}
			prefixOwner[prefix] = name
			board.Prefixes[i] = prefix
		}

		allNames := append([]string{name}, board.Aliases...)
		for i, rawAlias := range allNames {
			alias := normalize(rawAlias)
			if !boardNamePattern.MatchString(alias) {
				return fmt.Errorf("invalid alias %q for board %s", rawAlias, name)
			}
			if owner, exists := aliases[alias]; exists && owner != name {
				return fmt.Errorf("alias %q is used by both %s and %s", alias, owner, name)
			}
			aliases[alias] = name
			if i > 0 {
				board.Aliases[i-1] = alias
			}
		}
		normalizedBoards[name] = board
	}

	c.Boards = normalizedBoards
	c.aliases = aliases
	return nil
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (c *Config) ResolveBoard(name string) (string, bool) {
	board, ok := c.aliases[normalize(name)]
	return board, ok
}

func (c *Config) ValidateReloadFrom(current *Config) error {
	if c.Server != current.Server {
		return fmt.Errorf("server settings changed; restart is required")
	}
	if c.Web != current.Web {
		return fmt.Errorf("web settings changed; restart is required")
	}
	if c.Database.Path != current.Database.Path ||
		c.Database.BusyTimeoutMS != current.Database.BusyTimeoutMS {
		return fmt.Errorf("database path or busy timeout changed; restart is required")
	}
	return nil
}

func (c *Config) Pools() []Pool {
	boardNames := make([]string, 0, len(c.Boards))
	for name := range c.Boards {
		boardNames = append(boardNames, name)
	}
	sort.Strings(boardNames)

	var pools []Pool
	for _, name := range boardNames {
		for priority, prefix := range c.Boards[name].Prefixes {
			pools = append(pools, Pool{Board: name, Prefix: prefix, Priority: priority})
		}
	}
	return pools
}

func EnsureDirectories(c *Config) error {
	for _, path := range []string{filepath.Dir(c.Database.Path), c.Database.BackupDir} {
		if path == "" || path == "." {
			continue
		}
		if err := os.MkdirAll(path, 0o750); err != nil {
			return fmt.Errorf("create directory %q: %w", path, err)
		}
	}
	return nil
}
