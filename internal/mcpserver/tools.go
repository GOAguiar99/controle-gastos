package mcpserver

import (
	"context"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// tools resolve the Service per call: each MCP link belongs to one user and
// one ledger, carried on the request context.
type tools struct {
	svcFor func(context.Context) *app.Service
}

func ptr[T any](v T) *T { return &v }

var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)}
	write       = &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
	idempotent  = &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}
	destructive = &mcp.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)}
)

func (t *tools) register(s *mcp.Server) {
	// reads
	addTool(s, &mcp.Tool{Name: "get_overview", Annotations: readOnly,
		Description: "Resumo de um mês (receitas, gastos por grupo, sobra, status vs limites), caixa informado, caixa estimado hoje, ponto mais baixo projetado e fluxo mês a mês (6 meses atrás até 11 à frente). Também informa a data de hoje."},
		t.getOverview)
	addTool(s, &mcp.Tool{Name: "list_transactions", Annotations: readOnly,
		Description: "Lista lançamentos de um período (padrão: mês atual), com filtros. Com include_projected=true inclui ocorrências previstas de recorrências (projected=true, sem id) que podem ser confirmadas ou puladas."},
		t.listTransactions)
	addTool(s, &mcp.Tool{Name: "get_cash_projection", Annotations: readOnly,
		Description: "Projeção do caixa: saldo estimado hoje, saldo ao fim de cada mês, menor saldo e primeira data negativa."},
		t.cashProjection)
	addTool(s, &mcp.Tool{Name: "category_report", Annotations: readOnly,
		Description: "Totais por categoria e por ano (realizado/previsto) desde o primeiro lançamento até o próximo ano, mais a composição do mês, do ano e do histórico até hoje."},
		t.categoryReport)
	addTool(s, &mcp.Tool{Name: "list_categories", Annotations: readOnly,
		Description: "Categorias existentes, com tipo. A categoria diz o que foi comprado; como foi pago fica em cada lançamento (payment_method)."}, t.listCategories)
	addTool(s, &mcp.Tool{Name: "list_recurrences", Annotations: readOnly,
		Description: "Recorrências (receitas/gastos que se repetem) com periodicidade, término e próximos meses. pending=true são sugestões (parcelas de financiamento novo) ainda não adicionadas."}, t.listRecurrences)
	addTool(s, &mcp.Tool{Name: "list_debts", Annotations: readOnly,
		Description: "Dívidas e financiamentos com saldo atual, pagamentos e, para SAC/Price, parcelas pagas e juros restantes."}, t.listDebts)
	addTool(s, &mcp.Tool{Name: "get_limits", Annotations: readOnly,
		Description: "Limites configurados: limite do cartão e sobra mínima mensal."}, t.getLimits)
	addTool(s, &mcp.Tool{Name: "list_cards", Annotations: readOnly,
		Description: "Cartões de crédito com dia de fechamento e vencimento e a fatura aberta de cada um."}, t.listCards)
	addTool(s, &mcp.Tool{Name: "get_statement", Annotations: readOnly,
		Description: "Uma fatura de cartão (mês em que fecha, AAAA-MM; padrão a aberta): compras lançadas, valor informado (valor cheio), parte não detalhada, total, fechamento e vencimento."}, t.getStatement)

	// writes
	addTool(s, &mcp.Tool{Name: "add_transactions", Annotations: write,
		Description: "Cria um ou mais lançamentos reais. Tudo ou nada: se um item for inválido, nada é salvo. Confira duplicados com list_transactions antes. Em gastos, informe payment_method (credito, debito, pix, dinheiro, boleto, outro); sem ele, vale o da última compra na mesma categoria."},
		t.addTransactions)
	addTool(s, &mcp.Tool{Name: "update_transaction", Annotations: idempotent,
		Description: "Altera um lançamento existente; só os campos informados mudam."}, t.updateTransaction)
	addTool(s, &mcp.Tool{Name: "confirm_occurrence", Annotations: write,
		Description: "Confirma a ocorrência prevista de uma recorrência num mês, criando o lançamento real (valor/data opcionais; padrão = previstos)."},
		t.confirmOccurrence)
	addTool(s, &mcp.Tool{Name: "skip_occurrence", Annotations: idempotent,
		Description: "Pula a ocorrência de uma recorrência num mês (não acontecerá)."}, t.skipOccurrence)
	addTool(s, &mcp.Tool{Name: "unskip_occurrence", Annotations: idempotent,
		Description: "Desfaz o pulo de uma ocorrência, voltando a prevê-la."}, t.unskipOccurrence)
	addTool(s, &mcp.Tool{Name: "save_recurrence", Annotations: write,
		Description: "Cria (sem id) ou altera (com id; envie todos os campos) uma recorrência."}, t.saveRecurrence)
	addTool(s, &mcp.Tool{Name: "save_debt", Annotations: write,
		Description: "Cria (sem id) ou altera (com id; envie todos os campos) uma dívida. system: none (sem juros; pagamentos lançados avulsos com debt_id) ou sac/price (financiamento: ao criar, o app sugere a recorrência mensal das parcelas restantes, com o valor de cada parcela pela tabela; ela fica pendente até accept_recurrence)."}, t.saveDebt)
	addTool(s, &mcp.Tool{Name: "set_cash", Annotations: write,
		Description: "Informa o caixa real (soma das contas) numa data, padrão hoje. Lançamentos até essa data passam a ser considerados já incluídos."},
		t.setCash)
	addTool(s, &mcp.Tool{Name: "add_category", Annotations: idempotent,
		Description: "Cria uma categoria (se já existir com o mesmo nome, retorna a existente)."}, t.addCategory)
	addTool(s, &mcp.Tool{Name: "save_card", Annotations: idempotent,
		Description: "Cria (sem id) ou altera (com id) um cartão de crédito: nome, dia de fechamento e de vencimento. Mudar o vencimento move a data de pagamento das compras já lançadas."}, t.saveCard)
	addTool(s, &mcp.Tool{Name: "set_statement_total", Annotations: idempotent,
		Description: "Informa o valor total de uma fatura (valor cheio), para quem não quer lançar item a item. As compras lançadas na fatura contam dentro dele: só a diferença entra como \"Fatura cartão (não detalhado)\". total=0 remove."}, t.setStatementTotal)
	addTool(s, &mcp.Tool{Name: "import_statement", Annotations: write,
		Description: "Lança as compras de uma fatura de cartão item a item, cada uma na sua categoria, na fatura indicada (tudo ou nada). Itens com parcela (installment_n de installments, ex. 3/10) criam também as parcelas seguintes nas próximas faturas. Antes, consulte get_statement e list_transactions para não duplicar (parcelas já lançadas aparecem como itens da fatura)."}, t.importStatement)
	addTool(s, &mcp.Tool{Name: "set_limits", Annotations: idempotent,
		Description: "Altera limite do cartão e/ou sobra mínima mensal."}, t.setLimits)

	addTool(s, &mcp.Tool{Name: "accept_recurrence", Annotations: idempotent,
		Description: "Adiciona uma recorrência sugerida (pending) aos gastos recorrentes: as ocorrências passam a aparecer como previstas. Só com o aceite do usuário."},
		t.acceptRecurrence)

	// destructive
	addTool(s, &mcp.Tool{Name: "dismiss_recurrence", Annotations: destructive,
		Description: "Descarta uma recorrência sugerida (pending); as parcelas não serão previstas."}, t.dismissRecurrence)
	addTool(s, &mcp.Tool{Name: "delete_transaction", Annotations: destructive,
		Description: "Exclui um lançamento real (irreversível). Para não contar uma ocorrência prevista use skip_occurrence."}, t.deleteTransaction)
	addTool(s, &mcp.Tool{Name: "delete_recurrence", Annotations: destructive,
		Description: "Exclui uma recorrência e suas previsões futuras; lançamentos já confirmados ficam (irreversível)."}, t.deleteRecurrence)
	addTool(s, &mcp.Tool{Name: "delete_card", Annotations: destructive,
		Description: "Exclui um cartão sem compras lançadas (com compras, desative-o com save_card active=false)."}, t.deleteCard)
	addTool(s, &mcp.Tool{Name: "delete_debt", Annotations: destructive,
		Description: "Exclui uma dívida e as recorrências que a pagam; pagamentos já lançados continuam como gastos, sem vínculo (irreversível)."}, t.deleteDebt)
}

