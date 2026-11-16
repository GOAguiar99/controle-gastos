#!/bin/sh
# Restore test: loads the newest backup into a scratch database, checks that
# every table came back and prints row counts next to the live database, then
# drops the scratch copy. Run monthly (and after any backup change):
#   sudo sh /usr/local/lib/controle-gastos/restore-check.sh
# Exit status is non-zero when the restore fails or a table is missing.
set -eu
DB="${DB_NAME:-controle-gastos}"
DEST="${BACKUP_DIR:-/var/backups/controle-gastos}"
AS_PG="${AS_PG:-runuser -u postgres --}"
SCRATCH="restore_check_$(date +%s)"
DUMP="${1:-$(ls -1t "$DEST"/app-*.dump 2>/dev/null | head -1)}"
[ -n "$DUMP" ] && [ -f "$DUMP" ] || { echo "nenhum backup encontrado em $DEST" >&2; exit 1; }

cleanup() { $AS_PG dropdb --if-exists "$SCRATCH" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "backup: $DUMP ($(du -h "$DUMP" | cut -f1), $(date -r "$DUMP" '+%F %T'))"
$AS_PG createdb "$SCRATCH"
# --no-owner: the scratch copy belongs to postgres; the dump is read from stdin
# so the postgres user doesn't need access to the backup directory.
$AS_PG pg_restore --no-owner --exit-on-error -d "$SCRATCH" < "$DUMP"

status=0
printf '%-20s %10s %10s\n' tabela backup atual
for t in schema_migrations ledgers users ledger_members categories transactions recurrences debts cash_snapshots sessions mcp_tokens; do
  r=$($AS_PG psql -XtAc "SELECT count(*) FROM $t" "$SCRATCH" 2>/dev/null) || { echo "FALTA no backup: $t" >&2; status=1; continue; }
  l=$($AS_PG psql -XtAc "SELECT count(*) FROM $t" "$DB" 2>/dev/null || echo '?')
  printf '%-20s %10s %10s\n' "$t" "$r" "$l"
done
# RLS policies must come back too, or a restored DB would not isolate ledgers
p=$($AS_PG psql -XtAc "SELECT count(*) FROM pg_policies WHERE policyname = 'tenant'" "$SCRATCH")
echo "políticas RLS restauradas: $p"
[ "$p" -ge 7 ] || { echo "políticas RLS ausentes no backup" >&2; status=1; }
[ $status -eq 0 ] && echo "RESTAURAÇÃO OK" || echo "RESTAURAÇÃO FALHOU" >&2
exit $status
