package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("persistent document not found")

// Store persists application metadata. IMAGE POOL uses a JSONB document per
// logical aggregate so the domain stores retain their in-memory concurrency
// model while PostgreSQL remains the durable source of truth.
type Store interface {
	Load(context.Context, string, any) error
	Save(context.Context, string, any) error
	Delete(context.Context, string) error
	Health(context.Context) (Health, error)
	Close()
}

type Health struct {
	Backend     string `json:"backend"`
	Description string `json:"description"`
	DatabaseURL string `json:"database_url,omitempty"`
}

type Postgres struct {
	pool      *pgxpool.Pool
	publicURL string
}

func OpenPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return nil, fmt.Errorf("database_url is required for PostgreSQL storage")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database_url: %w", err)
	}
	config.MaxConns = 12
	config.MinConns = 1
	config.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	p := &Postgres{pool: pool, publicURL: MaskURL(databaseURL)}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS image_pool_state (
  key TEXT PRIMARY KEY,
  value JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS image_pool_state_updated_at_idx ON image_pool_state(updated_at DESC);`)
	if err != nil {
		return fmt.Errorf("migrate PostgreSQL schema: %w", err)
	}
	return nil
}

func (p *Postgres) Load(ctx context.Context, key string, dst any) error {
	var raw []byte
	err := p.pool.QueryRow(ctx, `SELECT value FROM image_pool_state WHERE key=$1`, strings.TrimSpace(key)).Scan(&raw)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return ErrNotFound
		}
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode PostgreSQL document %q: %w", key, err)
	}
	return nil
}

func (p *Postgres) Save(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO image_pool_state(key,value,updated_at) VALUES($1,$2::jsonb,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, strings.TrimSpace(key), raw)
	return err
}

func (p *Postgres) Delete(ctx context.Context, key string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM image_pool_state WHERE key=$1`, strings.TrimSpace(key))
	return err
}

func (p *Postgres) Health(ctx context.Context) (Health, error) {
	if err := p.pool.Ping(ctx); err != nil {
		return Health{Backend: "postgresql", Description: "PostgreSQL"}, err
	}
	return Health{Backend: "postgresql", Description: "PostgreSQL 数据库存储", DatabaseURL: p.publicURL}, nil
}

func (p *Postgres) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}

func MaskURL(value string) string {
	// PostgreSQL URLs contain credentials before @. Keep host/database visible
	// for diagnostics while never returning a password to the dashboard.
	start := strings.Index(value, "://")
	at := strings.LastIndex(value, "@")
	if start >= 0 && at > start+3 {
		credentials := value[start+3 : at]
		user := strings.SplitN(credentials, ":", 2)[0]
		return value[:start+3] + user + ":***@" + value[at+1:]
	}
	return value
}