// ---------- shared shapes ----------

type txOut struct {
	ID           int64  `json:"id,omitempty" jsonschema:"id do lançamento real (ausente em previstos)"`
	Kind         string `json:"kind"`
	Date         string `json:"date"`
	AmountCents  int64  `json:"amount_cents"`
	AmountBRL    string `json:"amount_brl"`
	Category     string `json:"category"`
	Payment      string `json:"payment_method,omitempty" jsonschema:"forma de pagamento do gasto; ausente em receitas"`
	Group        string `json:"group,omitempty" jsonschema:"grupo do gasto no resumo, derivado: cartao (pago no crédito), fixo (recorrência) ou outros; ausente em receitas"`
	Card         string `json:"card,omitempty" jsonschema:"cartão de crédito da compra"`
	Statement    string `json:"statement,omitempty" jsonschema:"fatura (mês em que fecha, AAAA-MM)"`
	CashDate     string `json:"cash_date,omitempty" jsonschema:"quando sai do caixa (vencimento da fatura), se diferente da data"`
	Installment  string `json:"installment,omitempty" jsonschema:"parcela, ex. 3/10"`
	Undetailed   bool   `json:"undetailed,omitempty" jsonschema:"parte não detalhada de um valor de fatura informado (set_statement_total); não é um lançamento"`
	Description  string `json:"description"`
	Projected    bool   `json:"projected,omitempty" jsonschema:"ocorrência prevista de recorrência, ainda não confirmada"`
	RecurrenceID int64  `json:"recurrence_id,omitempty"`
	Month        string `json:"occurrence_month,omitempty" jsonschema:"mês da ocorrência da recorrência (AAAA-MM)"`
	DebtID       int64  `json:"debt_id,omitempty"`
	CreatedBy    string `json:"created_by,omitempty" jsonschema:"quem lançou (em carteiras compartilhadas)"`
}

func txOf(e finance.Entry) txOut {
	o := txOut{ID: e.ID, Kind: string(e.Kind), Date: iso(e.Date), AmountCents: e.Amount, AmountBRL: finance.BRL(e.Amount),
		Category: e.Category, Description: e.Description, Projected: e.Projected,
		RecurrenceID: e.RecurrenceID, Month: e.OccMonth, DebtID: e.DebtID, CreatedBy: e.CreatedBy}
	if e.Kind == finance.Expense {
		o.Payment, o.Group = string(e.Payment), string(e.Group())
	}
	if e.CardID != 0 {
		o.Card, o.Statement = e.Card, e.Statement.String()
		if !e.CashDate.Equal(e.Date) {
			o.CashDate = iso(e.CashDate)
		}
	}
	if e.InstallmentOf != 0 {
		o.Installment = itoa(e.InstallmentN) + "/" + itoa(e.InstallmentOf)
	}
	o.Undetailed = e.StatementRest
	return o
}

