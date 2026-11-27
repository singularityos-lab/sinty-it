(function () {
  'use strict';
  const $ = (id) => document.getElementById(id);
  const el = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };
  const icon = (name, color) => {
    const i = document.createElement('sinty-icon');
    i.setAttribute('name', name);
    if (color) i.setAttribute('color', '');
    return i;
  };
  const toast = (text, opts) => SintyUI.toast(text, opts);
  const store = {
    get: (k, d) => { try { return localStorage.getItem('sinty-it.' + k) || d; } catch { return d; } },
    set: (k, v) => { try { localStorage.setItem('sinty-it.' + k, v); } catch { /* private mode */ } },
  };

  const kinds = {
    lock: ['Lock Screens', 'system-lock-screen-symbolic'],
    logout: ['Sign Everyone Out', 'system-log-out-symbolic'],
    update: ['Install Updates', 'software-update-available-symbolic'],
    reboot: ['Restart', 'system-reboot-symbolic'],
    'sync-policy': ['Apply Policy', 'emblem-synchronizing-symbolic'],
    wipe: ['Erase', 'edit-delete-symbolic'],
  };
  const kindName = (k) => (kinds[k] ? kinds[k][0] : k);
  const states = {
    waiting_approval: ['Waiting for Approval', 'warn'],
    queued: ['Queued', 'off'],
    sent: ['Sent', 'off'],
    done: ['Done', 'ok'],
    failed: ['Failed', 'bad'],
    expired: ['Expired', 'off'],
    canceled: ['Canceled', 'off'],
  };
  const problems = {
    not_encrypted: 'Not encrypted',
    not_sealed: 'Keys not sealed to this hardware',
    firewall_off: 'Firewall off',
    not_seen: 'Not seen for a week',
  };

  const state = {
    view: 'overview',
    arg: '',
    me: '',
    devices: [],
    waiting: [],
    selected: new Set(),
    filter: store.get('filter', 'all'),
    group: '',
    who: 'all',
    current: null,
  };

  async function call(method, path, body) {
    const init = { method, credentials: 'same-origin', headers: { 'X-Sinty-Request': '1' } };
    if (body !== undefined) {
      init.body = JSON.stringify(body);
      init.headers['Content-Type'] = 'application/json';
    }
    const r = await fetch(path, init);
    const back = encodeURIComponent(location.pathname + location.hash);
    if (r.status === 401) {
      location.assign('/login?back=' + back);
      throw new Error('signed out');
    }
    if (r.status === 403) {
      const e = await r.clone().json().catch(() => ({}));
      if (e.error === 'stronger') {
        location.assign('/login?acr=sinty:3&back=' + back);
        throw new Error('stronger');
      }
    }
    return r;
  }

  const reasons = {
    second_admin: 'Another administrator has to approve a request you made.',
    not_waiting: 'That request is no longer waiting.',
    not_found: 'It is no longer there.',
    invalid_request: 'Check the values and try again.',
    unknown_command: 'This device does not know that command.',
  };
  async function why(r) {
    const e = await r.json().catch(() => ({}));
    return reasons[e.error] || 'Something went wrong. Try again.';
  }

  const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
  const dayFmt = new Intl.DateTimeFormat(undefined, { weekday: 'long', day: 'numeric', month: 'long' });
  const fullFmt = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  function ago(when) {
    const s = Math.max(0, (Date.now() - new Date(when).getTime()) / 1000);
    if (s < 90) return 'Just now';
    if (s < 5400) return Math.round(s / 60) + ' minutes ago';
    if (s < 129600) return Math.round(s / 3600) + ' hours ago';
    return Math.round(s / 86400) + ' days ago';
  }
  function until(when) {
    const s = (new Date(when).getTime() - Date.now()) / 1000;
    if (s < 5400) return 'in ' + Math.max(1, Math.round(s / 60)) + ' minutes';
    if (s < 129600) return 'in ' + Math.round(s / 3600) + ' hours';
    return 'in ' + Math.round(s / 86400) + ' days';
  }
  const cap = (t) => (t ? t[0].toUpperCase() + t.slice(1) : t);
  const gb = (n) => (n / 1e9).toFixed(n < 1e10 ? 1 : 0) + ' GB';
  const system = (f) => [f.os, f.version].filter(Boolean).join(' ');
  const seenToday = (d) => Date.now() - new Date(d.seen).getTime() < 86400000;

  function pill(text, tone, name) {
    const p = el('span', 'pill ' + tone);
    if (name) p.append(icon(name));
    p.append(document.createTextNode(text));
    return p;
  }
  function health(d) {
    if (d.issues.includes('not_encrypted')) return pill('Not Encrypted', 'bad', 'dialog-error-symbolic');
    if (d.issues.includes('not_seen')) return pill('Not Seen', 'off', 'network-offline-symbolic');
    if (d.issues.length) return pill('Needs Attention', 'warn', 'dialog-warning-symbolic');
    return pill('Protected', 'ok', 'security-high-symbolic');
  }
  function row(heading, subtitle, prefix) {
    const r = document.createElement('sinty-row');
    r.setAttribute('heading', heading);
    if (subtitle) r.setAttribute('subtitle', subtitle);
    if (prefix) {
      prefix.slot = 'prefix';
      r.append(prefix);
    }
    return r;
  }
  function button(label, variant, run) {
    const b = el('sinty-button', '', label);
    if (variant) b.setAttribute('variant', variant);
    b.setAttribute('pill', '');
    b.addEventListener('click', (e) => { e.stopPropagation(); run(); });
    return b;
  }
  function group(heading, description) {
    const g = document.createElement('sinty-group');
    if (heading) g.setAttribute('heading', heading);
    if (description) g.setAttribute('description', description);
    return g;
  }
  const deviceIcon = (d) => (/phone|pixel/i.test(d.facts.model || '') ? 'phone' : /laptop|book|thinkpad|xps/i.test(d.facts.model || '') ? 'computer-laptop' : 'computer');
  const nameOf = (id) => (state.devices.find((d) => d.id === id) || {}).name || 'A removed device';

  async function refreshData() {
    const [d, w] = await Promise.all([call('GET', '/api/v1/devices'), call('GET', '/api/v1/commands?state=waiting_approval')]);
    state.devices = d.ok ? (await d.json()).devices : [];
    state.waiting = w.ok ? (await w.json()).commands : [];
    const attention = state.devices.filter((x) => x.issues.length).length;
    const rows = Object.fromEntries([...document.querySelectorAll('sinty-sidebar-row[data-view]')].map((r) => [r.dataset.view, r]));
    rows.devices.setAttribute('badge', attention ? String(attention) : '');
    rows.requests.setAttribute('badge', state.waiting.length ? String(state.waiting.length) : '');
    return d.ok;
  }

  function route() {
    const h = decodeURIComponent(location.hash.slice(1));
    const [view, ...rest] = h.split('/');
    const known = ['overview', 'devices', 'device', 'requests', 'groups', 'group', 'enroll', 'activity'];
    state.view = known.includes(view) ? view : 'overview';
    state.arg = rest.join('/');
    state.selected.clear();
    $('search').value = '';
    load();
  }
  const go = (view, arg) => {
    const h = '#' + view + (arg ? '/' + encodeURIComponent(arg) : '');
    if (location.hash === h) route(); else location.hash = h;
  };

  const titles = { overview: 'Overview', devices: 'Devices', requests: 'Requests', groups: 'Groups and Policies', enroll: 'Enrollment', activity: 'Activity' };
  const parents = { device: 'devices', group: 'groups' };

  function crumbs(last) {
    const top = parents[state.view] || state.view;
    const segs = [{ label: titles[top], target: top }];
    if (last) segs.push({ label: last, target: null });
    $('path').segments = segs;
  }

  function chrome() {
    const v = state.view;
    const sel = state.selected.size > 0;
    for (const r of document.querySelectorAll('sinty-sidebar-row[data-view]')) r.toggleAttribute('active', r.dataset.view === (parents[v] || v));
    for (const b of document.querySelectorAll('.std')) b.hidden = true;
    for (const b of document.querySelectorAll('.sel')) b.hidden = !sel;
    $('path').hidden = sel;
    $('selcount').hidden = !sel;
    $('selcount').textContent = state.selected.size === 1 ? '1 Selected' : state.selected.size + ' Selected';
    if (sel) return;
    $('refresh').hidden = false;
    if (v === 'devices') {
      $('search').hidden = false;
      $('search').setAttribute('placeholder', 'Search Devices');
      $('filter').hidden = false;
      $('filter').value = state.filter;
      $('group-filter').hidden = false;
      $('group-filter').setAttribute('text', state.group || 'All Groups');
    } else if (v === 'activity') {
      $('search').hidden = false;
      $('search').setAttribute('placeholder', 'Search Activity');
      $('who').hidden = false;
      $('who').value = state.who;
    } else if (v === 'device' && state.current) {
      for (const b of document.querySelectorAll('.dev')) b.hidden = false;
      $('refresh').hidden = true;
    } else if (v === 'enroll') {
      $('new-code').hidden = false;
    } else if (v === 'groups') {
      $('new-group').hidden = false;
    }
  }

  function showEmpty(iconName, heading, description, action) {
    const e = $('empty');
    e.hidden = false;
    e.setAttribute('icon', iconName);
    e.setAttribute('heading', heading);
    e.setAttribute('description', description);
    e.replaceChildren();
    if (action) e.append(action);
  }

  async function load() {
    const view = $('view');
    $('empty').hidden = true;
    state.current = null;
    document.title = (titles[state.view] || '') + ' - Sinty IT';
    crumbs();
    chrome();
    $('app').scrollToTop();
    const ok = await refreshData();
    view.replaceChildren();
    if (!ok) {
      showEmpty('dialog-warning', 'Cannot Reach the Server', 'Check the connection and try again.', button('Try Again', 'suggested', load));
      return;
    }
    const pages = { overview, devices, device, requests, groups, group: groupPage, enroll, activity };
    await pages[state.view]();
    chrome();
  }

  function overview() {
    const view = $('view');
    const list = state.devices;
    const attention = list.filter((d) => d.issues.length);
    const stat = (n, label, warn, run) => {
      const b = el('button', 'stat' + (warn && n ? ' warn' : ''));
      b.type = 'button';
      b.append(el('b', '', String(n)), el('span', '', label));
      b.addEventListener('click', run);
      return b;
    };
    const stats = el('div', 'stats');
    stats.append(
      stat(list.length, list.length === 1 ? 'Device' : 'Devices', false, () => { state.filter = 'all'; go('devices'); }),
      stat(attention.length, 'Need Attention', true, () => { state.filter = 'attention'; go('devices'); }),
      stat(state.waiting.length, state.waiting.length === 1 ? 'Request Waiting' : 'Requests Waiting', true, () => go('requests')),
      stat(list.filter(seenToday).length, 'Seen Today', false, () => { state.filter = 'all'; go('devices'); }),
    );
    view.append(stats);
    if (!list.length) {
      showEmpty('dev.sinty.IT', 'No Devices Yet', 'Create an enrollment code and run it on the first device to manage it from here.', button('New Enrollment Code', 'suggested', newCode));
      return;
    }
    if (state.waiting.length) view.append(waitingGroup('Waiting for a Second Administrator'));
    const g = group(attention.length ? 'Needs Attention' : 'All Devices Are Protected', attention.length ? '' : 'Every device is encrypted, sealed to its hardware, has the firewall on and checked in this week.');
    for (const d of attention.slice(0, 8)) g.append(deviceRow(d, d.issues.map((i) => problems[i] || i).join(', ')));
    if (attention.length > 8) {
      const more = row('Show All ' + attention.length, '');
      more.setAttribute('activatable', '');
      more.addEventListener('click', () => { state.filter = 'attention'; go('devices'); });
      g.append(more);
    }
    view.append(g);
    const groupsSeen = {};
    for (const d of list) groupsSeen[d.group] = (groupsSeen[d.group] || 0) + 1;
    const gg = group('Groups');
    for (const [name, n] of Object.entries(groupsSeen).sort()) {
      const r = row(name, n === 1 ? '1 device' : n + ' devices', icon('network-workgroup-symbolic'));
      r.setAttribute('activatable', '');
      r.addEventListener('click', () => go('group', name));
      r.append(icon('go-next-symbolic'));
      gg.append(r);
    }
    view.append(gg);
  }

  function deviceRow(d, subtitle) {
    const r = row(d.name, subtitle, icon(deviceIcon(d), true));
    r.setAttribute('activatable', '');
    r.addEventListener('click', () => go('device', d.id));
    r.append(health(d), icon('go-next-symbolic'));
    return r;
  }

  function waitingGroup(heading) {
    const g = group(heading, 'Erasing a device needs two administrators: the one who asks and one who approves.');
    for (const c of state.waiting) {
      const r = row(kindName(c.kind) + ' ' + nameOf(c.device), 'Asked by ' + c.by + ', ' + ago(c.created).toLowerCase() + ', expires ' + until(c.expires), icon(kinds[c.kind] ? kinds[c.kind][1] : 'dialog-warning-symbolic'));
      r.append(button('Cancel', 'flat', () => decide(c, 'cancel')));
      if (c.by !== state.me) r.append(button('Approve', 'destructive', () => decide(c, 'approve')));
      else r.append(pill('Asked by You', 'off'));
      g.append(r);
    }
    return g;
  }

  function filtered() {
    const q = $('search').value.trim().toLowerCase();
    return state.devices.filter((d) => {
      if (state.group && d.group !== state.group) return false;
      if (state.filter === 'attention' && !d.issues.length) return false;
      if (state.filter === 'away' && !d.issues.includes('not_seen')) return false;
      if (!q) return true;
      const f = d.facts;
      return [d.name, d.group, f.hostname, f.model, f.os, f.version, f.last_user].some((x) => x && x.toLowerCase().includes(q));
    });
  }

  function devices() {
    const view = $('view');
    view.replaceChildren();
    $('empty').hidden = true;
    if (!state.devices.length) {
      showEmpty('dev.sinty.IT', 'No Devices Yet', 'Create an enrollment code and run it on a device to manage it from here.', button('New Enrollment Code', 'suggested', newCode));
      return;
    }
    const list = filtered();
    if (!list.length) {
      showEmpty('edit-find-symbolic', 'No Matching Devices', 'Try another name or show all devices.');
      return;
    }
    const box = el('div', 'sinty-data-view');
    box.setAttribute('role', 'grid');
    box.setAttribute('aria-multiselectable', 'true');
    box.style.setProperty('--sinty-columns', 'minmax(0, 1fr) 110px 130px 140px 110px');
    const head = el('div', 'head');
    head.setAttribute('role', 'row');
    ['Name', 'Group', 'System', 'Status', 'Last Seen'].forEach((c, i) => {
      const h = el('div', i === 0 || i === 3 ? '' : 'wide', c);
      h.setAttribute('role', 'columnheader');
      head.append(h);
    });
    box.append(head);
    for (const d of list) {
      const item = el('div', 'item');
      item.setAttribute('role', 'row');
      item.tabIndex = -1;
      item.dataset.id = d.id;
      const name = el('div', 'namecell');
      name.append(icon(deviceIcon(d) === 'phone' ? 'phone-symbolic' : 'computer-symbolic'));
      const text = el('div');
      text.append(el('span', 'name', d.name), el('span', 'sub', [d.group, system(d.facts), ago(d.seen)].filter(Boolean).join(', ')));
      name.append(text);
      const st = el('div');
      st.append(health(d));
      item.append(name, el('div', 'dim wide', d.group), el('div', 'dim wide', system(d.facts)), st, el('div', 'dim wide', ago(d.seen)));
      item.addEventListener('click', (e) => {
        if (e.ctrlKey || e.metaKey || state.selected.size) toggle(d.id);
        else go('device', d.id);
      });
      item.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        if (!state.selected.has(d.id)) toggle(d.id);
        bulkMenu({ x: e.clientX, y: e.clientY });
      });
      item.addEventListener('keydown', (e) => {
        const all = [...box.querySelectorAll('.item')];
        const at = all.indexOf(item);
        if (e.key === 'Enter') go('device', d.id);
        else if (e.key === ' ') { e.preventDefault(); toggle(d.id); }
        else if (e.key === 'ArrowDown' && all[at + 1]) { e.preventDefault(); all[at + 1].focus(); }
        else if (e.key === 'ArrowUp' && all[at - 1]) { e.preventDefault(); all[at - 1].focus(); }
      });
      box.append(item);
    }
    view.append(box);
    sync();
  }

  function toggle(id) {
    if (state.selected.has(id)) state.selected.delete(id); else state.selected.add(id);
    sync();
  }
  function sync() {
    for (const n of $('view').querySelectorAll('.item[data-id]')) n.setAttribute('aria-selected', String(state.selected.has(n.dataset.id)));
    chrome();
  }

  function bulkMenu(point) {
    const ids = [...state.selected];
    SintyUI.menu([
      ...['lock', 'logout', 'update', 'reboot', 'sync-policy'].map((k) => ({ label: kindName(k), icon: kinds[k][1], run: () => sendMany(ids, k) })),
      null,
      { label: 'Move to Group…', icon: 'network-workgroup-symbolic', run: () => moveToGroup(ids) },
    ]).open(null, point);
  }

  async function sendMany(ids, kind) {
    let n = 0;
    for (const id of ids) {
      const r = await call('POST', '/api/v1/devices/' + encodeURIComponent(id) + '/commands', { kind });
      if (r.ok) n++;
    }
    state.selected.clear();
    toast(n === ids.length ? kindName(kind) + ': sent to ' + (n === 1 ? nameOf(ids[0]) : n + ' devices') : kindName(kind) + ': sent to ' + n + ' of ' + ids.length + ' devices', { timeout: 5000 });
    if (state.view === 'device') load(); else { await refreshData(); devices(); }
  }

  function nameDialog(heading, label, value, check, run) {
    const d = $('name-dialog');
    d.setAttribute('heading', heading);
    $('name-entry').setAttribute('label', label);
    $('name-cap').textContent = label;
    $('name-entry').value = value;
    $('name-error').textContent = '';
    const submit = async () => {
      const v = $('name-entry').value.trim();
      const problem = check(v);
      if (problem) { $('name-error').textContent = problem; return; }
      const err = await run(v);
      if (err) $('name-error').textContent = err; else d.close();
    };
    $('name-ok').onclick = submit;
    $('name-entry').onkeydown = (e) => { if (e.key === 'Enter') submit(); };
    d.show();
    requestAnimationFrame(() => $('name-entry').focus());
  }
  const checkGroup = (v) => (!v ? 'Type a group name.' : /^[a-z0-9][a-z0-9-]{0,62}$/.test(v) ? '' : 'Use lowercase letters, digits and dashes.');

  function moveToGroup(ids) {
    const current = ids.length === 1 ? (state.devices.find((d) => d.id === ids[0]) || {}).group || '' : '';
    nameDialog(ids.length === 1 ? 'Move ' + nameOf(ids[0]) + ' to a Group' : 'Move ' + ids.length + ' Devices to a Group', 'Group', current, checkGroup, async (g) => {
      for (const id of ids) {
        const r = await call('PATCH', '/api/v1/devices/' + encodeURIComponent(id), { group: g });
        if (!r.ok) return why(r);
      }
      state.selected.clear();
      toast((ids.length === 1 ? 'Moved' : ids.length + ' devices moved') + ' to ' + g, { action: 'Apply Policy', onaction: () => sendMany(ids, 'sync-policy') });
      load();
      return '';
    });
  }

  function confirm(heading, text, ok, run) {
    const d = $('confirm-dialog');
    d.setAttribute('heading', heading);
    $('confirm-text').textContent = text;
    $('confirm-ok').textContent = ok;
    $('confirm-ok').onclick = () => { d.close(); run(); };
    d.show();
  }

  async function device() {
    const view = $('view');
    const r = await call('GET', '/api/v1/devices/' + encodeURIComponent(state.arg));
    if (!r.ok) {
      showEmpty('dialog-warning', 'Device Not Found', 'It was removed from management.', button('Show Devices', 'suggested', () => go('devices')));
      return;
    }
    const data = await r.json();
    const d = data.device;
    state.current = d;
    crumbs(d.name);
    document.title = d.name + ' - Sinty IT';
    const f = d.facts;
    const hero = el('div', 'hero');
    const text = el('div');
    text.append(el('h2', '', d.name), el('p', '', [f.model, system(f)].filter(Boolean).join(', ') || 'Sinty device'));
    hero.append(icon(deviceIcon(d), true), text);
    const hp = el('div');
    hp.style.marginInlineStart = 'auto';
    hp.append(health(d));
    hero.append(hp);
    view.append(hero);

    const waiting = data.commands.filter((c) => c.state === 'waiting_approval');
    if (waiting.length) {
      const g = group('Waiting for a Second Administrator');
      for (const c of waiting) g.append(commandRow(c));
      view.append(g);
    }

    const prot = group('Protection');
    const check = (label, on, okText, badText, tone) => {
      const rw = row(label, '', icon(on ? 'emblem-ok-symbolic' : 'dialog-warning-symbolic'));
      rw.append(pill(on ? okText : badText, on ? 'ok' : tone));
      return rw;
    };
    prot.append(
      check('Disk Encryption', f.encrypted, 'On', 'Off', 'bad'),
      check('Keys Sealed to This Hardware', f.sealed, 'Sealed', 'Not Sealed', 'warn'),
      check('Firewall', f.firewall, 'On', 'Off', 'warn'),
      check('Checked In', !d.issues.includes('not_seen'), ago(d.seen), ago(d.seen), 'off'),
    );
    view.append(prot);

    const info = (label, value, mono) => {
      const rw = row(label, '');
      const v = el('span', 'value' + (mono ? ' mono' : ''), value);
      rw.append(v);
      return rw;
    };
    const sys = group('System');
    for (const [k, v, m] of [
      ['Operating System', system(f)],
      ['Model', f.model],
      ['Architecture', f.arch],
      ['Update Channel', cap(f.channel)],
      ['Startup Health', cap(f.boot_health)],
      ['Free Space', f.disk_free ? gb(f.disk_free) : ''],
      ['Last Signed In', f.last_user],
      ['Host Name', f.hostname, true],
    ]) if (v) sys.append(info(k, v, m));
    view.append(sys);

    const mg = group('Management');
    const groupRow = info('Group', d.group);
    groupRow.setAttribute('activatable', '');
    groupRow.addEventListener('click', () => go('group', d.group));
    groupRow.append(icon('go-next-symbolic'));
    mg.append(groupRow, info('Enrolled', fullFmt.format(new Date(d.enrolled))), info('Device ID', d.id, true));
    view.append(mg);

    const hist = group('Commands');
    const rest = data.commands.filter((c) => c.state !== 'waiting_approval');
    if (!rest.length) hist.append(row('No commands sent yet', 'Use the buttons at the top to lock, update or restart this device.'));
    for (const c of rest.slice(0, 30)) hist.append(commandRow(c));
    view.append(hist);
  }

  function commandRow(c) {
    const [label, tone] = states[c.state] || [c.state, 'off'];
    const sub = 'By ' + c.by + ', ' + ago(c.created).toLowerCase() + (c.approvers && c.approvers.length ? ', approved by ' + c.approvers.join(', ') : '');
    const r = row(kindName(c.kind), c.output ? sub + '. ' + c.output : sub, icon(kinds[c.kind] ? kinds[c.kind][1] : 'emblem-system-symbolic'));
    if (c.state === 'waiting_approval' && c.by !== state.me) r.append(button('Approve', 'destructive', () => decide(c, 'approve')));
    if (c.state === 'waiting_approval' || c.state === 'queued') r.append(button('Cancel', 'flat', () => decide(c, 'cancel')));
    r.append(pill(label, tone));
    return r;
  }

  async function decide(c, what) {
    if (what === 'approve' && c.kind === 'wipe') {
      confirm('Erase ' + nameOf(c.device) + '?', c.by + ' asked to erase this device. Once you approve, all its data becomes unreadable for good at its next check-in.', 'Approve Erase', () => decided(c, what));
      return;
    }
    decided(c, what);
  }
  async function decided(c, what) {
    const r = await call('POST', '/api/v1/commands/' + encodeURIComponent(c.id) + '/' + what);
    if (!r.ok) toast(await why(r));
    else toast(what === 'approve' ? 'Approved. It runs at the next check-in.' : 'Request canceled');
    load();
  }

  async function send(kind) {
    const d = state.current;
    if (!d) return;
    const r = await call('POST', '/api/v1/devices/' + encodeURIComponent(d.id) + '/commands', { kind });
    if (!r.ok) { toast(await why(r)); return; }
    const c = await r.json();
    toast(c.state === 'waiting_approval' ? 'Asked. Another administrator has to approve it.' : kindName(kind) + ' runs at the next check-in', { timeout: 5000 });
    load();
  }

  function deviceMenu() {
    const d = state.current;
    SintyUI.menu([
      { label: 'Apply Policy', icon: kinds['sync-policy'][1], run: () => send('sync-policy') },
      null,
      { label: 'Rename…', icon: 'document-edit-symbolic', run: () => nameDialog('Rename Device', 'Name', d.name, (v) => (!v ? 'Type a name.' : v.length > 64 ? 'Use at most 64 characters.' : ''), async (name) => {
        const r = await call('PATCH', '/api/v1/devices/' + encodeURIComponent(d.id), { name });
        if (!r.ok) return why(r);
        load();
        return '';
      }) },
      { label: 'Move to Group…', icon: 'network-workgroup-symbolic', run: () => moveToGroup([d.id]) },
      null,
      { label: 'Erase…', icon: 'edit-delete-symbolic', destructive: true, run: () => confirm('Erase ' + d.name + '?', 'All data on this device becomes unreadable for good and it restarts. Another administrator must approve before it runs.', 'Ask to Erase', () => send('wipe')) },
      { label: 'Remove from Management…', icon: 'list-remove-symbolic', destructive: true, run: () => confirm('Remove ' + d.name + '?', 'The device stops taking commands and policies, and its waiting commands are canceled. Its data stays on it. To manage it again, enroll it with a new code.', 'Remove', async () => {
        const r = await call('DELETE', '/api/v1/devices/' + encodeURIComponent(d.id));
        if (!r.ok) { toast(await why(r)); return; }
        toast(d.name + ' removed from management');
        go('devices');
      }) },
    ]).open($('dev-more'));
  }

  async function requests() {
    const view = $('view');
    const r = await call('GET', '/api/v1/commands');
    const all = r.ok ? (await r.json()).commands : [];
    const open = all.filter((c) => c.state === 'queued' || c.state === 'sent');
    const done = all.filter((c) => !['waiting_approval', 'queued', 'sent'].includes(c.state));
    if (!all.length) {
      showEmpty('mail-inbox-symbolic', 'No Requests', 'Commands you send to devices, and requests that wait for a second administrator, show up here.');
      return;
    }
    if (state.waiting.length) view.append(waitingGroup('Waiting for a Second Administrator'));
    const withDevice = (c) => {
      const rw = commandRow(c);
      rw.setAttribute('heading', kindName(c.kind) + ': ' + nameOf(c.device));
      rw.setAttribute('activatable', '');
      rw.addEventListener('click', () => go('device', c.device));
      return rw;
    };
    if (open.length) {
      const g = group('On the Way', 'Devices pick these up at their next check-in.');
      for (const c of open) g.append(withDevice(c));
      view.append(g);
    }
    if (done.length) {
      const g = group('Finished');
      for (const c of done.slice(0, 50)) g.append(withDevice(c));
      view.append(g);
    }
  }

  async function groupNames() {
    const [p, t] = await Promise.all([call('GET', '/api/v1/policies'), call('GET', '/api/v1/tokens')]);
    const policies = p.ok ? (await p.json()).policies : {};
    const tokens = t.ok ? (await t.json()).tokens : [];
    const names = new Set([...state.devices.map((d) => d.group), ...Object.keys(policies), ...tokens.map((x) => x.group)]);
    return { names: [...names].sort(), policies, tokens };
  }

  async function groups() {
    const view = $('view');
    const { names, policies } = await groupNames();
    if (!names.length) {
      showEmpty('network-workgroup', 'No Groups Yet', 'Groups collect devices that share a policy. A group is made when you enroll a device into it or give it a policy.', button('New Group', 'suggested', newGroup));
      return;
    }
    view.append(el('p', 'note', 'Every device belongs to one group and follows that group’s policy. Devices pick up a changed policy at their next check-in.'));
    const g = group();
    for (const name of names) {
      const n = state.devices.filter((d) => d.group === name).length;
      const p = policies[name];
      const keys = p ? Object.keys(p.settings).length : 0;
      const r = row(name, (n === 1 ? '1 device' : n + ' devices') + ', ' + (keys ? (keys === 1 ? '1 setting' : keys + ' settings') : 'no policy'), icon('network-workgroup-symbolic'));
      r.setAttribute('activatable', '');
      r.addEventListener('click', () => go('group', name));
      r.append(icon('go-next-symbolic'));
      g.append(r);
    }
    view.append(g);
  }

  function newGroup() {
    nameDialog('New Group', 'Group', '', checkGroup, async (name) => {
      const r = await call('PUT', '/api/v1/policies/' + encodeURIComponent(name), {});
      if (!r.ok) return why(r);
      go('group', name);
      return '';
    });
  }

  async function groupPage() {
    const name = state.arg;
    const view = $('view');
    crumbs(name);
    document.title = name + ' - Sinty IT';
    const { policies } = await groupNames();
    const policy = policies[name];
    const members = state.devices.filter((d) => d.group === name);
    const hero = el('div', 'hero');
    const text = el('div');
    text.append(el('h2', '', name), el('p', '', (members.length === 1 ? '1 device' : members.length + ' devices') + (policy ? ', policy changed by ' + policy.by + ' ' + ago(policy.updated).toLowerCase() : '')));
    hero.append(icon('network-workgroup', true), text);
    view.append(hero);

    const g = group('Policy', 'Settings these devices apply. Keys use lowercase letters, digits, dots and dashes, such as screen.lock-after.');
    const rows = el('div');
    const addSetting = (k, v) => {
      const line = el('div', 'setting');
      const key = document.createElement('sinty-entry');
      key.setAttribute('label', 'Setting');
      key.value = k;
      const val = document.createElement('sinty-entry');
      val.setAttribute('label', 'Value');
      val.value = v;
      const rm = el('button');
      rm.type = 'button';
      rm.setAttribute('aria-label', 'Remove Setting');
      rm.append(icon('list-remove-symbolic'));
      rm.addEventListener('click', () => line.remove());
      line.append(key, val, rm);
      rows.append(line);
      return key;
    };
    for (const [k, v] of Object.entries(policy ? policy.settings : {}).sort()) addSetting(k, v);
    g.append(rows);
    const err = el('small');
    err.style.cssText = 'color: var(--sinty-error-color); padding: 0 12px; display: block;';
    const bar = el('div', 'buttons');
    bar.append(
      button('Add Setting', 'flat', () => addSetting('', '').focus()),
      button('Save Policy', 'suggested', async () => {
        const out = {};
        for (const line of rows.querySelectorAll('.setting')) {
          const [k, v] = [...line.querySelectorAll('sinty-entry')].map((e) => e.value.trim());
          if (!k && !v) continue;
          if (!/^[a-z0-9][a-z0-9.-]{0,127}$/.test(k)) { err.textContent = '“' + k + '” is not a valid setting name.'; return; }
          if (k in out) { err.textContent = '“' + k + '” is there twice.'; return; }
          out[k] = v;
        }
        err.textContent = '';
        const r = await call('PUT', '/api/v1/policies/' + encodeURIComponent(name), out);
        if (!r.ok) { err.textContent = await why(r); return; }
        toast('Policy saved', members.length ? { action: 'Apply Now', onaction: () => sendMany(members.map((d) => d.id), 'sync-policy') } : undefined);
        load();
      }),
    );
    if (policy) bar.append(button('Delete Policy', 'flat', () => confirm('Delete the ' + name + ' Policy?', 'Devices in this group stop receiving these settings at their next check-in.', 'Delete Policy', async () => {
      const r = await call('DELETE', '/api/v1/policies/' + encodeURIComponent(name));
      toast(r.ok ? 'Policy deleted' : await why(r));
      load();
    })));
    g.append(err, bar);
    view.append(g);

    const known = group('Supported Settings', 'Values are text. Devices apply a change at their next check-in.');
    for (const [k, v] of [
      ['screen.lock-after', 'Seconds of inactivity before the screen locks, at most. People can choose a shorter time.'],
      ['usb.storage', 'allow, read-only or deny for USB drives and memory cards.'],
      ['signin.notice-title', 'Title of the notice shown before anyone signs in.'],
      ['signin.notice-text', 'Text of that notice. People accept it to reach the sign-in screen.'],
      ['updates.defer-days', 'Days to wait after a system update is released before offering it.'],
      ['updates.window', 'Hours when updates may be downloaded, such as 22:00-04:00.'],
      ['kiosk.app', 'Application ID to run alone, full screen, such as dev.sinty.browser. Applies at the next sign-in.'],
    ]) known.append(row(k, v));
    view.append(known);

    const dg = group('Devices');
    if (!members.length) dg.append(row('No devices in this group', 'Enroll a device with a code for this group, or move devices here.'));
    for (const d of members) dg.append(deviceRow(d, [system(d.facts), ago(d.seen)].filter(Boolean).join(', ')));
    if (members.length) {
      const b = el('div', 'buttons');
      b.append(
        button('Apply Policy to All', '', () => sendMany(members.map((d) => d.id), 'sync-policy')),
        button('Install Updates on All', '', () => sendMany(members.map((d) => d.id), 'update')),
      );
      dg.append(b);
    }
    view.append(dg);
  }

  async function enroll() {
    const view = $('view');
    const r = await call('GET', '/api/v1/tokens');
    const tokens = r.ok ? (await r.json()).tokens : [];
    view.append(el('p', 'note', 'A device joins with an enrollment code. Run the command an enrollment code gives you as an administrator on the device; it joins the code’s group and starts checking in.'));
    if (!tokens.length) {
      showEmpty('dialog-password', 'No Active Codes', 'Create a code for the devices you want to add. It works for the number of devices and the time you choose.', button('New Code', 'suggested', newCode));
      return;
    }
    const g = group('Active Codes');
    for (const t of tokens) {
      const rw = row('Code for ' + t.group, (t.uses === 1 ? '1 device left' : t.uses + ' devices left') + ', expires ' + until(t.expires) + ', made by ' + t.by, icon('dialog-password-symbolic'));
      rw.append(button('Revoke', 'flat', () => confirm('Revoke This Code?', 'Devices can no longer join with it. Devices that already joined stay managed.', 'Revoke', async () => {
        const x = await call('DELETE', '/api/v1/tokens/' + encodeURIComponent(t.id));
        toast(x.ok ? 'Code revoked' : await why(x));
        load();
      })));
      g.append(rw);
    }
    view.append(g);
  }

  function newCode() {
    $('code-group').value = state.view === 'group' ? state.arg : '';
    $('code-uses').value = '1';
    $('code-hours').value = '24';
    $('code-error').textContent = '';
    $('code-dialog').show();
  }

  async function createCode() {
    const g = $('code-group').value.trim() || 'default';
    const uses = parseInt($('code-uses').value, 10);
    const problem = checkGroup(g) || (!(uses >= 1 && uses <= 1000) ? 'Choose between 1 and 1000 devices.' : '');
    if (problem) { $('code-error').textContent = problem; return; }
    const hours = parseInt($('code-hours').value, 10);
    const r = await call('POST', '/api/v1/tokens', { group: g, uses, hours });
    if (!r.ok) { $('code-error').textContent = await why(r); return; }
    const t = await r.json();
    $('code-dialog').close();
    $('made-text').textContent = 'Run this as an administrator on ' + (uses === 1 ? 'the device' : 'each of the ' + uses + ' devices') + '. It joins the ' + g + ' group. The code expires ' + until(t.expires) + '.';
    $('made-command').textContent = 'sinty-it agent enroll -server ' + location.origin + ' -token ' + t.token;
    $('made-dialog').show();
  }

  async function activity() {
    const view = $('view');
    const r = await call('GET', '/api/v1/audit');
    if (!r.ok) {
      showEmpty('dialog-error-symbolic', 'The Log Failed Its Check', 'An entry in the signed activity log does not match its signature. Someone may have changed the log file.');
      return;
    }
    const data = await r.json();
    const names = data.devices || {};
    const dev = (id) => names[id] || 'a removed device';
    const who = (a) => (a.startsWith('device:') ? names[a.slice(7)] || 'A removed device' : a);
    const what = (e) => {
      const d = e.detail || {};
      const k = kindName(d.kind || '');
      switch (e.action) {
        case 'token.create': return ['Created an enrollment code for ' + e.target + ' (' + d.uses + (d.uses === '1' ? ' device)' : ' devices)'), 'dialog-password-symbolic'];
        case 'token.revoke': return ['Revoked an enrollment code for ' + e.target, 'dialog-password-symbolic'];
        case 'policy.set': return ['Changed the ' + e.target + ' policy' + (d.keys ? ': ' + d.keys.split(',').join(', ') : ''), 'network-workgroup-symbolic'];
        case 'policy.delete': return ['Deleted the ' + e.target + ' policy', 'network-workgroup-symbolic'];
        case 'device.enroll': return ['Joined the ' + d.group + ' group', 'list-add-symbolic'];
        case 'device.leave': return ['Left management', 'list-remove-symbolic'];
        case 'device.retire': return ['Removed ' + dev(e.target) + ' from management', 'list-remove-symbolic'];
        case 'device.edit': return ['Changed ' + dev(e.target) + (d.name ? ': name ' + d.name : '') + (d.group ? (d.name ? ',' : ':') + ' group ' + d.group : ''), 'document-edit-symbolic'];
        case 'command.create': return ['Asked ' + dev(e.target) + ' to run ' + k + (d.state === 'waiting_approval' ? ', waiting for a second administrator' : ''), kinds[d.kind] ? kinds[d.kind][1] : 'emblem-system-symbolic'];
        case 'command.approve': return ['Approved ' + k + ' on ' + dev(e.target), 'emblem-ok-symbolic'];
        case 'command.cancel': return ['Canceled ' + k + ' on ' + dev(e.target), 'process-stop-symbolic'];
        case 'command.done': return ['Finished ' + k, 'emblem-ok-symbolic'];
        case 'command.failed': return [k + ' failed', 'dialog-warning-symbolic'];
        case 'command.expired': return [k + ' expired before the device checked in', 'dialog-warning-symbolic'];
      }
      return [e.action, 'emblem-system-symbolic'];
    };
    const q = $('search').value.trim().toLowerCase();
    const entries = data.entries.filter((e) => {
      const device = e.actor.startsWith('device:');
      if (state.who === 'people' && device) return false;
      if (state.who === 'devices' && !device) return false;
      return !q || (who(e.actor) + ' ' + what(e)[0]).toLowerCase().includes(q);
    });
    const ver = row('Signed Log Verified', 'Every entry is chained and signed by the server. The last ' + data.entries.length + ' entries are shown.', icon('security-high-symbolic'));
    ver.append(pill('Intact', 'ok', 'emblem-ok-symbolic'));
    const vg = group();
    vg.append(ver);
    view.append(vg);
    if (!entries.length) {
      showEmpty('edit-find-symbolic', 'No Matching Activity', 'Try another search.');
      return;
    }
    let day = '';
    let g = null;
    for (const e of entries) {
      const when = new Date(e.time);
      const label = dayFmt.format(when);
      if (label !== day) {
        day = label;
        view.append(el('p', 'day', label));
        g = group();
        view.append(g);
      }
      const [text, ic] = what(e);
      const rw = row(who(e.actor) + ': ' + text, timeFmt.format(when), icon(ic));
      if (e.target && names[e.target]) {
        rw.setAttribute('activatable', '');
        rw.addEventListener('click', () => go('device', e.target));
      }
      g.append(rw);
    }
  }

  $('refresh').addEventListener('click', load);
  $('filter').addEventListener('change', () => { state.filter = $('filter').value; store.set('filter', state.filter); devices(); });
  $('who').addEventListener('change', () => { state.who = $('who').value; $('view').replaceChildren(); activity(); });
  let searchTimer = 0;
  $('search').addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      if (state.view === 'devices') devices();
      else if (state.view === 'activity') { $('view').replaceChildren(); $('empty').hidden = true; activity(); }
    }, 150);
  });
  $('group-filter').addEventListener('click', () => {
    const names = [...new Set(state.devices.map((d) => d.group))].sort();
    SintyUI.menu([
      { label: 'All Groups', checkable: true, checked: !state.group, run: () => { state.group = ''; devices(); chrome(); } },
      null,
      ...names.map((n) => ({ label: n, checkable: true, checked: state.group === n, run: () => { state.group = n; devices(); chrome(); } })),
    ]).open($('group-filter'));
  });
  for (const k of ['lock', 'logout', 'update', 'reboot']) $('dev-' + k).addEventListener('click', () => send(k));
  $('dev-more').addEventListener('click', deviceMenu);
  $('new-code').addEventListener('click', newCode);
  $('new-group').addEventListener('click', newGroup);
  $('code-cancel').addEventListener('click', () => $('code-dialog').close());
  $('code-create').addEventListener('click', createCode);
  $('made-copy').addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText($('made-command').textContent);
      toast('Command copied');
    } catch {
      toast('Select the command and copy it.');
    }
  });
  $('made-done').addEventListener('click', () => { $('made-dialog').close(); if (state.view === 'enroll') load(); });
  $('name-cancel').addEventListener('click', () => $('name-dialog').close());
  $('confirm-cancel').addEventListener('click', () => $('confirm-dialog').close());
  for (const b of document.querySelectorAll('.sel')) {
    b.addEventListener('click', () => {
      const ids = [...state.selected];
      if (b.dataset.act === 'clear') { state.selected.clear(); sync(); }
      else if (b.dataset.act === 'group') moveToGroup(ids);
      else sendMany(ids, b.dataset.act);
    });
  }
  for (const r of document.querySelectorAll('sinty-sidebar-row[data-view]')) r.addEventListener('click', () => go(r.dataset.view));
  $('path').addEventListener('navigate', (e) => { if (e.detail.target) go(e.detail.target); });
  $('account').addEventListener('click', () => {
    SintyUI.menu([
      { label: 'Signed in as ' + state.me, disabled: true },
      null,
      { label: 'Sign Out', icon: 'system-log-out-symbolic', run: async () => {
        await call('POST', '/logout');
        location.assign('/login');
      } },
    ]).open($('account'));
  });
  $('account').addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); $('account').click(); } });
  document.addEventListener('keydown', (e) => {
    if (e.target.closest('sinty-dialog, sinty-entry, sinty-search, input')) return;
    if ((e.ctrlKey || e.metaKey) && e.key === 'f' && !$('search').hidden) { e.preventDefault(); $('search').focus(); }
    else if (e.key === 'Escape' && state.selected.size) { state.selected.clear(); sync(); }
    else if ((e.ctrlKey || e.metaKey) && e.key === 'a' && state.view === 'devices') {
      e.preventDefault();
      for (const d of filtered()) state.selected.add(d.id);
      sync();
    }
  });
  setInterval(() => { if (document.visibilityState === 'visible') refreshData(); }, 30000);

  window.addEventListener('hashchange', route);
  call('GET', '/api/v1/me').then((r) => (r.ok ? r.json() : {})).then((m) => {
    state.me = m.user || '';
    $('user').textContent = state.me;
    $('avatar').setAttribute('name', state.me);
    if (!location.hash) history.replaceState(null, '', '#overview');
    route();
  });
})();
