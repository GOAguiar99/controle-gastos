# Controle de Gastos

App web pessoal de finanças: receitas × gastos mês a mês, gastos/receitas fixos com recorrência,
caixa atual e **fluxo de caixa futuro**. Nasceu de uma planilha e mantém o mesmo jeitão,
só que melhor.

**Stack mínima:** um binário Go (stdlib `net/http` + `html/template`) e PostgreSQL
(`pgx`, sem CGO). Os dados ficam numa *carteira*; o isolamento entre carteiras é feito por
Row-Level Security no banco. Sem framework JS, sem build de frontend; Chart.js vem embutido.

## O que tem

- **Dashboard** por mês: receitas, gastos (cartão / fixos / outros), sobra, % de poupança,
  uso do limite do cartão e status (mesmas regras da planilha).
- **Caixa hoje**: você informa o saldo atual; o app soma o que entrou/saiu desde então.
- **Fluxo de caixa futuro** (12 meses) ancorado no caixa: recorrências, lançamentos futuros e uma
  estimativa de gastos variáveis (média de cartão + outros dos últimos 3 meses, ou valor fixo).
  Mostra o menor saldo e quando fica negativo.
- **Receitas** e **Gastos** navegáveis por mês, com quebra por fonte/categoria.
- **Recorrentes** com periodicidade (mensal, bimestral, trimestral, semestral, anual) e duração
  (sem fim, N vezes, até um mês). As ocorrências são calculadas na hora: alterou o valor,
  todos os meses futuros mudam. Cada ocorrência prevista pode ser **confirmada** (com o valor real)
  ou **pulada**.
- **Dívidas** com progresso; pagamentos são gastos marcados com "abate dívida".
- Exportar CSV e JSON. Login com Google; cada pessoa tem sua carteira, e o dono pode **compartilhá-la**
  (Config → Compartilhar carteira) convidando até 4 pessoas pelo e-mail.
- **Conector MCP para seu assistente de IA** (Claude ou outro que aceite MCP remoto): ele lê e altera tudo (lança faturas, confirma recorrências,
  responde "quanto gastei com X em 2026?"). Veja abaixo.

## Usar com um assistente de IA (MCP)

O jeito mais fácil de usar: exporte o extrato/fatura do banco, mande para o assistente e peça para
"adicionar sem repetir". O servidor traz instruções para isso e dois prompts (`importar_extrato`,
`revisar_mes`), que aparecem como comandos em clientes que suportam prompts MCP. O método completo
está na página pública `/como-usar`.

O app expõe um servidor MCP (Streamable HTTP) em **links secretos**: `https://<seu-domínio>/mcp/<token>`.
Cada pessoa cria os seus em *Config → Conectar um assistente* (o link aparece uma vez só; o app guarda só o
hash) e cola:

- **Qualquer assistente com MCP remoto (HTTP):** peça para ele "configurar um conector MCP com este link".
- **Exemplo, Claude (web, desktop, celular):** Configurações → Conectores → Adicionar conector personalizado.
- **Claude Code:** `claude mcp add --transport http controle-gastos <link>`

Não há outro login nesse endereço: **quem tiver o link tem acesso total à carteira de quem o criou**,
então trate-o como senha. Se vazar, clique em *Revogar* (o link passa a responder 404). O Caddy padrão não grava
log de acesso; se você ativar `log`, filtre o caminho `/mcp/*` para o token não ir para os logs.
Exclusões são marcadas como destrutivas, então o assistente pede confirmação; lançamentos criados pelo
conector ficam com `source = 'mcp'`. Os esquemas de saída das ferramentas aceitam campos novos, para
que clientes conectados antes de um deploy não quebrem.

## Rodar localmente

Requer Go 1.26+ e PostgreSQL 15+ (um container serve).

```sh
docker run -d --name cg-pg -e POSTGRES_PASSWORD=dev -p 55432:5432 postgres:17-alpine
# o app não pode usar superuser (o RLS não se aplicaria): crie um papel comum
docker exec cg-pg psql -U postgres -c "CREATE ROLE cg LOGIN PASSWORD 'cg'" -c "CREATE DATABASE cg OWNER cg"
export DATABASE_URL='postgres://cg:cg@localhost:55432/cg?sslmode=disable'

# os testes criam um banco por teste a partir de uma conexão admin
TEST_DATABASE_URL='postgres://postgres:dev@localhost:55432/postgres?sslmode=disable' go test ./...
go build -o bin/app ./cmd/app

# importar a planilha antiga (opcional; confere os totais com a aba Dashboard)
./bin/app import "Controle Gastos.xlsx"

# servidor com login de desenvolvimento (botão "Entrar como dev"; só em localhost)
APP_INSECURE_NO_AUTH=1 ADDR=127.0.0.1:8080 ./bin/app serve   # http://localhost:8080
```

