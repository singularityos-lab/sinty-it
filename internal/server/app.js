(function () {
  'use strict';
  const $ = (id) => document.getElementById(id);
  const kinds = { lock: 'Lock screens', logout: 'Sign everyone out', reboot: 'Restart', update: 'Install updates', 'sync-policy': 'Apply policy', wipe: 'Erase' };
  const states = { waiting_approval: 'Waiting for approval', queued: 'Queued', sent: 'Sent', done: 'Done', failed: 'Failed', expired: 'Expired', canceled: 'Canceled' };
  const problems = { not_encrypted: 'Not encrypted', not_sealed: 'Keys not sealed to this hardware', firewall_off: 'Firewall off', not_seen: 'Not seen for a week' };
  let current = null;
  let me = '';

  const say = (t) => { $('message').textContent = t; };

  async function call(method, path, body) {
    const init = { method, credentials: 'same-origin', headers: { 'X-Sinty-Request': '1' } };
    if (body !== undefined) { init.body = JSON.stringify(body); init.headers['Content-Type'] = 'application/json'; }
    const r = await fetch(path, init);
    if (r.status === 401) { location.assign('/login?back=' + encodeURIComponent(location.pathname)); throw new Error('signed out'); }
    if (r.status === 403) {
      const e = await r.clone().json().catch(() => ({}));
      if (e.error === 'stronger') { location.assign('/login?acr=sinty:3&back=' + encodeURIComponent(location.pathname)); throw new Error('stronger'); }
    }
    return r;
  }

  function ago(when) {
    const s = Math.max(0, (Date.now() - new Date(when).getTime()) / 1000);
    if (s < 90) return 'just now';
    if (s < 5400) return Math.round(s / 60) + ' minutes ago';
    if (s < 129600) return Math.round(s / 3600) + ' hours ago';
    return Math.round(s / 86400) + ' days ago';
  }

  function row(heading, subtitle) {
    const r = document.createElement('sinty-row');
    r.setAttribute('heading', heading);
    if (subtitle) r.setAttribute('subtitle', subtitle);
    return r;
  }

  function button(label, variant, onclick) {
    const b = document.createElement('sinty-button');
    if (variant) b.setAttribute('variant', variant);
    b.textContent = label;
    b.addEventListener('click', (ev) => { ev.stopPropagation(); onclick(); });
    return b;
  }

  function issues(list) {
    const span = document.createElement('span');
    if (!list.length) { span.className = 'good'; span.textContent = 'Protected'; return span; }
    span.className = 'issue';
    const icon = document.createElement('sinty-icon');
    icon.setAttribute('name', 'dialog-warning-symbolic');
    span.append(icon, document.createTextNode(list.map((i) => problems[i] || i).join(', ')));
    return span;
  }

  function show(view) {
    for (const v of ['devices-view', 'device-view', 'activity-view']) $(v).hidden = v !== view;
    $('to-devices').toggleAttribute('aria-current', view !== 'activity-view');
    $('to-activity').toggleAttribute('aria-current', view === 'activity-view');
    $('add').hidden = view !== 'devices-view';
  }

  async function devices() {
    current = null;
    show('devices-view');
    $('title').textContent = 'Devices';
    say('');
    const [d, w] = await Promise.all([call('GET', '/api/v1/devices'), call('GET', '/api/v1/commands?state=waiting_approval')]);
    if (!d.ok) { say('The devices cannot be listed.'); return; }
    const list = (await d.json()).devices;
    const box = $('devices');
    box.replaceChildren();
    if (!list.length) {
      const p = document.createElement('p');
      p.className = 'empty';
      p.textContent = 'No devices yet. Add one to start managing it.';
      box.append(p);
    }
    const names = {};
    for (const dev of list) {
      names[dev.id] = dev.name;
      const f = dev.facts;
      const r = row(dev.name, [dev.group, [f.os, f.version].filter(Boolean).join(' '), 'seen ' + ago(dev.seen)].filter(Boolean).join(' · '));
      r.setAttribute('activatable', '');
      r.addEventListener('click', () => { location.hash = 'device/' + encodeURIComponent(dev.id); });
      r.append(issues(dev.issues));
      const go = document.createElement('sinty-icon');
      go.setAttribute('name', 'go-next-symbolic');
      r.append(go);
      box.append(r);
    }
    const waiting = w.ok ? (await w.json()).commands : [];
    $('waiting').hidden = !waiting.length;
    $('waiting').replaceChildren(...waiting.map((c) => {
      const r = row((kinds[c.kind] || c.kind) + ' ' + (names[c.device] || c.device), 'Asked by ' + c.by + ', ' + ago(c.created));
      r.append(button('Cancel', 'flat', () => decide(c.id, 'cancel')));
      if (c.by !== me) r.append(button('Approve', 'destructive', () => decide(c.id, 'approve')));
      return r;
    }));
  }

  async function decide(id, what) {
    const r = await call('POST', '/api/v1/commands/' + encodeURIComponent(id) + '/' + what);
    if (r.status === 403) say('Another administrator has to approve a request you made.');
    else if (!r.ok) say('That request is no longer waiting.');
    current ? device(current.id) : devices();
  }

  async function device(id) {
    show('device-view');
    say('');
    const r = await call('GET', '/api/v1/devices/' + encodeURIComponent(id));
    if (!r.ok) { say('This device cannot be opened.'); return; }
    const data = await r.json();
    current = data.device;
    $('title').textContent = current.name;
    const f = current.facts;
    const facts = [
      ['Protection', current.issues.length ? current.issues.map((i) => problems[i] || i).join(', ') : 'Encrypted, keys sealed to this hardware, firewall on'],
      ['System', [f.os, f.version, f.arch].filter(Boolean).join(' ')],
      ['Model', f.model],
      ['Update channel', f.channel],
      ['Free space', f.disk_free ? (f.disk_free / 1e9).toFixed(1) + ' GB' : ''],
      ['Group', current.group],
      ['Last seen', ago(current.seen)],
      ['Enrolled', new Date(current.enrolled).toLocaleString()],
    ].filter((x) => x[1]);
    $('facts').replaceChildren(...facts.map(([k, v]) => row(k, v)));
    const history = $('history');
    history.replaceChildren();
    if (!data.commands.length) {
      const p = document.createElement('p');
      p.className = 'empty';
      p.textContent = 'No commands sent to this device.';
      history.append(p);
    }
    for (const c of data.commands) {
      const sub = [states[c.state] || c.state, 'by ' + c.by, ago(c.created)];
      if (c.output) sub.push(c.output);
      const r2 = row(kinds[c.kind] || c.kind, sub.join(' · '));
      if (c.state === 'waiting_approval' && c.by !== me) r2.append(button('Approve', 'destructive', () => decide(c.id, 'approve')));
      if (c.state === 'waiting_approval' || c.state === 'queued') r2.append(button('Cancel', 'flat', () => decide(c.id, 'cancel')));
      history.append(r2);
    }
  }

  async function send(kind) {
    if (!current) return;
    const r = await call('POST', '/api/v1/devices/' + encodeURIComponent(current.id) + '/commands', { kind });
    if (!r.ok) say('The command could not be sent.');
    device(current.id);
  }

  async function activity() {
    show('activity-view');
    $('title').textContent = 'Activity';
    say('');
    const r = await call('GET', '/api/v1/audit');
    if (!r.ok) { say('The activity log failed its integrity check.'); return; }
    const data = await r.json();
    const names = data.devices || {};
    const who = (a) => (a.startsWith('device:') ? names[a.slice(7)] || 'A device' : a);
    const dev = (id) => names[id] || id;
    const what = (e) => {
      const d = e.detail || {};
      const kind = (kinds[d.kind] || d.kind || '').toLowerCase();
      switch (e.action) {
        case 'token.create': return 'Created an enrollment code for ' + e.target + ', ' + d.uses + ' use' + (d.uses === '1' ? '' : 's');
        case 'policy.set': return 'Changed the ' + e.target + ' policy: ' + (d.keys || '').split(',').join(', ');
        case 'device.enroll': return 'Joined the ' + d.group + ' group';
        case 'device.retire': return 'Removed ' + dev(e.target) + ' from management';
        case 'command.create': return 'Asked ' + dev(e.target) + ' to ' + kind + (d.state === 'waiting_approval' ? ', waiting for a second administrator' : '');
        case 'command.approve': return 'Approved: ' + kind + ' on ' + dev(e.target);
        case 'command.cancel': return 'Canceled: ' + kind + ' on ' + dev(e.target);
        case 'command.done': return kinds[d.kind] || d.kind;
        case 'command.failed': return (kinds[d.kind] || d.kind) + ' failed';
        case 'command.expired': return (kinds[d.kind] || d.kind) + ' expired';
      }
      return e.action;
    };
    $('activity').replaceChildren(...data.entries.map((e) => row(who(e.actor) + ': ' + what(e), new Date(e.time).toLocaleString())));
  }

  for (const b of document.querySelectorAll('[data-kind]')) b.addEventListener('click', () => send(b.dataset.kind));
  $('to-devices').addEventListener('click', () => { location.hash = ''; route(); });
  $('to-activity').addEventListener('click', () => { location.hash = 'activity'; });
  $('wipe').addEventListener('click', () => { $('wipe-name').textContent = current.name; $('wipe-dialog').show(); });
  $('wipe-cancel').addEventListener('click', () => $('wipe-dialog').close());
  $('wipe-confirm').addEventListener('click', () => { $('wipe-dialog').close(); send('wipe'); });
  $('retire').addEventListener('click', async () => {
    const r = await call('DELETE', '/api/v1/devices/' + encodeURIComponent(current.id));
    if (!r.ok) say('The device could not be removed.');
    location.hash = '';
    route();
  });
  $('add').addEventListener('click', () => { $('add-group').value = ''; $('add-uses').value = '1'; $('add-dialog').show(); });
  $('add-cancel').addEventListener('click', () => $('add-dialog').close());
  $('add-create').addEventListener('click', async () => {
    const group = $('add-group').value.trim() || 'default';
    const uses = parseInt($('add-uses').value, 10) || 1;
    const r = await call('POST', '/api/v1/tokens', { group, uses, hours: 24 });
    $('add-dialog').close();
    if (!r.ok) { say('Group names use lowercase letters, digits and dashes.'); return; }
    const t = await r.json();
    $('code-command').textContent = 'sinty-it agent enroll -server ' + location.origin + ' -token ' + t.token;
    $('code-dialog').show();
  });
  $('code-done').addEventListener('click', () => { $('code-dialog').close(); devices(); });

  function route() {
    const h = decodeURIComponent(location.hash.slice(1));
    if (h === 'activity') activity();
    else if (h.startsWith('device/')) device(h.slice(7));
    else devices();
  }
  window.addEventListener('hashchange', route);
  call('GET', '/api/v1/me').then((r) => (r.ok ? r.json() : {})).then((m) => { me = m.user || ''; route(); });
})();