func iso(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

type moneyOut struct {
	Cents int64  `json:"cents"`
	BRL   string `json:"brl"`
}

func money(c int64) moneyOut { return moneyOut{Cents: c, BRL: finance.BRL(c)} }

func month(v string) (finance.Month, error) {
	if v == "" {
		return 0, nil
	}
	m, err := finance.ParseMonth(v)
	if err != nil {
		return 0, &app.ValidationError{Msg: "mês inválido " + v + " (use AAAA-MM)"}
	}
	return m, nil
}

func date(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	d, err := finance.ParseDate(v)
	if err != nil {
		return d, &app.ValidationError{Msg: "data inválida " + v + " (use AAAA-MM-DD)"}
	}
	return d, nil
}

func kind(v string) (finance.Kind, error) {
	if v == "" {
		return "", nil
	}
	return app.ParseKind(v)
}

type idOut struct {
	ID      int64  `json:"id"`
	Message string `json:"message"`
}

// ---------- reads ----------

type overviewIn struct {
	Month string `json:"month,omitempty" jsonschema:"mês AAAA-MM; padrão: mês atual"`
}

type flowMonthOut struct {
	Month   string   `json:"month"`
	In      moneyOut `json:"income"`
	Out     moneyOut `json:"expense"`
	Card    moneyOut `json:"card"`
	Fixed   moneyOut `json:"fixed"`
	Other   moneyOut `json:"other"`
	Opening moneyOut `json:"opening"`
	Closing moneyOut `json:"closing"`
	Past    bool     `json:"past,omitempty"`
	Current bool     `json:"current,omitempty"`
}

type overviewOut struct {
	Today            string         `json:"today"`
	Month            string         `json:"month"`
	Income           moneyOut       `json:"income"`
	Expense          moneyOut       `json:"expense"`
	Card             moneyOut       `json:"card"`
	Fixed            moneyOut       `json:"fixed"`
	Other            moneyOut       `json:"other"`
	Balance          moneyOut       `json:"balance" jsonschema:"sobra do mês = receitas - gastos"`
	SavingsPct       float64        `json:"savings_pct"`
	Status           string         `json:"status"`
	CardLimit        moneyOut       `json:"card_limit"`
	MinSavings       moneyOut       `json:"min_savings"`
	CashInformed     *moneyOut      `json:"cash_informed,omitempty" jsonschema:"último caixa informado"`
	CashInformedDate string         `json:"cash_informed_date,omitempty"`
	CashToday        moneyOut       `json:"cash_today" jsonschema:"caixa estimado hoje"`
	LowestCash       moneyOut       `json:"lowest_cash" jsonschema:"menor saldo projetado nos próximos 12 meses"`
	LowestCashDate   string         `json:"lowest_cash_date"`
	FirstNegative    string         `json:"first_negative_date,omitempty"`
	Flow             []flowMonthOut `json:"monthly_flow"`
}

func flowOut(fm finance.FlowMonth) flowMonthOut {
	return flowMonthOut{Month: fm.Month.String(), In: money(fm.In), Out: money(fm.Out), Card: money(fm.Card),
		Fixed: money(fm.Fixed), Other: money(fm.Other), Opening: money(fm.Opening), Closing: money(fm.Closing),
		Past: fm.Past, Current: fm.Current}
}

func (t *tools) getOverview(ctx context.Context, _ *mcp.CallToolRequest, in overviewIn) (*mcp.CallToolResult, overviewOut, error) {
	var out overviewOut
	m, err := month(in.Month)
	if err != nil {
		return nil, out, err
	}
	o, err := t.svcFor(ctx).Overview(m)
	if err != nil {
		return nil, out, toolErr(err)
	}
	s := o.Summary
	out = overviewOut{Today: iso(o.Today), Month: o.Month.String(), Income: money(s.Income), Expense: money(s.Expense),
		Card: money(s.Card), Fixed: money(s.Fixed), Other: money(s.Other), Balance: money(s.Balance),
		SavingsPct: s.SavingsPct, Status: s.Status, CardLimit: money(o.Limits.CardLimit), MinSavings: money(o.Limits.MinSavings),
		CashToday: money(o.Flow.Current), LowestCash: money(o.Flow.Low), LowestCashDate: iso(o.Flow.LowDate),
		FirstNegative: iso(o.Flow.FirstNegative)}
	if o.Snapshot.Set {
		out.CashInformed, out.CashInformedDate = ptr(money(o.Snapshot.Amount)), iso(o.Snapshot.Date)
	}
	for _, fm := range o.FlowRows {
		out.Flow = append(out.Flow, flowOut(fm))
	}
	return nil, out, nil
}

type listTxIn struct {
	From             string `json:"from,omitempty" jsonschema:"mês inicial AAAA-MM; padrão: mês atual"`
	To               string `json:"to,omitempty" jsonschema:"mês final AAAA-MM (inclusive); padrão: igual ao inicial"`
	Kind             string `json:"kind,omitempty" jsonschema:"income ou expense"`
	Category         string `json:"category,omitempty" jsonschema:"nome exato da categoria"`
	Text             string `json:"text,omitempty" jsonschema:"trecho da descrição ou categoria"`
	IncludeProjected bool   `json:"include_projected,omitempty" jsonschema:"incluir ocorrências previstas de recorrências"`
}

type listTxOut struct {
	Transactions []txOut `json:"transactions"`
	Count        int     `json:"count"`
	Income       int64   `json:"total_income_cents"`
	Expense      int64   `json:"total_expense_cents"`
}

func (t *tools) listTransactions(ctx context.Context, _ *mcp.CallToolRequest, in listTxIn) (*mcp.CallToolResult, listTxOut, error) {
	out := listTxOut{Transactions: []txOut{}}
	f := app.TxFilter{Category: in.Category, Text: in.Text, IncludeProjected: in.IncludeProjected}
	var err error
	if f.From, err = month(in.From); err != nil {
		return nil, out, err
	}
	if f.To, err = month(in.To); err != nil {
		return nil, out, err
	}
	if f.To == 0 {
		f.To = f.From
	}
	if f.Kind, err = kind(in.Kind); err != nil {
		return nil, out, err
	}
	es, err := t.svcFor(ctx).ListTx(f)
	if err != nil {
		return nil, out, toolErr(err)
	}
	for _, e := range es {
		out.Transactions = append(out.Transactions, txOf(e))
		if e.Kind == finance.Income {
			out.Income += e.Amount
		} else {
			out.Expense += e.Amount
		}
	}
	out.Count = len(es)
	return nil, out, nil
}

type projectionIn struct {
	Months int `json:"months,omitempty" jsonschema:"meses à frente (1–60); padrão 12"`
}

type projectionOut struct {
	Today         string         `json:"today"`
	CashToday     moneyOut       `json:"cash_today"`
	Lowest        moneyOut       `json:"lowest"`
	LowestDate    string         `json:"lowest_date"`
	FirstNegative string         `json:"first_negative_date,omitempty"`
	Months        []flowMonthOut `json:"months"`
}

func (t *tools) cashProjection(ctx context.Context, _ *mcp.CallToolRequest, in projectionIn) (*mcp.CallToolResult, projectionOut, error) {
	var out projectionOut
	if in.Months == 0 {
		in.Months = 12
	}
	cf, err := t.svcFor(ctx).CashProjection(in.Months)
	if err != nil {
		return nil, out, toolErr(err)
	}
	out = projectionOut{Today: iso(cf.Today), CashToday: money(cf.Current), Lowest: money(cf.Low), LowestDate: iso(cf.LowDate),
		FirstNegative: iso(cf.FirstNegative), Months: []flowMonthOut{}}
	for _, fm := range cf.Months {
		out.Months = append(out.Months, flowOut(fm))
	}
	return nil, out, nil
}

type reportIn struct {
	Kind  string `json:"kind" jsonschema:"income ou expense"`
	Month string `json:"month,omitempty" jsonschema:"mês da composição AAAA-MM; padrão: mês atual"`
}

type reportYear struct {
	Year       string             `json:"year"`
	State      string             `json:"state" jsonschema:"realizado, parcial (ano atual) ou previsto"`
	Months     int                `json:"months_with_data"`
	Realized   float64            `json:"realized_reais"`
	Projected  float64            `json:"projected_reais"`
	ByCategory map[string]float64 `json:"by_category_reais"`
}

type reportPeriod struct {
	Label      string             `json:"label"`
	Total      float64            `json:"total_reais"`
	ByCategory map[string]float64 `json:"by_category_reais"`
}

type reportOut struct {
	Categories []string       `json:"categories" jsonschema:"categorias em ordem de total; as menores somadas em Outras"`
	Years      []reportYear   `json:"years"`
	Periods    []reportPeriod `json:"composition"`
}

func (t *tools) categoryReport(ctx context.Context, _ *mcp.CallToolRequest, in reportIn) (*mcp.CallToolResult, reportOut, error) {
	out := reportOut{Categories: []string{}, Years: []reportYear{}, Periods: []reportPeriod{}}
	k, err := app.ParseKind(in.Kind)
	if err != nil {
		return nil, out, err
	}
	m, err := month(in.Month)
	if err != nil {
		return nil, out, err
	}
	c, err := t.svcFor(ctx).CategoryReport(k, m)
	if err != nil {
		return nil, out, toolErr(err)
	}
	out.Categories = append(out.Categories, c.Cats...)
	byCat := func(vals func(i int) float64) map[string]float64 {
		mm := map[string]float64{}
		for i, name := range c.Cats {
			if v := vals(i); v != 0 {
				mm[name] = v
			}
		}
		return mm
	}
	for y, year := range c.Years {
		out.Years = append(out.Years, reportYear{Year: year, State: c.YearState[y], Months: c.YearMons[y],
			Realized: c.YearReal[y], Projected: c.YearProj[y], ByCategory: byCat(func(i int) float64 { return c.Values[i][y] })})
	}
	for _, p := range c.Periods {
		out.Periods = append(out.Periods, reportPeriod{Label: p.Label, Total: p.Total, ByCategory: byCat(func(i int) float64 { return p.Values[i] })})
	}
	return nil, out, nil
}

type kindIn struct {
	Kind string `json:"kind,omitempty" jsonschema:"income ou expense; vazio = todos"`
}

type categoryOut struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type categoriesOut struct {
	Categories []categoryOut `json:"categories"`
}

func (t *tools) listCategories(ctx context.Context, _ *mcp.CallToolRequest, in kindIn) (*mcp.CallToolResult, categoriesOut, error) {
	out := categoriesOut{Categories: []categoryOut{}}
	k, err := kind(in.Kind)
	if err != nil {
		return nil, out, err
	}
	cats, err := t.svcFor(ctx).Categories(k)
	if err != nil {
		return nil, out, toolErr(err)
	}
	for _, c := range cats {
		out.Categories = append(out.Categories, categoryOut{ID: c.ID, Name: c.Name, Kind: string(c.Kind)})
	}
	return nil, out, nil
}

type recurrenceOut struct {
	ID          int64    `json:"id"`
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Amount      moneyOut `json:"amount"`
	Day         int      `json:"day"`
	Interval    int      `json:"interval_months"`
	Frequency   string   `json:"frequency"`
	Start       string   `json:"start"`
	EndMode     string   `json:"end_mode"`
	Count       int      `json:"count,omitempty"`
	End         string   `json:"end,omitempty"`
	EndLabel    string   `json:"end_label"`
	Active      bool     `json:"active"`
	DebtID      int64    `json:"debt_id,omitempty"`
	Payment     string   `json:"payment_method,omitempty" jsonschema:"forma de pagamento das ocorrências; ausente em receitas"`
	CardID      int64    `json:"card_id,omitempty" jsonschema:"cartão em que as ocorrências são cobradas"`
	Pending     bool     `json:"pending,omitempty" jsonschema:"sugestão aguardando o usuário: accept_recurrence ou dismiss_recurrence"`
	Next        []string `json:"next_months"`
}

type recurrencesOut struct {
	Recurrences []recurrenceOut `json:"recurrences"`
}

func (t *tools) listRecurrences(ctx context.Context, _ *mcp.CallToolRequest, in kindIn) (*mcp.CallToolResult, recurrencesOut, error) {
	out := recurrencesOut{Recurrences: []recurrenceOut{}}
	k, err := kind(in.Kind)
	if err != nil {
		return nil, out, err
	}
	recs, err := t.svcFor(ctx).Recurrences(k)
	if err != nil {
		return nil, out, toolErr(err)
	}
	cur := finance.MonthOf(t.svcFor(ctx).Today())
	for _, r := range recs {
		o := recurrenceOut{ID: r.ID, Kind: string(r.Kind), Description: r.Description, Category: r.Category,
			Amount: money(r.Amount), Day: r.Day, Interval: r.Interval, Frequency: r.FrequencyLabel(), Start: r.Start.String(),
			EndMode: r.EndMode, Count: r.Count, EndLabel: r.EndLabel(), Active: r.Active, DebtID: r.DebtID, Pending: r.Pending, Next: []string{}}
		if r.Kind == finance.Expense {
			o.Payment, o.CardID = string(r.Payment), r.CardID
		}
		if r.EndMode == finance.EndUntil {
			o.End = r.End.String()
		}
		for _, m := range r.Next(cur, 3) {
			o.Next = append(o.Next, m.String())
		}
		out.Recurrences = append(out.Recurrences, o)
	}
	return nil, out, nil
}

type debtOut struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	System        string    `json:"system"`
	Initial       moneyOut  `json:"initial"`
	Current       moneyOut  `json:"current_balance"`
	Paid          moneyOut  `json:"paid"`
	Payments      int       `json:"payments"`
	PaidPct       int       `json:"paid_pct"`
	MonthlyRate   float64   `json:"monthly_rate_pct,omitempty"`
	Term          int       `json:"term,omitempty"`
	FirstDue      string    `json:"first_due,omitempty"`
	Fees          *moneyOut `json:"fees,omitempty"`
	InstallsPaid  int       `json:"installments_paid,omitempty"`
	InterestLeft  *moneyOut `json:"interest_left,omitempty"`
	NextDue       string    `json:"next_due,omitempty"`
	NextPayment   *moneyOut `json:"next_payment,omitempty"`
	AnchorN       int       `json:"anchor_n,omitempty"`
	AnchorBalance *moneyOut `json:"anchor_balance,omitempty"`
}