Variáveis de ambiente:

| variável | padrão | uso |
|---|---|---|
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | — | cliente OAuth do Google (tipo "Aplicativo da Web") |
| `APP_AUTH` | `google` | `google` (Login com Google) ou `password` (e-mail e senha, para instalar sem Google) |
| `APP_BASE_URL` | — | URL pública https (http só para `localhost`); o redirect autorizado no Google é `<APP_BASE_URL>/auth/google/callback` |
| `APP_SIGNUP` | `approval` | `approval`: contas novas aguardam aprovação de um admin em `/admin`; `closed`: só a lista abaixo e quem já tem conta |
| `APP_ALLOWED_EMAILS` | — | aprovados na hora (vírgulas). `*` aprova qualquer conta Google verificada: não recomendado |
| `APP_CONTROLLER_NAME`, `APP_CONTACT_EMAIL` | — | controlador e canal do titular mostrados em `/privacidade` e `/termos` (LGPD) |
| `APP_SECRET` | aleatório | assina o cookie temporário do login (32+ chars) |
| `DATABASE_URL` | — | conexão PostgreSQL (papel comum, sem superuser/BYPASSRLS) |
| `ADDR` | `:8080` | endereço HTTP |
| `APP_TZ` | `America/Sao_Paulo` | fuso usado para "hoje" |
| `METRICS_ADDR` | — | endereço das métricas Prometheus (`/metrics`), só loopback, ex.: `127.0.0.1:9091`; vazio = desligado |
| `APP_INSECURE_NO_AUTH` | — | `1` liga o login de desenvolvimento (recusado fora de localhost) |

## Instalação simples (sem Google)

Para usar só você (ou sua família) sem configurar o Google: Docker, login por e-mail e senha.

```sh
cp deploy/selfhost.env.example .env    # preencha os 3 segredos: openssl rand -hex 32
docker compose up -d --build
docker compose exec app /controle-gastos set-password voce@exemplo.com   # pede a senha (mín. 12)
```

Abra http://localhost:8080. Cada e-mail com senha tem a própria carteira. A senha só é definida pela
linha de comando (nunca por argumento ou variável de ambiente), fica guardada como hash argon2id, e
5 tentativas erradas bloqueiam a conta por 15 minutos. O app só escuta em `127.0.0.1`: para acessar de
outro computador ou do celular, coloque um proxy HTTPS na frente (ex.: Caddy) e use
`APP_BASE_URL=https://...`; o app se recusa a iniciar com `http://` fora de `localhost`.
Backup: `docker compose exec db pg_dump -U postgres -Fc controle > backup.dump`.

## Administração (instância pública)

Com `APP_SIGNUP=approval` (padrão), quem entra com uma conta Google nova vê "aguardando aprovação" e
nenhuma carteira é criada. Dê o papel de admin pela linha de comando no servidor
(`controle-gastos grant-admin voce@gmail.com`) e aprove, recuse ou suspenda contas em `/admin`. Essa
página não mostra dados financeiros e exige um login de menos de 12 horas. Detalhes em
[docs/SECURITY.md](docs/SECURITY.md).

## Hospedagem gratuita

O volume de dados é minúsculo (~300 lançamentos/ano por carteira), então o PostgreSQL roda na
mesma VM grátis, só no socket local. O app roda como um único serviço:

1. Crie uma VM no **Oracle Cloud Always Free** (VM e IP público grátis, sem prazo):
   região home *Brazil East (São Paulo)*, imagem *Canonical Ubuntu 24.04*, shape
   `VM.Standard.E2.1.Micro` (x86 — o CI compila linux/amd64), sub-rede pública com IPv4 público.
   Na *Security List* da VCN, libere entrada TCP 80 e 443 de `0.0.0.0/0`
   (o firewall interno da VM o `setup-vm.sh` abre sozinho).
