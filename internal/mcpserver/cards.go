package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
	"controle-gastos/internal/finance"
)

type statementOut struct {
	Card       string   `json:"card"`
	CardID     int64    `json:"card_id"`
	Month      string   `json:"month" jsonschema:"mês em que a fatura fecha (AAAA-MM)"`
	Closing    string   `json:"closing_date"`
	Due        string   `json:"due_date" jsonschema:"vencimento: quando a fatura sai do caixa"`
	Open       bool     `json:"open,omitempty" jsonschema:"ainda recebe compras"`
	Items      moneyOut `json:"items" jsonschema:"soma das compras lançadas na fatura"`
	Count      int      `json:"items_count"`
	Declared   moneyOut `json:"declared_total,omitempty" jsonschema:"valor cheio informado (set_statement_total)"`
	HasTotal   bool     `json:"has_declared_total,omitempty"`
	Undetailed moneyOut `json:"undetailed" jsonschema:"parte do valor informado não coberta pelas compras"`
	Total      moneyOut `json:"total" jsonschema:"o que a fatura cobra: o maior entre a soma das compras e o valor informado"`
}

func statementOf(v app.StatementView) statementOut {
	o := statementOut{Card: v.Card.Name, CardID: v.Card.ID, Month: v.Month.String(), Closing: iso(v.Closing), Due: iso(v.Due),
		Open: v.Open, Items: money(v.Statement.Items), Count: v.Count, Undetailed: money(v.Rest()), Total: money(v.Total()), HasTotal: v.HasTotal}
	if v.HasTotal {
		o.Declared = money(v.Declared)
	}
	return o
}

type cardOut struct {
	ID         int64        `json:"id"`
	Name       string       `json:"name"`
	ClosingDay int          `json:"closing_day"`
	DueDay     int          `json:"due_day"`
	Active     bool         `json:"active"`
	Open       statementOut `json:"open_statement" jsonschema:"fatura aberta"`
}

type cardsOut struct {
	Cards []cardOut `json:"cards"`
}

func (t *tools) listCards(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, cardsOut, error) {
	out := cardsOut{Cards: []cardOut{}}
	svc := t.svcFor(ctx)
	cards, err := svc.Cards()
	if err != nil {
		return nil, out, toolErr(err)
	}
	for _, c := range cards {
		v, err := svc.Statement(c.ID, 0)
		if err != nil {
			return nil, out, toolErr(err)
		}
		out.Cards = append(out.Cards, cardOut{ID: c.ID, Name: c.Name, ClosingDay: c.ClosingDay, DueDay: c.DueDay, Active: c.Active, Open: statementOf(v)})
	}
	return nil, out, nil
}

type cardIn struct {
	ID         int64  `json:"id,omitempty" jsonschema:"para alterar um cartão existente"`
	Name       string `json:"name"`
	ClosingDay int    `json:"closing_day" jsonschema:"dia do fechamento, 1–31"`
	DueDay     int    `json:"due_day" jsonschema:"dia do vencimento, 1–31"`
	Active     *bool  `json:"active,omitempty" jsonschema:"padrão true"`
}

func (t *tools) saveCard(ctx context.Context, _ *mcp.CallToolRequest, in cardIn) (*mcp.CallToolResult, idOut, error) {
	id, err := t.svcFor(ctx).SaveCard(app.CardIn{ID: in.ID, Name: in.Name, ClosingDay: in.ClosingDay, DueDay: in.DueDay,
		Active: in.Active == nil || *in.Active})
	if err != nil {
		return nil, idOut{}, toolErr(err)
	}
	return nil, idOut{ID: id, Message: "cartão salvo"}, nil
}

type cardRefIn struct {
	ID int64 `json:"id"`
}

func (t *tools) deleteCard(ctx context.Context, _ *mcp.CallToolRequest, in cardRefIn) (*mcp.CallToolResult, msgOut, error) {
	c, err := t.svcFor(ctx).DeleteCard(in.ID)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	return nil, msgOut{Message: "cartão " + c.Name + " excluído"}, nil
}

type statementIn struct {
	Card  string `json:"card" jsonschema:"nome do cartão (list_cards)"`
	Month string `json:"month,omitempty" jsonschema:"mês em que a fatura fecha, AAAA-MM; padrão: a fatura aberta"`
}

type statementDetailOut struct {
	Statement statementOut `json:"statement"`
	Items     []txOut      `json:"items" jsonschema:"compras desta fatura (inclui recorrências previstas e parcelas)"`
	Warning   string       `json:"warning,omitempty"`
}

func (t *tools) statementView(ctx context.Context, card, mon string) (app.StatementView, error) {
	c, err := t.svcFor(ctx).CardByName(card)
	if err != nil {
		return app.StatementView{}, err
	}
	m, err := month(mon)
	if err != nil {
		return app.StatementView{}, err
	}
	return t.svcFor(ctx).Statement(c.ID, m)
}