type debtsOut struct {
	Debts []debtOut `json:"debts"`
}

func (t *tools) listDebts(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, debtsOut, error) {
	out := debtsOut{Debts: []debtOut{}}
	ds, err := t.svcFor(ctx).Debts()
	if err != nil {
		return nil, out, toolErr(err)
	}
	for _, d := range ds {
		o := debtOut{ID: d.ID, Name: d.Name, System: d.Loan.System, Initial: money(d.Initial), Current: money(d.Current()),
			Paid: money(d.Paid), Payments: d.Payments, PaidPct: d.PaidPct()}
		if o.System == "" {
			o.System = finance.LoanNone
		}
		if d.HasInterest() {
			o.MonthlyRate, o.Term, o.FirstDue = d.Loan.MonthlyRate, d.Loan.Term, iso(d.Loan.FirstDue)
			o.Fees, o.InstallsPaid, o.InterestLeft = ptr(money(d.Loan.Fees)), d.Status.Paid, ptr(money(d.Status.InterestLeft))
			o.AnchorN = d.Loan.AnchorN
			if d.Loan.AnchorN > 0 {
				o.AnchorBalance = ptr(money(d.Loan.AnchorBalance))
			}
			if sched := d.Loan.Schedule(); d.Status.Paid < len(sched) {
				next := sched[d.Status.Paid]
				o.NextDue, o.NextPayment = iso(next.Due), ptr(money(next.Total))
			}
		}
		out.Debts = append(out.Debts, o)
	}
	return nil, out, nil
}

