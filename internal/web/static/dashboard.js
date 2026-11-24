// Dashboard: cash-flow charts and the period/view filters of the flow card.
(function () {
  var el = document.getElementById('chart-data');
  if (!el) return;
  var d = JSON.parse(el.textContent);
  // colors come from the theme (app.css); 6-digit hex so an alpha suffix can be appended
  var css = getComputedStyle(document.documentElement);
  function v(name, def) { return css.getPropertyValue(name).trim() || def; }
  var C = {
    income: v('--income', '#2e7d32'), expense: v('--expense', '#c62828'), balance: v('--balance', '#1565c0'),
    card: v('--card', '#8e24aa'), fixed: v('--fixed', '#1e88e5'), other: v('--other', '#fb8c00'), forecast: v('--forecast', '#78909c'),
    grid: v('--chart-grid', '#e3e8ec'), muted: v('--muted', '#66727c')
  };
  document.addEventListener('cg:themechange', function () { location.reload(); });
  var brl = cgFx.money();
  var compact = cgFx.compact();
  var committed = null;

  if (window.Chart) {
    Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
    Chart.defaults.color = C.muted;
    Chart.defaults.maintainAspectRatio = false;
    Chart.defaults.plugins.legend.labels.boxWidth = 12;
    Chart.defaults.plugins.legend.labels.boxHeight = 12;
    Chart.defaults.plugins.tooltip.callbacks.label = function (ctx) {
      var v = ctx.parsed.y;
      return v == null ? null : ' ' + ctx.dataset.label + ': ' + cgMoney.text(brl.format(v));
    };
    var yAxis = { grid: { color: C.grid }, border: { display: false }, ticks: { callback: function (v) { return cgMoney.hidden() ? '' : compact.format(v); } } };
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
        ].concat(d.comForecast ? [{ type: 'bar', label: 'Previsão', data: [], stack: 'out', order: 1 }] : []) },
        options: {
          interaction: { mode: 'index', intersect: false },
          scales: { x: Object.assign({ stacked: true }, xAxis), y: Object.assign({ stacked: true }, yAxis) },
          plugins: { tooltip: { callbacks: { footer: function (items) {
            var ds = items[0].chart.data.datasets, i = items[0].dataIndex;
            var out = 0;
            for (var k = 1; k < ds.length; k++) out += ds[k].data[i] || 0;
            return 'Sobra: ' + cgMoney.text(brl.format(ds[0].data[i] - out));
          } } } }
        }
      });
    }

    // Future cash flow: stepped balance line, red below zero. With the
    // spending forecast on, the other two scenarios are dashed around it.
    var cf = document.getElementById('chartFlow');
    if (cf) {
      var main = {
        label: d.scenario ? 'Saldo projetado (' + label(d.scenario) + ')' : 'Saldo projetado',
        data: d.flowY, stepped: 'before', pointRadius: 0, pointHitRadius: 8, borderWidth: 2, order: 0,
        borderColor: C.balance, fill: { target: { value: 0 }, above: 'rgba(21,101,192,.10)', below: 'rgba(198,40,40,.16)' },
        segment: { borderColor: function (ctx) { return ctx.p1.parsed.y < 0 ? C.expense : C.balance; } }
      };
      var sets = [main];
      if (d.flowScen) {
        ['otimista', 'pessimista'].forEach(function (sc) {
          if (sc === d.scenario || !d.flowScen[sc]) return;
          sets.push({ label: label(sc), data: d.flowScen[sc], stepped: 'before', pointRadius: 0, pointHitRadius: 8,
            borderWidth: 1.5, borderDash: [5, 4], borderColor: sc === 'otimista' ? C.income : C.expense, fill: false, order: 1 });
        });
        if (d.scenario !== 'base' && d.flowScen.base) {
          sets.push({ label: label('base'), data: d.flowScen.base, stepped: 'before', pointRadius: 0, pointHitRadius: 8,
            borderWidth: 1.5, borderDash: [2, 3], borderColor: C.muted, fill: false, order: 1 });
        }
      }
      new Chart(cf, {
        type: 'line',
        data: {
          labels: d.flowX.map(function (s) { var p = s.split('-'); return p[2] + '/' + p[1] + '/' + p[0].slice(2); }),
          datasets: sets
        },
        options: {
          interaction: { mode: 'index', intersect: false },
          plugins: { legend: { display: sets.length > 1 } },
          scales: { x: Object.assign({}, xAxis, { ticks: { maxTicksLimit: 8, autoSkip: true, maxRotation: 0 } }), y: yAxis }
        }
      });
    }
  }

  function label(sc) { return { otimista: 'Otimista', base: 'Base', pessimista: 'Pessimista' }[sc] || sc; }

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
    if (d.comForecast) {
      committed.data.datasets[4].data = pick(d.comForecast, m);
      committed.data.datasets[4].backgroundColor = colors(C.forecast);
    }
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
