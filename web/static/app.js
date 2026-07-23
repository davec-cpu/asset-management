// Demo script — điền bảng so sánh transfer size (đã nén) và decoded size
// (sau giải nén) của chính các resource trên trang, đọc từ Resource Timing API.
(function () {
  'use strict';

  function formatBytes(n) {
    if (n === 0) return '0 B (cache)';
    if (n < 1024) return n + ' B';
    return (n / 1024).toFixed(1) + ' KB';
  }

  function row(name, transfer, decoded) {
    var tr = document.createElement('tr');

    var tdName = document.createElement('td');
    tdName.textContent = name;
    tr.appendChild(tdName);

    var tdTransfer = document.createElement('td');
    tdTransfer.className = 'num';
    tdTransfer.textContent = formatBytes(transfer);
    tr.appendChild(tdTransfer);

    var tdDecoded = document.createElement('td');
    tdDecoded.className = 'num';
    tdDecoded.textContent = formatBytes(decoded);
    tr.appendChild(tdDecoded);

    var tdSaving = document.createElement('td');
    tdSaving.className = 'num saving';
    if (transfer > 0 && decoded > transfer) {
      tdSaving.textContent = '-' + Math.round(100 * (1 - transfer / decoded)) + '%';
    } else {
      tdSaving.textContent = '—';
    }
    tr.appendChild(tdSaving);

    return tr;
  }

  function fillReport() {
    var tbody = document.querySelector('#report tbody');
    if (!tbody) return;

    var nav = performance.getEntriesByType('navigation')[0];
    if (nav) {
      tbody.appendChild(row('index.html', nav.transferSize, nav.decodedBodySize));
    }

    performance.getEntriesByType('resource').forEach(function (entry) {
      if (entry.initiatorType !== 'link' && entry.initiatorType !== 'script') return;
      var name = entry.name.split('/').pop();
      tbody.appendChild(row(name, entry.transferSize, entry.decodedBodySize));
    });
  }

  if (document.readyState === 'complete') {
    fillReport();
  } else {
    window.addEventListener('load', fillReport);
  }
})();
