package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db/dbtest"
)

func TestHealthzChecksDatabase(t *testing.T) {
	d := dbtest.Open(t)
	srv, err := New(context.Background(), app.NewApp(d, time.UTC), Config{DevLogin: true, Secret: []byte(strings.Repeat("k", 32))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
		return rec
	}
	if rec := get(); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("banco no ar: %d %q", rec.Code, rec.Body.String())
	}
	d.Close()
	srv.healthAt = time.Time{} // skip the 2 s cache
	if rec := get(); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("banco fora: queria 503, veio %d", rec.Code)
	}
}
