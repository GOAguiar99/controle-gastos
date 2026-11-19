package db

import (
	"context"
)

// OpenUntil opens url applying only the migrations named before stop, so a
// test can write data in an older schema and then call MigrateRest.
func OpenUntil(ctx context.Context, url, stop string) (*DB, error) { return open(ctx, url, stop) }

// MigrateRest applies the remaining migrations.
func (d *DB) MigrateRest(ctx context.Context) error { return d.migrate(ctx, "") }

// ExecRaw runs a statement in the store's ledger, for tests that set up data
// the current API can no longer write.
func (s *Store) ExecRaw(q string, args ...any) error { return s.exec(q, args...) }
