#!/bin/sh
# Runs once, when the database volume is created. The app must not use a
# superuser: data isolation between accounts relies on row-level security,
# which superusers bypass (the app refuses to start as one).
set -eu
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -v pw="$APP_DB_PASSWORD" <<'SQL'
CREATE ROLE controle LOGIN PASSWORD :'pw' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE DATABASE controle OWNER controle;
SQL
