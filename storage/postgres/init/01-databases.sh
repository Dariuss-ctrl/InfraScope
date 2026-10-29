#!/bin/sh
# Runs once, on the first start of an empty data volume.
# Creates one role and database for InfraScope's configuration and a separate
# role and database for the demo service, so the two are never mixed.
# Passwords come from the container environment (see .env.example).
# The entrypoint may source this file rather than execute it, so it avoids
# shell options (like `set -u`) that would leak into the entrypoint.
set -e

psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname postgres \
  -v infrascope_password="$INFRASCOPE_DB_PASSWORD" \
  -v shop_password="$SHOP_DB_PASSWORD" <<'SQL'
CREATE ROLE infrascope LOGIN PASSWORD :'infrascope_password';
CREATE DATABASE infrascope OWNER infrascope;
REVOKE ALL ON DATABASE infrascope FROM PUBLIC;

CREATE ROLE shop LOGIN PASSWORD :'shop_password';
CREATE DATABASE shop OWNER shop;
REVOKE ALL ON DATABASE shop FROM PUBLIC;
SQL
