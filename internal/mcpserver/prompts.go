package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Prompts are ready-made workflows the assistant's user can pick (Claude
// Desktop and Claude Code list them as commands). Assistants without prompt
// support get the same guidance from the server instructions.

const importPrompt = `Vou te passar um extrato ou fatura (arquivo ou texto). Importe para o Controle de Gastos sem repetir nada:

1. Leia todas as linhas: data, valor, descrição. Diga quantas linhas leu, o período e o total, para eu conferir.
2. Consulte list_categories e list_transactions do período do extrato com include_projected=true (inclua um mês antes e um depois, para compras que caem em outra data).
3. Para cada linha, classifique:
   - já lançado: mesma data (±3 dias) e mesmo valor, descrição compatível;
   - recorrência prevista: bate com uma ocorrência prevista → use confirm_occurrence com o valor/data reais;
   - novo: proponha a categoria pelo nome (use as existentes; não crie categoria sem me perguntar) e a forma de pagamento (fatura do cartão → credito; extrato da conta → debito, pix ou boleto, conforme a linha);
   - ignorar: transferências entre minhas contas, estornos que se anulam, pagamento da fatura do cartão quando os itens já estão lançados;
   - dúvida: me pergunte.
4. Fatura do cartão: descubra o cartão (list_cards; se não houver, pergunte o dia de fechamento e de vencimento e cadastre com save_card) e o mês da fatura (o mês em que ela fecha). Consulte get_statement dessa fatura: o que já está nela não entra de novo (inclusive parcelas "4/10" criadas na importação anterior). Grave com import_statement, cada compra na sua categoria, com installment_n/installments nas linhas parceladas. Se eu preferir só o total, use set_statement_total. Se get_statement avisar de um "Fatura cartão" total sem cartão no mês, me pergunte antes: itens + total contariam em dobro. Nunca lance os itens e também o pagamento da fatura.
5. Me mostre uma tabela (novos, confirmações, já lançados, ignorados, dúvidas) com os totais. Não grave nada antes do meu ok.
6. Com o ok, grave os novos numa chamada só (import_statement para fatura de cartão, add_transactions para extrato de conta; é tudo ou nada) e as confirmações com confirm_occurrence. No fim, mostre o resumo do mês com get_overview.`

const reviewPrompt = `Revise o mês no Controle de Gastos e me diga o que precisa de atenção. Só leia; não altere nada sem me perguntar.

1. get_overview do mês e list_transactions do mês com include_projected=true.
2. Aponte:
   - possíveis duplicados (mesmo valor e datas próximas);
   - lançamentos sem descrição ou em categorias genéricas ("Outros") que eu deveria reclassificar;
   - recorrências previstas ainda não confirmadas (já aconteceram?) e recorrências sugeridas pendentes (list_recurrences);
   - gastos do cartão acima do limite ou sobra abaixo da meta (get_limits);
   - o que mudou em relação ao mês anterior (category_report), com os maiores aumentos;
   - se o caixa informado está antigo (último set_cash) e o ponto mais baixo projetado (get_cash_projection).
3. Termine com uma lista curta de ações sugeridas, em ordem de importância, e pergunte quais devo fazer.`

func addPrompts(s *mcp.Server) {
	month := []*mcp.PromptArgument{{Name: "mes", Title: "Mês", Description: "AAAA-MM; padrão: mês atual"}}
	s.AddPrompt(&mcp.Prompt{Name: "importar_extrato", Title: "Importar extrato sem repetir",
		Description: "Lê um extrato/fatura que você manda e lança só o que ainda não está no app, com sua confirmação."},
		staticPrompt("Importar extrato sem repetir", importPrompt))
	s.AddPrompt(&mcp.Prompt{Name: "revisar_mes", Title: "Revisar o mês", Arguments: month,
		Description: "Confere o mês: duplicados, categorias, recorrências pendentes, limites e caixa; sugere ajustes."},
		func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			text := reviewPrompt
			if m := strings.TrimSpace(req.Params.Arguments["mes"]); m != "" {
				text = "Mês a revisar: " + m + ".\n\n" + text
			}
			return result("Revisar o mês", text), nil
		})
}

func staticPrompt(desc, text string) mcp.PromptHandler {
	return func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return result(desc, text), nil
	}
}

func result(desc, text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{Description: desc, Messages: []*mcp.PromptMessage{
		{Role: "user", Content: &mcp.TextContent{Text: text}},
	}}
}
