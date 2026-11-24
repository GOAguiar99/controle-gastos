// Package mcpserver exposes the app over the Model Context Protocol so Claude
// can read and update the finances. Tools are thin adapters over app.Service:
// all rules and validation live there, exactly as for the web UI.
package mcpserver

import (
	"context"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
)

const instructions = `Controle de gastos pessoal (valores em reais; lançamentos podem ser em outras moedas). Este conector acessa uma carteira (os dados de uma pessoa ou de quem a compartilha) em nome do dono do link; o que você altera fica registrado como feito por ele via Claude.

Modelo:
- Lançamentos (transactions) são receitas (kind=income) ou gastos (kind=expense), com data, valor, categoria e descrição.
- A categoria diz o que foi comprado (Alimentação, Lazer…). Como foi pago fica em cada gasto: payment_method = credito, debito, pix, dinheiro, boleto ou outro. A mesma categoria pode ser paga de jeitos diferentes.
- O grupo do resumo (group) é derivado de cada gasto, nunca da categoria: cartao (pago no crédito; é o que o limite do cartão soma), fixo (vem de uma recorrência) ou outros.
- Sem payment_method, o gasto herda o da última compra na mesma categoria. Informe-o sempre que o extrato ou o usuário disser como pagou (fatura do cartão → credito; extrato da conta → debito, pix ou boleto).
- Cartões é um módulo que o usuário liga ou desliga (get_limits.cards_module). Desligado, as tools de cartão respondem que está desativado: lance a fatura como antes (gastos no crédito, sem card) e não ligue o módulo sem o usuário pedir.
- Compra no cartão conta no mês em que a fatura vence (como a fatura inteira sempre contou), não no mês da compra: list_transactions, get_overview e category_report de novembro trazem as compras da fatura que vence em novembro.
- Cartões (list_cards) têm dia de fechamento e de vencimento. Uma compra com card cai numa fatura (statement, nomeada pelo mês em que fecha; compra no dia do fechamento ou depois vai para a seguinte) e sai do caixa no vencimento (cash_date). Sem card, credito vai no único cartão ativo, se houver um; sem nenhum cartão, sai do caixa na data da compra. Exceção: um total de fatura (categoria "Fatura cartão") cuja descrição cita outro banco ("Fatura Bradesco") não vai para o cartão único: sai do caixa na data. Pagamento ou adiantamento de fatura feito pela conta vai como pix, debito ou boleto.
- Há dois jeitos de lançar uma fatura, e eles se combinam sem contar duas vezes: valor cheio (set_statement_total: o total entra como "Fatura cartão (não detalhado)") ou item a item (import_statement ou add_transactions com card). Se a fatura tem valor informado, as compras lançadas nela contam dentro dele e só a diferença fica sem detalhe.
- Compra parcelada: em add_transactions, installments = número de parcelas e amount = total da compra; em import_statement, cada linha "3/10" vai com installment_n=3, installments=10 e amount = valor da parcela. Em ambos as parcelas seguintes são criadas nas próximas faturas; ao importar a fatura seguinte, a linha "4/10" já existe (get_statement mostra) e não deve ser lançada de novo.
- Recorrências geram ocorrências previstas (projected) mês a mês até serem confirmadas (viram lançamento real) ou puladas. Use confirm_occurrence / skip_occurrence com recurrence_id + month (AAAA-MM) vindos de list_transactions(include_projected=true).
- O caixa pode ser dividido em contas (list_cash_accounts), cada uma com o % do CDI que rende (bruto). Com contas, set_cash recebe o saldo de cada uma (accounts) e o caixa é a soma; a projeção soma o rendimento estimado mês a mês pelo CDI do Banco Central. Esse rendimento aparece só no fluxo de caixa, não nas receitas.
- Previsão de gastos (get_forecast / set_forecast): média mensal do gasto do dia a dia (tudo que não é conta fixa/recorrência nem pagamento de dívida) nos últimos 6 meses fechados. Ligada, a projeção de caixa conta essa média em cada mês: o que já está lançado no mês entra dentro dela e só a diferença é estimada (forecast no fluxo; day_to_day = lançado + previsão). É só estimativa: não cria lançamentos e não entra nas receitas e gastos do mês. Ligue ou desligue só quando o usuário pedir; get_forecast já diz onde o caixa chega mantendo a média, mesmo desligada.
- "Caixa hoje" (set_cash) é o saldo real informado numa data: lançamentos com data até essa data já estão incluídos nele; o caixa futuro = caixa informado + lançamentos posteriores (reais e previstos).
- Dívidas podem ser simples (sem juros: cada pagamento é um gasto avulso com debt_id) ou financiamentos SAC/Price. Ao criar um financiamento, o app sugere a recorrência das parcelas (categoria Financiamento, valores pela tabela mês a mês), que fica pendente: pergunte ao usuário e use accept_recurrence ou dismiss_recurrence. Não crie outra recorrência para o mesmo financiamento.
- Outras moedas: um gasto ou receita pode ser lançado em USD, EUR etc. (add_transactions/update_transaction com currency). O app guarda o valor e a moeda originais e a cotação usada (fx_rate, reais por unidade: a PTAX de venda do Banco Central da data, ou do último dia útil antes dela; o usuário pode informar outra, como a da fatura do cartão) e converte para reais: amount_cents é sempre em reais, e é ele que conta em totais, faturas e fluxo. Saídas desses lançamentos trazem currency, original_amount e fx_rate. Recorrências, dívidas, caixa, limites e faturas importadas (import_statement) são só em reais.
- Moeda de exibição (get_currency / set_display_currency): preferência da carteira para o app mostrar os valores em outra moeda pela PTAX mais recente. Não muda o que está guardado nem as respostas das tools (sempre em reais); para falar em outra moeda com o usuário, converta com a cotação de get_currency. Troque só quando o usuário pedir.

Convenções:
- Valores de entrada em reais como número decimal (1234.56), salvo amount com currency. Saídas trazem amount_cents e amount_brl.
- Datas AAAA-MM-DD, meses AAAA-MM. "Hoje" e o mês atual vêm de get_overview.
- Categorias por nome (sem diferenciar maiúsculas/acentos). Use list_categories; só crie categoria nova (create_category=true) se o usuário pedir ou nenhuma existente servir.
- Antes de lançar em lote (ex.: fatura colada), consulte list_transactions no período para não duplicar.
- Antes de excluir, confirme com o usuário qual item (mostre data, valor e descrição).

Método e importação de extratos (o uso mais comum):
- A ideia do app é concentrar o dia a dia no cartão (crédito ou débito): o grosso dos gastos vira o extrato/fatura do cartão, e o resto são contas fixas e recorrências.
- Quando o usuário mandar um extrato ou fatura (CSV, PDF, OFX, print ou texto colado), siga o fluxo do prompt importar_extrato: leia tudo, consulte list_categories e list_transactions(include_projected=true) no período, separe novos, já lançados e duvidosos, mostre a proposta e só grave com o ok do usuário, numa chamada só (import_statement para fatura de cartão, add_transactions para extrato de conta).
- Fatura do cartão: pergunte (ou siga o padrão da carteira) se o usuário quer valor cheio ou item a item. Item a item: check_statement com as compras lidas (mostra o que já está no app e a categoria usada antes) e depois import_statement só com as novas, cada uma na sua categoria. Duplicadas com existing_amount diferente: corrija o valor do lançamento existente com update_transaction, não importe de novo.
- Memória de categorias: quando o usuário corrige a categoria de uma compra (na fatura ou com recategorize), o app lembra o estabelecimento. check_statement traz essa escolha como suggested_category com suggested_from "memória: ...": use-a sempre, mesmo que você pense diferente. Se o usuário disser que algo está errado, use recategorize (não update_transaction) para corrigir e ensinar.
- Categorize pelo que o estabelecimento é, não pelo nome no extrato. Para linhas com needs_research cujo nome não é óbvio (razão social, nome de pessoa, prefixo de CNPJ como "50921356roberto", nomes cortados), pesquise na web (nome + cidade da fatura, ou o CNPJ) antes de escolher a categoria; diga ao usuário o que encontrou. Sem resultado, use Outros e pergunte. Prefixos como Ifd*, Vmt*, Dl*, Mp*, Jim.Com* são intermediários (iFood, máquina, 99/Uber, Mercado Pago), não o estabelecimento. Importar é só por aqui: o app não tem tela de importação, você lê a fatura e categoriza. Valor cheio: set_statement_total. Se get_statement avisar de gastos no crédito sem cartão no mês (um "Fatura cartão" total antigo), pergunte qual manter. Nunca lance também o pagamento da fatura (o débito na conta): o cartão já conta o gasto.
- Duplicado = mesma data (±3 dias, compras podem compensar depois) e mesmo valor, com descrição compatível. Linhas que batem com uma recorrência prevista viram confirm_occurrence, não lançamento novo.
- Ignore transferências entre contas da própria pessoa, estornos que se anulam e o próprio pagamento da fatura quando os itens já estão lançados. Na dúvida, pergunte.`

// New builds the MCP server with every tool registered. svcFor returns the
// Service of the ledger the request is for.
func New(svcFor func(context.Context) *app.Service) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "controle-gastos", Title: "Controle de gastos", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: instructions})
	t := &tools{svcFor: svcFor}
	t.register(s)
	addPrompts(s)
	return s
}

// Handler serves the server over Streamable HTTP, stateless (each request is
// independent, so restarts and multiple clients just work).
func Handler(svcFor func(context.Context) *app.Service) http.Handler {
	srv := New(svcFor)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The app sits behind a reverse proxy on localhost; access is guarded
		// by the secret URL (checked before this handler), not by the Host header.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        1 << 20,
	})
}

// toolErr turns service errors into tool errors Claude can act on: validation
// messages are returned as-is, anything else is logged and kept generic.
func toolErr(err error) error {
	if err == nil || app.IsValidation(err) {
		return err
	}
	log.Printf("mcp: %v", err)
	return errInternal
}

type internalError struct{}

func (internalError) Error() string { return "erro interno ao acessar os dados; tente novamente" }

var errInternal error = internalError{}
