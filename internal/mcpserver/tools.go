package mcpserver

import (
	"cmp"
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
		Description: "Projeção do caixa: saldo estimado hoje, saldo ao fim de cada mês, menor saldo e primeira data negativa. Com a previsão de gastos (forecast, padrão a preferência salva), cada mês conta a média do gasto do dia a dia."},
		t.cashProjection)
	addTool(s, &mcp.Tool{Name: "get_forecast", Annotations: readOnly,
		Description: "Previsão de gastos: média mensal do gasto do dia a dia (tudo que não é conta fixa/recorrência nem pagamento de dívida) nos últimos 6 meses fechados, os meses usados com o valor de cada um, se está ligada e onde o caixa chega mantendo essa média (menor saldo, primeira data negativa e saldo no fim)."},
		t.getForecast)
	addTool(s, &mcp.Tool{Name: "category_report", Annotations: readOnly,
		Description: "Totais por categoria e por ano (realizado/previsto) desde o primeiro lançamento até o próximo ano, mais a composição do mês, do ano e do histórico até hoje."},
		t.categoryReport)
	addTool(s, &mcp.Tool{Name: "get_currency", Annotations: readOnly,
		Description: "Moeda de exibição da carteira (e a cotação usada para mostrar os valores) e as moedas aceitas. Com currency (e date), a cotação PTAX de venda do Banco Central naquele dia ou no último dia útil antes dele."},
		t.getCurrency)
	addTool(s, &mcp.Tool{Name: "set_display_currency", Annotations: idempotent,
		Description: "Escolhe a moeda em que o app mostra totais, dashboard e fluxo (padrão BRL), convertidos pela PTAX mais recente. Os valores continuam guardados em reais e as tools continuam respondendo em reais. Só quando o usuário pedir."},
		t.setDisplayCurrency)
	addTool(s, &mcp.Tool{Name: "list_categories", Annotations: readOnly,
		Description: "Categorias existentes, com tipo. A categoria diz o que foi comprado; como foi pago fica em cada lançamento (payment_method)."}, t.listCategories)
	addTool(s, &mcp.Tool{Name: "list_recurrences", Annotations: readOnly,
		Description: "Recorrências (receitas/gastos que se repetem) com periodicidade, término e próximos meses. pending=true são sugestões (parcelas de financiamento novo) ainda não adicionadas."}, t.listRecurrences)
	addTool(s, &mcp.Tool{Name: "list_debts", Annotations: readOnly,
		Description: "Dívidas e financiamentos com saldo atual, pagamentos e, para SAC/Price, parcelas pagas e juros restantes."}, t.listDebts)
	addTool(s, &mcp.Tool{Name: "get_limits", Annotations: readOnly,
		Description: "Limites configurados (limite do cartão e sobra mínima mensal) e se o módulo de cartões está ligado."}, t.getLimits)
	addTool(s, &mcp.Tool{Name: "list_cards", Annotations: readOnly,
		Description: "Cartões de crédito com dia de fechamento e vencimento e a fatura aberta de cada um."}, t.listCards)
	addTool(s, &mcp.Tool{Name: "list_category_rules", Annotations: readOnly,
		Description: "Memória de categorias: estabelecimento → categoria que o usuário escolheu ao corrigir compras. check_statement já usa isso nas sugestões (suggested_from começa com \"memória:\"); siga essas sugestões."}, t.listCategoryRules)
	addTool(s, &mcp.Tool{Name: "check_statement", Annotations: readOnly,
		Description: "Confere as compras lidas de uma fatura antes de importar: diz quais são novas e quais já estão no app (mesmo cartão e valor com data a ±3 dias, ou a mesma parcela já criada por uma importação anterior, mesmo com centavos de diferença: aí vem existing_id e existing_amount para corrigir com update_transaction) e sugere a categoria usada antes para cada descrição. Linhas sem histórico vêm com needs_research: pesquise o nome na web antes de categorizar. Não grava nada; depois use import_statement só com as novas."}, t.checkStatement)
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
		Description: "Informa o caixa real numa data, padrão hoje: com contas cadastradas, o saldo de cada uma em accounts (o caixa é a soma); sem contas, o total em amount. Lançamentos até essa data passam a ser considerados já incluídos."},
		t.setCash)
	addTool(s, &mcp.Tool{Name: "list_cash_accounts", Annotations: readOnly,
		Description: "Contas do caixa (onde está o dinheiro), com % do CDI que rendem (bruto) e último saldo, o CDI atual e o rendimento médio que a projeção de caixa usa."}, t.listCashAccounts)
	addTool(s, &mcp.Tool{Name: "save_cash_account", Annotations: idempotent,
		Description: "Cria (sem id) ou altera (com id) uma conta do caixa: nome e % do CDI que rende (bruto; 0 = não rende, 100 = 100% do CDI). O saldo se informa com set_cash."}, t.saveCashAccount)
	addTool(s, &mcp.Tool{Name: "delete_cash_account", Annotations: destructive,
		Description: "Exclui uma conta do caixa (o caixa informado antes não muda)."}, t.deleteCashAccount)
	addTool(s, &mcp.Tool{Name: "add_category", Annotations: idempotent,
		Description: "Cria uma categoria (se já existir com o mesmo nome, retorna a existente)."}, t.addCategory)
	addTool(s, &mcp.Tool{Name: "set_cards_module", Annotations: idempotent,
		Description: "Liga ou desliga o módulo de cartões (só quando o usuário pedir). Ligado: compras no crédito caem na fatura do cartão e saem do caixa no vencimento. Desligado: saem na data da compra; o que já está em faturas fica como está, e as tools de cartão respondem que o módulo está desativado."}, t.setCardsModule)
	addTool(s, &mcp.Tool{Name: "set_forecast", Annotations: idempotent,
		Description: "Liga (ligado) ou desliga (desligado) a previsão de gastos, só quando o usuário pedir. Ligada, a projeção de caixa conta em cada mês a média do gasto do dia a dia: o que já está lançado no mês entra dentro dela e só a diferença é estimada. Não cria lançamentos nem muda receitas e gastos do mês."}, t.setForecast)
	addTool(s, &mcp.Tool{Name: "save_card", Annotations: idempotent,
		Description: "Cria (sem id) ou altera (com id) um cartão de crédito: nome, dia de fechamento e de vencimento. Mudar o vencimento move a data de pagamento das compras já lançadas."}, t.saveCard)
	addTool(s, &mcp.Tool{Name: "set_statement_total", Annotations: idempotent,
		Description: "Informa o valor total de uma fatura (valor cheio), para quem não quer lançar item a item. As compras lançadas na fatura contam dentro dele: só a diferença entra como \"Fatura cartão (não detalhado)\". total=0 remove."}, t.setStatementTotal)
	addTool(s, &mcp.Tool{Name: "import_statement", Annotations: write,
		Description: "Lança as compras de uma fatura de cartão item a item, cada uma na sua categoria, na fatura indicada (tudo ou nada). Itens com parcela (installment_n de installments, ex. 3/10) criam também as parcelas seguintes nas próximas faturas, exceto as que já existem (ao importar uma fatura antiga depois de uma nova). Antes, rode check_statement com as mesmas linhas e importe só as marcadas como novas."}, t.importStatement)
	addTool(s, &mcp.Tool{Name: "recategorize", Annotations: write,
		Description: "Corrige a categoria de um gasto e, por padrão, aprende: as outras compras do mesmo estabelecimento que estavam na categoria antiga mudam junto e as próximas são sugeridas na nova. Use quando o usuário disser que uma compra está na categoria errada."}, t.recategorize)
	addTool(s, &mcp.Tool{Name: "remember_category", Annotations: idempotent,
		Description: "Ensina a categoria de um estabelecimento para as próximas compras, sem mudar as já lançadas."}, t.rememberCategory)
	addTool(s, &mcp.Tool{Name: "forget_category", Annotations: destructive,
		Description: "Apaga a memória de categoria de um estabelecimento (as próximas compras voltam a seguir o histórico)."}, t.forgetCategory)
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
	ID           int64    `json:"id,omitempty" jsonschema:"id do lançamento real (ausente em previstos)"`
	Kind         string   `json:"kind"`
	Date         string   `json:"date"`
	AmountCents  int64    `json:"amount_cents"`
	AmountBRL    string   `json:"amount_brl"`
	Category     string   `json:"category"`
	Payment      string   `json:"payment_method,omitempty" jsonschema:"forma de pagamento do gasto; ausente em receitas"`
	Group        string   `json:"group,omitempty" jsonschema:"grupo do gasto no resumo, derivado: cartao (pago no crédito), fixo (recorrência) ou outros; ausente em receitas"`
	Card         string   `json:"card,omitempty" jsonschema:"cartão de crédito da compra"`
	Statement    string   `json:"statement,omitempty" jsonschema:"fatura (mês em que fecha, AAAA-MM)"`
	CashDate     string   `json:"cash_date,omitempty" jsonschema:"quando sai do caixa (vencimento da fatura), se diferente da data"`
	Installment  string   `json:"installment,omitempty" jsonschema:"parcela, ex. 3/10"`
	Undetailed   bool     `json:"undetailed,omitempty" jsonschema:"parte não detalhada de um valor de fatura informado (set_statement_total); não é um lançamento"`
	Description  string   `json:"description"`
	Projected    bool     `json:"projected,omitempty" jsonschema:"ocorrência prevista de recorrência, ainda não confirmada"`
	RecurrenceID int64    `json:"recurrence_id,omitempty"`
	Month        string   `json:"occurrence_month,omitempty" jsonschema:"mês da ocorrência da recorrência (AAAA-MM)"`
	DebtID       int64    `json:"debt_id,omitempty"`
	CreatedBy    string   `json:"created_by,omitempty" jsonschema:"quem lançou (em carteiras compartilhadas)"`
	Currency     string   `json:"currency,omitempty" jsonschema:"moeda em que foi lançado, se não for reais; amount_cents é o valor convertido em reais"`
	Original     *moneyIn `json:"original_amount,omitempty" jsonschema:"valor lançado na moeda original"`
	FXRate       float64  `json:"fx_rate,omitempty" jsonschema:"cotação usada (reais por unidade da moeda)"`
}

