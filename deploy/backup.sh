#!/bin/sh
# Daily dump of the PostgreSQL database (custom format, restore with
# pg_restore); keeps the last 14. Runs as root, dumps as the app's role.
set -eu
DB="${DB_NAME:-controle-gastos}"
DEST="${BACKUP_DIR:-/var/backups/controle-gastos}"
install -d -m 700 "$DEST"
OUT="$DEST/app-$(date +%F).dump"
runuser -u controle-gastos -- pg_dump -Fc "$DB" > "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
chmod 600 "$OUT"
ls -1t "$DEST"/app-*.dump | tail -n +15 | xargs -r rm -f
