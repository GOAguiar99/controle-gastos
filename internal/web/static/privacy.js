// "Ocultar valores": masks every amount (R$) so a screenshot can be shared
// without showing how much comes in or goes out. Display only, remembered per
// browser. Loaded in <head> without defer so values never flash on load.
// Server-rendered amounts carry <span class="money"> (the brl template helper);
// app.css hides them under html.hide-money. Charts and scripts use cgMoney.
(function () {
  var KEY = 'cg-hide-money', CLS = 'hide-money', MASK = 'R$ •••••';
  var root = document.documentElement;

  function hidden() { return root.classList.contains(CLS); }
  try { if (localStorage.getItem(KEY) === '1') root.classList.add(CLS); } catch (e) {}

  window.cgMoney = {
    hidden: hidden,
    // text: an amount for plain text (chart tooltips, ticks): masked when hidden
    text: function (s) { return hidden() ? MASK : s; },
    // html: the same markup as the brl template helper, for scripts that build HTML
    html: function (s) { return '<span class="money"><span>' + s + '</span></span>'; }
  };

  function label() {
    var on = hidden(), t = on ? 'Mostrar valores' : 'Ocultar valores';
    document.querySelectorAll('[data-money-toggle]').forEach(function (b) {
      b.setAttribute('aria-pressed', on ? 'true' : 'false');
      b.title = t;
      b.setAttribute('aria-label', t);
    });
  }

  function set(on) {
    root.classList.toggle(CLS, on);
    try { localStorage.setItem(KEY, on ? '1' : '0'); } catch (e) {}
    label();
    // charts format ticks when they draw: redraw them with the new setting
    if (window.Chart && Chart.instances) {
      Object.keys(Chart.instances).forEach(function (k) { Chart.instances[k].update('none'); });
    }
  }

  document.addEventListener('DOMContentLoaded', function () {
    label();
    document.querySelectorAll('[data-money-toggle]').forEach(function (b) {
      b.addEventListener('click', function () { set(!hidden()); });
    });
  });
})();