// moneyIn is an amount in a currency other than reais.
type moneyIn struct {
	Cents int64  `json:"cents"`
	Text  string `json:"text" jsonschema:"ex. US$ 100,00"`
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
	if e.Foreign() {
		o.Currency, o.FXRate = e.Currency, e.FXRate
		o.Original = &moneyIn{Cents: e.OrigAmount, Text: finance.FormatMoney(e.OrigAmount, e.Currency)}
	}
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
	Month string   `json:"month"`
	In    moneyOut `json:"income"`
	Out   moneyOut `json:"expense"`
	Card  moneyOut `json:"card"`
	Fixed moneyOut `json:"fixed"`
	Other moneyOut `json:"other"`
	// Forecast is the estimated day-to-day spending in expense (previsão de gastos ligada)
	Forecast *moneyOut `json:"forecast,omitempty" jsonschema:"previsão de gastos do dia a dia incluída em expense (estimativa, não é lançamento)"`
	DayToDay *moneyOut `json:"day_to_day,omitempty" jsonschema:"gasto do dia a dia do mês: lançado + previsão (com a previsão ligada, nos meses futuros é a média, ou mais se já houver lançado acima dela)"`
	Opening  moneyOut  `json:"opening"`
	Closing  moneyOut  `json:"closing"`
	Past     bool      `json:"past,omitempty"`
	Current  bool      `json:"current,omitempty"`
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
	Forecast         forecastBrief  `json:"forecast" jsonschema:"previsão de gastos: se está ligada e a média mensal do gasto do dia a dia"`
}