type limitsOut struct {
	CardLimit  moneyOut `json:"card_limit"`
	MinSavings moneyOut `json:"min_savings"`
}

func (t *tools) getLimits(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, limitsOut, error) {
	lim := t.svcFor(ctx).Limits()
	return nil, limitsOut{CardLimit: money(lim.CardLimit), MinSavings: money(lim.MinSavings)}, nil
}

// ---------- writes ----------

type txItem struct {
	Kind           string  `json:"kind" jsonschema:"income ou expense"`
	Date           string  `json:"date" jsonschema:"AAAA-MM-DD"`
	Amount         float64 `json:"amount" jsonschema:"valor positivo em reais, ex. 129.90"`
	Category       string  `json:"category" jsonschema:"nome da categoria (use list_categories)"`
	CreateCategory bool    `json:"create_category,omitempty" jsonschema:"criar a categoria se não existir"`
	Description    string  `json:"description,omitempty"`
	DebtID         int64   `json:"debt_id,omitempty" jsonschema:"dívida que este gasto abate"`
	PaymentMethod  string  `json:"payment_method,omitempty" jsonschema:"forma de pagamento do gasto: credito, debito, pix, dinheiro, boleto ou outro (padrão: a da última compra na categoria)"`
	Card           string  `json:"card,omitempty" jsonschema:"cartão de crédito (nome, de list_cards); implica credito. Sem ele, credito vai no único cartão ativo, se houver um"`
	Installments   int     `json:"installments,omitempty" jsonschema:"compra parcelada: número de parcelas; amount é o total da compra, dividido entre as faturas"`
}

