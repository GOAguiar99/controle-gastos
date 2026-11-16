#!/bin/sh
# Daily dump of the PostgreSQL database (custom format, restore with
# pg_restore); keeps the last 14. Runs as root and dumps as the postgres
# superuser: the app's role is subject to the forced row-level security, so
# pg_dump refuses to dump as it (it could only see one ledger).
set -eu
DB="${DB_NAME:-controle-gastos}"
DEST="${BACKUP_DIR:-/var/backups/controle-gastos}"
install -d -m 700 "$DEST"
OUT="$DEST/app-$(date +%F).dump"
(cd / && runuser -u postgres -- pg_dump -Fc "$DB") > "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
chmod 600 "$OUT"
ls -1t "$DEST"/app-*.dump | tail -n +15 | xargs -r rm -f
