package app

import (
	"log/slog"
	"os"
)

// auditLog writes security events as JSON lines to stderr (journald on the
// VM), tagged audit=true so they can be filtered and alerted on. Callers pass
// ids and categories only: never tokens, cookies, emails or financial text.
var auditLog = slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("audit", true)

// OnAudit, when set, is called with each event name (metrics counters). Set
// it once at startup, before serving.
var OnAudit func(event string)

// Audit records a security-relevant event.
func Audit(event string, attrs ...any) {
	auditLog.Info(event, attrs...)
	if OnAudit != nil {
		OnAudit(event)
	}
}
