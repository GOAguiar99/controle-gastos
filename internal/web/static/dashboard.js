// Dashboard: cash-flow charts and the period/view filters of the flow card.
(function () {
  var el = document.getElementById('chart-data');
  if (!el) return;
  var d = JSON.parse(el.textContent);
  var C = {
    income: '#2E7D32', expense: '#C62828', balance: '#1565C0',
    card: '#8E24AA', fixed: '#1E88E5', other: '#FB8C00', grid: '#E3E8EC', muted: '#66727C'
  };
  var brl = new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' });
  var compact = new Intl.NumberFormat('pt-BR', { notation: 'compact', maximumFractionDigits: 1 });
  var committed = null;

  if (window.Chart) {
    Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
    Chart.defaults.color = C.muted;
    Chart.defaults.maintainAspectRatio = false;
    Chart.defaults.plugins.legend.labels.boxWidth = 12;
    Chart.defaults.plugins.legend.labels.boxHeight = 12;
    Chart.defaults.plugins.tooltip.callbacks.label = function (ctx) {
      var v = ctx.parsed.y;
      return v == null ? null : ' ' + ctx.dataset.label + ': ' + brl.format(v);
    };
    var yAxis = { grid: { color: C.grid }, border: { display: false }, ticks: { callback: function (v) { return compact.format(v); } } };
    var xAxis = { grid: { display: false }, ticks: { maxRotation: 0, autoSkip: true } };

    // Realized and committed per month (Cartão / Fixos / Outros vs entradas).
    var cc = document.getElementById('chartCommitted');
    if (cc) {
      committed = new Chart(cc, {
        data: { labels: [], datasets: [
          { type: 'line', label: 'Entradas', data: [], borderColor: C.income, backgroundColor: C.income, pointRadius: 3, tension: .2, order: 0, stack: 'in' },
          { type: 'bar', label: 'Cartão', data: [], stack: 'out', order: 1 },
          { type: 'bar', label: 'Fixos', data: [], stack: 'out', order: 1 },
          { type: 'bar', label: 'Outros', data: [], stack: 'out', order: 1 }
        ] },
        options: {
          interaction: { mode: 'index', intersect: false },
          scales: { x: Object.assign({ stacked: true }, xAxis), y: Object.assign({ stacked: true }, yAxis) },
          plugins: { tooltip: { callbacks: { footer: function (items) {
            var ds = items[0].chart.data.datasets, i = items[0].dataIndex;
            return 'Sobra: ' + brl.format(ds[0].data[i] - ds[1].data[i] - ds[2].data[i] - ds[3].data[i]);
          } } } }
        }
      });
    }

    // Future cash flow: stepped balance line, red below zero.
    var cf = document.getElementById('chartFlow');
    if (cf) {
      new Chart(cf, {
        type: 'line',
        data: {
          labels: d.flowX.map(function (s) { var p = s.split('-'); return p[2] + '/' + p[1] + '/' + p[0].slice(2); }),
          datasets: [{
            label: 'Saldo projetado', data: d.flowY, stepped: 'before', pointRadius: 0, pointHitRadius: 8, borderWidth: 2,
            borderColor: C.balance, fill: { target: { value: 0 }, above: 'rgba(21,101,192,.10)', below: 'rgba(198,40,40,.16)' },
            segment: { borderColor: function (ctx) { return ctx.p1.parsed.y < 0 ? C.expense : C.balance; } }
          }]
        },
        options: {
          interaction: { mode: 'index', intersect: false },
          plugins: { legend: { display: false } },
          scales: { x: Object.assign({}, xAxis, { ticks: { maxTicksLimit: 8, autoSkip: true, maxRotation: 0 } }), y: yAxis }
        }
      });
    }
  }

  // ---- filters ----
  function keep(period) {
    return d.comPast.map(function (p) { return period === 'all' || (period === 'past') === p; });
  }

  function pick(arr, mask) { return arr.filter(function (_, i) { return mask[i]; }); }

  function applyPeriod(period) {
    document.querySelectorAll('.flow-table [data-when]').forEach(function (tr) {
      tr.hidden = period !== 'all' && tr.dataset.when !== period;
    });
    document.querySelectorAll('[data-period-hide]').forEach(function (n) {
      n.hidden = n.dataset.periodHide === period;
    });
    if (!committed) return;
    var m = keep(period);
    var past = pick(d.comPast, m);
    function colors(c) { return past.map(function (p) { return p ? c + '80' : c; }); }
    committed.data.labels = pick(d.comX, m);
    committed.data.datasets[0].data = pick(d.comIn, m);
    committed.data.datasets[1].data = pick(d.comCard, m);
    committed.data.datasets[2].data = pick(d.comFixed, m);
    committed.data.datasets[3].data = pick(d.comOther, m);
    committed.data.datasets[1].backgroundColor = colors(C.card);
    committed.data.datasets[2].backgroundColor = colors(C.fixed);
    committed.data.datasets[3].backgroundColor = colors(C.other);
    committed.update();
  }

  function applyView(view) {
    document.querySelectorAll('[data-flow-part]').forEach(function (n) {
      n.hidden = view !== 'all' && n.dataset.flowPart !== view;
    });
  }

  function load(key, def) { try { return localStorage.getItem(key) || def; } catch (e) { return def; } }
  function save(key, v) { try { localStorage.setItem(key, v); } catch (e) { /* private mode */ } }

  function setup(attr, key, apply) {
    var btns = document.querySelectorAll('[data-flow-filters] [data-' + attr + ']');
    function select(v) {
      btns.forEach(function (b) { b.classList.toggle('on', b.dataset[attr] === v); b.setAttribute('aria-pressed', b.dataset[attr] === v); });
      apply(v);
    }
    btns.forEach(function (b) {
      b.addEventListener('click', function () { save(key, b.dataset[attr]); select(b.dataset[attr]); });
    });
    select(load(key, 'all'));
  }

  setup('period', 'cg.flow.period', applyPeriod);
  setup('view', 'cg.flow.view', applyView);
})();