func flowOut(fm finance.FlowMonth) flowMonthOut {
	out := flowMonthOut{Month: fm.Month.String(), In: money(fm.In), Out: money(fm.Out), Card: money(fm.Card),
		Fixed: money(fm.Fixed), Other: money(fm.Other), Opening: money(fm.Opening), Closing: money(fm.Closing),
		Past: fm.Past, Current: fm.Current}
	if fm.Forecast != 0 {
		out.Forecast = ptr(money(fm.Forecast))
	}
	if v := fm.DayToDay(); v != 0 {
		out.DayToDay = ptr(money(v))
	}
	return out
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
		FirstNegative: iso(o.Flow.FirstNegative), Forecast: briefOf(o.Forecast)}
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
	Months   int    `json:"months,omitempty" jsonschema:"meses à frente (1–60); padrão 12"`
	Forecast string `json:"forecast,omitempty" jsonschema:"previsão de gastos: ligado ou desligado; padrão: a preferência salva (get_forecast)"`
	Scenario string `json:"scenario,omitempty" jsonschema:"obsoleto: use forecast"`
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
	cf, err := t.svcFor(ctx).CashProjection(in.Months, cmp.Or(in.Forecast, in.Scenario))
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
	CardsOn    bool     `json:"cards_module" jsonschema:"módulo de cartões (faturas com fechamento e vencimento) ligado"`
}

