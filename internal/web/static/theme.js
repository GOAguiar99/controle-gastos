// Theme: "auto" (follow the system) or one of the palettes in app.css, remembered per browser.
// Loaded in <head> without defer so the page never flashes the wrong theme.
// Palettes live in app.css as :root[data-theme="<id>"]; this list feeds the picker menu.
(function () {
  var KEY = 'cg-theme';
  // id, name, dark?, swatch [background, accent, text]
  var THEMES = [
    ['auto', 'Automático (sistema)', null, null],
    ['light', 'Claro', false, ['#f6f8f9', '#0b2f4a', '#1f1f1f']],
    ['dark', 'Escuro', true, ['#0f1418', '#4f8cc9', '#e3e8ec']],
    ['rosa', 'Rosa claro', false, ['#fdf4f7', '#c2185b', '#2b1a22']],
    ['rose-pine', 'Rosé Pine', true, ['#191724', '#eb6f92', '#e0def4']],
    ['dracula', 'Dracula', true, ['#282a36', '#bd93f9', '#ff79c6']],
    ['catppuccin', 'Catppuccin Mocha', true, ['#1e1e2e', '#cba6f7', '#a6e3a1']],
    ['tokyo-night', 'Tokyo Night', true, ['#1a1b26', '#7aa2f7', '#bb9af7']],
    ['nord', 'Nord', true, ['#2e3440', '#88c0d0', '#a3be8c']],
    ['gruvbox', 'Gruvbox', true, ['#282828', '#fabd2f', '#b8bb26']],
    ['solarized', 'Solarized claro', false, ['#fdf6e3', '#268bd2', '#cb4b16']],
    ['preto', 'Preto (AMOLED)', true, ['#000000', '#4f8cc9', '#e8e8e8']],
    ['terminal', 'Terminal (fósforo)', true, ['#0a0f0a', '#33ff66', '#5fd7ff']]
  ];
  var BY_ID = {};
  THEMES.forEach(function (t) { BY_ID[t[0]] = t; });

  function get() {
    var t;
    try { t = localStorage.getItem(KEY); } catch (e) {}
    return BY_ID[t] ? t : 'auto';
  }
  function apply(t) {
    if (t === 'auto') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.setAttribute('data-theme', t);
  }
  apply(get());

  function icon(t) { var d = BY_ID[t][2]; return d === null ? '◐' : d ? '☾' : '☀'; }

  function label() {
    var t = get(), title = 'Tema: ' + BY_ID[t][1] + ' (clique para trocar)';
    document.querySelectorAll('[data-theme-toggle]').forEach(function (b) {
      b.textContent = b.hasAttribute('data-short') ? icon(t) : 'Tema: ' + BY_ID[t][1];
      b.title = title;
      b.setAttribute('aria-label', title);
    });
    document.querySelectorAll('[data-theme-set]').forEach(function (o) {
      o.setAttribute('aria-checked', o.getAttribute('data-theme-set') === t ? 'true' : 'false');
    });
  }

  function set(next) {
    if (next === get()) return;
    try { localStorage.setItem(KEY, next); } catch (e) {}
    apply(next);
    label();
    document.dispatchEvent(new Event('cg:themechange'));
  }

  function fill(panel) {
    THEMES.forEach(function (t) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'theme-option';
      b.setAttribute('role', 'menuitemradio');
      b.setAttribute('data-theme-set', t[0]);
      var sw = document.createElement('span');
      sw.className = 'theme-swatch';
      sw.setAttribute('aria-hidden', 'true');
      if (t[3]) {
        sw.style.background = t[3][0];
        t[3].slice(1).forEach(function (c) {
          var dot = document.createElement('i');
          dot.style.background = c;
          sw.appendChild(dot);
        });
      } else {
        sw.classList.add('auto');
      }
      b.appendChild(sw);
      b.appendChild(document.createTextNode(t[1]));
      b.addEventListener('click', function () {
        var d = panel.closest('details');
        if (d) d.open = false;
        set(t[0]);
      });
      panel.appendChild(b);
    });
  }

  document.addEventListener('DOMContentLoaded', function () {
    document.querySelectorAll('[data-theme-menu]').forEach(fill);
    label();
  });
  // charts read colors once; redraw them when the theme really changes
  var mq = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)');
  if (mq && mq.addEventListener) mq.addEventListener('change', function () {
    if (get() === 'auto') document.dispatchEvent(new Event('cg:themechange'));
  });
})();
