// Banano Explorer: small progressive enhancements. Every page works without this file.
(function () {
  'use strict';
  var store = {
    get: function (k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { localStorage.setItem(k, v); } catch (e) {} }
  };

  function toast(msg) {
    var t = document.createElement('div');
    t.className = 'toast';
    t.setAttribute('role', 'status');
    t.textContent = msg;
    document.body.appendChild(t);
    setTimeout(function () { t.remove(); }, 1800);
  }

  document.addEventListener('click', function (e) {
    var c = e.target.closest('[data-copy]');
    if (c) {
      e.preventDefault();
      navigator.clipboard.writeText(c.getAttribute('data-copy')).then(function () { toast('Copied'); }, function () { toast('Copy failed'); });
    }
  });

  // Search suggestions for known account names.
  function suggest(input) {
    var box = document.createElement('div');
    box.className = 'sugg';
    box.setAttribute('role', 'listbox');
    box.hidden = true;
    input.parentNode.appendChild(box);
    input.setAttribute('aria-autocomplete', 'list');
    var timer, sel = -1, items = [];
    function render(list) {
      items = list; sel = -1;
      box.textContent = '';
      list.forEach(function (s) {
        var a = document.createElement('a');
        a.href = s.url;
        a.setAttribute('role', 'option');
        var n = document.createElement('b'); n.textContent = s.name; a.appendChild(n);
        if (s.category) { var k = document.createElement('span'); k.className = 'kind'; k.textContent = s.category; a.appendChild(k); }
        var ad = document.createElement('span'); ad.className = 'sa'; ad.textContent = s.short; a.appendChild(ad);
        box.appendChild(a);
      });
      box.hidden = list.length === 0;
      input.setAttribute('aria-expanded', String(!box.hidden));
    }
    function mark() {
      Array.prototype.forEach.call(box.children, function (el, i) { el.setAttribute('aria-selected', String(i === sel)); });
    }
    input.addEventListener('input', function () {
      clearTimeout(timer);
      var q = input.value.trim();
      if (q.length < 2 || /^ban_/i.test(q) || /^[0-9a-f]{64}$/i.test(q)) { render([]); return; }
      timer = setTimeout(function () {
        fetch('/api/suggest?q=' + encodeURIComponent(q)).then(function (r) { return r.json(); }).then(function (l) {
          if (input.value.trim() === q) render(l);
        }).catch(function () {});
      }, 120);
    });
    input.addEventListener('keydown', function (e) {
      if (box.hidden) return;
      if (e.key === 'ArrowDown') { sel = Math.min(sel + 1, items.length - 1); mark(); e.preventDefault(); }
      else if (e.key === 'ArrowUp') { sel = Math.max(sel - 1, -1); mark(); e.preventDefault(); }
      else if (e.key === 'Enter' && sel >= 0) { location.href = items[sel].url; e.preventDefault(); }
      else if (e.key === 'Escape') { render([]); }
    });
    input.addEventListener('blur', function () { setTimeout(function () { box.hidden = true; }, 150); });
  }
  document.querySelectorAll('input[data-suggest]').forEach(suggest);

  // "/" focuses search, like most sites.
  document.addEventListener('keydown', function (e) {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) return;
    var i = document.querySelector('input[data-suggest]');
    if (i && i.offsetParent) { i.focus(); e.preventDefault(); }
    else if (!i || !i.offsetParent) { location.href = '/search'; }
  });

  // Relative ages ("12 s ago") tick along.
  function ago(ts) {
    var d = Date.now() / 1000 - ts;
    if (d < 5) return 'just now';
    if (d < 60) return Math.floor(d) + ' s ago';
    if (d < 3600) return Math.floor(d / 60) + ' min ago';
    if (d < 172800) return Math.floor(d / 3600) + ' h ago';
    return Math.floor(d / 86400) + ' days ago';
  }
  function tick() {
    document.querySelectorAll('[data-ago]').forEach(function (el) { el.textContent = ago(+el.getAttribute('data-ago')); });
  }
  setInterval(tick, 5000);

  // Live list: new confirmations stream in from /api/live.
  var list = document.querySelector('[data-live]'), tpl = document.getElementById('blrow');
  if (list && tpl && window.EventSource) {
    var paused = false, queue = [];
    var dot = document.querySelector('[data-livedot]'), txt = document.querySelector('[data-livetext]');
    var pause = document.querySelector('[data-pause]');
    function state(on, label) {
      if (dot) dot.classList.toggle('off', !on);
      if (txt) txt.textContent = label;
    }
    function row(b) {
      var el = tpl.content.firstElementChild.cloneNode(true);
      var f = function (n) { return el.querySelector('[data-f="' + n + '"]') || document.createElement('span'); };
      f('href').href = '/block/' + b.hash;
      f('href').setAttribute('aria-label', 'Open block ' + b.hash.slice(0, 8) + '…');
      var ty = f('type'); ty.textContent = b.type.charAt(0).toUpperCase() + b.type.slice(1);
      ty.className = 'pill ' + (b.type === 'send' ? 'out' : b.type === 'receive' ? 'in' : 'chg');
      f('who').href = '/account/' + b.account;
      f('name').textContent = b.name || '';
      f('short').textContent = b.short;
      var sign = b.type === 'send' ? '−' : b.type === 'receive' ? '+' : '';
      var am = f('amount');
      am.textContent = b.amount ? (am.hasAttribute('data-sign') ? sign : '') + b.amount : '';
      var sp = f('sp');
      if (b.ms) {
        sp.classList.toggle('slow', !!b.slow);
        f('bar').style.width = Math.min(Math.max(Math.floor(b.ms / 5), 4), 80) + 'px';
        f('ms').textContent = b.ms + ' ms';
      } else { sp.innerHTML = '<span class="faint" title="Confirmed without a vote on our node">—</span>'; }
      f('age').setAttribute('data-ago', b.time);
      f('age').textContent = ago(b.time);
      var h = f('hash'); h.href = '/block/' + b.hash; h.textContent = b.hash.slice(0, 8) + '…';
      el.classList.add('fresh');
      return el;
    }
    function show(b) {
      var e = list.querySelector('[data-empty]'); if (e) e.remove();
      list.insertBefore(row(b), list.firstChild);
      var keep = +list.getAttribute('data-keep') || 100;
      while (list.children.length > keep) list.lastElementChild.remove();
      showLabels();
    }
    var es = new EventSource(list.getAttribute('data-live'));
    es.onopen = function () { state(true, paused ? 'Paused' : 'Live'); };
    es.onerror = function () { state(false, 'Reconnecting'); };
    es.onmessage = function (m) {
      var b; try { b = JSON.parse(m.data); } catch (e) { return; }
      if (paused) { queue.push(b); if (queue.length > 100) queue.shift(); return; }
      show(b);
    };
    if (pause) {
      pause.hidden = false;
      pause.addEventListener('click', function () {
        paused = !paused;
        pause.textContent = paused ? 'Resume' : 'Pause';
        pause.setAttribute('aria-pressed', String(paused));
        state(true, paused ? 'Paused' : 'Live');
        if (!paused) { queue.forEach(show); queue = []; }
      });
    }
  }

  // Filter boxes: hide rows whose data-text doesn't contain the query.
  document.querySelectorAll('input[data-filter]').forEach(function (inp) {
    var box = document.querySelector(inp.getAttribute('data-filter'));
    if (!box) return;
    inp.addEventListener('input', function () {
      var q = inp.value.trim().toLowerCase();
      box.querySelectorAll('[data-text]').forEach(function (row) {
        row.hidden = q !== '' && row.getAttribute('data-text').toLowerCase().indexOf(q) < 0;
      });
    });
  });

  document.querySelectorAll('select[data-autosubmit]').forEach(function (sel) {
    sel.addEventListener('change', function () { sel.form.submit(); });
  });

  // Pages still working something out reload themselves.
  var rf = document.querySelector('[data-refresh]');
  if (rf) setTimeout(function () { location.reload(); }, 1000 * (+rf.getAttribute('data-refresh') || 5));

  // ---- Dialogs (settings, shortcuts, menu, QR, label) ----
  var lastFocus = null;
  function openDialog(id) {
    var d = document.getElementById(id);
    if (!d || !(d.classList.contains('overlay') || d.classList.contains('drawer'))) return false;
    closeDialogs();
    lastFocus = document.activeElement;
    d.hidden = false;
    document.body.style.overflow = 'hidden';
    var f = d.querySelector('input:not([type=hidden]):not([hidden]), select, button, a[href]');
    if (f) f.focus();
    if (id === 'settings') syncSettings();
    return true;
  }
  function closeDialogs() {
    var any = false;
    document.querySelectorAll('.overlay:not([hidden]), .drawer:not([hidden])').forEach(function (d) { d.hidden = true; any = true; });
    document.body.style.overflow = '';
    if (any && lastFocus) { lastFocus.focus(); lastFocus = null; }
    return any;
  }
  document.addEventListener('click', function (e) {
    var o = e.target.closest('[data-open]');
    if (o && openDialog(o.getAttribute('data-open'))) { e.preventDefault(); return; }
    if (e.target.closest('[data-close]') || e.target.classList.contains('overlay')) { e.preventDefault(); closeDialogs(); }
  });
  // Keep Tab inside an open dialog.
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Tab') return;
    var d = document.querySelector('.overlay:not([hidden]) .modal, .drawer:not([hidden])');
    if (!d) return;
    var f = Array.prototype.filter.call(d.querySelectorAll('a[href], button, input, select, textarea'), function (x) { return !x.disabled && x.offsetParent; });
    if (!f.length) return;
    var first = f[0], last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { last.focus(); e.preventDefault(); }
    else if (!e.shiftKey && document.activeElement === last) { first.focus(); e.preventDefault(); }
  });

  // ---- Settings ----
  var prefs = {
    theme: function () { return store.get('theme') || 'auto'; },
    fiat: function () { return store.get('fiat') || 'usd'; },
    time: function () { return store.get('time') || 'local'; },
    dust: function () { return store.get('dust') || 'hide'; }
  };
  function applyPrefs() {
    var t = prefs.theme(), root = document.documentElement;
    if (t === 'auto') root.removeAttribute('data-theme'); else root.setAttribute('data-theme', t);
    root.classList.toggle('hide-dust', prefs.dust() === 'hide');
    showTimes();
    showFiat();
  }
  function syncSettings() {
    document.querySelectorAll('#settings [data-pref]').forEach(function (g) {
      var v = prefs[g.getAttribute('data-pref')]();
      if (g.tagName === 'SELECT') { g.value = v; return; }
      g.querySelectorAll('button').forEach(function (b) { b.setAttribute('aria-pressed', String(b.getAttribute('data-v') === v)); });
    });
    var n = Object.keys(labels()).length, lc = document.querySelector('[data-labelcount]');
    if (lc) lc.textContent = n ? n + ' label' + (n === 1 ? '' : 's') : 'No labels yet';
  }
  document.querySelectorAll('#settings [data-pref]').forEach(function (g) {
    var key = g.getAttribute('data-pref');
    if (g.tagName === 'SELECT') {
      g.addEventListener('change', function () { store.set(key, g.value); applyPrefs(); });
      return;
    }
    g.addEventListener('click', function (e) {
      var b = e.target.closest('button[data-v]');
      if (!b) return;
      store.set(key, b.getAttribute('data-v'));
      syncSettings();
      applyPrefs();
    });
  });

  // Times: <time data-ts> show UTC from the server; switch to the visitor's zone.
  var months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  function pad(n) { return n < 10 ? '0' + n : '' + n; }
  function showTimes() {
    var utc = prefs.time() === 'utc';
    document.querySelectorAll('time[data-ts]').forEach(function (el) {
      var d = new Date(+el.getAttribute('data-ts') * 1000);
      if (isNaN(d)) return;
      var s = utc
        ? d.getUTCDate() + ' ' + months[d.getUTCMonth()] + ' ' + d.getUTCFullYear() + ', ' + pad(d.getUTCHours()) + ':' + pad(d.getUTCMinutes()) + ' UTC'
        : d.getDate() + ' ' + months[d.getMonth()] + ' ' + d.getFullYear() + ', ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
      el.textContent = s;
      el.setAttribute('datetime', d.toISOString());
    });
  }

  // Fiat: the server renders USD; convert or hide per the setting.
  var prices = {};
  try { var pm = document.querySelector('meta[name="x-prices"]'); if (pm) prices = JSON.parse(pm.content); } catch (e) {}
  var symbols = { usd: '$', eur: '€', try: '₺', gbp: '£', btc: '₿' };
  function money(v, cur) {
    var sym = symbols[cur] || '';
    if (cur === 'btc') return sym + v.toFixed(v >= 0.01 ? 4 : 8);
    if (v >= 100) return sym + Math.round(v).toLocaleString('en-US');
    if (v >= 0.01) return sym + v.toFixed(2);
    return sym + v.toFixed(4);
  }
  function showFiat() {
    var cur = prefs.fiat();
    document.querySelectorAll('[data-fiat]').forEach(function (el) {
      var p = prices[cur];
      if (cur === 'none' || !p) { el.hidden = cur === 'none' || !prices.usd; return; }
      el.hidden = false;
      el.textContent = '≈ ' + money(+el.getAttribute('data-fiat') * p, cur);
    });
  }

  // ---- Your own labels ----
  function labels() { try { return JSON.parse(store.get('labels') || '{}') || {}; } catch (e) { return {}; } }
  function saveLabels(l) { store.set('labels', JSON.stringify(l)); }
  function showLabels() {
    var l = labels();
    document.querySelectorAll('[data-mine]').forEach(function (el) { el.remove(); });
    document.querySelectorAll('a[href^="/account/ban_"]').forEach(function (a) {
      var addr = a.getAttribute('href').slice(9).split('?')[0];
      if (!l[addr] || a.classList.contains('stretch')) return;
      var w = document.createElement('span');
      w.setAttribute('data-mine', '');
      var m = document.createElement('span'); m.className = 'mine'; m.textContent = l[addr];
      var y = document.createElement('span'); y.className = 'yours'; y.textContent = 'your label';
      w.appendChild(m); w.appendChild(y); w.appendChild(document.createTextNode(' '));
      a.insertBefore(w, a.firstChild);
    });
    document.querySelectorAll('[data-label-for]').forEach(function (h) {
      var addr = h.getAttribute('data-label-for');
      if (!l[addr]) return;
      var w = document.createElement('span');
      w.setAttribute('data-mine', '');
      w.innerHTML = ' <span class="mine" style="font-size: 20px; font-weight: 600"></span><span class="yours">your label</span>';
      w.firstElementChild.textContent = l[addr];
      h.appendChild(w);
    });
  }
  var lf = document.getElementById('labelform');
  if (lf) {
    var addr = lf.getAttribute('data-addr'), inp = lf.querySelector('input');
    inp.value = labels()[addr] || '';
    lf.addEventListener('submit', function (e) {
      e.preventDefault();
      var l = labels(), v = inp.value.trim().slice(0, 40);
      if (v) l[addr] = v; else delete l[addr];
      saveLabels(l);
      closeDialogs();
      showLabels();
      toast(v ? 'Label saved' : 'Label removed');
    });
  }
  var ex = document.querySelector('[data-export]');
  if (ex) ex.addEventListener('click', function () {
    var blob = new Blob([JSON.stringify({ labels: labels() }, null, 2)], { type: 'application/json' });
    var a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'banano-explorer-labels.json';
    a.click();
    setTimeout(function () { URL.revokeObjectURL(a.href); }, 1000);
  });
  var im = document.querySelector('[data-import]');
  if (im) im.addEventListener('change', function () {
    var f = im.files && im.files[0];
    if (!f) return;
    f.text().then(function (t) {
      var d = JSON.parse(t), l = labels(), n = 0;
      Object.keys(d.labels || {}).forEach(function (k) {
        if (/^ban_[13][13456789abcdefghijkmnopqrstuwxyz]{59}$/.test(k) && typeof d.labels[k] === 'string') { l[k] = d.labels[k].slice(0, 40); n++; }
      });
      saveLabels(l);
      syncSettings();
      showLabels();
      toast('Imported ' + n + ' label' + (n === 1 ? '' : 's'));
    }).catch(function () { toast("That file isn't a labels export"); });
    im.value = '';
  });

  // ---- QR sheet: the server draws the code; we just change the amount ----
  var qa = document.getElementById('qr-amount');
  if (qa) {
    var qimg = document.getElementById('qr-img'), qlink = document.getElementById('qr-link'), qdl = document.getElementById('qr-dl');
    var qaddr = qa.getAttribute('data-addr');
    var updateQR = function () {
      var raw = toRaw(qa.value);
      var uri = 'ban:' + qaddr + (raw ? '?amount=' + raw : '');
      var src = '/qr/' + qaddr + '.svg' + (raw ? '?amount=' + raw : '');
      qimg.src = src; qdl.href = src; qlink.textContent = uri;
      qlink.closest('[data-copy-wrap]').querySelector('[data-copy]').setAttribute('data-copy', uri);
    };
    qa.addEventListener('input', updateQR);
  }
  // "1.5" BAN -> raw string, or "" if empty/invalid.
  function toRaw(v) {
    v = (v || '').trim().replace(/,/g, '');
    var m = /^(\d{0,10})(?:\.(\d{0,29}))?$/.exec(v);
    if (!m || (!m[1] && !m[2])) return '';
    var raw = ((m[1] || '0') + ((m[2] || '') + '00000000000000000000000000000').slice(0, 29)).replace(/^0+/, '');
    return raw || '';
  }

  // Share: the phone's share sheet when there is one, else copy the link.
  document.addEventListener('click', function (e) {
    var b = e.target.closest('[data-share]');
    if (!b) return;
    var url = location.origin + b.getAttribute('data-share');
    if (navigator.share) { navigator.share({ title: b.getAttribute('data-title') || document.title, url: url }).catch(function () {}); }
    else navigator.clipboard.writeText(url).then(function () { toast('Link copied'); });
  });

  // ---- Keyboard shortcuts ----
  var rowIdx = -1, gPending = false;
  function rows() { return Array.prototype.filter.call(document.querySelectorAll('.rowlink'), function (r) { return r.offsetParent; }); }
  function select(i) {
    var r = rows();
    if (!r.length) return;
    rowIdx = Math.max(0, Math.min(i, r.length - 1));
    r.forEach(function (x) { x.classList.remove('selected'); });
    r[rowIdx].classList.add('selected');
    r[rowIdx].scrollIntoView({ block: 'nearest' });
  }
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') { if (closeDialogs()) e.preventDefault(); return; }
    if (e.ctrlKey || e.metaKey || e.altKey || /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) return;
    if (document.querySelector('.overlay:not([hidden])') && e.key !== '?') return;
    if (gPending) {
      gPending = false;
      if (e.key === 'h') { location.href = '/'; return; }
      if (e.key === 'r') { location.href = '/representatives'; return; }
    }
    switch (e.key) {
      case 'g': gPending = true; setTimeout(function () { gPending = false; }, 1200); break;
      case '?': openDialog('shortcuts'); e.preventDefault(); break;
      case 'j': select(rowIdx + 1); break;
      case 'k': select(rowIdx - 1); break;
      case 'Enter': { var r = rows()[rowIdx]; var a = r && r.querySelector('a.stretch'); if (a) location.href = a.href; break; }
      case 'ArrowLeft': { var p = document.querySelector('a[rel="prev"]'); if (p) location.href = p.href; break; }
      case 'ArrowRight': { var n = document.querySelector('a[rel="next"]'); if (n) location.href = n.href; break; }
      case 'c': { var c = document.querySelector('[data-copy][data-main]') || document.querySelector('main [data-copy]'); if (c) c.click(); break; }
      case 'q': openDialog('qr'); break;
    }
  });

  applyPrefs();
  showLabels();
  if (location.hash.length > 1) openDialog(location.hash.slice(1));
})();
