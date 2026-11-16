# Controle de Gastos

App web pessoal de finanças: receitas × gastos mês a mês, gastos/receitas fixos com recorrência,
caixa atual e **fluxo de caixa futuro**. Nasceu de uma planilha e mantém o mesmo jeitão,
só que melhor.

**Stack mínima:** um binário Go (stdlib `net/http` + `html/template`) e um arquivo SQLite
(`modernc.org/sqlite`, sem CGO). Sem framework JS, sem build de frontend; Chart.js vem embutido.

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
- Exportar CSV e baixar backup do banco. Login com senha única.

## Rodar localmente

Requer Go 1.26+.

```sh
go test ./...
go build -o bin/app ./cmd/app

# importar a planilha antiga (opcional; confere os totais com a aba Dashboard)
./bin/app import "Controle Gastos.xlsx"

# servidor sem login, só para desenvolvimento
APP_INSECURE_NO_AUTH=1 ./bin/app serve   # http://localhost:8080
```

Variáveis de ambiente:

| variável | padrão | uso |
|---|---|---|
| `APP_PASSWORD_HASH` | — | hash bcrypt da senha (`printf 'senha\n' \| ./bin/app hash-password`) |
| `APP_SECRET` | aleatório | chave HMAC do cookie de sessão (32+ chars; fixe para não deslogar a cada restart) |
| `DB_PATH` | `data/app.db` | arquivo SQLite |
| `ADDR` | `:8080` | endereço HTTP |
| `APP_TZ` | `America/Sao_Paulo` | fuso usado para "hoje" |
| `APP_INSECURE_NO_AUTH` | — | `1` desativa o login (só local!) |

## Hospedagem gratuita

O volume de dados é minúsculo (~300 lançamentos/ano → < 1 MB), então um arquivo SQLite numa VM
grátis resolve. Cloudflare Pages não executa Go nem guarda SQLite, por isso o app roda como um
único serviço:

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
   sudo nano /etc/controle-gastos.env   # cole o APP_PASSWORD_HASH
   ```
4. (Opcional) Leve seus dados: importe localmente e copie o banco antes do primeiro start:
   `scp data/app.db vm:/tmp/ && ssh vm 'sudo install -o controle-gastos -g controle-gastos -m 600 /tmp/app.db /var/lib/controle-gastos/app.db'`
5. No GitHub, em *Settings → Secrets and variables → Actions*, crie:
   - `DEPLOY_HOST` — IP ou domínio da VM
   - `DEPLOY_USER` — usuário SSH com `sudo` sem senha (`ubuntu` na Oracle)
   - `DEPLOY_SSH_KEY` — chave privada (gere uma só para deploy e adicione a pública na VM)
   - `DEPLOY_KNOWN_HOSTS` — saída de `ssh-keyscan <host>`

A cada push na `main`, o workflow [`ci.yml`](.github/workflows/ci.yml) roda `gofmt`, `go vet` e os testes,
compila o binário estático linux/amd64, copia para a VM e reinicia o serviço (com health check).
Sem os secrets, o deploy é pulado e só o CI roda.

Backups: `controle-gastos-backup.timer` faz `VACUUM INTO` diário em `/var/backups/controle-gastos`
(14 cópias). Também dá para baixar o `.db` em *Config*.

## Estrutura

```
cmd/app/            main: serve | import | hash-password
internal/finance/   regras puras (meses, recorrências, resumo, fluxo de caixa) + testes
internal/db/        SQLite, migrations embutidas, Store
internal/web/       handlers, login, templates e estáticos embutidos
internal/importer/  leitura da planilha .xlsx (stdlib) e conferência
deploy/             systemd, Caddy, backup, setup da VM
```

## Roadmap

- Integração com Open Finance (o modelo já tem `source`/`external_id` nos lançamentos).

## Licença

MIT