func detailOf(v app.StatementView) statementDetailOut {
	out := statementDetailOut{Statement: statementOf(v), Items: []txOut{}}
	for _, e := range v.Items {
		out.Items = append(out.Items, txOf(e))
	}
	if len(v.Loose) > 0 {
		out.Warning = "o mês tem gastos no crédito sem cartão (ex.: \"Fatura cartão\" lançada como total); lançar os itens também contaria em dobro: pergunte ao usuário se exclui o total ou o vincula a este cartão (update_transaction com card)"
	}
	return out
}

func (t *tools) getStatement(ctx context.Context, _ *mcp.CallToolRequest, in statementIn) (*mcp.CallToolResult, statementDetailOut, error) {
	v, err := t.statementView(ctx, in.Card, in.Month)
	if err != nil {
		return nil, statementDetailOut{}, toolErr(err)
	}
	return nil, detailOf(v), nil
}

type statementTotalIn struct {
	Card  string  `json:"card" jsonschema:"nome do cartão"`
	Month string  `json:"month" jsonschema:"mês em que a fatura fecha, AAAA-MM"`
	Total float64 `json:"total" jsonschema:"valor total da fatura em reais; 0 remove"`
}

func (t *tools) setStatementTotal(ctx context.Context, _ *mcp.CallToolRequest, in statementTotalIn) (*mcp.CallToolResult, statementDetailOut, error) {
	c, err := t.svcFor(ctx).CardByName(in.Card)
	if err != nil {
		return nil, statementDetailOut{}, toolErr(err)
	}
	m, err := month(in.Month)
	if err != nil || m == 0 {
		return nil, statementDetailOut{}, &app.ValidationError{Msg: "mês da fatura inválido (use AAAA-MM)"}
	}
	v, err := t.svcFor(ctx).SetStatementTotal(c.ID, m, finance.FromReais(in.Total))
	if err != nil {
		return nil, statementDetailOut{}, toolErr(err)
	}
	return nil, detailOf(v), nil
}

type importItemIn struct {
	Date           string  `json:"date" jsonschema:"data da compra AAAA-MM-DD"`
	Description    string  `json:"description"`
	Amount         float64 `json:"amount" jsonschema:"valor em reais; em parcelas, o valor de uma parcela"`
	Category       string  `json:"category" jsonschema:"nome da categoria (list_categories)"`
	CreateCategory bool    `json:"create_category,omitempty"`
	InstallmentN   int     `json:"installment_n,omitempty" jsonschema:"parcela desta fatura (o 3 de 3/10)"`
	Installments   int     `json:"installments,omitempty" jsonschema:"total de parcelas (o 10 de 3/10)"`
}

type importIn struct {
	Card  string         `json:"card" jsonschema:"nome do cartão"`
	Month string         `json:"month" jsonschema:"mês em que a fatura fecha, AAAA-MM"`
	Items []importItemIn `json:"items"`
}

func (t *tools) importStatement(ctx context.Context, _ *mcp.CallToolRequest, in importIn) (*mcp.CallToolResult, addTxOut, error) {
	var out addTxOut
	c, err := t.svcFor(ctx).CardByName(in.Card)
	if err != nil {
		return nil, out, toolErr(err)
	}
	m, err := month(in.Month)
	if err != nil || m == 0 {
		return nil, out, &app.ValidationError{Msg: "mês da fatura inválido (use AAAA-MM)"}
	}
	items := make([]app.ImportItem, len(in.Items))
	var total int64
	for i, it := range in.Items {
		d, err := date(it.Date)
		if err != nil || d.IsZero() {
			return nil, out, &app.ValidationError{Msg: "item " + itoa(i+1) + ": data inválida (use AAAA-MM-DD)"}
		}
		items[i] = app.ImportItem{Date: d, Description: it.Description, Amount: finance.FromReais(it.Amount),
			Category:     app.CategoryRef{Name: it.Category, Create: it.CreateCategory},
			InstallmentN: it.InstallmentN, Installments: it.Installments}
		total += items[i].Amount
	}
	ids, err := t.svcFor(ctx).ImportStatement(c.ID, m, items, app.SourceMCP)
	if err != nil {
		return nil, out, toolErr(err)
	}
	msg := itoa(len(items)) + " compra(s) lançada(s) na fatura " + c.Name + " " + m.Label() + ", total " + finance.BRL(total)
	if extra := len(ids) - len(items); extra > 0 {
		msg += ", mais " + itoa(extra) + " parcela(s) nas próximas faturas"
	}
	return nil, addTxOut{IDs: ids, Message: msg}, nil
}
