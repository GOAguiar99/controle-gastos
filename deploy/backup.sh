#!/bin/sh
# Daily consistent copy of the SQLite database; keeps the last 14.
set -eu
SRC="${DB_PATH:-/var/lib/controle-gastos/app.db}"
DEST="${BACKUP_DIR:-/var/backups/controle-gastos}"
mkdir -p "$DEST"
OUT="$DEST/app-$(date +%F).db"
rm -f "$OUT"
sqlite3 "$SRC" "VACUUM INTO '$OUT'"
ls -1t "$DEST"/app-*.db | tail -n +15 | xargs -r rm -f
