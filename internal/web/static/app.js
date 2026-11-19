// Small progressive enhancements; every page works without JS.
(function () {
  // Submit GET/POST forms when a [data-autosubmit] field changes (month pickers).
  document.querySelectorAll('[data-autosubmit]').forEach(function (el) {
    el.addEventListener('change', function () { if (el.value) el.form.submit(); });
  });

  // Card and installments only make sense for credit purchases: hide them
  // (and leave them out of the form) for any other payment method.
  document.querySelectorAll('[data-card-fields]').forEach(function (box) {
    var form = box.closest('form'), pay = form && form.elements['payment_method'];
    if (!pay) return;
    function sync() {
      var credit = pay.value === 'credito';
      box.hidden = !credit;
      box.querySelectorAll('select, input').forEach(function (el) { el.disabled = !credit; });
    }
    pay.addEventListener('change', sync);
    sync();
  });

  // A checkbox that saves its form as soon as it changes.
  document.querySelectorAll('[data-autosubmit-check]').forEach(function (el) {
    el.addEventListener('change', function () { el.form.submit(); });
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

// Menus built on <details data-menu>: close on a click outside or on Esc.
document.addEventListener('click', function (e) {
  document.querySelectorAll('details[data-menu][open]').forEach(function (d) {
    if (!d.contains(e.target)) d.open = false;
  });
});
document.addEventListener('keydown', function (e) {
  if (e.key !== 'Escape') return;
  document.querySelectorAll('details[data-menu][open]').forEach(function (d) {
    d.open = false;
    d.querySelector('summary').focus();
  });
});
