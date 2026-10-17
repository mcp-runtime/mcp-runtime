package clickhouse

import (
	"context"
	"crypto/tls"
	"fmt"
	"regexp"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"mcp-runtime/pkg/serviceutil"
)

// Client wraps a ClickHouse connection.
type Client struct {
	Conn   clickhouse.Conn
	DBName string
}

// Config provides ClickHouse connection configuration.
type Config struct {
	Addr        string
	Database    string
	DialTimeout time.Duration
	TLS         *tls.Config
	Username    string
	Password    string
}

// NewClient creates a new ClickHouse client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}

	if err := ValidateDBName(cfg.Database); err != nil {
		return nil, fmt.Errorf("invalid database name: %w", err)
	}

	options, err := Options(cfg)
	if err != nil {
		return nil, err
	}
	conn, err := clickhouse.Open(options)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ClickHouse: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to ping ClickHouse: %w", err)
	}

	return &Client{Conn: conn, DBName: cfg.Database}, nil
}

// Options is shared by every native ClickHouse caller so direct driver users
// cannot accidentally omit configured analytics transport encryption.
func Options(cfg Config) (*clickhouse.Options, error) {
	if cfg.TLS == nil {
		var err error
		cfg.TLS, err = serviceutil.ClientTLSFromEnv("CLICKHOUSE")
		if err != nil {
			return nil, err
		}
	}
	return &clickhouse.Options{
		Addr: []string{cfg.Addr},
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
		DialTimeout: cfg.DialTimeout,
		TLS:         cfg.TLS,
	}, nil
}

// Close closes the ClickHouse connection.
func (c *Client) Close() error {
	return c.Conn.Close()
}

// ValidateDBName validates ClickHouse database name format.
func ValidateDBName(name string) error {
	if name == "" {
		return fmt.Errorf("empty")
	}
	matched, err := regexp.MatchString(`^[A-Za-z_][A-Za-z0-9_]*$`, name)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("must match ^[A-Za-z_][A-Za-z0-9_]*$")
	}
	return nil
}

// Ping checks the database connection.
func (c *Client) Ping(ctx context.Context) error {
	return c.Conn.Ping(ctx)
}
