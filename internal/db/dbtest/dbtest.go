// Package dbtest gives each test its own PostgreSQL database.
//
// Set TEST_DATABASE_URL to an admin connection (it must be allowed to create
// roles and databases), e.g. for a local container:
//
//	docker run -d --name cg-pg -e POSTGRES_PASSWORD=dev -p 55432:5432 postgres:17-alpine
//	export TEST_DATABASE_URL=postgres://postgres:dev@localhost:55432/postgres?sslmode=disable
//
// Tests connect as the ordinary role cg_test, never as the admin, so
// Row-Level Security applies exactly as in production.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"controle-gastos/internal/db"
)

const role, rolePassword = "cg_test", "cg_test"

// Open creates a fresh database, applies the migrations and returns it. The
// database is dropped when the test ends. Without TEST_DATABASE_URL the test
// is skipped (it fails instead when CI is set, so CI never skips silently).
func Open(t testing.TB) *db.DB {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL não definida no CI")
		}
		t.Skip("TEST_DATABASE_URL não definida; veja internal/db/dbtest")
	}
	ctx := context.Background()
	ac, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Close(ctx)
	// Parallel test packages may race to create the role.
	_, err = ac.Exec(ctx, `DO $$ BEGIN
		CREATE ROLE `+role+` LOGIN PASSWORD '`+rolePassword+`';
	EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	rand.Read(b)
	name := "cg_test_" + hex.EncodeToString(b)
	if _, err := ac.Exec(ctx, `CREATE DATABASE `+name+` OWNER `+role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		c.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, rolePassword)
	u.Path = "/" + name
	d, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

// Ledger opens a fresh database and returns the store of a new, seeded ledger.
func Ledger(t testing.TB) *db.Store {
	t.Helper()
	d := Open(t)
	id, err := d.CreateLedger(context.Background(), "Teste")
	if err != nil {
		t.Fatal(err)
	}
	return d.Ledger(id)
}
