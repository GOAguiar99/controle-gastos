// Moeda de exibição: <html data-currency data-fx> (preferência da carteira).
// Charts and scripts get amounts in reais and format them with cgFx, which
// converts at the rate the server used (reais per unit).
(function () {
  var root = document.documentElement;
  var code = root.getAttribute('data-currency') || 'BRL';
  var rate = parseFloat(root.getAttribute('data-fx')) || 1;
  function conv(fmt) { return { format: function (v) { return fmt.format(v / rate); } }; }
  window.cgFx = {
    code: code,
    rate: rate,
    money: function () { return conv(new Intl.NumberFormat('pt-BR', { style: 'currency', currency: code })); },
    compact: function () { return conv(new Intl.NumberFormat('pt-BR', { notation: 'compact', maximumFractionDigits: 1 })); }
  };
})();
