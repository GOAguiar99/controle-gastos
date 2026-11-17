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

const instructions = `Controle de gastos pessoal (valores em reais). Este conector acessa uma carteira (os dados de uma pessoa ou de quem a compartilha) em nome do dono do link; o que você altera fica registrado como feito por ele via Claude.

Modelo:
- Lançamentos (transactions) são receitas (kind=income) ou gastos (kind=expense), com data, valor, categoria e descrição.
- Cada categoria de gasto pertence a um grupo (bucket): cartao (fatura do cartão), fixo (contas fixas) ou outros.
- Recorrências geram ocorrências previstas (projected) mês a mês até serem confirmadas (viram lançamento real) ou puladas. Use confirm_occurrence / skip_occurrence com recurrence_id + month (AAAA-MM) vindos de list_transactions(include_projected=true).
- "Caixa hoje" (set_cash) é o saldo real informado numa data: lançamentos com data até essa data já estão incluídos nele; o caixa futuro = caixa informado + lançamentos posteriores (reais e previstos).
- Dívidas podem ser simples (sem juros: cada pagamento é um gasto avulso com debt_id) ou financiamentos SAC/Price. Ao criar um financiamento, o app sugere a recorrência das parcelas (categoria Financiamento, valores pela tabela mês a mês), que fica pendente: pergunte ao usuário e use accept_recurrence ou dismiss_recurrence. Não crie outra recorrência para o mesmo financiamento.

Convenções:
- Valores de entrada em reais como número decimal (1234.56). Saídas trazem amount_cents e amount_brl.
- Datas AAAA-MM-DD, meses AAAA-MM. "Hoje" e o mês atual vêm de get_overview.
- Categorias por nome (sem diferenciar maiúsculas/acentos). Use list_categories; só crie categoria nova (create_category=true) se o usuário pedir ou nenhuma existente servir.
- Antes de lançar em lote (ex.: fatura colada), consulte list_transactions no período para não duplicar.
- Antes de excluir, confirme com o usuário qual item (mostre data, valor e descrição).

Método e importação de extratos (o uso mais comum):
- A ideia do app é concentrar o dia a dia no cartão (crédito ou débito): o grosso dos gastos vira o extrato/fatura do cartão, e o resto são contas fixas e recorrências.
- Quando o usuário mandar um extrato ou fatura (CSV, PDF, OFX, print ou texto colado), siga o fluxo do prompt importar_extrato: leia tudo, consulte list_categories e list_transactions(include_projected=true) no período, separe novos, já lançados e duvidosos, mostre a proposta e só grave com o ok do usuário, com add_transactions numa chamada só.
- Siga o padrão que a carteira já usa: se os meses anteriores têm um lançamento "Fatura cartão" por mês, lance a fatura como um total nessa categoria em vez de item a item; se os itens são lançados um a um, faça igual. Nunca lance os itens da fatura e também o pagamento da fatura (o mesmo gasto contaria duas vezes).
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
