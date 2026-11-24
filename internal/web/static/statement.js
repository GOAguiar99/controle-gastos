// Fatura: filter the purchase list instantly (category from the bars or the
// select, text search, order), keeping the filter in the URL so a reload or
// a shared link shows the same view. Without JS the same filters work via GET.
(function () {
  var form = document.querySelector('[data-purchase-filter]');
  var list = document.querySelector('[data-purchases]');
  if (!form || !list) return;
  var q = form.querySelector('[data-filter-q]'), cat = form.querySelector('[data-filter-select]'),
      order = form.querySelector('[data-filter-order]'), clear = form.querySelector('[data-filter-clear]'),
      count = form.querySelector('[data-filter-count]'), total = form.querySelector('[data-filter-total]'),
      empty = document.querySelector('[data-filter-empty]');
  var rows = Array.prototype.slice.call(list.children);
  var brl = cgFx.money();
  function fold(s) { return s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase(); }

  function apply(scroll) {
    var term = fold(q.value.trim()), c = cat.value, n = 0, sum = 0;
    rows.forEach(function (li) {
      var show = (!c || li.dataset.cat === c) && (!term || li.dataset.key.indexOf(term) >= 0);
      li.hidden = !show;
      if (show) { n++; sum += +li.dataset.amount; }
    });
    var by = order.value;
    rows.slice().sort(function (a, b) {
      if (by === 'valor') return b.dataset.amount - a.dataset.amount;
      if (by === 'categoria') return a.dataset.cat.localeCompare(b.dataset.cat, 'pt-BR') || b.dataset.amount - a.dataset.amount;
      return a.dataset.date < b.dataset.date ? -1 : a.dataset.date > b.dataset.date ? 1 : 0;
    }).forEach(function (li) { list.appendChild(li); });
    count.textContent = n;
    total.innerHTML = cgMoney.html(brl.format(sum / 100));
    empty.hidden = n > 0;
    clear.hidden = !c && !term;
    document.querySelectorAll('.bar-filter').forEach(function (a) { a.classList.toggle('on', a.dataset.filterCat === c); });
    var u = new URL(location.href);
    ['cat', 'q', 'ordem'].forEach(function (k) { u.searchParams.delete(k); });
    if (c) u.searchParams.set('cat', c);
    if (term) u.searchParams.set('q', q.value.trim());
    if (by !== 'data') u.searchParams.set('ordem', by);
    history.replaceState(null, '', u.pathname + u.search + '#compras');
    // after a correction the app comes back here: keep the filter
    document.querySelectorAll('form.cat-pick input[name="back"]').forEach(function (b) { b.value = u.pathname + u.search; });
    if (scroll) form.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  // a category in the bars or the largest purchases: filter (again to clear)
  document.addEventListener('click', function (e) {
    var a = e.target.closest('[data-filter-cat]');
    if (!a) return;
    e.preventDefault();
    cat.value = cat.value === a.dataset.filterCat ? '' : a.dataset.filterCat;
    apply(true);
  });
  q.addEventListener('input', function () { apply(false); });
  cat.addEventListener('change', function () { apply(false); });
  order.addEventListener('change', function () { apply(false); });
  form.addEventListener('submit', function (e) { e.preventDefault(); apply(false); });
  clear.addEventListener('click', function (e) { e.preventDefault(); q.value = ''; cat.value = ''; apply(false); });
})();