type addTxIn struct {
	Items []txItem `json:"items" jsonschema:"lançamentos a criar"`
}

type addTxOut struct {
	IDs     []int64 `json:"ids"`
	Message string  `json:"message"`
}

func (t *tools) addTransactions(ctx context.Context, _ *mcp.CallToolRequest, in addTxIn) (*mcp.CallToolResult, addTxOut, error) {
	var out addTxOut
	ins := make([]app.TxIn, len(in.Items))
	var total int64
	for i, it := range in.Items {
		k, err := app.ParseKind(it.Kind)
		if err != nil {
			return nil, out, &app.ValidationError{Msg: "item " + itoa(i+1) + ": " + err.Error()}
		}
		d, err := date(it.Date)
		if err != nil || d.IsZero() {
			return nil, out, &app.ValidationError{Msg: "item " + itoa(i+1) + ": data inválida (use AAAA-MM-DD)"}
		}
		pay, err := app.ParsePaymentMethod(it.PaymentMethod)
		if err != nil {
			return nil, out, &app.ValidationError{Msg: "item " + itoa(i+1) + ": " + err.Error()}
		}
		ins[i] = app.TxIn{Kind: k, Date: d, Amount: finance.FromReais(it.Amount),
			Category:    app.CategoryRef{Name: it.Category, Create: it.CreateCategory},
			Description: it.Description, DebtID: it.DebtID, Source: app.SourceMCP, Payment: pay, Installments: it.Installments}
		if it.Card != "" {
			c, err := t.svcFor(ctx).CardByName(it.Card)
			if err != nil {
				return nil, out, &app.ValidationError{Msg: "item " + itoa(i+1) + ": " + err.Error()}
			}
			ins[i].CardID = c.ID
		}
		total += ins[i].Amount
	}
	ids, err := t.svcFor(ctx).AddTxs(ins)
	if err != nil {
		return nil, out, toolErr(err)
	}
	return nil, addTxOut{IDs: ids, Message: itoa(len(ids)) + " lançamento(s) salvo(s), total " + finance.BRL(total)}, nil
}

type updateTxIn struct {
	ID             int64    `json:"id"`
	Date           string   `json:"date,omitempty" jsonschema:"AAAA-MM-DD"`
	Amount         *float64 `json:"amount,omitempty" jsonschema:"valor em reais"`
	Category       string   `json:"category,omitempty"`
	CreateCategory bool     `json:"create_category,omitempty"`
	Description    *string  `json:"description,omitempty"`
	DebtID         *int64   `json:"debt_id,omitempty" jsonschema:"0 remove o vínculo com dívida"`
	PaymentMethod  string   `json:"payment_method,omitempty" jsonschema:"forma de pagamento do gasto: credito, debito, pix, dinheiro, boleto ou outro"`
	Card           string   `json:"card,omitempty" jsonschema:"cartão de crédito (nome); implica credito"`
}

type txResult struct {
	Transaction txOut  `json:"transaction"`
	Message     string `json:"message"`
}

func (t *tools) updateTransaction(ctx context.Context, _ *mcp.CallToolRequest, in updateTxIn) (*mcp.CallToolResult, txResult, error) {
	var out txResult
	old, err := t.svcFor(ctx).Tx(in.ID)
	if err != nil {
		return nil, out, toolErr(err)
	}
	tx := app.TxIn{Date: old.Date, Amount: old.Amount, Category: app.CategoryRef{ID: old.CategoryID},
		Description: old.Description, DebtID: old.DebtID, Payment: old.Payment}
	if in.PaymentMethod != "" {
		if tx.Payment, err = app.ParsePaymentMethod(in.PaymentMethod); err != nil {
			return nil, out, err
		}
	}
	if in.Card != "" {
		c, err := t.svcFor(ctx).CardByName(in.Card)
		if err != nil {
			return nil, out, err
		}
		tx.CardID, tx.Payment = c.ID, finance.Credit
	}
	if in.Date != "" {
		if tx.Date, err = date(in.Date); err != nil {
			return nil, out, err
		}
	}
	if in.Amount != nil {
		tx.Amount = finance.FromReais(*in.Amount)
	}
	if in.Category != "" {
		tx.Category = app.CategoryRef{Name: in.Category, Create: in.CreateCategory}
	}
	if in.Description != nil {
		tx.Description = *in.Description
	}
	if in.DebtID != nil {
		tx.DebtID = *in.DebtID
	}
	e, err := t.svcFor(ctx).UpdateTx(in.ID, tx)
	if err != nil {
		return nil, out, toolErr(err)
	}
	return nil, txResult{Transaction: txOf(e), Message: "lançamento atualizado"}, nil
}

type occurrenceIn struct {
	RecurrenceID int64    `json:"recurrence_id"`
	Month        string   `json:"month" jsonschema:"mês da ocorrência AAAA-MM"`
	Amount       *float64 `json:"amount,omitempty" jsonschema:"valor real em reais; padrão: o previsto"`
	Date         string   `json:"date,omitempty" jsonschema:"data real AAAA-MM-DD; padrão: o dia da recorrência"`
}

