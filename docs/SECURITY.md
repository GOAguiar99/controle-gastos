# Segurança e privacidade — Controle de Gastos

Referência: OWASP ASVS 5.0 nível 2 e LGPD. Este documento descreve os controles existentes, os
procedimentos operacionais e o que ainda é responsabilidade de quem opera o serviço.
Última revisão: 16/11/2026.

## 1. Arquitetura e superfície

- Um binário Go (`net/http`, `html/template`) atrás do Caddy (HTTPS automático, Let's Encrypt),
  numa VM Oracle Cloud Always Free (São Paulo). O app escuta só em `127.0.0.1:8080`.
- PostgreSQL na mesma VM, **só socket Unix** (`listen_addresses = ''`), autenticação *peer*, sem senha.
- Login: Google OIDC (code flow + PKCE + `state` + `nonce`). Identidade = claim `sub`.
- Superfícies: UI web (sessão por cookie) e MCP em `/mcp/{token}` (link secreto por usuário).
- Administração: **só por SSH** na VM (`controle-gastos suspend-user|unsuspend-user|delete-user`).
  Não existe papel de administrador na web, então nenhuma sessão web alcança dados de outra pessoa.

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
| Contas | suspensão (bloqueia login, sessões e links MCP); exclusão pelo usuário e pelo operador | CLI, Config |
| Titular (LGPD) | exportação completa em JSON, CSV, exclusão de conta self-service | Config |
| Auditoria | eventos JSON `"audit":true` no journald: login ok/falha, logout, rate limit, links MCP, exportação, exclusão, suspensão, negação de acesso; sem tokens, e-mails ou descrições financeiras; retenção 6 meses | `internal/app/audit.go` |
| Segredos | só em `/etc/controle-gastos.env` (0600, root); nada no git (gitleaks no CI, histórico limpo); `.gitignore` bloqueia `.env`, dumps e planilhas | CI |
| Dependências | toolchain Go fixado com correções (`toolchain go1.26.11`); govulncheck no CI | `go.mod`, CI |
| Backup | `pg_dump` diário (14 cópias, 0600) + teste de restauração mensal automatizado (`restore-check.sh`) | `deploy/` |
| systemd | usuário sem privilégios, `ProtectSystem=strict`, `NoNewPrivileges`, etc. | `controle-gastos.service` |

## 3. Riscos conhecidos e aceitos

1. **O papel do app é dono das tabelas** (roda as migrations). Um invasor com execução de SQL
   arbitrário como esse papel poderia desligar o RLS. Não há SQL dinâmico com entrada do usuário, então
   o risco é residual. Mitigação futura: papel separado para migrations e papel de runtime só com DML.
2. **Links MCP são credenciais ao portador na URL.** Quem tiver o link acessa a carteira. Mitigado por
   256 bits de entropia, hash no banco, revogação, rate limit e o Caddy sem log de acesso. Se ativar
   logs no Caddy, filtre `/mcp/*`.
3. **Backup na mesma VM.** Se a VM ou o disco forem perdidos, os dumps vão junto. Ver §5 (cópia fora da VM).
4. **Rate limit em memória**: zera ao reiniciar o app e vale só para uma instância (que é o caso).
5. **Sem alertas ativos**: eventos ficam no journald; ninguém é avisado automaticamente (§4.3).

## 4. Procedimentos operacionais

### 4.1 Acesso administrativo
- Acesso à VM só por chave SSH (sem senha). Ative **MFA na conta Oracle Cloud** e na conta Google que
  é dona do cliente OAuth e do GitHub (secrets de deploy). Este é o "MFA de administrador": a única
  forma de administrar é pelo console/SSH.
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
- Eventos: `journalctl -u controle-gastos -o cat | grep '"audit":true'`.
- Sinais a observar: picos de `login_failure`, `rate_limited`, `authz_denied`, `account_deleted`.
- Falhas de backup/restauração: `systemctl --failed` e `systemctl status controle-gastos-restore-check`.
- Recomendado (grátis): um monitor de uptime externo em `https://<domínio>/healthz` e um
  ping de heartbeat (ex.: Healthchecks.io) no fim do `backup.sh`.

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

- **RPO: 24 h** (dump diário às 03:30). **RTO: ~1 h** (nova VM + `setup-vm.sh` + `pg_restore`).
- Criptografia em repouso: o disco da VM (boot volume) é criptografado pela Oracle (chaves gerenciadas
  pela Oracle, AES-256). Os dumps ficam nesse disco, com permissão 0600 (root).
- **Ação manual necessária — cópia fora da VM**: no console da Oracle, *Block Storage → Boot Volumes →
  (volume da VM) → Backup policy*, atribua uma política (o Always Free inclui 5 backups de volume),
  que ficam no Object Storage, separados da VM e criptografados.
- Teste de restauração: automático todo mês (`controle-gastos-restore-check.timer`) e manual com
  `sudo sh /usr/local/lib/controle-gastos/restore-check.sh`. Ele restaura o dump mais novo num banco
  temporário, confere tabelas, contagens e as políticas RLS, e apaga a cópia.

**Recuperar em uma VM nova:**
```sh
sudo sh deploy/setup-vm.sh <dominio>              # Caddy, Postgres, serviço, timers
sudo cp /root/controle-gastos.env /etc/controle-gastos.env   # env guardado fora do repositório
# antes de iniciar o app (banco vazio); como postgres e SEM --no-owner: os objetos voltam com o
# dono controle-gastos e o RLS continua valendo (testado em 16/11/2026)
sudo -u postgres pg_restore --exit-on-error -d controle-gastos < app-AAAA-MM-DD.dump
sudo systemctl start controle-gastos && curl -fsS http://127.0.0.1:8080/healthz
```
Depois, confira os totais do dashboard de uma conta de teste e o DNS.

## 6. LGPD — registro das operações de tratamento (art. 37)

| Operação | Dados | Finalidade | Base legal | Retenção | Compartilhamento |
|---|---|---|---|---|---|
| Conta e login | sub, nome, e-mail, foto (Google) | autenticar | execução de contrato (7º V) | enquanto a conta existir | Google (login) |
| Finanças | lançamentos, categorias, recorrências, dívidas, caixa | prestar o serviço | execução de contrato (7º V) | enquanto a conta existir; backups até 14 dias após exclusão | Oracle Cloud (operador, BR) |
| Sessões | hash do token, IP, user agent, datas | segurança | legítimo interesse (7º IX) | até 30 dias + revogadas | — |
| Logs de segurança | id do usuário, IP, evento | prevenção a fraude, auditoria | legítimo interesse (7º IX) | 6 meses | — |
| Conector Claude | dados da carteira, quando o usuário cria o link | a pedido do titular | execução de contrato / pedido do titular | enquanto o link existir | Anthropic (EUA, transferência a pedido do titular, art. 33 VIII/IX) |

## 7. Checklist LGPD

Feito no código:
- [x] Política de Privacidade (`/privacidade`) e Termos (`/termos`), públicos, linkados no login.
- [x] Acesso e portabilidade (JSON completo + CSV), correção (edição) e exclusão self-service.
- [x] Exclusão apaga carteira (se não for compartilhada), sessões e links; operador também pode excluir.
- [x] Minimização: só os escopos `openid email profile`; logs sem dados financeiros nem e-mail.
- [x] Medidas técnicas (§2) e procedimento de incidente (§4.4) e registro de operações (§6).

Pendente (operador / jurídico):
- [ ] Preencher `APP_CONTROLLER_NAME` e `APP_CONTACT_EMAIL` (canal do titular; o encarregado pode
      ser dispensado para agente de pequeno porte, Res. CD/ANPD nº 2/2022, mas o canal é obrigatório).
- [ ] Revisão jurídica da Política e dos Termos (são rascunhos técnicos).
- [ ] Confirmar enquadramento como agente de pequeno porte (sem tratamento de alto risco em larga escala).
- [ ] Manter registro de incidentes e de pedidos de titulares (planilha simples serve) e responder em até 15 dias.
- [ ] Verificação do app OAuth no Google (exigida para produção com usuários externos): domínio,
      links para a Política e os Termos, tela de consentimento.
- [ ] Rever retenção legal: o app não emite documento fiscal nem guarda dados de pagamento hoje; se
      houver cobrança no futuro, dados de faturamento terão prazos legais próprios (ex.: 5 anos, CTN).

## 8. Validação contínua (CI)

Todo push/PR roda: `gofmt`, `go vet`, testes com PostgreSQL real (incluindo isolamento entre usuários
por HTTP, sessão, logout, suspensão, exclusão, exportação, cabeçalhos, limites e rate limit),
`govulncheck` e `gitleaks` no histórico completo. O deploy só acontece se tudo passar.
Faça uma revisão de segurança manual a cada mudança de autenticação/autorização e a cada 6 meses.
