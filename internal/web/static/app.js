// Small progressive enhancements; every page works without JS.
(function () {
  // Submit GET/POST forms when a [data-autosubmit] field changes (month pickers, bucket selects).
  document.querySelectorAll('[data-autosubmit]').forEach(function (el) {
    el.addEventListener('change', function () { if (el.value) el.form.submit(); });
  });

  // Confirm destructive actions.
  document.querySelectorAll('form[data-confirm]').forEach(function (f) {
    f.addEventListener('submit', function (e) {
      if (!window.confirm(f.dataset.confirm)) e.preventDefault();
    });
  });

  // Copy buttons: [data-copy="#input"] copies that input's value.
  document.querySelectorAll('[data-copy]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var input = document.querySelector(btn.dataset.copy);
      if (!input) return;
      input.select();
      var done = function () {
        var label = btn.textContent;
        btn.textContent = 'Copiado';
        setTimeout(function () { btn.textContent = label; }, 1500);
      };
      if (navigator.clipboard) navigator.clipboard.writeText(input.value).then(done, function () { document.execCommand('copy'); done(); });
      else { document.execCommand('copy'); done(); }
    });
  });

  // Recurrence fields: show only the duration input that matters.
  document.querySelectorAll('[data-recurrence]').forEach(function (box) {
    var mode = box.querySelector('[data-endmode]');
    function sync() {
      box.querySelectorAll('[data-show]').forEach(function (l) {
        var on = l.dataset.show === mode.value;
        l.style.display = on ? '' : 'none';
        l.querySelector('input').disabled = !on;
      });
    }
    mode.addEventListener('change', sync);
    sync();
  });

  // Close other "Confirmar" popovers when one opens.
  document.querySelectorAll('details.pop').forEach(function (d) {
    d.addEventListener('toggle', function () {
      if (!d.open) return;
      document.querySelectorAll('details.pop[open]').forEach(function (o) { if (o !== d) o.open = false; });
    });
  });
})();

// On narrow screens, forms marked data-collapse-mobile start collapsed.
if (window.matchMedia && window.matchMedia('(max-width: 860px)').matches) {
  document.querySelectorAll('details[data-collapse-mobile]').forEach(function (d) { d.open = false; });
}