func (t *tools) getLimits(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, limitsOut, error) {
	lim := t.svcFor(ctx).Limits()
	return nil, limitsOut{CardLimit: money(lim.CardLimit), MinSavings: money(lim.MinSavings), CardsOn: t.svcFor(ctx).CardsEnabled()}, nil
}

// ---------- writes ----------

type txItem struct {
	Kind           string  `json:"kind" jsonschema:"income ou expense"`
	Date           string  `json:"date" jsonschema:"AAAA-MM-DD"`
	Amount         float64 `json:"amount" jsonschema:"valor positivo na moeda do item (padrão reais), ex. 129.90"`
	Currency       string  `json:"currency,omitempty" jsonschema:"moeda do valor: BRL (padrão), USD, EUR... (get_currency); é convertido para reais pela PTAX do dia"`
	FXRate         float64 `json:"fx_rate,omitempty" jsonschema:"cotação em reais por unidade, só se o usuário informar uma (ex. a da fatura); padrão a PTAX da data ou do último dia útil"`
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
			Description: it.Description, DebtID: it.DebtID, Source: app.SourceMCP, Payment: pay, Installments: it.Installments,
			Currency: it.Currency, FXRate: it.FXRate}
		if it.Card != "" {
			c, err := t.svcFor(ctx).CardByName(it.Card)
			if err != nil {
				return nil, out, &app.ValidationError{Msg: "item " + itoa(i+1) + ": " + err.Error()}
			}
			ins[i].CardID = c.ID
		}
	}
	ids, err := t.svcFor(ctx).AddTxs(ins)
	if err != nil {
		return nil, out, toolErr(err)
	}
	for _, id := range ids { // in reais, as saved (other currencies converted)
		if e, err := t.svcFor(ctx).Tx(id); err == nil {
			total += e.Amount
		}
	}
	return nil, addTxOut{IDs: ids, Message: itoa(len(ids)) + " lançamento(s) salvo(s), total " + finance.BRL(total)}, nil
}

type updateTxIn struct {
	ID             int64    `json:"id"`
	Date           string   `json:"date,omitempty" jsonschema:"AAAA-MM-DD"`
	Amount         *float64 `json:"amount,omitempty" jsonschema:"valor na moeda do lançamento (reais, ou a moeda em que foi lançado)"`
	Currency       string   `json:"currency,omitempty" jsonschema:"muda a moeda do valor (BRL volta para reais); informe amount junto"`
	FXRate         *float64 `json:"fx_rate,omitempty" jsonschema:"cotação em reais por unidade; 0 = PTAX da data. Sem ela, a cotação é mantida se moeda e data não mudam"`
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
	if old.Foreign() {
		tx.Amount, tx.Currency = old.OrigAmount, old.Currency
	}
	if in.Currency != "" {
		tx.Currency = in.Currency
	}
	if in.FXRate != nil {
		tx.FXRate = *in.FXRate
	}
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
	Amount   float64          `json:"amount,omitempty" jsonschema:"saldo total em reais (pode ser negativo); use quando não há contas cadastradas"`
	Accounts []accountBalance `json:"accounts,omitempty" jsonschema:"saldo de cada conta do caixa (list_cash_accounts); o caixa vira a soma, contas omitidas mantêm o último saldo"`
	Date     string           `json:"date,omitempty" jsonschema:"AAAA-MM-DD; padrão hoje"`
	Note     string           `json:"note,omitempty"`
}

type accountBalance struct {
	Account string  `json:"account" jsonschema:"nome da conta"`
	Amount  float64 `json:"amount" jsonschema:"saldo em reais"`
}

func (t *tools) setCash(ctx context.Context, _ *mcp.CallToolRequest, in cashIn) (*mcp.CallToolResult, msgOut, error) {
	d, err := date(in.Date)
	if err != nil {
		return nil, msgOut{}, err
	}
	svc := t.svcFor(ctx)
	if len(in.Accounts) > 0 {
		bal := map[int64]int64{}
		for _, a := range in.Accounts {
			acc, err := svc.CashAccountByName(a.Account)
			if err != nil {
				return nil, msgOut{}, toolErr(err)
			}
			bal[acc.ID] = finance.FromReais(a.Amount)
		}
		total, err := svc.SetCashBalances(d, bal, in.Note)
		if err != nil {
			return nil, msgOut{}, toolErr(err)
		}
		return nil, msgOut{Message: "caixa de " + finance.BRL(total) + " (soma das contas) informado"}, nil
	}
	cents := finance.FromReais(in.Amount)
	d, err = svc.SetCash(d, cents, in.Note)
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
