// Receitas / Gastos: yearly totals per category (stacked bars) and the
// composition of the month, its year or the whole history (segmented bar).
(function () {
  var el = document.getElementById('cat-data');
  if (!el) return;
  var d = JSON.parse(el.textContent);
  // Fixed categorical order (validated); "Outras" is neutral gray. Color follows the category, never the period.
  var SLOTS = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6'];
  var OTHER = '#8c8a85';
  function color(i) { return d.cats[i] === 'Outras' ? OTHER : SLOTS[i]; }
  var brl = new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' });
  var compact = new Intl.NumberFormat('pt-BR', { notation: 'compact', maximumFractionDigits: 1 });
  var pctFmt = new Intl.NumberFormat('pt-BR', { style: 'percent', maximumFractionDigits: 1 });
  var STATE = { realizado: '', parcial: 'em andamento', previsto: 'previsto' };

  function sum(arr) { return arr.reduce(function (a, b) { return a + b; }, 0); }
  function yearTotal(y) { return sum(d.values.map(function (v) { return v[y]; })); }

  // ---- yearly stacked bars ----
  var cv = document.getElementById('chartYears');
  if (cv && window.Chart) {
    var css = getComputedStyle(document.documentElement);
    var muted = css.getPropertyValue('--muted').trim() || '#66727c';
    var grid = css.getPropertyValue('--chart-grid').trim() || '#e3e8ec';
    var surface = css.getPropertyValue('--surface').trim() || '#ffffff';
    document.addEventListener('cg:themechange', function () { location.reload(); });
    Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
    Chart.defaults.color = muted;
    Chart.defaults.maintainAspectRatio = false;
    Chart.defaults.plugins.legend.labels.boxWidth = 12;
    Chart.defaults.plugins.legend.labels.boxHeight = 12;
    var last = d.cats.length - 1;
    new Chart(cv, {
      type: 'bar',
      data: {
        labels: d.years.map(function (y, i) { return STATE[d.yearState[i]] ? [y, STATE[d.yearState[i]]] : y; }),
        datasets: d.cats.map(function (name, i) {
          return {
            label: name, data: d.values[i], stack: 'y', maxBarThickness: 64,
            // future and in-progress years are lighter: part of it is still a forecast
            backgroundColor: d.yearState.map(function (s) { return s === 'realizado' ? color(i) : color(i) + '99'; }),
            borderColor: surface, borderWidth: { top: 2 }, borderSkipped: 'bottom',
            borderRadius: i === last ? { topLeft: 4, topRight: 4 } : 0
          };
        })
      },
      options: {
        interaction: { mode: 'index', intersect: false },
        scales: {
          x: { stacked: true, grid: { display: false }, ticks: { maxRotation: 0 } },
          y: { stacked: true, grid: { color: grid }, border: { display: false }, ticks: { callback: function (v) { return compact.format(v); } } }
        },
        plugins: {
          legend: { position: 'bottom' },
          tooltip: {
            itemSort: function (a, b) { return b.parsed.y - a.parsed.y; },
            filter: function (it) { return it.parsed.y > 0; },
            callbacks: {
              title: function (items) { var i = items[0].dataIndex; return d.years[i] + (STATE[d.yearState[i]] ? ' (' + STATE[d.yearState[i]] + ')' : ''); },
              label: function (ctx) { return ' ' + ctx.dataset.label + ': ' + brl.format(ctx.parsed.y); },
              footer: function (items) {
                var i = items[0].dataIndex, t = yearTotal(i), out = ['Total: ' + brl.format(t), 'Média mensal: ' + brl.format(t / d.yearMonths[i])];
                if (d.yearProj[i] > 0) out.push('Realizado ' + brl.format(d.yearReal[i]) + ' · previsto ' + brl.format(d.yearProj[i]));
                return out;
              }
            }
          }
        }
      }
    });
  }

  // ---- table view (same numbers as the chart) ----
  var tbl = document.querySelector('[data-year-table]');
  if (tbl) {
    var h = '<thead><tr><th></th>' + d.years.map(function (y, i) {
      return '<th class="num">' + y + (STATE[d.yearState[i]] ? ' <small class="muted">' + STATE[d.yearState[i]] + '</small>' : '') + '</th>';
    }).join('') + '</tr></thead><tbody>';
    d.cats.forEach(function (name, c) {
      h += '<tr><td><span class="dot" style="background:' + color(c) + '"></span> ' + esc(name) + '</td>' +
        d.values[c].map(function (v) { return '<td class="num">' + brl.format(v) + '</td>'; }).join('') + '</tr>';
    });
    h += '</tbody><tfoot><tr><td><strong>Total</strong></td>' + d.years.map(function (_, i) { return '<td class="num"><strong>' + brl.format(yearTotal(i)) + '</strong></td>'; }).join('') + '</tr>' +
      '<tr><td>Média mensal</td>' + d.years.map(function (_, i) { return '<td class="num">' + brl.format(yearTotal(i) / d.yearMonths[i]) + '</td>'; }).join('') + '</tr></tfoot>';
    tbl.innerHTML = h;
  }

  // ---- composition: tall segmented bar + legend with figures ----
  var bar = document.querySelector('[data-comp-bar]');
  var legend = document.querySelector('[data-comp-legend]');
  var seg = document.querySelector('[data-comp-periods]');
  function esc(s) { var n = document.createElement('span'); n.textContent = s; return n.innerHTML; }

  function show(p) {
    var per = d.periods[p], order = per.values.map(function (_, i) { return i; })
      .filter(function (i) { return per.values[i] > 0; })
      .sort(function (a, b) { return d.cats[a] === 'Outras' ? 1 : d.cats[b] === 'Outras' ? -1 : per.values[b] - per.values[a]; });
    bar.setAttribute('aria-label', 'Composição — ' + per.label + ': ' + order.map(function (i) { return d.cats[i] + ' ' + pctFmt.format(per.values[i] / per.total); }).join(', '));
    if (!order.length) { bar.innerHTML = '<p class="empty">Nada neste período.</p>'; legend.innerHTML = ''; return; }
    bar.innerHTML = order.map(function (i) {
      var share = per.values[i] / per.total;
      return '<span style="flex-grow:' + share + ';background:' + color(i) + '" title="' + esc(d.cats[i]) + ': ' + brl.format(per.values[i]) + '">' +
        (share >= 0.08 ? pctFmt.format(share) : '') + '</span>';
    }).join('');
    legend.innerHTML = order.map(function (i) {
      return '<li><span class="dot" style="background:' + color(i) + '"></span><span>' + esc(d.cats[i]) + '</span>' +
        '<strong>' + brl.format(per.values[i]) + '</strong><small class="muted">' + pctFmt.format(per.values[i] / per.total) + '</small></li>';
    }).join('') + '<li class="comp-total"><span></span><span>Total</span><strong>' + brl.format(per.total) + '</strong><small></small></li>';
  }

  if (bar && seg) {
    seg.innerHTML = d.periods.map(function (p, i) { return '<button type="button" data-p="' + i + '">' + esc(p.label) + '</button>'; }).join('');
    var key = 'cg.cat.period';
    function select(i) {
      seg.querySelectorAll('button').forEach(function (b) { var on = +b.dataset.p === i; b.classList.toggle('on', on); b.setAttribute('aria-pressed', on); });
      show(i);
    }
    seg.addEventListener('click', function (e) {
      var b = e.target.closest('button'); if (!b) return;
      try { localStorage.setItem(key, b.dataset.p); } catch (_) { /* private mode */ }
      select(+b.dataset.p);
    });
    var start = 0;
    try { start = +(localStorage.getItem(key) || 0); } catch (_) { /* private mode */ }
    select(start < d.periods.length ? start : 0);
  }
})();