func (t *tools) confirmOccurrence(ctx context.Context, _ *mcp.CallToolRequest, in occurrenceIn) (*mcp.CallToolResult, txResult, error) {
	var out txResult
	m, err := month(in.Month)
	if err != nil {
		return nil, out, err
	}
	d, err := date(in.Date)
	if err != nil {
		return nil, out, err
	}
	var amount int64
	if in.Amount != nil {
		if amount = finance.FromReais(*in.Amount); amount <= 0 {
			return nil, out, &app.ValidationError{Msg: "valor inválido"}
		}
	}
	id, err := t.svcFor(ctx).ConfirmOccurrence(in.RecurrenceID, m, amount, d, app.SourceMCP)
	if err != nil {
		return nil, out, toolErr(err)
	}
	e, err := t.svcFor(ctx).Tx(id)
	if err != nil {
		return nil, out, toolErr(err)
	}
	return nil, txResult{Transaction: txOf(e), Message: "ocorrência confirmada"}, nil
}

type skipIn struct {
	RecurrenceID int64  `json:"recurrence_id"`
	Month        string `json:"month" jsonschema:"mês da ocorrência AAAA-MM"`
}

type msgOut struct {
	Message string `json:"message"`
}

func (t *tools) skipOccurrence(ctx context.Context, _ *mcp.CallToolRequest, in skipIn) (*mcp.CallToolResult, msgOut, error) {
	m, err := month(in.Month)
	if err != nil {
		return nil, msgOut{}, err
	}
	r, err := t.svcFor(ctx).SkipOccurrence(in.RecurrenceID, m)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	return nil, msgOut{Message: r.Description + " de " + m.Label() + " pulado"}, nil
}

func (t *tools) unskipOccurrence(ctx context.Context, _ *mcp.CallToolRequest, in skipIn) (*mcp.CallToolResult, msgOut, error) {
	m, err := month(in.Month)
	if err != nil {
		return nil, msgOut{}, err
	}
	r, err := t.svcFor(ctx).UnskipOccurrence(in.RecurrenceID, m)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	return nil, msgOut{Message: r.Description + " de " + m.Label() + " voltou a ser previsto"}, nil
}

type recurrenceIn struct {
	ID             int64   `json:"id,omitempty" jsonschema:"para alterar uma recorrência existente"`
	Kind           string  `json:"kind" jsonschema:"income ou expense"`
	Description    string  `json:"description,omitempty" jsonschema:"padrão: nome da categoria"`
	Category       string  `json:"category"`
	CreateCategory bool    `json:"create_category,omitempty"`
	Amount         float64 `json:"amount" jsonschema:"valor em reais"`
	Day            int     `json:"day" jsonschema:"dia do mês 1–31"`
	Interval       int     `json:"interval_months,omitempty" jsonschema:"1 mensal (padrão), 2, 3, 6 ou 12"`
	Start          string  `json:"start" jsonschema:"primeiro mês AAAA-MM"`
	EndMode        string  `json:"end_mode,omitempty" jsonschema:"never (padrão), count ou until"`
	Count          int     `json:"count,omitempty" jsonschema:"número de ocorrências (end_mode=count)"`
	End            string  `json:"end,omitempty" jsonschema:"último mês AAAA-MM (end_mode=until)"`
	Active         *bool   `json:"active,omitempty" jsonschema:"padrão true"`
	DebtID         int64   `json:"debt_id,omitempty" jsonschema:"dívida que esta recorrência paga"`
	PaymentMethod  string  `json:"payment_method,omitempty" jsonschema:"forma de pagamento do gasto: credito, debito, pix, dinheiro, boleto ou outro (padrão outro; ao alterar, mantém)"`
	Card           string  `json:"card,omitempty" jsonschema:"cartão de crédito (nome); implica credito"`
}

func (t *tools) saveRecurrence(ctx context.Context, _ *mcp.CallToolRequest, in recurrenceIn) (*mcp.CallToolResult, idOut, error) {
	r := app.RecIn{ID: in.ID, Description: in.Description, Category: app.CategoryRef{Name: in.Category, Create: in.CreateCategory},
		Amount: finance.FromReais(in.Amount), Day: in.Day, Interval: in.Interval, EndMode: in.EndMode, Count: in.Count,
		Active: in.Active == nil || *in.Active, DebtID: in.DebtID}
	var err error
	if r.Payment, err = app.ParsePaymentMethod(in.PaymentMethod); err != nil {
		return nil, idOut{}, err
	}
	if in.Card != "" {
		c, err := t.svcFor(ctx).CardByName(in.Card)
		if err != nil {
			return nil, idOut{}, err
		}
		r.CardID = c.ID
	}
	if r.Kind, err = app.ParseKind(in.Kind); err != nil {
		return nil, idOut{}, err
	}
	if r.Interval == 0 {
		r.Interval = 1
	}
	if r.Start, err = month(in.Start); err != nil {
		return nil, idOut{}, err
	}
	if r.End, err = month(in.End); err != nil {
		return nil, idOut{}, err
	}
	id, err := t.svcFor(ctx).SaveRecurrence(r)
	if err != nil {
		return nil, idOut{}, toolErr(err)
	}
	return nil, idOut{ID: id, Message: "recorrência salva"}, nil
}

type debtIn struct {
	ID            int64   `json:"id,omitempty" jsonschema:"para alterar uma dívida existente"`
	Name          string  `json:"name"`
	Initial       float64 `json:"initial" jsonschema:"valor inicial / financiado em reais"`
	System        string  `json:"system,omitempty" jsonschema:"none (padrão, sem juros), sac ou price"`
	MonthlyRate   float64 `json:"monthly_rate_pct,omitempty" jsonschema:"taxa de juros % ao mês, ex. 0.96"`
	Term          int     `json:"term,omitempty" jsonschema:"número total de parcelas"`
	FirstDue      string  `json:"first_due,omitempty" jsonschema:"vencimento da 1ª parcela AAAA-MM-DD"`
	Fees          float64 `json:"fees,omitempty" jsonschema:"seguros/taxas por parcela em reais"`
	AnchorN       int     `json:"anchor_n,omitempty" jsonschema:"parcela após a qual o saldo devedor é conhecido"`
	AnchorBalance float64 `json:"anchor_balance,omitempty" jsonschema:"saldo devedor após a parcela anchor_n, em reais"`
}

