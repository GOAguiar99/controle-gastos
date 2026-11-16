// Dashboard charts. Colors match the original spreadsheet charts.
(function () {
  var el = document.getElementById('chart-data');
  if (!el || !window.Chart) return;
  var d = JSON.parse(el.textContent);
  var C = {
    income: '#2E7D32', expense: '#C62828', balance: '#1565C0', closing: '#FB8C00',
    card: '#8E24AA', limit: '#D81B60', fixed: '#1E88E5', other: '#FB8C00', grid: '#E3E8EC', muted: '#66727C', navy: '#0B2F4A'
  };
  var brl = new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' });
  var compact = new Intl.NumberFormat('pt-BR', { notation: 'compact', maximumFractionDigits: 1 });

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
  var xAxis = { grid: { display: false } };

  // Highlight the selected month by fading the others.
  function fade(color) {
    return d.labels.map(function (_, i) { return i === d.selected ? color : color + '99'; });
  }

  function chart(id, cfg) {
    var c = document.getElementById(id);
    if (c) new Chart(c, cfg);
  }

  function goMonth(evt, els) {
    if (!els.length) return;
    var i = els[0].index;
    var y = new URLSearchParams(location.search).get('mes');
    var year = (y || '').slice(0, 4) || String(new Date().getFullYear());
    location.href = '/?mes=' + year + '-' + String(i + 1).padStart(2, '0');
  }

  chart('chartIncExp', {
    type: 'bar',
    data: {
      labels: d.labels, datasets: [
        { label: 'Receitas', data: d.income, backgroundColor: fade(C.income), borderRadius: 4 },
        { label: 'Total Gastos', data: d.expense, backgroundColor: fade(C.expense), borderRadius: 4 }
      ]
    },
    options: { scales: { x: xAxis, y: yAxis }, onClick: goMonth }
  });

  chart('chartBalance', {
    data: {
      labels: d.labels, datasets: [
        { type: 'line', label: 'Caixa no fim do mês', data: d.closing, borderColor: C.closing, backgroundColor: C.closing, tension: .3, pointRadius: 3, spanGaps: true, order: 0 },
        { type: 'bar', label: 'Sobra', data: d.balance, backgroundColor: d.balance.map(function (v, i) { var c = v < 0 ? C.expense : C.balance; return i === d.selected ? c : c + '99'; }), borderRadius: 4, order: 1 }
      ]
    },
    options: { scales: { x: xAxis, y: yAxis }, onClick: goMonth }
  });

  chart('chartComp', {
    type: 'bar',
    data: {
      labels: d.labels, datasets: [
        { label: 'Cartão', data: d.card, backgroundColor: C.card },
        { label: 'Fixos', data: d.fixed, backgroundColor: C.fixed },
        { label: 'Outros', data: d.other, backgroundColor: C.other }
      ]
    },
    options: { scales: { x: Object.assign({ stacked: true }, xAxis), y: Object.assign({ stacked: true }, yAxis) }, onClick: goMonth }
  });

  chart('chartCard', {
    data: {
      labels: d.labels, datasets: [
        { type: 'line', label: 'Limite cartão', data: d.labels.map(function () { return d.limit; }), borderColor: C.limit, borderDash: [6, 4], pointRadius: 0, order: 0 },
        { type: 'bar', label: 'Cartão', data: d.card, backgroundColor: d.card.map(function (v, i) { var c = v > d.limit ? C.limit : C.card; return i === d.selected ? c : c + '99'; }), borderRadius: 4, order: 1 }
      ]
    },
    options: { scales: { x: xAxis, y: yAxis }, onClick: goMonth }
  });

  // What is already committed in each future month, split like the spreadsheet.
  chart('chartCommitted', {
    data: {
      labels: d.comX, datasets: [
        { type: 'line', label: 'Entradas', data: d.comIn, borderColor: C.income, backgroundColor: C.income, pointRadius: 3, tension: .2, order: 0, stack: 'in' },
        { type: 'bar', label: 'Cartão', data: d.comCard, backgroundColor: C.card, stack: 'out', order: 1 },
        { type: 'bar', label: 'Fixos', data: d.comFixed, backgroundColor: C.fixed, stack: 'out', order: 1 },
        { type: 'bar', label: 'Outros', data: d.comOther, backgroundColor: C.other, stack: 'out', order: 1 }
      ]
    },
    options: {
      interaction: { mode: 'index', intersect: false },
      scales: { x: Object.assign({ stacked: true }, xAxis), y: Object.assign({ stacked: true }, yAxis) },
      plugins: { tooltip: { callbacks: { footer: function (items) {
        var i = items[0].dataIndex, out = d.comCard[i] + d.comFixed[i] + d.comOther[i];
        return 'Sobra: ' + brl.format(d.comIn[i] - out);
      } } } }
    }
  });

  // Future cash flow: stepped balance line, red below zero.
  var flowLabels = d.flowX.map(function (s) { var p = s.split('-'); return p[2] + '/' + p[1] + '/' + p[0].slice(2); });
  chart('chartFlow', {
    type: 'line',
    data: {
      labels: flowLabels, datasets: [{
        label: 'Saldo projetado', data: d.flowY, stepped: 'before', pointRadius: 0, pointHitRadius: 8, borderWidth: 2,
        borderColor: C.balance, fill: { target: { value: 0 }, above: 'rgba(21,101,192,.10)', below: 'rgba(198,40,40,.16)' },
        segment: { borderColor: function (ctx) { return ctx.p1.parsed.y < 0 ? C.expense : C.balance; } }
      }]
    },
    options: {
      interaction: { mode: 'index', intersect: false },
      plugins: { legend: { display: false } },
      scales: { x: Object.assign({ ticks: { maxTicksLimit: 12, autoSkip: true, maxRotation: 0 } }, xAxis), y: yAxis }
    }
  });
})();
