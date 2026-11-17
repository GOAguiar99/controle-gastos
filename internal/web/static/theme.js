// Theme: "auto" (follow the system), "dark" or "light", remembered per browser.
// Loaded in <head> without defer so the page never flashes the wrong theme.
(function () {
  var KEY = 'cg-theme', ORDER = ['auto', 'dark', 'light'];
  var LABEL = { auto: '◐ Tema: automático', dark: '☾ Tema: escuro', light: '☀ Tema: claro' };
  function get() { try { return localStorage.getItem(KEY) || 'auto'; } catch (e) { return 'auto'; } }
  function apply(t) {
    if (t === 'auto') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.setAttribute('data-theme', t);
  }
  apply(get());

  function label() {
    var t = get();
    document.querySelectorAll('[data-theme-toggle]').forEach(function (b) {
      b.textContent = b.hasAttribute('data-short') ? LABEL[t].slice(0, 1) : LABEL[t];
      b.title = LABEL[t] + ' (clique para trocar)';
      b.setAttribute('aria-label', b.title);
    });
  }
  document.addEventListener('DOMContentLoaded', function () {
    label();
    document.querySelectorAll('[data-theme-toggle]').forEach(function (b) {
      b.addEventListener('click', function () {
        var next = ORDER[(ORDER.indexOf(get()) + 1) % ORDER.length];
        try { localStorage.setItem(KEY, next); } catch (e) {}
        apply(next);
        label();
        document.dispatchEvent(new Event('cg:themechange'));
      });
    });
  });
  // charts read colors once; redraw them when the theme really changes
  var mq = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)');
  if (mq && mq.addEventListener) mq.addEventListener('change', function () {
    if (get() === 'auto') document.dispatchEvent(new Event('cg:themechange'));
  });
})();
