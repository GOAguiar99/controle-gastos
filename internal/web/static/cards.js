// Cartões: each card's fatura totals month by month (one series, the card color;
// open and future faturas lighter). The table of faturas sits under each chart.
(function () {
  var el = document.getElementById('card-history');
  if (!el || !window.Chart) return;
  var data = JSON.parse(el.textContent);
  var css = getComputedStyle(document.documentElement);
  function v(name, def) { return css.getPropertyValue(name).trim() || def; }
  var card = v('--card', '#8e24aa'), grid = v('--chart-grid', '#e3e8ec'), muted = v('--muted', '#66727c');
  var brl = new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' });
  var compact = new Intl.NumberFormat('pt-BR', { notation: 'compact', maximumFractionDigits: 1 });
  document.addEventListener('cg:themechange', function () { location.reload(); });
  Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
  Chart.defaults.color = muted;
  Chart.defaults.maintainAspectRatio = false;

  data.forEach(function (h) {
    var cv = document.getElementById('card-hist-' + h.id);
    if (!cv) return;
    new Chart(cv, {
      type: 'bar',
      data: {
        labels: h.labels,
        datasets: [{
          label: 'Fatura', data: h.values, maxBarThickness: 28,
          backgroundColor: h.future.map(function (f) { return f ? card + '66' : card; }),
          borderRadius: { topLeft: 4, topRight: 4 }, borderSkipped: 'bottom'
        }]
      },
      options: {
        plugins: {
          legend: { display: false },
          tooltip: { callbacks: {
            label: function (ctx) { return ' ' + cgMoney.text(brl.format(ctx.parsed.y)) + (h.future[ctx.dataIndex] ? ' (até agora)' : ''); }
          } }
        },
        scales: {
          y: { grid: { color: grid }, border: { display: false }, ticks: { callback: function (x) { return cgMoney.hidden() ? '' : compact.format(x); } } },
          x: { grid: { display: false }, ticks: { maxRotation: 0, autoSkip: true } }
        }
      }
    });
  });
})();