func (t *tools) saveDebt(ctx context.Context, _ *mcp.CallToolRequest, in debtIn) (*mcp.CallToolResult, debtSavedOut, error) {
	d := db.DebtInput{Name: in.Name, Initial: finance.FromReais(in.Initial), System: in.System, MonthlyRate: in.MonthlyRate,
		Term: in.Term, Fees: finance.FromReais(in.Fees), AnchorN: in.AnchorN, AnchorBalance: finance.FromReais(in.AnchorBalance)}
	var err error
	if d.FirstDue, err = date(in.FirstDue); err != nil {
		return nil, debtSavedOut{}, err
	}
	res, err := t.svcFor(ctx).SaveDebt(in.ID, d)
	if err != nil {
		return nil, debtSavedOut{}, toolErr(err)
	}
	out := debtSavedOut{ID: res.ID, SuggestedRecurrenceID: res.SuggestedID, Message: "dívida salva"}
	if res.SuggestedID != 0 {
		out.Message += "; recorrência das parcelas sugerida: pergunte ao usuário se quer adicioná-la (accept_recurrence) ou descartá-la (dismiss_recurrence)"
	}
	return nil, out, nil
}

type debtSavedOut struct {
	ID                    int64  `json:"id"`
	SuggestedRecurrenceID int64  `json:"suggested_recurrence_id,omitempty" jsonschema:"recorrência das parcelas sugerida (financiamento novo SAC/Price); só entra na previsão após accept_recurrence"`
	Message               string `json:"message"`
}

type cashIn struct {
	Amount float64 `json:"amount" jsonschema:"saldo real em reais (pode ser negativo)"`
	Date   string  `json:"date,omitempty" jsonschema:"AAAA-MM-DD; padrão hoje"`
	Note   string  `json:"note,omitempty"`
}

func (t *tools) setCash(ctx context.Context, _ *mcp.CallToolRequest, in cashIn) (*mcp.CallToolResult, msgOut, error) {
	d, err := date(in.Date)
	if err != nil {
		return nil, msgOut{}, err
	}
	cents := finance.FromReais(in.Amount)
	d, err = t.svcFor(ctx).SetCash(d, cents, in.Note)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	return nil, msgOut{Message: "caixa de " + finance.BRL(cents) + " informado em " + iso(d)}, nil
}

type categoryIn struct {
	Name string `json:"name"`
	Kind string `json:"kind" jsonschema:"income ou expense"`
}

func (t *tools) addCategory(ctx context.Context, _ *mcp.CallToolRequest, in categoryIn) (*mcp.CallToolResult, idOut, error) {
	k, err := app.ParseKind(in.Kind)
	if err != nil {
		return nil, idOut{}, err
	}
	id, err := t.svcFor(ctx).AddCategory(in.Name, k)
	if err != nil {
		return nil, idOut{}, toolErr(err)
	}
	return nil, idOut{ID: id, Message: "categoria pronta"}, nil
}

type limitsIn struct {
	CardLimit  *float64 `json:"card_limit,omitempty" jsonschema:"limite do cartão em reais"`
	MinSavings *float64 `json:"min_savings,omitempty" jsonschema:"sobra mínima mensal em reais"`
}

func (t *tools) setLimits(ctx context.Context, _ *mcp.CallToolRequest, in limitsIn) (*mcp.CallToolResult, limitsOut, error) {
	lim := t.svcFor(ctx).Limits()
	if in.CardLimit != nil {
		lim.CardLimit = finance.FromReais(*in.CardLimit)
	}
	if in.MinSavings != nil {
		lim.MinSavings = finance.FromReais(*in.MinSavings)
	}
	if err := t.svcFor(ctx).SetLimits(lim); err != nil {
		return nil, limitsOut{}, toolErr(err)
	}
	return nil, limitsOut{CardLimit: money(lim.CardLimit), MinSavings: money(lim.MinSavings)}, nil
}

// ---------- destructive ----------

type idIn struct {
	ID int64 `json:"id"`
}

func (t *tools) deleteTransaction(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, txResult, error) {
	e, err := t.svcFor(ctx).DeleteTx(in.ID)
	if err != nil {
		return nil, txResult{}, toolErr(err)
	}
	return nil, txResult{Transaction: txOf(e), Message: "lançamento excluído"}, nil
}

func (t *tools) acceptRecurrence(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, msgOut, error) {
	r, err := t.svcFor(ctx).AcceptRecurrence(in.ID)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	return nil, msgOut{Message: r.Description + " adicionado aos gastos recorrentes; as parcelas já aparecem como previstas"}, nil
}

func (t *tools) dismissRecurrence(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, msgOut, error) {
	r, err := t.svcFor(ctx).DismissRecurrence(in.ID)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	return nil, msgOut{Message: "sugestão " + r.Description + " descartada"}, nil
}

func (t *tools) deleteRecurrence(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, msgOut, error) {
	r, err := t.svcFor(ctx).DeleteRecurrence(in.ID)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	return nil, msgOut{Message: "recorrência " + r.Description + " excluída"}, nil
}

func (t *tools) deleteDebt(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, msgOut, error) {
	d, err := t.svcFor(ctx).DeleteDebt(in.ID)
	if err != nil {
		return nil, msgOut{}, toolErr(err)
	}
	return nil, msgOut{Message: "dívida " + d.Name + " excluída"}, nil
}

func itoa(n int) string { return strconv.Itoa(n) }
