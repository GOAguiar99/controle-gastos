#!/bin/sh
# One-time setup of a fresh Debian/Ubuntu VM. Run from a clone of this repo:
#   sudo sh deploy/setup-vm.sh seu-subdominio.duckdns.org
set -eu
DOMAIN="${1:?uso: sudo sh deploy/setup-vm.sh <dominio>}"
cd "$(dirname "$0")"

apt-get update
apt-get install -y curl debian-keyring debian-archive-keyring apt-transport-https gnupg

# Swap: the VM has 1 GB of RAM for Postgres, the app, Caddy and Alloy; swap
# absorbs peaks instead of the OOM killer (alert "SwapIntenso" if it's used a lot)
if ! swapon --show=NAME --noheadings | grep -q '^/swapfile$'; then
  [ -f /swapfile ] || { fallocate -l 1G /swapfile && chmod 600 /swapfile && mkswap /swapfile; }
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi
echo 'vm.swappiness=10' > /etc/sysctl.d/90-controle-gastos.conf
sysctl -q -p /etc/sysctl.d/90-controle-gastos.conf

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

sh setup-postgres.sh
install -m 644 controle-gastos.service /etc/systemd/system/controle-gastos.service
if [ ! -f /etc/controle-gastos.env ]; then
  install -m 600 controle-gastos.env.example /etc/controle-gastos.env
  sed -i "s/^APP_SECRET=.*/APP_SECRET=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')/" /etc/controle-gastos.env
  sed -i "s#^APP_BASE_URL=.*#APP_BASE_URL=https://$DOMAIN#" /etc/controle-gastos.env
  echo ">> Edite /etc/controle-gastos.env: GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET e APP_ALLOWED_EMAILS"
fi
# installs created before metrics existed: turn them on (loopback only)
grep -q '^METRICS_ADDR=' /etc/controle-gastos.env || echo 'METRICS_ADDR=127.0.0.1:9091' >> /etc/controle-gastos.env

# Backups: local dump + restic (encrypted) to OCI Object Storage through rclone,
# authenticated as the VM itself (instance principal). Ubuntu's rclone is too
# old for the Oracle backend (needs >= 1.61), so a pinned release is installed.
RCLONE_VERSION=v1.68.2
apt-get install -y restic
if ! rclone version 2>/dev/null | head -1 | grep -q "rclone $RCLONE_VERSION$"; then
  arch="$(dpkg --print-architecture)"
  deb="rclone-$RCLONE_VERSION-linux-$arch.deb"
  tmp="$(mktemp -d)"
  curl -fsSLo "$tmp/$deb" "https://downloads.rclone.org/$RCLONE_VERSION/$deb"
  curl -fsSLo "$tmp/SHA256SUMS" "https://downloads.rclone.org/$RCLONE_VERSION/SHA256SUMS"
  (cd "$tmp" && grep " $deb\$" SHA256SUMS | sha256sum -c -)
  dpkg -i "$tmp/$deb"
  rm -rf "$tmp"
fi
install -d -m 700 /var/cache/restic
if [ ! -f /etc/controle-gastos-backup.env ]; then
  install -m 600 controle-gastos-backup.env.example /etc/controle-gastos-backup.env
  echo ">> Edite /etc/controle-gastos-backup.env: RCLONE_CONFIG_OCI_NAMESPACE, HC_BACKUP_URL e HC_RESTORE_URL"
fi
# installs created before this setting existed: the bucket already exists, never let rclone create it
grep -q '^RCLONE_CONFIG_OCI_NO_CHECK_BUCKET=' /etc/controle-gastos-backup.env \
  || echo 'RCLONE_CONFIG_OCI_NO_CHECK_BUCKET=true' >> /etc/controle-gastos-backup.env
if [ ! -f /etc/controle-gastos-restic.pass ]; then
  (umask 077 && head -c 32 /dev/urandom | base64 > /etc/controle-gastos-restic.pass)
  echo ">> Senha do restic gerada em /etc/controle-gastos-restic.pass. COPIE-A AGORA para o seu gerenciador"
  echo "   de senhas (sudo cat /etc/controle-gastos-restic.pass): sem ela os backups remotos não podem ser lidos."
fi
install -d /usr/local/lib/controle-gastos /var/backups/controle-gastos
install -m 755 backup.sh restore-check.sh /usr/local/lib/controle-gastos/
install -m 644 controle-gastos-backup.service controle-gastos-backup.timer \
  controle-gastos-restore-check.service controle-gastos-restore-check.timer /etc/systemd/system/

# Audit log retention (journald)
install -d /etc/systemd/journald.conf.d
install -m 644 journald-controle-gastos.conf /etc/systemd/journald.conf.d/controle-gastos.conf
systemctl restart systemd-journald

# Monitoring: Grafana Alloy (metrics of the app, the VM and Postgres, plus
# audit events without IPs) to Grafana Cloud. Credentials in /etc/alloy/grafana-cloud.env.
if ! command -v alloy >/dev/null; then
  install -d -m 755 /etc/apt/keyrings
  curl -fsSL https://apt.grafana.com/gpg.key | gpg --dearmor -o /etc/apt/keyrings/grafana.gpg
  echo "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main" > /etc/apt/sources.list.d/grafana.list
  apt-get update && apt-get install -y alloy
fi
usermod -aG systemd-journal alloy
install -m 644 monitoring/config.alloy /etc/alloy/config.alloy
install -d /etc/systemd/system/alloy.service.d
install -m 644 monitoring/alloy-override.conf /etc/systemd/system/alloy.service.d/controle-gastos.conf
if [ ! -f /etc/alloy/grafana-cloud.env ]; then
  install -m 640 -g alloy alloy.env.example /etc/alloy/grafana-cloud.env
fi
# read-only monitoring role for the Postgres exporter (peer auth over the socket, no password)
runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'alloy'" | grep -q 1 \
  || runuser -u postgres -- createuser --no-superuser --no-createdb --no-createrole alloy
runuser -u postgres -- psql -qc "GRANT pg_monitor TO alloy"

systemctl daemon-reload
systemctl enable controle-gastos controle-gastos-backup.timer controle-gastos-restore-check.timer
systemctl start controle-gastos-backup.timer controle-gastos-restore-check.timer
if grep -q '^GRAFANA_CLOUD_TOKEN=.' /etc/alloy/grafana-cloud.env; then
  systemctl enable alloy && systemctl restart alloy
else
  echo ">> Preencha /etc/alloy/grafana-cloud.env e rode: sudo systemctl enable --now alloy"
fi
echo ">> Pronto. O primeiro push na main (com os secrets DEPLOY_*) instala o binário e inicia o serviço."
