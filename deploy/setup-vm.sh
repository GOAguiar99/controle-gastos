#!/bin/sh
# One-time setup of a fresh Debian/Ubuntu VM. Run from a clone of this repo:
#   sudo sh deploy/setup-vm.sh seu-subdominio.duckdns.org
set -eu
DOMAIN="${1:?uso: sudo sh deploy/setup-vm.sh <dominio>}"
cd "$(dirname "$0")"

apt-get update
apt-get install -y sqlite3 curl debian-keyring debian-archive-keyring apt-transport-https gnupg

# Oracle Cloud Ubuntu images ship iptables rules that block everything but SSH.
if command -v netfilter-persistent >/dev/null && ! iptables -C INPUT -p tcp -m multiport --dports 80,443 -m conntrack --ctstate NEW -j ACCEPT 2>/dev/null; then
  iptables -I INPUT -p tcp -m multiport --dports 80,443 -m conntrack --ctstate NEW -j ACCEPT
  netfilter-persistent save
fi

# Caddy (HTTPS automático)
if ! command -v caddy >/dev/null; then
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update && apt-get install -y caddy
fi
sed "s/seu-subdominio.duckdns.org/$DOMAIN/" Caddyfile > /etc/caddy/Caddyfile
systemctl reload caddy || systemctl restart caddy

# App user, service, env file
id controle-gastos >/dev/null 2>&1 || useradd --system --home /var/lib/controle-gastos --shell /usr/sbin/nologin controle-gastos
install -m 644 controle-gastos.service /etc/systemd/system/controle-gastos.service
if [ ! -f /etc/controle-gastos.env ]; then
  install -m 600 controle-gastos.env.example /etc/controle-gastos.env
  sed -i "s/^APP_SECRET=.*/APP_SECRET=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')/" /etc/controle-gastos.env
  echo ">> Edite /etc/controle-gastos.env e preencha APP_PASSWORD_HASH"
fi

# Backups
install -d /usr/local/lib/controle-gastos /var/backups/controle-gastos
install -m 755 backup.sh /usr/local/lib/controle-gastos/backup.sh
install -m 644 controle-gastos-backup.service controle-gastos-backup.timer /etc/systemd/system/

systemctl daemon-reload
systemctl enable controle-gastos controle-gastos-backup.timer
systemctl start controle-gastos-backup.timer
echo ">> Pronto. O primeiro push na main (com os secrets DEPLOY_*) instala o binário e inicia o serviço."
