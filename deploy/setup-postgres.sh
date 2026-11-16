#!/bin/sh
# Installs and configures PostgreSQL for the app (idempotent; setup-vm.sh runs it):
#   sudo sh deploy/setup-postgres.sh
set -eu
command -v psql >/dev/null || { apt-get update && apt-get install -y postgresql; }
id controle-gastos >/dev/null 2>&1 || useradd --system --home /var/lib/controle-gastos --shell /usr/sbin/nologin controle-gastos

# PostgreSQL: local socket only, peer auth (no password, no TCP). The app's role
# is an ordinary role (no superuser / BYPASSRLS): tenant isolation relies on RLS.
PGCONF="$(ls -d /etc/postgresql/*/main | sort -V | tail -1)"
sed -i "s/^#\?listen_addresses.*/listen_addresses = ''/" "$PGCONF/postgresql.conf"
cat > "$PGCONF/conf.d/controle-gastos.conf" <<'PGEOF'
# small VM (1 GB RAM)
shared_buffers = 128MB
max_connections = 20
work_mem = 4MB
PGEOF
systemctl restart postgresql
runuser -u postgres -- psql -v ON_ERROR_STOP=1 -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'controle-gastos'" | grep -q 1 \
  || runuser -u postgres -- createuser --no-superuser --no-createdb --no-createrole controle-gastos
runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname = 'controle-gastos'" | grep -q 1 \
  || runuser -u postgres -- createdb --owner controle-gastos controle-gastos