2. Aponte um domínio para o IP da VM. Grátis: um subdomínio no [DuckDNS](https://www.duckdns.org).
   Se você já tem domínio na Cloudflare, pode usar Cloudflare Tunnel no lugar do Caddy.
3. Na VM (usuário `ubuntu`):
   ```sh
   git clone https://github.com/<você>/controle-gastos && cd controle-gastos
   sudo sh deploy/setup-vm.sh seu-subdominio.duckdns.org
   sudo nano /etc/controle-gastos.env   # GOOGLE_CLIENT_ID/SECRET e APP_ALLOWED_EMAILS
   ```
4. (Opcional) Leve seus dados. Do banco SQLite da versão anterior do app (copia tudo para a carteira,
   numa transação só):
   ```sh
   scp app.db vm:/tmp/ && ssh vm 'sudo -u controle-gastos env $(sudo cat /etc/controle-gastos.env | xargs) \
     controle-gastos migrate-sqlite /tmp/app.db'
   ```
   Depois reserve essa carteira para a sua conta Google (o primeiro login com esse e-mail entra nela):
   `controle-gastos claim-ledger voce@gmail.com` (com o mesmo `env`).
   Confira os totais do dashboard antes e depois; guarde o `.db` antigo como backup.
5. No GitHub, em *Settings → Secrets and variables → Actions*, crie:
   - `DEPLOY_HOST` — IP ou domínio da VM
   - `DEPLOY_USER` — usuário SSH com `sudo` sem senha (`ubuntu` na Oracle)
   - `DEPLOY_SSH_KEY` — chave privada (gere uma só para deploy e adicione a pública na VM)
   - `DEPLOY_KNOWN_HOSTS` — saída de `ssh-keyscan <host>`

A cada push na `main`, o workflow [`ci.yml`](.github/workflows/ci.yml) roda `gofmt`, `go vet` e os testes,
compila o binário estático linux/amd64, copia para a VM e reinicia o serviço (com health check).
Sem os secrets, o deploy é pulado e só o CI roda.

Backups: `controle-gastos-backup.timer` faz `pg_dump -Fc` diário (3 cópias locais em
`/var/backups/controle-gastos`) e envia ao OCI Object Storage com restic, criptografado (7 diárias +
4 semanais); `controle-gastos-restore-check.timer` testa a restauração do backup remoto todo mês. Os
dois avisam o Healthchecks.io. Configuração em `/etc/controle-gastos-backup.env`
([exemplo](deploy/controle-gastos-backup.env.example)).

Monitoramento: o app expõe métricas em `127.0.0.1:9091` e o Grafana Alloy
([config](deploy/monitoring/config.alloy)) as envia, com as da VM, do Postgres e os eventos de
auditoria sem IP, ao Grafana Cloud (free tier); alertas em
[`deploy/monitoring/alerts.yml`](deploy/monitoring/alerts.yml). Credenciais em
`/etc/alloy/grafana-cloud.env` ([exemplo](deploy/alloy.env.example)). O `setup-vm.sh` instala tudo
(e 1 GB de swap) e pode ser rodado de novo numa VM existente.

Procedimentos de segurança, recuperação, incidentes, monitoramento e LGPD:
[docs/SECURITY.md](docs/SECURITY.md).

## Estrutura

```
cmd/app/            main: serve | import | migrate-sqlite | claim-ledger | set-password | suspend-user | unsuspend-user
                    | delete-user | grant-admin | revoke-admin
internal/finance/   regras puras (meses, recorrências, resumo, fluxo de caixa) + testes
internal/db/        PostgreSQL, migrations embutidas, RLS por carteira, Store (dbtest: banco por teste)
internal/app/       a API do app: regras de negócio e validação (usada pela web e pelo MCP) + testes
internal/web/       handlers HTTP finos, login com Google (OIDC + PKCE), templates e estáticos embutidos
internal/mcpserver/ ferramentas MCP (finas, sobre internal/app) + teste em memória
internal/importer/  leitura da planilha .xlsx (stdlib) e conferência
deploy/             systemd, Caddy, backup (restic), setup da VM; monitoring/ (Alloy, alertas); selfhost/ (Docker, modo senha)
internal/metrics/   métricas Prometheus (rotas, ferramentas MCP, pool do banco), listener só local
```

## Roadmap

- Integração com Open Finance (o modelo já tem `source`/`external_id` nos lançamentos).

## Licença

MIT
