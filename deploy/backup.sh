#!/bin/sh
# Daily backup of the PostgreSQL database.
#
# 1. pg_dump (custom format) to BACKUP_DIR; keeps the last LOCAL_KEEP (3) locally,
#    for a quick restore without the network.
# 2. When RESTIC_REPOSITORY is set: sends the dump to an encrypted restic
#    repository off the VM (OCI Object Storage via rclone, authenticated by the
#    VM's own identity: no cloud key on disk) and applies the retention
#    (7 daily + 4 weekly, ~35 days: the privacy policy promises that deleted
#    accounts disappear from backups within 35 days).
# 3. When HC_BACKUP_URL is set: reports start, success or failure (with the
#    log) to Healthchecks.io, which also alerts when the backup does not run.
#
# Runs as root and dumps as the postgres superuser: the app's role is subject
# to the forced row-level security, so pg_dump refuses to dump as it.
# Configuration: /etc/controle-gastos-backup.env (see controle-gastos-backup.env.example).
set -eu
DB="${DB_NAME:-controle-gastos}"
DEST="${BACKUP_DIR:-/var/backups/controle-gastos}"
LOCAL_KEEP="${LOCAL_KEEP:-3}"
TAG=controle-gastos

ping_hc() { # $1 = "/start", "/0" or "/<exit code>"; $2 = optional body file
  [ -n "${HC_BACKUP_URL:-}" ] || return 0
  if [ -n "${2:-}" ]; then
    curl -fsS -m 10 --retry 3 -o /dev/null --data-binary "@$2" "$HC_BACKUP_URL$1" || true
  else
    curl -fsS -m 10 --retry 3 -o /dev/null "$HC_BACKUP_URL$1" || true
  fi
}

run() {
  install -d -m 700 "$DEST"
  OUT="$DEST/app-$(date +%F).dump"
  (cd / && runuser -u postgres -- pg_dump -Fc "$DB") > "$OUT.tmp"
  mv "$OUT.tmp" "$OUT"
  chmod 600 "$OUT"
  ls -1t "$DEST"/app-*.dump | tail -n +"$((LOCAL_KEEP + 1))" | xargs -r rm -f
  echo "dump local: $OUT ($(du -h "$OUT" | cut -f1))"

  if [ -z "${RESTIC_REPOSITORY:-}" ]; then
    echo "AVISO: RESTIC_REPOSITORY vazio: o backup ficou só nesta VM" >&2
    return 0
  fi
  # first run: create the repository (fails, as it should, if one exists with another password)
  restic cat config >/dev/null 2>&1 || restic init
  restic backup --tag "$TAG" --host controle-gastos "$OUT"
  restic forget --tag "$TAG" --host controle-gastos --keep-daily 7 --keep-weekly 4 --prune
}

LOG="$(mktemp)"
trap 'rm -f "$LOG"' EXIT
ping_hc /start
set +e
run > "$LOG" 2>&1
status=$?
set -e
cat "$LOG"
tail -c 10000 "$LOG" > "$LOG.tail" && mv "$LOG.tail" "$LOG"
ping_hc "/$status" "$LOG"
exit $status
