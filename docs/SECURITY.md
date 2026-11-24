# Segurança e privacidade — Controle de Gastos

Referência: OWASP ASVS 5.0 nível 2 e LGPD. Este documento descreve os controles existentes, os
procedimentos operacionais e o que ainda é responsabilidade de quem opera o serviço.
Última revisão: 23/11/2026.

## 1. Arquitetura e superfície

- Um binário Go (`net/http`, `html/template`) atrás do Caddy (HTTPS automático, Let's Encrypt),
  numa VM Oracle Cloud Always Free (São Paulo). O app escuta só em `127.0.0.1:8080`.
- PostgreSQL na mesma VM, **só socket Unix** (`listen_addresses = ''`), autenticação *peer*, sem senha.
- Login: Google OIDC (code flow + PKCE + `state` + `nonce`). Identidade = claim `sub`.
- Observabilidade: métricas Prometheus num listener só local (`METRICS_ADDR=127.0.0.1:9091`, o app
  recusa outro endereço), lidas pelo Grafana Alloy na VM e enviadas ao Grafana Cloud junto com métricas
  da VM, do Postgres e os eventos de auditoria (sem IP). Backups vão criptografados (restic) ao OCI
  Object Storage, com a identidade da VM. Ver §4.3 e §5.
- Superfícies: UI web (sessão por cookie) e MCP em `/mcp/{token}` (link secreto por usuário).
- Administração: papéis `user` e `admin`. O papel de admin só é dado por SSH
  (`controle-gastos grant-admin`), nunca por comparação de e-mail. A página `/admin` aprova, recusa,
  suspende e reativa contas e **não mostra dados financeiros**; nenhuma sessão alcança a carteira de
  outra pessoa. Exclusão de contas ativas continua só pelo próprio usuário ou por SSH (`delete-user`).
- Cadastro: `APP_SIGNUP=approval` (padrão) deixa contas novas aguardando aprovação, sem carteira e sem
  sessão; no máximo 200 pendentes ao mesmo tempo.
- Modo self-host `APP_AUTH=password` (Docker): e-mail e senha no lugar do Google.

## 2. Controles implementados

| Área | Controle | Onde |
|---|---|---|
| Isolamento entre usuários | RLS forçado em todas as tabelas de dados, `app.ledger_id` definido pelo pool a cada conexão; carteira derivada só da sessão/link no servidor; FKs compostas `(ledger_id, id)`; app recusa papel superuser/BYPASSRLS | `internal/db`, migrations 0001 |
| IDOR | IDs de outra carteira resultam em 4xx (RLS); teste HTTP cobre todos os endpoints com id | `internal/web/security_test.go` |
| OIDC | assinatura (JWKS do Google), `iss`, `aud`, `exp`, `nonce`; `state` em cookie assinado (HMAC) de 10 min, uso único; PKCE S256; exige e-mail verificado | `internal/web/auth.go` |
| Sessão | token aleatório 256 bits, só o SHA-256 no banco; `HttpOnly`, `Secure`, `SameSite=Lax`, prefixo `__Host-`; 30 dias máx., 7 dias ocioso; logout revoga no servidor | `internal/app/accounts.go` |
| CSRF | `http.CrossOriginProtection` (Sec-Fetch-Site/Origin) em toda a UI + SameSite | middleware |
| XSS | `html/template` (escape contextual); CSP `script-src 'self'`, `object-src 'none'`, `frame-ancestors 'none'` | middleware |
| SQL injection | só consultas parametrizadas (pgx) | `internal/db` |
| Cabeçalhos | HSTS (1 ano), nosniff, X-Frame-Options, Referrer-Policy, Permissions-Policy, `Cache-Control: no-store` em páginas autenticadas | middleware |
| Abuso | limite de 1 MiB por requisição (UI e MCP); rate limit 30/min por IP em `/auth/*` e 120/min por link MCP; timeouts no servidor HTTP | `ratelimit.go`, `main.go` |
| CSV | exportação neutraliza fórmulas (`= + - @`); importação de planilha é só por CLI | `handlers.go` |
| Admin | papel no banco, checado no servidor a cada operação; login com menos de 12 h; 404 para quem não é admin; não pode suspender a si nem outro admin; tudo auditado | `internal/app/admin.go` |
| Compartilhamento | só o dono convida/remove; convite preso ao e-mail verificado, 7 dias, máx. 5 pessoas; aceitar nunca apaga carteira com dados; remoção revoga sessões e links MCP; tudo auditado | `internal/app/sharing.go`, `internal/db/sharing.go` |
| Cadastro | aprovação manual; limite de 200 pendentes; recusa apaga os dados do pedido | `accounts.go` |
| Senha (self-host) | argon2id (19 MiB, t=2), 12–128 caracteres, definida só pela CLI (sem argv/env), bloqueio de 15 min após 5 erros (persistido), mesmo erro para tudo, hash falso para e-mail inexistente (tempo constante), sessões revogadas ao trocar | `password.go` |
| Contas | suspensão (bloqueia login, sessões e links MCP); exclusão pelo usuário e pelo operador | CLI, Config |
| Último acesso | `users.last_seen_at` e `mcp_tokens.last_used_at` gravados no máximo a cada 5 min (sem escrita por requisição); visível ao próprio titular, aos membros da carteira dele e ao admin (sem dados financeiros); nunca em rótulos de métricas nem em logs | `internal/app/accounts.go` |
| Titular (LGPD) | exportação completa em JSON, CSV, exclusão de conta self-service | Config |
| Inatividade | contas aprovadas, não suspensas e não admin sem acesso (web ou MCP: `users.last_seen_at`, `last_login_at`, `created_at`) por 12 meses são excluídas como na exclusão pelo titular (carteira compartilhada fica com os outros); prazo contado a partir de 01/12/2026; roda 1×/dia no `serve` (desligável com `APP_NO_INACTIVE_PURGE=1`) e por `purge-inactive [-dry-run]`; evento `account_deleted` com `by=inactivity`; sem aviso por e-mail (o app não envia e-mail) | `internal/app/trust.go` |
| Transparência | página pública `/seguranca` ("Seus dados e segurança"), mostrada após o login até o usuário confirmar a versão atual (`users.trust_version`, `app.TrustVersion`); perfil, exportação e páginas legais continuam acessíveis antes | `internal/web/trust.go` |
| Auditoria | eventos JSON `"audit":true` no journald: login ok/falha, logout, rate limit, links MCP, exportação, exclusão, suspensão, negação de acesso; sem tokens, e-mails ou descrições financeiras; retenção 6 meses | `internal/app/audit.go` |
| Segredos | só em `/etc/controle-gastos.env` (0600, root); nada no git (gitleaks no CI, histórico limpo); `.gitignore` bloqueia `.env`, dumps e planilhas | CI |
| Dependências | toolchain Go fixado com correções (`toolchain go1.26.11`); govulncheck no CI | `go.mod`, CI |
| Backup | `pg_dump` diário (3 cópias locais, 0600) + restic criptografado (AES-256) no OCI Object Storage, 7 diárias + 4 semanais (~35 dias), acesso pela identidade da VM (sem chave no disco); teste de restauração mensal a partir do repositório remoto, com `restic check`; falhas e atrasos avisados pelo Healthchecks.io | `deploy/backup.sh`, `deploy/restore-check.sh` |
| Monitoramento | `/healthz` testa o banco (503 se cair; cache de 2 s); métricas sem URL nos rótulos (só o padrão da rota: o token do MCP nunca vira rótulo); alertas versionados; uptime externo | `internal/metrics`, `deploy/monitoring/` |
| systemd | usuário sem privilégios, `ProtectSystem=strict`, `NoNewPrivileges`, etc. | `controle-gastos.service` |

## 3. Riscos conhecidos e aceitos

1. **O papel do app é dono das tabelas** (roda as migrations). Um invasor com execução de SQL
   arbitrário como esse papel poderia desligar o RLS. Não há SQL dinâmico com entrada do usuário, então
   o risco é residual. Mitigação futura: papel separado para migrations e papel de runtime só com DML.
2. **Links MCP são credenciais ao portador na URL.** Quem tiver o link acessa a carteira. Mitigado por
   256 bits de entropia, hash no banco, revogação, rate limit e o Caddy sem log de acesso. Se ativar
   logs no Caddy, filtre `/mcp/*`.
3. **Backup na mesma conta Oracle.** A cópia remota está fora da VM, mas na mesma conta (tenancy):
   quem dominar a conta Oracle, ou a VM (que tem permissão de apagar objetos do bucket para aplicar a
   retenção), pode apagar os backups. Mitigação: MFA na conta Oracle (§4.1); futuro: cópia imutável em
   outro provedor.
4. **Rate limit em memória**: zera ao reiniciar o app e vale só para uma instância (que é o caso).
5. **Alertas dependem de serviços externos gratuitos** (Grafana Cloud, Healthchecks.io), sem SLA. Se
   o Alloy parar, o alerta `VMSemMetricas` dispara; se o Grafana Cloud cair, o Healthchecks.io continua
   avisando sobre backups, mas não sobre o app.

6. **MFA de admin não é verificável pelo app**: o Google nem sempre informa se houve 2ª etapa. Exigimos
   login recente (12 h) para o admin e a verificação em duas etapas ativada nas contas Google de admin.
7. **Self-host atrás de proxy**: dentro do Docker o app vê o IP do proxy/bridge, então o rate limit por IP
   vale para todos juntos; o bloqueio por conta continua individual.

## 4. Procedimentos operacionais

### 4.1 Acesso administrativo
- Acesso à VM só por chave SSH (sem senha). Ative **MFA na conta Oracle Cloud** e na conta Google que
  é dona do cliente OAuth e do GitHub (secrets de deploy). Este é o "MFA de administrador": a única
  forma de administrar é pelo console/SSH.
- Admin: `controle-gastos grant-admin voce@gmail.com` (e `revoke-admin`). Ative a verificação em duas
  etapas na conta Google de admin. Novos cadastros aparecem em `/admin` e no evento `signup_pending`.
- Suspender: `sudo -u controle-gastos env $(sudo cat /etc/controle-gastos.env | xargs) controle-gastos suspend-user email@x.com`
  (revoga sessões na hora; links MCP param de funcionar). Reativar: `unsuspend-user`.
- O operador não tem tela para ver finanças de clientes. Se o suporte exigir olhar dados, peça ao
  usuário a exportação (Config → Baixar todos os meus dados) em vez de consultar o banco; se for
  inevitável consultar como `postgres`, registre motivo, data e quem fez no registro de incidentes/suporte.

### 4.2 Rotação de segredos (sem mudar código)
- `GOOGLE_CLIENT_SECRET`: gere outro no Google Cloud Console, troque no env, `systemctl restart controle-gastos`, apague o antigo.
- `APP_SECRET`: troque e reinicie (só derruba logins em andamento).
- Sessões: `UPDATE sessions SET revoked_at = now() WHERE revoked_at IS NULL;` como `postgres` desloga todos.
- Links MCP de todos: `UPDATE mcp_tokens SET revoked_at = now() WHERE revoked_at IS NULL;`.
- Chave de deploy: gere outra, troque `DEPLOY_SSH_KEY` no GitHub e o `authorized_keys` da VM.
- Produção e desenvolvimento usam clientes OAuth e bancos diferentes; nunca copie o env de produção.

### 4.3 Monitoramento
Peças (todas grátis):
- **App** (`internal/metrics`): em `127.0.0.1:9091/metrics` (nunca exposto pelo Caddy):
  `cg_http_request_duration_seconds{route,code}` (p50/p95/p99, req/s, taxa de 5xx; `route` é o padrão
  do ServeMux, ou `unmatched`/`rejected`), `cg_mcp_tool_duration_seconds{tool}` e
  `cg_mcp_tool_calls_total{tool,outcome}`, `cg_db_pool_*` (conexões em uso/máx., espera), `cg_audit_events_total{event}`,
  runtime Go e processo.
- **Grafana Alloy** na VM (`deploy/monitoring/config.alloy`, limite de 250 MB): coleta a cada 60 s o app,
  a VM (CPU, memória, swap, disco, rede) e o Postgres (papel `alloy` com `pg_monitor`, peer, sem senha);
  envia ao Grafana Cloud as métricas e **só** as linhas `"audit":true` do journald, com o campo `ip`
  esvaziado antes de sair da VM. Credenciais em `/etc/alloy/grafana-cloud.env` (token só com
  `metrics:write` e `logs:write`).
- **Alertas** (`deploy/monitoring/alerts.yml`): app fora, p95 > 500 ms, 5xx > 2%, pool do banco ≥ 90%,
  espera por conexão, erros internos no MCP, picos de `login_failure`/`rate_limited`/`authz_denied`,
  VM sem métricas, disco < 20%, memória < 100 MB, swap intenso, CPU > 90%. Carregar com `mimirtool`
  (instruções no topo do arquivo) e importar como regras gerenciadas pelo Grafana para usar o e-mail.
- **Uptime externo**: Grafana Cloud Synthetic Monitoring em `https://<domínio>/healthz` (2–3 locais,
  a cada 1–2 min).
- **Backups**: Healthchecks.io recebe início, sucesso ou falha (com o log) do `backup.sh` e do
  `restore-check.sh`, e avisa se não rodarem no prazo.

Consultas locais, se o Grafana estiver fora: `curl -s 127.0.0.1:9091/metrics`,
`journalctl -u controle-gastos -o cat | grep '"audit":true'`, `systemctl --failed`,
`journalctl -u alloy -n 50`.

### 4.4 Resposta a incidentes (LGPD art. 48, Resolução CD/ANPD nº 15/2024)
1. **Conter**: revogar sessões/links (§4.2), suspender contas envolvidas, tirar o serviço do ar se preciso
   (`systemctl stop controle-gastos`), girar segredos.
2. **Preservar evidências**: copiar `journalctl`, logs do Caddy e um `pg_dump` antes de mudar dados.
3. **Avaliar**: quais titulares, quais dados, desde quando, se há risco ou dano relevante.
4. **Comunicar**: se houver risco ou dano relevante, comunicar a **ANPD** e os **titulares** em até
   **3 dias úteis** do conhecimento (prazo em dobro para agente de pequeno porte, Res. CD/ANPD nº 2/2022),
   com: natureza dos dados, titulares afetados, medidas tomadas, riscos, contato.
5. **Registrar** todo incidente (mesmo sem comunicação) por no mínimo 5 anos: data, descrição, dados,
   titulares, avaliação de risco, medidas e se comunicou.
6. **Corrigir** a causa e revisar este documento.

## 5. Backup e recuperação (DR)

- **RPO: 24 h** (dump diário às 03:30). **RTO: ~1 h** (nova VM + `setup-vm.sh` + restauração).
- Onde ficam: 3 dumps locais (`/var/backups/controle-gastos`, 0600) para restauração rápida, e um
  repositório **restic** no bucket privado `controle-gastos-backups` (OCI Object Storage, São Paulo),
  com retenção de 7 diárias + 4 semanais (~35 dias, o prazo prometido na Política para dados excluídos).
- Criptografia: o restic cifra tudo (AES-256 + MAC) com a senha em `/etc/controle-gastos-restic.pass`
  antes de sair da VM; o Object Storage cifra de novo em repouso. **A senha precisa estar também fora da
  VM** (gerenciador de senhas): sem ela, os backups remotos são ilegíveis.
- Acesso ao bucket: rclone com *instance principal*. Dynamic Group `controle-gastos-vm` (domínio
  Default) com a regra `instance.id = '<OCID da VM>'` (só esta VM; a VM fica no compartimento raiz,
  onde uma regra por compartimento valeria para qualquer instância futura) e a Policy
  `controle-gastos-backup` no compartimento raiz:
  `Allow dynamic-group 'Default'/'controle-gastos-vm' to read buckets in tenancy where target.bucket.name = 'controle-gastos-backups'`
  e `Allow dynamic-group 'Default'/'controle-gastos-vm' to manage objects in tenancy where target.bucket.name = 'controle-gastos-backups'`.
  Nenhuma chave de nuvem fica na VM. **Numa VM nova (DR), troque o OCID na regra do Dynamic Group antes
  de restaurar**, senão ela não consegue ler os backups. O Always Free inclui 10 GiB de Object Storage
  Standard (sobra muito: cada dump tem poucos MB).
- Teste de restauração: automático todo mês (`controle-gastos-restore-check.timer`) e manual com
  `sudo sh /usr/local/lib/controle-gastos/restore-check.sh`. Ele roda `restic check` numa amostra de
  10% dos dados, restaura o backup remoto mais novo num banco temporário, confere tabelas, contagens e
  as políticas RLS, e apaga a cópia. Resultado no Healthchecks.io.

**Recuperar em uma VM nova:**
```sh
sudo sh deploy/setup-vm.sh <dominio>              # Caddy, Postgres, serviço, timers, restic, Alloy
sudo cp /root/controle-gastos.env /etc/controle-gastos.env   # env guardado fora do repositório
# senha do restic: a do gerenciador de senhas (a gerada pelo setup numa VM nova NÃO serve)
sudo sh -c 'umask 077; cat > /etc/controle-gastos-restic.pass'      # cole a senha, Enter, Ctrl-D
# no console da Oracle: Dynamic Group controle-gastos-vm → regra com o OCID da VM nova
sudo nano /etc/controle-gastos-backup.env         # namespace e URLs do Healthchecks
sudo sh -c 'set -a; . /etc/controle-gastos-backup.env; restic snapshots; \
  restic restore latest --tag controle-gastos --target /root/restore'
# antes de iniciar o app (banco vazio); como postgres e SEM --no-owner: os objetos voltam com o
# dono controle-gastos e o RLS continua valendo (testado em 16/11/2026)
sudo sh -c 'sudo -u postgres pg_restore --exit-on-error -d controle-gastos < $(find /root/restore -name "app-*.dump")'
sudo systemctl start controle-gastos && curl -fsS http://127.0.0.1:8080/healthz
```
Depois, confira os totais do dashboard de uma conta de teste e o DNS.

## 6. LGPD — registro das operações de tratamento (art. 37)

| Operação | Dados | Finalidade | Base legal | Retenção | Compartilhamento |
|---|---|---|---|---|---|
| Conta e login | sub, nome, e-mail, foto (Google) | autenticar | execução de contrato (7º V) | enquanto a conta existir, até 12 meses sem acesso | Google (login) |
| Pedido de cadastro | sub, nome, e-mail, foto | avaliar o acesso | procedimentos preliminares de contrato (7º V) | até aprovação ou recusa (recusa apaga) | Google (login) |
| Finanças | lançamentos, categorias, recorrências, dívidas, caixa | prestar o serviço | execução de contrato (7º V) | enquanto a conta existir (excluída após 12 meses sem acesso); backups até 35 dias após exclusão | Oracle Cloud (operador, BR) |
| Convites de carteira | e-mail convidado, quem convidou, datas | compartilhar a carteira a pedido do dono | execução de contrato (7º V) | até aceite, recusa, cancelamento ou expiração (7 dias) | — |
| Sessões | hash do token, IP, user agent, datas | segurança | legítimo interesse (7º IX) | até 30 dias + revogadas | — |
| Último acesso | data/hora do último uso da conta (web ou MCP) e de cada link MCP, gravada no máximo a cada 5 min | mostrar ao titular, às pessoas da carteira dele e ao admin (contas inativas, suporte) | legítimo interesse (7º IX) | enquanto a conta existir (só o valor mais recente) | — |
| Logs de segurança | id do usuário, IP, evento | prevenção a fraude, auditoria | legítimo interesse (7º IX) | 6 meses na VM; cópia sem IP 14 dias no Grafana Cloud | Grafana Labs (operador; só a cópia sem IP) |
| Monitoramento | métricas técnicas (latência, erros, uso da VM e do banco), sem dados pessoais | disponibilidade e segurança | legítimo interesse (7º IX) | 14 dias | Grafana Labs (operador) |
| Conector Claude | dados da carteira, quando o usuário cria o link | a pedido do titular | execução de contrato / pedido do titular | enquanto o link existir | Anthropic (EUA, transferência a pedido do titular, art. 33 VIII/IX) |

## 7. Checklist LGPD

Feito no código:
- [x] Política de Privacidade (`/privacidade`) e Termos (`/termos`), públicos, linkados no login.
- [x] Acesso e portabilidade (JSON completo + CSV), correção (edição) e exclusão self-service.
- [x] Exclusão apaga carteira (se não for compartilhada), sessões e links; operador também pode excluir.
- [x] Retenção limitada: contas sem acesso por 12 meses são excluídas automaticamente (art. 15, I e 16).
- [x] Transparência (art. 6º, VI): `/seguranca` resume em linguagem simples segurança, acesso, retenção e direitos; confirmada a cada versão.
- [x] Minimização: só os escopos `openid email profile`; logs sem dados financeiros nem e-mail.
- [x] Medidas técnicas (§2) e procedimento de incidente (§4.4) e registro de operações (§6).

Pendente (operador / jurídico):
- [x] `APP_CONTROLLER_NAME` e `APP_CONTACT_EMAIL` preenchidos na VM (canal do titular; o encarregado
      pode ser dispensado para agente de pequeno porte, Res. CD/ANPD nº 2/2022, mas o canal é obrigatório).
- [ ] Revisão jurídica da Política e dos Termos (são rascunhos técnicos).
- [ ] Confirmar enquadramento como agente de pequeno porte (sem tratamento de alto risco em larga escala).
- [ ] Manter registro de incidentes e de pedidos de titulares (planilha simples serve) e responder em até 15 dias.
- [ ] Verificação do app OAuth no Google (exigida para produção com usuários externos): domínio,
      links para a Política e os Termos, tela de consentimento.
- [ ] Aviso por e-mail antes da exclusão por inatividade (o app ainda não envia e-mail).
- [ ] Rever retenção legal: o app não emite documento fiscal nem guarda dados de pagamento hoje; se
      houver cobrança no futuro, dados de faturamento terão prazos legais próprios (ex.: 5 anos, CTN).

## 8. Validação contínua (CI)

Todo push/PR roda: `gofmt`, `go vet`, testes com PostgreSQL real (incluindo isolamento entre usuários
por HTTP, sessão, logout, suspensão, exclusão, exportação, cabeçalhos, limites e rate limit),
`govulncheck` e `gitleaks` no histórico completo. O deploy só acontece se tudo passar.
Faça uma revisão de segurança manual a cada mudança de autenticação/autorização e a cada 6 meses.
