#!/bin/sh
# Restore test: loads a backup into a scratch database, checks that every
# table and the RLS policies came back, prints row counts next to the live
# database, then drops the scratch copy. Runs monthly (timer) and after any
# backup change:
#   sudo sh /usr/local/lib/controle-gastos/restore-check.sh          # newest backup
#   sudo sh /usr/local/lib/controle-gastos/restore-check.sh arq.dump # a given dump
#
# With RESTIC_REPOSITORY set, the backup comes from the off-site repository
# (what a disaster recovery would use) and the repository's integrity is
# checked on a sample of its data; otherwise from the local dumps.
# Reports to Healthchecks.io when HC_RESTORE_URL is set.
# Exit status is non-zero when the restore fails or a table is missing.
set -eu
DB="${DB_NAME:-controle-gastos}"
DEST="${BACKUP_DIR:-/var/backups/controle-gastos}"
AS_PG="${AS_PG:-runuser -u postgres --}"
SCRATCH="restore_check_$(date +%s)"
WORK="$(mktemp -d /var/tmp/restore-check.XXXXXX)"
LOG="$WORK/log"

cleanup() {
  $AS_PG dropdb --if-exists "$SCRATCH" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

ping_hc() {
  [ -n "${HC_RESTORE_URL:-}" ] || return 0
  if [ -n "${2:-}" ]; then
    curl -fsS -m 10 --retry 3 -o /dev/null --data-binary "@$2" "$HC_RESTORE_URL$1" || true
  else
    curl -fsS -m 10 --retry 3 -o /dev/null "$HC_RESTORE_URL$1" || true
  fi
}

check() {
  DUMP="${1:-}"
  if [ -z "$DUMP" ] && [ -n "${RESTIC_REPOSITORY:-}" ]; then
    echo "origem: repositório remoto ($RESTIC_REPOSITORY)"
    restic check --read-data-subset=10%
    restic restore latest --tag controle-gastos --host controle-gastos --target "$WORK/restore"
    DUMP="$(find "$WORK/restore" -name 'app-*.dump' -type f | sort | tail -1)"
  elif [ -z "$DUMP" ]; then
    echo "origem: dumps locais em $DEST (RESTIC_REPOSITORY vazio)"
    DUMP="$(ls -1t "$DEST"/app-*.dump 2>/dev/null | head -1)"
  fi
  [ -n "$DUMP" ] && [ -f "$DUMP" ] || { echo "nenhum backup encontrado" >&2; return 1; }

  echo "backup: $(basename "$DUMP") ($(du -h "$DUMP" | cut -f1))"
  $AS_PG createdb "$SCRATCH"
  # --no-owner: the scratch copy belongs to postgres; the dump is read from stdin
  # so the postgres user doesn't need access to the backup files.
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
  return $status
}

ping_hc /start
set +e
check "${1:-}" > "$LOG" 2>&1
status=$?
set -e
[ $status -eq 0 ] && echo "RESTAURAÇÃO OK" >> "$LOG" || echo "RESTAURAÇÃO FALHOU" >> "$LOG"
cat "$LOG"
tail -c 10000 "$LOG" > "$LOG.tail"
ping_hc "/$status" "$LOG.tail"
exit $status
