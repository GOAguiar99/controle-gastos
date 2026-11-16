// Package db opens the PostgreSQL database, runs migrations and exposes the
// ledger-scoped Store.
//
// Tenant isolation: every user-data table has Row-Level Security keyed on the
// app.ledger_id setting. The pool sets it from the context on every connection
// it hands out (PrepareConn), so a Store bound to a ledger can only see and
// write that ledger's rows, even if a query forgets a WHERE clause.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNoRows is returned when a lookup by id finds nothing (in this ledger).
var ErrNoRows = pgx.ErrNoRows

type ledgerKey struct{}

// withLedger tags ctx with the ledger whose rows queries may touch.
func withLedger(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, ledgerKey{}, id)
}

func ledgerFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(ledgerKey{}).(int64)
	return id
}

// DB is the connection pool. Use Ledger to get a Store for one ledger.
type DB struct{ pool *pgxpool.Pool }

// Open connects to url, refuses roles that would bypass RLS, and applies
// migrations.
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL inválida: %w", err)
	}
	cfg.MaxConns = 10
	// Always (re)set the tenant: a connection never carries the previous
	// user's ledger, and without one in ctx it is empty, which matches no row.
	cfg.PrepareConn = func(ctx context.Context, c *pgx.Conn) (bool, error) {
		v := ""
		if id := ledgerFrom(ctx); id > 0 {
			v = strconv.FormatInt(id, 10)
		}
		if _, err := c.Exec(ctx, `SELECT set_config('app.ledger_id', $1, false)`, v); err != nil {
			return false, err
		}
		return true, nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	d := &DB{pool: pool}
	if err := d.checkRole(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := d.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() { d.pool.Close() }

// checkRole refuses superusers and BYPASSRLS roles: RLS would not apply to them.
func (d *DB) checkRole(ctx context.Context) error {
	var bypass bool
	err := d.pool.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass)
	if err != nil {
		return err
	}
	if bypass {
		return errors.New("o usuário do banco é superuser ou BYPASSRLS; use um papel comum (o isolamento entre carteiras depende de RLS)")
	}
	return nil
}

func (d *DB) migrate(ctx context.Context) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// Serialize concurrent starts (e.g. two instances during a deploy).
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(7310591)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(7310591)`)
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, f := range files {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version = $1`, f.Name()).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + f.Name())
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", f.Name(), err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, f.Name())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// CreateLedger creates a ledger with the default categories and settings.
func (d *DB) CreateLedger(ctx context.Context, name string) (int64, error) {
	var id int64
	if err := d.pool.QueryRow(ctx, `INSERT INTO ledgers (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := d.pool.Exec(withLedger(ctx, id), `SELECT seed_current_ledger()`); err != nil {
		return 0, err
	}
	return id, nil
}

// FirstLedger returns the oldest ledger (ok=false when there is none).
// Single-ledger deployments use it until per-user ledgers exist.
func (d *DB) FirstLedger(ctx context.Context) (id int64, ok bool, err error) {
	err = d.pool.QueryRow(ctx, `SELECT id FROM ledgers ORDER BY id LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// DefaultLedger returns the first ledger, creating it when the database is new.
func (d *DB) DefaultLedger(ctx context.Context) (int64, error) {
	id, ok, err := d.FirstLedger(ctx)
	if err != nil || ok {
		return id, err
	}
	return d.CreateLedger(ctx, "Minha carteira")
}

// Ledger returns a Store whose queries only see and change ledger id.
func (d *DB) Ledger(id int64) *Store {
	return &Store{pool: d.pool, q: d.pool, ctx: withLedger(context.Background(), id), ledgerID: id}
}
