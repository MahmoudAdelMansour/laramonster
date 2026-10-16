// LaraMonster dashboard. Talks to the engine's JSON API (same origin).
//
// Alpine note: Alpine *calls* any expression that evaluates to a function.
// Never bind a bare function reference in x-show/x-text/:attr — coerce with
// !! or call it explicitly, or it will run on every render.

async function api(method, path, body) {
  const res = await fetch('/api' + path, {
    method,
    headers: { 'X-LaraMonster': '1', 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  // An engine older than this UI answers unknown endpoints with the page itself.
  if (res.ok && !res.headers.get('content-type')?.includes('json')) {
    throw new Error('The LaraMonster engine is out of date. Restart it to finish updating.');
  }
  let data = {};
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) {
    const e = new Error(res.status === 402 ? 'LaraMonster Pro is needed for this.' : (data.error || res.statusText || 'request failed'));
    e.status = res.status;
    e.data = data;
    throw e;
  }
  return data;
}

// desktopMode makes the app window behave like an app rather than a web page:
// no browser right-click menu, no reload/print shortcuts, no dragging images
// out, and links open in the system browser instead of this window.
function desktopMode() {
  document.documentElement.classList.add('desktop');
  const selectable = 'input, textarea, pre, code, .mono, [contenteditable]';
  document.addEventListener('contextmenu', e => {
    if (!e.target.closest?.(selectable) && !String(getSelection())) e.preventDefault();
  });
  document.addEventListener('keydown', e => {
    const mod = e.ctrlKey || e.metaKey;
    if (e.key === 'F5' || (mod && ['r', 'R', 'p', 'P'].includes(e.key))) e.preventDefault();
  });
  document.addEventListener('dragstart', e => { if (e.target.closest?.('a, img')) e.preventDefault(); });
  document.addEventListener('click', e => {
    const a = e.target.closest?.('a[href]');
    if (!a || a.hasAttribute('download')) return;
    const url = new URL(a.href, location.href);
    if (url.origin === location.origin || !/^https?:$/.test(url.protocol)) return;
    e.preventDefault();
    fetch('/app/open-url?url=' + encodeURIComponent(url.href)).catch(() => {});
  }, true);
  // Same for window.open (sites, service UIs).
  window.open = u => { if (u) fetch('/app/open-url?url=' + encodeURIComponent(new URL(u, location.href).href)).catch(() => {}); return null; };
}

// ---------- .env parsing ----------
// Lines keep their original text, so saving only rewrites what was edited and
// comments, blank lines and order survive.
function parseEnv(text, baseline) {
  const orig = {};
  if (baseline !== undefined) for (const l of parseEnv(baseline)) if (l.t === 'var') orig[l.key] = l.value;
  let id = 0;
  return text.replace(/\r\n/g, '\n').replace(/\n$/, '').split('\n').map(raw => {
    const m = raw.match(/^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*=(.*)$/);
    if (!m) return { t: 'text', raw, id: ++id };
    const value = unquoteEnv(m[2]);
    const o = baseline === undefined ? value : orig[m[1]];
    return { t: 'var', id: ++id, key: m[1], value, orig: o, isNew: o === undefined, raw };
  });
}
function unquoteEnv(v) {
  v = v.trim();
  const q = v[0];
  if (q === '"' || q === "'") {
    let out = '';
    for (let i = 1; i < v.length; i++) {
      const c = v[i];
      if (c === '\\' && q === '"' && i + 1 < v.length) { const n = v[++i]; out += n === 'n' ? '\n' : n; continue; }
      if (c === q) return out;
      out += c;
    }
    return v; // unbalanced quote: keep as written
  }
  const hash = v.search(/\s#/);
  return (hash >= 0 ? v.slice(0, hash) : v).trim();
}
function quoteEnvValue(v) {
  if (v === '' || !/[\s#"'\\`=]/.test(v)) return v;
  return '"' + v.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, '\\n') + '"';
}
function serializeEnv(lines) {
  return lines.filter(l => !l.deleted).map(l => {
    if (l.t === 'text') return l.raw;
    return (l.isNew || l.value !== l.orig) ? l.key + '=' + quoteEnvValue(l.value) : l.raw;
  }).join('\n') + '\n';
}
const ENV_GROUPS = [
  ['App', /^(APP|LOG)_/],
  ['Database', /^DB_/],
  ['Mail', /^MAIL_/],
  ['Cache, sessions & queues', /^(CACHE|SESSION|QUEUE|BROADCAST|FILESYSTEM)_/],
  ['Redis', /^(REDIS|MEMCACHED)_/],
  ['Frontend', /^VITE_/],
  ['Cloud services', /^(AWS|PUSHER|REVERB|STRIPE|MAILGUN|POSTMARK|RESEND|SES|SLACK|OPENAI|ANTHROPIC)_/],
];
function envGroupOf(key) { return (ENV_GROUPS.find(([, re]) => re.test(key)) || ['Other'])[0]; }
function isSecretKey(key) { return /(KEY|SECRET|PASSWORD|PASS|TOKEN|PRIVATE|DSN)/i.test(key); }

// Width of the monitor's history window, in samples (3 minutes at 2 s).
function historyMax() { return 90; }

// ---------- JSON payload tree (escaped HTML, collapsible) ----------
function escHTML(s) { return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]); }
function jsonTree(v, depth = 0, key = null) {
  const k = key === null ? '' : `<span class="jt-key">${escHTML(key)}</span>: `;
  if (v === null) return `<div class="jt-row">${k}<span class="jt-null">null</span></div>`;
  if (typeof v !== 'object') {
    const cls = typeof v === 'string' ? 'jt-str' : typeof v === 'number' ? 'jt-num' : 'jt-bool';
    const text = typeof v === 'string' ? '"' + v + '"' : String(v);
    return `<div class="jt-row">${k}<span class="${cls}">${escHTML(text)}</span></div>`;
  }
  const entries = Array.isArray(v) ? v.map((x, i) => [String(i), x]) : Object.entries(v);
  const label = v['@model'] ? `${v['@model'].split('\\').pop()} #${v['@key']}`
    : v['@object'] ? v['@object'].split('\\').pop()
    : v['@collection'] ? `${v['@collection'].split('\\').pop()} (${v.count})`
    : Array.isArray(v) ? `array (${v.length})` : `{${entries.length}}`;
  if (!entries.length) return `<div class="jt-row">${k}<span class="jt-meta">${Array.isArray(v) ? '[]' : '{}'}</span></div>`;
  const kids = entries.filter(([n]) => !['@model', '@object', '@collection', '@key'].includes(n)).map(([n, x]) => jsonTree(x, depth + 1, n)).join('');
  return `<details class="jt" ${depth < 2 ? 'open' : ''}><summary>${k}<span class="jt-meta">${escHTML(label)}</span></summary><div class="jt-kids">${kids}</div></details>`;
}
function shortClass(c) { return String(c || '').split('\\').pop(); }

function storeGet(k, d) { try { return localStorage.getItem(k) ?? d; } catch { return d; } }
function storeSet(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } }
function confirmSafe(msg) { try { return window.confirm(msg); } catch { return true; } }

// A full wizard form, so bindings never read undefined before it opens.
function blankProject(dir) {
  return { name: '', domain: '', dirChoice: dir || '__custom', dir: '', php: '', kit: 'laravel', database: 'sqlite', mail: true, redis: false, secure: false, npm: true, git: true, packages: ['laravel/boost'] };
}

const STAGES = ['Create the project', 'Serve it on its domain', 'Configure .env and database', 'Install packages', 'Run migrations', 'Build frontend assets', 'First Git commit'];

document.addEventListener('alpine:init', () => {
  Alpine.data('app', () => ({
    s: null,
    offline: false,
    onboard: { open: false, step: 0, setup: null, folder: '', busy: '' },
    page: 'dashboard',
    theme: storeGet('lm-theme', 'system'),
    desktop: false,
    drawer: false,
    q: '',
    acct: null,
    signin: { email: '', password: '', key: '', showKey: false, busy: false, error: '', needAccount: false, notice: '' },
    update: null, // { available, latest, download_url } from /api/update (cached 6h by the engine)
    upd: null, // desktop app only: { kind: self|store|none, phase, version, progress, error } from /app/update
    updDialog: false,
    updRestarting: false,
    moving: false,
    updChecking: false,
    updShown: false, // the "update available" dialog opens once per launch
    updTimer: null,
    proSites: null,
    proSite: storeGet('lm.proSite', ''),
    proError: '',
    proLocked: false,
    ev: { tab: 'live', traces: [], tracking: false, loading: false, q: '', sel: null, trace: null, ix: 0, catalog: null, catLoading: false, cls: null, mode: 'form', form: {}, raw: '', running: false, result: null, exported: null },
    jb: { tab: 'jobs', data: null, loading: false, q: '', cls: null, mode: 'form', form: {}, raw: '', artisan: '', runMode: 'sync', queue: '', connection: '', delay: 0, running: false, result: null, artisanOut: null, exported: null, queues: null, qLoading: false, qConn: '', workerQueue: '', workerTries: 1, log: null, logId: '' },
    mon: null,
    monError: '',
    spark: { key: '', i: -1 },
    siteFilter: storeGet('lm.siteFilter', 'all'),
    siteSort: storeGet('lm.siteSort', 'laravel'),
    panel: { open: false, name: '', tab: 'general', addr: '', mode: 'php', target: '', busy: '' },
    env: { loading: false, file: null, lines: [], view: 'vars', q: '', raw: '', reveal: {}, newKey: '', newVal: '', adding: false, saving: false },
    modal: null,
    addTab: 'park',
    addForm: { path: '', name: '' },
    form: {},
    available: [], availableLoading: false, availableError: '',
    tools: [], toolsManager: '', toolsLoading: false,
    logNames: [], logName: '', logLines: [],
    toasts: [], busy: {}, seenJobs: {}, trusting: false,
    mail: { list: [], total: 0, q: '', loading: false, error: '', msg: null, opening: false, tab: 'html', raw: '' },
    catalog: { kits: [], packages: [], categories: [] },
    pk: { q: '', cat: 'Recommended', results: [], top: [], loading: false, error: '' },
    wizard: { open: false, step: 0, maxStep: 0, touched: false, domainEdited: false, submitting: false, job: null, f: blankProject() },
    // The job log under a running job stays as the person left it.
    logOpen: storeGet('lm.logOpen', '') === '1',
    // Packages tab of the site panel: { installed, php, sel, job } for panel.name.
    sitePk: { loading: false, error: '', installed: [], php: null, sel: [], job: null },
    fixJobs: {}, // warning key → job id of its one-click fix
    // Performance (Pro): checkup, recorded requests, load test.
    pf: { tab: storeGet('lm.pfTab', 'checkup'), checkup: null, cLoading: false, cError: '', report: null, tracking: false, rLoading: false, rError: '',
      fixJobs: {}, explain: {}, load: { path: '/', users: 10, seconds: 15, job: null, confirm: false }, hover: { key: '', i: -1 } },
    // php.ini settings editor: scope "global", a version, or "site:<name>".
    ini: { open: false, scope: '', view: null, loading: false, error: '', edits: {}, saving: false, q: '', newName: '', newVal: '' },
    wizardSteps: ['Name & domain', 'Stack', 'Packages', 'Review'],
    progressStages: STAGES,
    nav: [],

    start() {
      // Ask once shortly after launch, then every 6 hours; failures stay silent.
      const checkUpdate = () => api('GET', '/update').then(u => { this.update = u; if (u.available) this.updateFound(); }).catch(() => {});
      setTimeout(checkUpdate, 4000);
      setInterval(checkUpdate, 6 * 3600 * 1000);
      this.nav = [
        { id: 'dashboard', label: 'Dashboard', icon: 'home', count: () => null },
        { id: 'sites', label: 'Sites', icon: 'globe', count: () => this.s?.sites.length ?? null },
        { id: 'mail', label: 'Mail', icon: 'mail', count: () => this.s?.mail.unread ? this.s.mail.unread : null, hot: () => true },
        { id: 'php', label: 'PHP', icon: 'code', group: 'Environment', count: () => this.s?.php.length ?? null },
        { id: 'services', label: 'Services', icon: 'db', count: () => this.s ? (this.runningServices() || null) : null },
        { id: 'tools', label: 'Tools', icon: 'wrench', count: () => null },
        { id: 'activity', label: 'Activity', icon: 'timeline', group: 'Pro', count: () => this.s && !this.s.pro ? 'PRO' : null, pro: true },
        { id: 'events', label: 'Events', icon: 'zap', count: () => this.s && !this.s.pro ? 'PRO' : null, pro: true },
        { id: 'jobs', label: 'Jobs & Queues', icon: 'box', count: () => this.s && !this.s.pro ? 'PRO' : null, pro: true },
        { id: 'performance', label: 'Performance', icon: 'gauge', count: () => this.s && !this.s.pro ? 'PRO' : (this.pf.report?.n_plus_one?.length || null), hot: () => !!this.pf.report?.n_plus_one?.length, pro: true },
        { id: 'monitor', label: 'Monitor', icon: 'activity', group: 'System', count: () => this.monAlerts() || null, hot: () => true },
        { id: 'account', label: 'Account', icon: 'user', count: () => this.acctAlert() ? '!' : null, hot: () => true },
        { id: 'logs', label: 'Logs', icon: 'logs', count: () => null },
        { id: 'settings', label: 'Settings', icon: 'cog', count: () => null },
      ];
      this.applyTheme();
      const route = () => {
        const [p0, query] = location.hash.slice(1).split('?');
        const p = p0 || 'dashboard';
        if (p === 'account' && query) {
          // Opened from a license email (laramonster://activate): key ready to use.
          const q = new URLSearchParams(query);
          if (q.get('key')) Object.assign(this.signin, { showKey: true, key: q.get('key'), email: q.get('email') || '', needAccount: false, notice: 'Your license key from the email is filled in. Click Sign in to finish.' });
          history.replaceState(null, '', '#account');
        }
        if (p === 'new') {
          // Deep link (tray menu, CLI): open the wizard over the dashboard.
          history.replaceState(null, '', '#dashboard');
          this.page = 'dashboard';
          this.openWizard();
          return;
        }
        this.page = this.nav.some(n => n.id === p) ? p : 'dashboard';
        if (this.page === 'php' && !this.available.length) this.loadAvailable();
        if (this.page === 'logs') this.loadLogs();
        if (this.page === 'tools' || this.page === 'services') this.loadTools();
        if (this.page === 'mail') this.loadMail();
        if (this.page === 'monitor') this.loadMonitor();
        if (this.page === 'settings' && this.desktop) this.loadUpd();
        if (this.page === 'account') this.loadAccount();
        // Activity (what ran, recorded) and Events (the app's event classes)
        // share the Event Tracker's data and site picker.
        if (['activity', 'events'].includes(this.page)) {
          // Each page has its own search; a cross-link may hand one over.
          if (!this.ev.keepQ && this.ev.tab !== (this.page === 'activity' ? 'live' : 'catalog')) this.ev.q = '';
          this.ev.keepQ = false;
        }
        if (this.page === 'activity') { this.ev.tab = 'live'; this.openPro('events'); }
        if (this.page === 'events') { this.ev.tab = 'catalog'; this.openPro('events'); }
        if (this.page === 'jobs') this.openPro('jobs');
        if (this.page === 'performance') this.openPro('performance');
        window.scrollTo(0, 0);
      };
      window.addEventListener('hashchange', route);
      const info = fetch('/app/info').then(r => {
        this.desktop = r.ok && !!r.headers.get('content-type')?.includes('json');
        if (this.desktop) desktopMode();
      }).catch(() => {});
      // Route only once the first status arrived, so pages see real data. The
      // desktop window stays hidden until then, so it never flashes empty.
      Promise.all([info, this.refresh()]).then(() => {
        route();
        if (this.desktop) this.$nextTick(() => fetch('/app/ready').catch(() => {}));
      });
      this.loadTools();
      setInterval(() => this.refresh(), 2000);
      setInterval(() => { if (this.page === 'monitor' && !this.offline) this.loadMonitor(); }, 2000);
      setInterval(() => { if (this.page === 'activity' && this.proSite && !this.offline && document.visibilityState === 'visible') this.loadTraces(true); }, 3000);
      this.loadAccount(true);
      setInterval(() => this.loadAccount(true), 60000);
      // The nav badge: a light peek every 30 s that doesn't speed up sampling.
      const peek = () => { if (this.page !== 'monitor' && !this.offline) api('GET', '/monitor?peek=1').then(m => { this.mon = m; }).catch(() => {}); };
      setTimeout(peek, 3000);
      setInterval(peek, 30000);
      setInterval(() => {
        if (this.offline) return;
        if (this.page === 'logs') this.loadLog(true);
        if (this.page === 'mail' && this.s?.mail.running) this.loadMail(true);
      }, 4000);
    },

    // ---------- state ----------
    async refresh() {
      try {
        this.apply(await api('GET', '/status'));
        this.offline = false;
      } catch {
        this.offline = true;
      }
    },

    apply(st) {
      if (!st || !st.config) return;
      const first = !this.s;
      const prevMail = this.s?.mail;
      this.s = st;
      if (first) this.syncForm();
      // Arriving from a license email: activation first, setup can wait.
      if (first && !st.onboarded && !location.hash.startsWith('#account?')) this.openOnboard();
      for (const j of st.jobs) {
        if (j.done && !this.seenJobs[j.id] && !first) {
          const failed = !!j.error;
          if (failed && this.wizard.open && this.wizard.job === j.id) this.logOpen = true; // show why, without remembering it
          if (!(this.wizard.open && this.wizard.job === j.id)) {
            this.toast(failed ? j.title + ' failed: ' + j.error : j.title + ' — done', failed ? 'err' : 'ok');
          }
          if (j.title.startsWith('Install PHP') || j.title === 'Install the Visual C++ runtime') this.loadAvailable(true);
          if (j.id === this.sitePk.job && this.panel.open) this.loadSitePackages();
          if (Object.values(this.pf.fixJobs).includes(j.id) && this.page === 'performance') this.loadCheckup();
          if (j.title.startsWith('Install') || j.title === 'Set up Mail') this.loadTools();
          if (j.title === 'Set up Mail') this.loadMail();
        }
        if (j.done) this.seenJobs[j.id] = true;
      }
      if (this.page === 'mail' && st.mail.running && (!prevMail?.running || st.mail.total !== prevMail.total)) this.loadMail(true);
    },

    syncForm() {
      const c = this.s.config;
      this.form = { tld: c.tld, http_port: c.http_port, https_port: c.https_port, dns_port: c.dns_port, editor: c.editor };
    },

    async act(promise, ok) {
      try {
        const res = await promise;
        if (res && res.config) this.apply(res); else this.refresh();
        // A started job shows its own progress card: no second message.
        const isJob = res && typeof res.id === 'number' && 'title' in res;
        if (ok && !isJob) this.toast(ok, 'ok');
        return res ?? true;
      } catch (e) {
        this.toast(e.message, 'err');
        this.refresh();
        return null;
      }
    },

    toast(text, kind = 'ok') {
      const t = { id: Math.random(), text, kind };
      this.toasts.push(t);
      setTimeout(() => { this.toasts = this.toasts.filter(x => x !== t); }, kind === 'err' ? 9000 : 3500);
    },

    escape() {
      if (this.drawer) this.drawer = false;
      else if (this.panel.open) this.closeSitePanel();
      else if (this.wizard.open) this.closeWizard();
      else if (this.modal) this.modal = null;
      else if (this.mail.msg && window.innerWidth <= 860) this.closeMail();
    },

    // ---------- helpers ----------
    applyTheme() {
      const root = document.documentElement;
      if (this.theme === 'system') root.removeAttribute('data-theme');
      else root.setAttribute('data-theme', this.theme);
    },
    cycleTheme() {
      this.theme = { system: 'light', light: 'dark', dark: 'system' }[this.theme];
      storeSet('lm-theme', this.theme);
      this.applyTheme();
    },
    slug(v) { return (v || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, ''); },
    runningServices() { return this.s ? this.s.services.filter(x => x.running).length : 0; },
    sitesOn(v) { return this.s.sites.filter(x => x.php === v).length; },
    serviceInstalled(name) { return !!this.s?.services.find(x => x.name === name)?.installed; },
    // Stopped, with another server (the system's own MySQL, Redis…) on its port.
    svcBusyPort(name) { const x = this.s?.services.find(x => x.name === name); return x?.port_busy ? x.port : 0; },
    nodeInstalled() { return !!this.tools.find(t => t.id === 'node')?.installed; },
    // Laravel projects first (by default), then by name.
    sortedSites() {
      const list = [...this.s.sites];
      if (this.siteSort === 'laravel') list.sort((a, b) => (b.laravel - a.laravel) || (b.enabled - a.enabled) || a.name.localeCompare(b.name));
      return list;
    },
    siteCounts() {
      const l = this.s.sites;
      return { all: l.length, active: l.filter(x => x.enabled).length, off: l.filter(x => !x.enabled).length, down: l.filter(x => x.health === 'down').length };
    },
    filteredSites() {
      const q = this.q.trim().toLowerCase();
      const f = this.siteFilter;
      return this.sortedSites().filter(x =>
        (!q || x.name.includes(q) || x.path.toLowerCase().includes(q)) &&
        (f === 'all' || (f === 'active' && x.enabled) || (f === 'off' && !x.enabled) || (f === 'down' && x.health === 'down')));
    },
    setSiteFilter(f) { this.siteFilter = f; storeSet('lm.siteFilter', f); },
    setSiteSort(v) { this.siteSort = v; storeSet('lm.siteSort', v); },
    healthDot(site) { return { up: 'on', down: 'err', off: 'off' }[site.health] || 'off'; },
    healthText(site) {
      if (site.health === 'off') return 'Switched off';
      if (site.health === 'down') return site.reason || 'Not responding';
      return site.proxy ? 'Answering at ' + site.proxy : 'Running';
    },
    toggleSite(site, on) {
      this.act(api('POST', '/sites/enable', { name: site.name, on }), on ? site.name + ' is on' : site.name + ' is switched off');
    },

    // ---------- site panel ----------
    panelSite() { return this.s?.sites.find(x => x.name === this.panel.name); },
    openSitePanel(site, tab) {
      this.panel = { open: true, name: site.name, tab: tab || 'general', addr: site.name, mode: site.proxy ? 'proxy' : 'php', target: site.proxy || '', busy: '' };
      this.env = { ...this.env, file: null, lines: [], view: 'vars', q: '', raw: '', reveal: {}, newKey: '', newVal: '', adding: false };
      this.sitePk = { loading: false, error: '', installed: [], php: null, sel: [], job: null };
      if (this.panel.tab === 'env') this.loadEnv();
      if (this.panel.tab === 'packages') this.loadSitePackages();
      this.ini = { ...this.ini, open: false, scope: '', view: null, edits: {} };
      if (this.panel.tab === 'php') this.loadIni('site:' + site.name);
    },
    closeSitePanel() {
      if (this.envDirty() && !confirmSafe('Discard your unsaved .env changes?')) return;
      if (this.ini.scope.startsWith('site:') && this.iniDirty() && !confirmSafe('Discard your unsaved PHP settings?')) return;
      this.panel.open = false;
    },
    setPanelTab(tab) {
      this.panel.tab = tab;
      if (tab === 'env' && !this.env.file) this.loadEnv();
      if (tab === 'packages' && !this.sitePk.php && !this.sitePk.loading) this.loadSitePackages();
      if (tab === 'php' && this.ini.scope !== 'site:' + this.panel.name) this.loadIni('site:' + this.panel.name);
    },
    addrSuffix(site) { return site.url.split('://')[1].slice(site.name.length); },
    async saveAddress() {
      const site = this.panelSite();
      this.panel.busy = 'addr';
      try {
        const res = await api('POST', '/sites/rename', { name: site.name, to: this.panel.addr });
        this.panel.name = res.name;
        this.panel.addr = res.name;
        await this.refresh();
        this.toast(site.name + ' now opens at ' + res.url + (res.app_url_updated ? ' (APP_URL updated)' : ''), 'ok');
        if (this.env.file) this.loadEnv();
      } catch (e) { this.toast(e.message, 'err'); }
      finally { this.panel.busy = ''; }
    },
    async saveServedBy() {
      const site = this.panelSite();
      const target = this.panel.mode === 'proxy' ? this.panel.target.trim() : '';
      if (this.panel.mode === 'proxy' && !target) { this.toast('Enter where your server runs, like http://127.0.0.1:3000', 'err'); return; }
      this.panel.busy = 'proxy';
      const res = await this.act(api('POST', '/sites/proxy', { name: site.name, target }), target ? site.name + ' now uses your server' : site.name + ' is served by LaraMonster again');
      this.panel.busy = '';
      if (res) this.panel.target = this.panelSite()?.proxy || '';
    },
    servedByChanged() {
      const site = this.panelSite();
      return site && (this.panel.mode === 'proxy' ? this.panel.target.trim() !== (site.proxy || '') : !!site.proxy);
    },

    // ---------- .env editor ----------
    async loadEnv() {
      this.env.loading = true;
      try { this.setEnvFile(await api('GET', '/sites/env?name=' + encodeURIComponent(this.panel.name))); }
      catch (e) { this.toast(e.message, 'err'); }
      finally { this.env.loading = false; }
    },
    setEnvFile(f) {
      this.env.file = f;
      this.env.lines = parseEnv(f.content);
      this.env.raw = f.content;
      this.env.reveal = {};
    },
    envText() { return this.env.view === 'raw' ? this.env.raw : serializeEnv(this.env.lines); },
    envDirty() {
      const f = this.env.file;
      if (!f || !f.exists) return false;
      return this.envText().replace(/\n+$/, '') !== f.content.replace(/\n+$/, '');
    },
    envChangeCount() {
      if (this.env.view === 'raw') return this.envDirty() ? 1 : 0;
      const live = this.env.lines.filter(l => l.t === 'var' && !l.deleted);
      const keys = new Set(live.map(l => l.key));
      const removed = parseEnv(this.env.file.content).filter(l => l.t === 'var' && !keys.has(l.key)).length;
      return live.filter(l => l.isNew || l.value !== l.orig).length + removed;
    },
    setEnvView(v) {
      if (v === this.env.view) return;
      if (v === 'raw') this.env.raw = serializeEnv(this.env.lines);
      else this.env.lines = parseEnv(this.env.raw, this.env.file.content);
      this.env.view = v;
    },
    envGroups() {
      const q = this.env.q.trim().toLowerCase();
      const groups = {};
      for (const l of this.env.lines) {
        if (l.t !== 'var' || l.deleted) continue;
        if (q && !l.key.toLowerCase().includes(q) && !l.value.toLowerCase().includes(q)) continue;
        (groups[envGroupOf(l.key)] ||= []).push(l);
      }
      return [...ENV_GROUPS.map(([n]) => n), 'Other'].filter(n => groups[n]).map(name => ({ name, lines: groups[name] }));
    },
    envChanged(l) { return l.isNew || l.value !== l.orig; },
    envIsBool(l) { return ['true', 'false'].includes(String(l.orig ?? l.value).toLowerCase()); },
    envIsSecret(l) { return isSecretKey(l.key); },
    envSecret(l) { return isSecretKey(l.key) && !!l.value && !this.env.reveal[l.id]; },
    envMissingKey() { return this.env.lines.some(l => l.t === 'var' && !l.deleted && l.key === 'APP_KEY' && !l.value); },
    generateAppKey(l) {
      const bytes = crypto.getRandomValues(new Uint8Array(32));
      l.value = 'base64:' + btoa(String.fromCharCode(...bytes));
      this.env.reveal[l.id] = true;
    },
    removeEnvVar(l) { l.deleted = true; },
    addEnvVar() {
      const key = this.env.newKey.trim().toUpperCase().replace(/[^A-Z0-9_.]/g, '_');
      if (!/^[A-Z_][A-Z0-9_.]*$/.test(key)) { this.toast('Variable names use letters, numbers and _', 'err'); return; }
      const existing = this.env.lines.find(l => l.t === 'var' && !l.deleted && l.key === key);
      if (existing) { existing.value = this.env.newVal; this.toast(key + ' already exists; its value was updated', 'ok'); }
      else this.env.lines.push({ t: 'var', id: Date.now(), key, value: this.env.newVal, orig: undefined, isNew: true, raw: '' });
      this.env.newKey = ''; this.env.newVal = ''; this.env.adding = false;
    },
    discardEnv() { this.setEnvFile(this.env.file); },
    async saveEnv() {
      if (!this.envDirty() || this.env.saving) return;
      this.env.saving = true;
      try {
        this.setEnvFile(await api('POST', '/sites/env', { name: this.panel.name, content: this.envText() }));
        this.toast('.env saved. The previous version is kept as .env.backup', 'ok');
      } catch (e) { this.toast(e.message, 'err'); }
      finally { this.env.saving = false; }
    },
    async createEnv() {
      try { this.setEnvFile(await api('POST', '/sites/env/create', { name: this.panel.name })); this.toast('.env created', 'ok'); }
      catch (e) { this.toast(e.message, 'err'); }
    },
    async clearConfigCache() {
      try { this.setEnvFile(await api('POST', '/sites/env/clear-cache', { name: this.panel.name })); this.toast('Config cache cleared; Laravel reads .env again', 'ok'); }
      catch (e) { this.toast(e.message, 'err'); }
    },
    panelKeys(e) {
      if (!this.panel.open) return;
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') { e.preventDefault(); if (this.panel.tab === 'env') this.saveEnv(); }
    },
    jobBusy(title) { return !!this.s?.jobs.some(j => j.title === title && !j.done); },
    // ---------- progress of a running job ----------
    jobFor(title) { return (this.s?.jobs || []).find(j => j.title === title && !j.done) || null; },
    mbText(n) { return n >= 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(0, Math.round(n / 1024)) + ' KB'; },
    jobPct(j) { return j && !j.done && j.progress >= 0 ? Math.min(100, Math.round(j.progress * 100)) + '%' : ''; },
    // "12.4 of 40.1 MB" while downloading, else what the job is doing.
    jobSizes(j) {
      const t = j?.transfer;
      if (!t) return j?.error || j?.detail || '';
      if (!t.done) return 'Starting download…';
      return t.total > 0 ? this.mbText(t.done) + ' of ' + this.mbText(t.total) : this.mbText(t.done) + ' downloaded';
    },
    // "2.3 MB/s · 15 s left"
    jobSpeed(j) {
      const t = j?.transfer;
      if (!t || !(t.rate > 0)) return '';
      let out = this.mbText(t.rate) + '/s';
      if (t.total > 0 && t.done < t.total) {
        const sec = Math.ceil((t.total - t.done) / t.rate);
        out += ' · ' + (sec < 60 ? sec + ' s' : Math.round(sec / 60) + ' min') + ' left';
      }
      return out;
    },
    jobStep(j) {
      const t = j?.transfer;
      if (!t) return '';
      if (t.steps > 1) return 'File ' + t.step + ' of ' + t.steps + (t.label ? ' · ' + t.label : '');
      return t.label || '';
    },
    visibleJobs() {
      const now = Date.now();
      return (this.s?.jobs || [])
        .filter(j => !(this.wizard.open && this.wizard.job === j.id))
        .filter(j => !j.done || now - new Date(j.finished).getTime() < 6000).slice(0, 3);
    },
    copy(text) {
      navigator.clipboard?.writeText(text).then(() => this.toast('Copied to clipboard'), () => this.toast('Copy failed: select the text and copy it manually', 'err'));
    },
    fmtNum(n) { return n >= 1e6 ? (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + 'M' : n >= 1e3 ? Math.round(n / 1e3) + 'k' : String(n); },
    size(n) { return n > 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB'; },
    ago(d) {
      const s = (Date.now() - new Date(d).getTime()) / 1000;
      if (s < 60) return 'now';
      if (s < 3600) return Math.floor(s / 60) + 'm';
      if (s < 86400) return Math.floor(s / 3600) + 'h';
      return new Date(d).toLocaleDateString();
    },
    serviceIcon(sv) { return { Database: 'db', Cache: 'cache', Mail: 'mail', Search: 'search' }[sv.category] || 'zap'; },
    serviceTone(sv) { return { Database: 'tone-blue', Cache: 'tone-flame', Mail: 'tone-orange', Search: 'tone-green' }[sv.category] || 'tone-blue'; },
    logClass(l) {
      const s = l.toLowerCase();
      if (s.includes('error') || s.includes('fatal')) return 'err';
      if (s.includes('warn')) return 'warn';
      return '';
    },
    dnsText() {
      const tld = this.s.config.tld;
      return {
        none: '*.localhost always points to your machine. Nothing to configure.',
        resolver: 'macOS sends *.' + tld + ' to LaraMonster’s built-in DNS through /etc/resolver/' + tld + '.',
        resolved: 'systemd-resolved sends *.' + tld + ' to LaraMonster’s built-in DNS.',
        hosts: 'Your system has no per-domain DNS routing, so each site is written to the hosts file. Sync it after adding sites.',
      }[this.s.dns.method];
    },
    tlsOK() { return this.s.tls.trusted && ['trusted', 'system'].includes(this.s.tls.browsers); },
    browserText() {
      return { trusted: 'Trusted', partial: 'Some profiles', none: 'Not trusted', 'missing-certutil': 'Needs certutil', unknown: '—' }[this.s.tls.browsers] || '—';
    },

    checklist() {
      const s = this.s;
      return [
        { title: 'Install PHP', icon: 'code', done: s.php.length > 0, text: s.php.length ? 'PHP ' + s.default_php + ' is the default' : 'One download, no compiling', button: 'Install', action: () => { location.hash = 'php'; } },
        { title: 'Add your projects', icon: 'folder', done: s.sites.length > 0, text: s.sites.length ? s.sites.length + ' site(s) served' : 'Park the folder that holds your projects', button: 'Add folder', action: () => this.openAdd('park') },
        { title: 'Trust local HTTPS', icon: 'shield', done: this.tlsOK(), text: this.tlsOK() ? 'Secured sites get a valid padlock' : 'One password prompt', button: 'Trust', action: () => { location.hash = 'settings'; } },
        { title: 'Mail inbox', icon: 'mail', done: s.mail.running, text: s.mail.running ? 'Catching emails from your apps' : 'See every email your apps send', button: 'Set up', action: () => { location.hash = 'mail'; } },
        { title: 'Start a database', icon: 'db', done: s.services.some(x => x.category === 'Database' && x.running), text: 'MySQL/MariaDB or PostgreSQL for your apps', button: 'Services', action: () => { location.hash = 'services'; } },
      ];
    },
    checklistDone() { return this.checklist().filter(c => c.done).length; },

    // ---------- sites ----------
    reload() { this.act(api('POST', '/reload'), 'Configuration reloaded'); },
    isolate(name, version) { this.act(api('POST', '/sites/isolate', { name, version }), version ? name + ' now uses PHP ' + version : name + ' uses the default PHP'); },
    secure(name, on) { this.act(api('POST', '/sites/secure', { name, on }), on ? name + ' is now served over HTTPS' : name + ' is now served over HTTP'); },
    unlink(name) { if (confirmSafe('Unlink ' + name + '? The folder itself is not touched.')) this.act(api('POST', '/sites/unlink', { name }), 'Unlinked ' + name); },
    forget(path) { if (confirmSafe('Stop serving every site in ' + path + '?')) this.act(api('POST', '/sites/forget', { path }), 'Forgot ' + path); },
    openSite(site, target) {
      if (target === 'browser' && !this.desktop) { window.open(site.url, '_blank', 'noopener'); return; }
      this.act(api('POST', '/sites/open', { name: site.name, target }));
    },
    openAdd(tab) { this.addTab = tab; this.addForm = { path: '', name: '' }; this.modal = 'add'; },
    async addFolder() {
      const f = this.addForm;
      if (!f.path.trim()) { this.toast('Enter a folder path', 'err'); return; }
      const res = this.addTab === 'park'
        ? await this.act(api('POST', '/sites/park', { path: f.path }), 'Parked ' + f.path)
        : await this.act(api('POST', '/sites/link', { path: f.path, name: f.name }), 'Linked ' + f.path);
      if (res) this.modal = null;
    },
    async pick(form, field) {
      try {
        const j = await (await fetch('/app/pick-folder')).json();
        if (j.path) this[form][field] = j.path;
      } catch { /* not in the desktop app */ }
    },
    async pickInto(field) {
      try {
        const j = await (await fetch('/app/pick-folder')).json();
        if (j.path) { this.wizard.f.dirChoice = '__custom'; this.wizard.f[field] = j.path; }
      } catch { /* not in the desktop app */ }
    },

    // ---------- PHP ----------
    async loadAvailable(force) {
      if (this.availableLoading || (this.available.length && !force)) return;
      this.availableLoading = true;
      this.availableError = '';
      try { this.available = await api('GET', '/php/available'); }
      catch (e) { this.availableError = 'Couldn’t load versions: ' + e.message; }
      finally { this.availableLoading = false; }
    },
    installVCRedist() { this.act(api('POST', '/php/vcredist'), 'Downloading the Visual C++ runtime… Windows will ask for permission.'); },
    installPHP(version) { this.act(api('POST', '/php/install', { version }), 'Installing PHP ' + version + '…'); },
    async uninstallPHP(version) {
      if (!confirmSafe('Uninstall PHP ' + version + '?')) return;
      if (await this.act(api('POST', '/php/uninstall', { version }), 'PHP ' + version + ' uninstalled')) this.loadAvailable(true);
    },
    setDefault(version) { this.act(api('POST', '/php/default', { version }), 'PHP ' + version + ' is now the default'); },

    // ---------- account (LaraMonster Pro) ----------
    async loadAccount(quiet) {
      try { this.acct = await api('GET', '/account'); } catch (e) { if (!quiet) this.toast(e.message, 'err'); }
    },
    acctAlert() { return ['replaced', 'admin', 'expired', 'revoked', 'offline'].includes(this.acct?.state); },
    acctStateLabel() {
      return { active: 'Active', offline: 'Offline too long', expired: 'Expired', revoked: 'Revoked', replaced: 'Signed out', admin: 'Signed out', signed_out: 'Signed out', none: 'Not signed in' }[this.acct?.state] || '—';
    },
    acctTone() { return { active: 'badge-green', expired: 'badge-orange', offline: 'badge-orange', revoked: 'badge-red', replaced: 'badge-red', admin: 'badge-red' }[this.acct?.state] || ''; },
    async signIn() {
      const f = this.signin;
      f.error = '';
      const key = f.key.trim();
      // Your own key (the email's "Open in LaraMonster" link) works on its
      // own; anything else needs the account: email + password.
      const keyOnly = key && !f.password;
      if (!keyOnly && (!f.email.trim() || !f.password)) { f.error = 'Enter your monstersgate.com email and password.'; return; }
      f.busy = true;
      try {
        const res = await api('POST', '/account/sign-in', keyOnly
          ? { key }
          : { email: f.email, password: f.password, key });
        this.acct = res.account;
        if (!res.ok) {
          if (res.code === 'key_needs_account') f.needAccount = true;
          f.error = res.message;
          return;
        }
        f.password = '';
        f.needAccount = false;
        this.toast(res.replaced ? 'Signed in. ' + (res.replaced.device_name || 'Your other device') + ' was signed out.' : 'Signed in: LaraMonster Pro is on', 'ok');
        this.refresh();
      } catch (e) { f.error = e.message; }
      finally { f.busy = false; }
    },
    async checkLicense() {
      try { this.acct = await api('POST', '/account/check'); this.toast(this.acct.pro ? 'License checked: active' : 'License checked', this.acct.pro ? 'ok' : 'err'); this.refresh(); }
      catch (e) { this.toast(e.message, 'err'); }
    },
    async signOutAccount() {
      if (!confirmSafe('Sign out of LaraMonster Pro on this computer? Events and Jobs lock until you sign in again.')) return;
      try { this.acct = await api('POST', '/account/sign-out'); this.toast('Signed out', 'ok'); this.refresh(); }
      catch (e) { this.toast(e.message, 'err'); }
    },
    openURL(u) { if (u) window.open(u, '_blank', 'noopener'); },

    // ---------- updates ----------
    // A newer version is out. The desktop app installs it by itself (unless
    // turned off in Settings) and asks to restart; otherwise it says so once
    // per launch. Store copies are updated by the Microsoft Store.
    async updateFound() {
      if (!this.desktop) return;
      await this.loadUpd();
      if (!this.upd) return;
      if (this.upd.phase === 'ready') { this.updDialog = true; return; }
      if (this.upd.kind === 'self' && !this.s?.config.manual_updates) { this.installUpdate(true); return; }
      if (this.upd.phase === 'idle' && !this.updShown) { this.updShown = true; this.updDialog = true; }
    },
    async loadUpd() {
      try { const r = await fetch('/app/update'); if (r.ok) { const j = await r.json(); this.upd = { ...j.state, store_url: j.store_url }; } } catch { /* browser dashboard */ }
    },
    async installUpdate(auto) {
      try {
        const r = await fetch('/app/update/install', { method: 'POST' });
        const j = await r.json();
        if (!r.ok) throw new Error(j.error || 'The update could not start');
      } catch (e) {
        if (!auto) this.toast(e.message, 'err');
        return;
      }
      if (!auto) this.updDialog = false;
      clearInterval(this.updTimer);
      this.updTimer = setInterval(async () => {
        await this.loadUpd();
        const phase = this.upd?.phase;
        if (phase === 'ready' || phase === 'error' || phase === 'idle') clearInterval(this.updTimer);
        if (phase === 'ready') this.updDialog = true;
        if (phase === 'error') this.toast('Update failed: ' + this.upd.error, 'err');
      }, 700);
      await this.loadUpd();
    },
    async restartForUpdate() {
      this.updRestarting = true;
      try {
        const r = await fetch('/app/update/restart', { method: 'POST' });
        const j = await r.json();
        if (!r.ok) throw new Error(j.error || 'Could not restart');
      } catch (e) { this.updRestarting = false; this.toast(e.message, 'err'); }
    },
    // macOS: move a translocated (read-only) copy into Applications; the app
    // restarts from there.
    async moveToApplications() {
      this.moving = true;
      try {
        const r = await fetch('/app/move', { method: 'POST' });
        const j = await r.json();
        if (!r.ok) throw new Error(j.error || 'Could not move LaraMonster');
      } catch (e) { this.moving = false; this.toast('Couldn’t move LaraMonster: ' + e.message + '. In Finder, drag it into Applications instead.', 'err'); }
    },
    // Settings → Updates: one word for the badge, one sentence for the detail.
    updStatus() {
      const v = 'v' + (this.update?.latest || '');
      if (this.updChecking) return { word: 'Checking', badge: 'badge-blue', detail: '' };
      switch (this.upd?.phase) {
        case 'downloading': return { word: 'Downloading', badge: 'badge-blue', detail: 'Downloading ' + v + '… ' + (this.upd.progress || 0) + '%' };
        case 'installing': return { word: 'Installing', badge: 'badge-blue', detail: 'Installing ' + v + '…' };
        case 'ready': return { word: 'Restart needed', badge: 'badge-orange', detail: 'v' + this.upd.version + ' is installed. Restart LaraMonster to start using it; your sites pause for a few seconds.', alert: 'info' };
        case 'error': return { word: 'Update failed', badge: 'badge-red', detail: this.upd.error, alert: 'error' };
      }
      if (this.update?.error) return { word: 'Couldn’t check', badge: 'badge-red', detail: 'Couldn’t check for updates: ' + this.update.error + '.', alert: 'error' };
      if (this.upd?.kind === 'translocated') return { word: this.update?.available ? 'Update available' : 'Can’t update', badge: 'badge-orange', detail: 'macOS runs LaraMonster from a read-only copy while it stays where it was downloaded, so it can’t update itself. Move it to Applications to fix that.', alert: 'info' };
      if (this.update?.available) return { word: 'Update available', badge: 'badge-orange', detail: v + ' is available.' };
      if (!this.update) return { word: 'Not checked', badge: '', detail: '' };
      if (/dev|-\d+-g[0-9a-f]+$|-dirty$/.test(this.update.current || '')) return { word: 'Development build', badge: '', detail: 'This is a development build, so it doesn’t update itself.' };
      return { word: 'Up to date', badge: 'badge-green', detail: '' };
    },
    async checkForUpdates() {
      this.updChecking = true;
      try {
        this.update = await api('GET', '/update?refresh=1');
        if (this.desktop) await this.loadUpd();
        if (this.update.available) this.updateFound();
        else if (!this.update.error) this.toast('LaraMonster is up to date', 'ok');
      } catch (e) {
        this.toast(e.message, 'err');
      } finally {
        this.updChecking = false;
      }
    },
    updChipText() {
      const v = 'v' + (this.upd?.version || this.update?.latest || '');
      switch (this.upd?.phase) {
        case 'downloading': return 'Downloading ' + v + '… ' + (this.upd.progress || 0) + '%';
        case 'installing': return 'Installing ' + v + '…';
        case 'ready': return 'Restart to update to ' + v;
      }
      return 'Update available: ' + v;
    },
    updChipClick() {
      if (!this.desktop) { this.openURL(this.update?.download_url); return; }
      this.updDialog = true;
    },
    async setAutoUpdate(on) {
      await this.act(api('POST', '/settings', { auto_update: on }), on ? 'Updates install by themselves' : 'You’ll be told when an update is out');
      if (on && this.update?.available) this.updateFound();
    },
    // Pages on the licensing site (sign up, password reset), from the buy link the server sends.
    siteURL(path) { try { return new URL(path, this.acct?.buy_url || 'https://monstersgate.com').toString(); } catch { return 'https://monstersgate.com' + path; } },
    licenseExpiry() {
      const l = this.acct?.license;
      if (!l) return '';
      if (l.perpetual) return 'Never expires';
      return l.expires_at ? 'Expires ' + new Date(l.expires_at).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' }) : '';
    },

    // ---------- Pro: shared ----------
    proFail(e) {
      if (e.status === 402) { this.proLocked = true; if (e.data?.account) this.acct = e.data.account; return; }
      this.proError = e.message;
    },
    async openPro(which) {
      this.proError = '';
      if (!this.s?.pro) { this.proLocked = true; return; }
      this.proLocked = false;
      if (!this.proSites) await this.loadProSites();
      if (!this.proSite && this.proSites?.length) this.setProSite(this.proSites[0].name, true);
      if (!this.proSite) return;
      if (which === 'events') { this.loadTraces(); if (this.ev.tab === 'catalog') this.loadCatalog(); }
      if (which === 'jobs') { this.jb.tab === 'queues' ? this.loadQueues() : this.loadJobs(); }
      if (which === 'performance') this.pfTab(this.pf.tab);
    },
    async loadProSites() {
      try { this.proSites = await api('GET', '/pro/sites'); } catch (e) { this.proFail(e); }
    },
    proSiteInfo() { return (this.proSites || []).find(x => x.name === this.proSite); },
    setProSite(name, quiet) {
      this.proSite = name;
      storeSet('lm.proSite', name);
      this.ev = { ...this.ev, traces: [], sel: null, trace: null, catalog: null, cls: null, result: null, exported: null };
      this.jb = { ...this.jb, data: null, cls: null, result: null, artisanOut: null, queues: null, exported: null, log: null };
      this.pf = { ...this.pf, checkup: null, report: null, explain: {}, load: { ...this.pf.load, job: null } };
      if (!quiet) this.openPro(this.page === 'activity' ? 'events' : this.page);
    },
    paramInput(p) {
      return { int: 'number', float: 'number', date: 'datetime-local' }[p.kind] || 'text';
    },
    paramHint(p) {
      if (p.kind === 'model') return shortClass(p.class) + ' ID';
      if (p.kind === 'array') return 'JSON, like [1, 2] or {"a": 1}';
      if (p.kind === 'object') return 'Resolved from the container';
      if (p.optional) return 'optional · default ' + JSON.stringify(p.default);
      if (p.nullable) return 'optional (null)';
      return p.type;
    },
    blankForm(params) {
      const f = {};
      for (const p of params || []) f[p.name] = p.kind === 'bool' ? false : '';
      return f;
    },
    async exportGuide(kind, extra, target) {
      try {
        const res = await api('POST', '/pro/export', { site: this.proSite, kind, ...extra });
        target.exported = res;
        this.toast('Saved ' + res.name, 'ok');
      } catch (e) { this.toast(e.message, 'err'); }
    },
    revealExport(x, folder) { api('POST', '/pro/reveal', { path: x.path, folder: !!folder }).catch(e => this.toast(e.message, 'err')); },
    errLine(e) { return e ? shortClass(e.class) + ': ' + e.message + (e.file ? ' (' + e.file + ':' + e.line + ')' : '') : ''; },

    // ---------- Event Tracker ----------
    async loadTraces(quiet) {
      if (!quiet) this.ev.loading = true;
      try {
        const res = await api('GET', '/pro/events?site=' + encodeURIComponent(this.proSite));
        this.ev.traces = res.traces;
        this.ev.tracking = res.tracking;
      } catch (e) { if (!quiet) this.proFail(e); }
      finally { this.ev.loading = false; }
    },
    async setTracking(on) {
      try {
        await api('POST', '/pro/tracking', { site: this.proSite, on });
        this.ev.tracking = on;
        const info = this.proSiteInfo(); if (info) info.tracking = on;
        this.toast(on ? 'Recording events for ' + this.proSite + '. Use the site and they show up here.' : 'Stopped recording ' + this.proSite, 'ok');
      } catch (e) { this.toast(e.message, 'err'); this.loadTraces(true); }
    },
    filteredTraces() {
      const q = this.ev.q.trim().toLowerCase();
      return this.ev.traces.filter(t => !q || t.title.toLowerCase().includes(q) || t.app_events.some(n => n.toLowerCase().includes(q)));
    },
    async openTrace(id) {
      this.ev.sel = id;
      this.ev.exported = null;
      try {
        this.ev.trace = await api('GET', '/pro/events/trace?site=' + encodeURIComponent(this.proSite) + '&id=' + encodeURIComponent(id));
        const firstApp = this.ev.trace.events.findIndex(e => e.app);
        this.ev.ix = firstApp >= 0 ? firstApp : 0;
      } catch (e) { this.toast(e.message, 'err'); }
    },
    traceEvent() { return this.ev.trace?.events?.[this.ev.ix]; },
    barStyle(e) {
      const total = Math.max(this.ev.trace?.duration || 1, 0.01);
      const left = Math.min(98, (e.at / total) * 100);
      const width = Math.max(0.8, Math.min(100 - left, ((e.duration || 0) / total) * 100));
      return `margin-inline-start:${left}%;width:${width}%`;
    },
    statusText(t) {
      if (typeof t.status === 'number') return String(t.status);
      return { done: 'Done', failed: 'Failed' }[t.status] || (t.status ?? '—');
    },
    statusTone(t) {
      if (t.failed) return 'badge-red';
      if (typeof t.status === 'number') return t.status >= 400 ? 'badge-orange' : 'badge-green';
      return t.status === 'done' ? 'badge-green' : '';
    },
    kindLabel(k) { return { request: 'HTTP', command: 'CLI', job: 'JOB', laramonster: 'YOU' }[k] || k; },
    async clearTraces() {
      if (!confirmSafe('Delete every recorded trace for ' + this.proSite + '?')) return;
      try { await api('POST', '/pro/events/clear', { site: this.proSite }); this.ev.traces = []; this.ev.trace = null; this.ev.sel = null; this.toast('Traces cleared', 'ok'); }
      catch (e) { this.toast(e.message, 'err'); }
    },
    async loadCatalog(force) {
      if (this.ev.catalog && !force) return;
      this.ev.catLoading = true;
      try {
        this.ev.catalog = await api('GET', '/pro/events/catalog?site=' + encodeURIComponent(this.proSite));
        if (!this.ev.cls && this.ev.catalog.length) this.pickEvent(this.ev.catalog[0].class);
      } catch (e) { this.proFail(e); }
      finally { this.ev.catLoading = false; }
    },
    catEvent() { return (this.ev.catalog || []).find(e => e.class === this.ev.cls); },
    pickEvent(cls) {
      this.ev.cls = cls;
      this.ev.form = this.blankForm(this.catEvent()?.params);
      this.ev.result = null;
      this.ev.exported = null;
      this.ev.raw = '';
    },
    filteredCatalog() {
      const q = this.ev.q.trim().toLowerCase();
      return (this.ev.catalog || []).filter(e => !q || e.class.toLowerCase().includes(q));
    },
    async dispatchEvent() {
      this.ev.running = true;
      this.ev.result = null;
      try {
        const body = { site: this.proSite, class: this.ev.cls };
        if (this.ev.mode === 'raw') body.raw = this.ev.raw; else body.args = this.ev.form;
        this.ev.result = await api('POST', '/pro/events/dispatch', body);
        if (this.ev.result.ok) this.toast(shortClass(this.ev.result.event) + ' dispatched', 'ok');
        this.loadTraces(true);
        this.$nextTick(() => document.querySelector('[x-show*="ev.tab === \'catalog\'"] .run-result')?.scrollIntoView({ behavior: 'smooth', block: 'nearest' }));
      } catch (e) { this.proFail(e); this.toast(e.message, 'err'); }
      finally { this.ev.running = false; }
    },
    showResultTrace() {
      const id = this.ev.result?.trace?.id;
      location.hash = 'activity';
      if (id) this.loadTraces(true).then(() => this.openTrace(id));
    },
    // Events → the recordings where this event fired.
    eventActivity(cls) {
      this.ev.q = shortClass(cls);
      this.ev.keepQ = true;
      location.hash = 'activity';
    },
    // Activity → this event's page in the catalog.
    async openEventClass(cls) {
      location.hash = 'events';
      await this.loadCatalog();
      this.ev.q = '';
      if ((this.ev.catalog || []).some(e => e.class === cls)) this.pickEvent(cls);
      else this.toast(shortClass(cls) + ' isn’t one of this app’s event classes (it comes from Laravel or a package)', 'err');
    },
    resultRows(r) { return (r?.trace?.events || []); },

    // ---------- Performance ----------
    pfTab(tab) {
      this.pf.tab = tab;
      storeSet('lm.pfTab', tab);
      if (!this.proSite) return;
      if (tab === 'checkup' && !this.pf.checkup) this.loadCheckup();
      if (tab === 'requests') this.loadPerfRequests();
      if (tab === 'load' && !this.pf.report) this.loadPerfRequests(true);
    },
    async loadCheckup() {
      const site = this.proSite;
      this.pf.cLoading = true; this.pf.cError = '';
      try { const c = await api('GET', '/pro/perf/checkup?site=' + encodeURIComponent(site)); if (this.proSite === site) this.pf.checkup = c; }
      catch (e) { this.pf.cError = e.message; this.proFail(e); }
      finally { this.pf.cLoading = false; }
    },
    async loadPerfRequests(quiet) {
      const site = this.proSite;
      if (!quiet) this.pf.rLoading = true;
      this.pf.rError = '';
      try {
        const r = await api('GET', '/pro/perf/requests?site=' + encodeURIComponent(site));
        if (this.proSite === site) { this.pf.report = r.report; this.pf.tracking = r.tracking; }
      } catch (e) { this.pf.rError = e.message; this.proFail(e); }
      finally { this.pf.rLoading = false; }
    },
    checkupGroups() { return [...new Set((this.pf.checkup?.checks || []).map(c => c.group))]; },
    checkIcon(c) { return { good: 'check', warn: 'alert', bad: 'alert', info: 'info' }[c.status]; },
    checkWord(c) { return { good: 'Good', warn: 'Improve', bad: 'Fix this', info: 'Note' }[c.status]; },
    scoreTone(n) { return n >= 85 ? 'good' : n >= 60 ? 'warn' : 'bad'; },
    pfFixState(action) {
      const id = this.pf.fixJobs[action];
      const j = id && (this.s?.jobs || []).find(x => x.id === id);
      return !j ? '' : !j.done ? 'running' : j.error ? 'failed' : 'done';
    },
    async runPerfFix(fix) {
      try {
        const job = await api('POST', '/pro/perf/fix', { site: this.proSite, action: fix.action });
        this.pf.fixJobs = { ...this.pf.fixJobs, [fix.action]: job.id };
        this.refresh();
      } catch (e) { this.toast(e.message, 'err'); }
    },
    async setPerfTracking(on) {
      await this.setTracking(on);
      this.loadPerfRequests(true);
    },
    sqlKey(q) { return q.sql + '|' + (q.src || ''); },
    async explainQuery(q) {
      const k = this.sqlKey(q);
      this.pf.explain = { ...this.pf.explain, [k]: { loading: true } };
      try {
        const r = await api('POST', '/pro/perf/explain', { site: this.proSite, sql: q.sql, bindings: q.bindings || [], conn: q.conn || '' });
        this.pf.explain = { ...this.pf.explain, [k]: { result: r } };
      } catch (e) { this.pf.explain = { ...this.pf.explain, [k]: { error: e.message } }; }
    },
    ms(v) { return v >= 1000 ? (v / 1000).toFixed(v >= 10000 ? 0 : 1) + ' s' : (v >= 10 ? Math.round(v) : (Math.round(v * 10) / 10)) + ' ms'; },
    // Open a recorded request in Activity.
    openPerfTrace(id) {
      if (!id) return;
      location.hash = 'activity';
      this.loadTraces(true).then(() => this.openTrace(id));
    },
    perfRoutes() { return (this.pf.report?.routes || []).filter(r => r.route.startsWith('GET ') && !r.route.includes('{')).map(r => r.route.slice(4)); },
    loadJob() { const id = this.pf.load.job; return id ? (this.s?.jobs || []).find(j => j.id === id) : (this.s?.jobs || []).find(j => j.title.startsWith('Load test') && !j.done); },
    loadResult() { const j = this.loadJob(); return j?.done && !j.error ? j.result : null; },
    async startLoadTest() {
      const l = this.pf.load;
      try {
        const job = await api('POST', '/pro/perf/load', { site: this.proSite, path: l.path.trim() || '/', users: +l.users, seconds: +l.seconds });
        this.pf.load = { ...l, job: job.id, confirm: false };
        this.refresh();
      } catch (e) { this.toast(e.message, 'err'); }
    },
    // A load test's chart: one series, scaled to its own range from zero.
    loadPath(key, w, h, area) {
      const pts = this.loadResult()?.series || [];
      if (pts.length < 2) return '';
      const vals = pts.map(p => p[key] || 0);
      const top = Math.max(...vals, 1) * 1.15;
      const x = i => (i / (pts.length - 1)) * w;
      const y = v => h - 2 - (v / top) * (h - 4);
      let d = vals.map((v, i) => (i ? 'L' : 'M') + x(i).toFixed(1) + ' ' + y(v).toFixed(1)).join(' ');
      if (area) d += ` L${w} ${h} L0 ${h} Z`;
      return d;
    },
    loadHover(key, e) {
      const pts = this.loadResult()?.series || [];
      if (!pts.length) return;
      const r = e.currentTarget.getBoundingClientRect();
      const i = Math.round(Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)) * (pts.length - 1));
      this.pf.hover = { key, i };
    },
    loadPoint(key, fmt) {
      const pts = this.loadResult()?.series || [];
      const h = this.pf.hover;
      if (h.key !== key || h.i < 0 || !pts[h.i]) return null;
      return { left: pts.length > 1 ? h.i / (pts.length - 1) * 100 : 0, text: fmt(pts[h.i][key]) + ' · ' + pts[h.i].t + ' s' };
    },

    // ---------- Jobs & Queues ----------
    async loadJobs(force) {
      if (this.jb.data && !force) return;
      this.jb.loading = true;
      try {
        this.jb.data = await api('GET', '/pro/jobs?site=' + encodeURIComponent(this.proSite));
        if (!this.jb.cls && this.jb.data.jobs.length) this.pickJob(this.jb.data.jobs[0].class);
      } catch (e) { this.proFail(e); }
      finally { this.jb.loading = false; }
    },
    curJob() { return (this.jb.data?.jobs || []).find(j => j.class === this.jb.cls); },
    pickJob(cls) {
      this.jb.cls = cls;
      this.jb.form = this.blankForm(this.curJob()?.params);
      this.jb.result = null;
      this.jb.exported = null;
      this.jb.raw = '';
    },
    filteredJobs() {
      const q = this.jb.q.trim().toLowerCase();
      return (this.jb.data?.jobs || []).filter(j => !q || j.class.toLowerCase().includes(q) || j.analysis.summary.toLowerCase().includes(q));
    },
    stepIcon(kind) {
      return { mail: 'mail', notify: 'inbox', http: 'globe', db: 'db', model: 'db', files: 'folder', cache: 'cache', dispatch: 'box', event: 'zap', command: 'code', shell: 'code', log: 'logs', wait: 'loader', retry: 'refresh', batch: 'box', throw: 'alert' }[kind] || 'check';
    },
    async runJob(mode) {
      this.jb.running = true;
      this.jb.result = null;
      try {
        const body = { site: this.proSite, class: this.jb.cls, mode };
        if (this.jb.mode === 'raw') body.raw = this.jb.raw; else body.args = this.jb.form;
        if (mode === 'queue') Object.assign(body, { queue: this.jb.queue, connection: this.jb.connection, delay: Number(this.jb.delay) || 0 });
        this.jb.result = await api('POST', '/pro/jobs/run', body);
        if (this.jb.result.ok) this.toast(mode === 'queue' ? shortClass(this.jb.result.job) + ' queued' : shortClass(this.jb.result.job) + ' ran in ' + Math.round(this.jb.result.duration) + ' ms', 'ok');
        this.$nextTick(() => document.querySelector('[x-show*="jb.tab === \'jobs\'"] .run-result')?.scrollIntoView({ behavior: 'smooth', block: 'nearest' }));
      } catch (e) { this.proFail(e); this.toast(e.message, 'err'); }
      finally { this.jb.running = false; }
    },
    async runArtisan() {
      if (!this.jb.artisan.trim()) return;
      this.jb.running = true;
      this.jb.artisanOut = null;
      try { this.jb.artisanOut = await api('POST', '/pro/artisan', { site: this.proSite, command: this.jb.artisan }); }
      catch (e) { this.toast(e.message, 'err'); }
      finally { this.jb.running = false; }
    },
    async loadQueues() {
      this.jb.qLoading = true;
      try {
        if (!this.jb.data) await this.loadJobs();
        const names = (this.jb.data?.queues || []).join(',');
        const res = await api('GET', '/pro/queues?site=' + encodeURIComponent(this.proSite) + '&connection=' + encodeURIComponent(this.jb.qConn) + '&queues=' + encodeURIComponent(names));
        this.jb.queues = { ...res.result, workers: res.workers };
        if (!this.jb.workerQueue) this.jb.workerQueue = ['default', ...(this.jb.data?.queues || [])].filter((v, i, a) => a.indexOf(v) === i).join(',');
      } catch (e) { this.proFail(e); }
      finally { this.jb.qLoading = false; }
    },
    async queueAction(action, extra, confirmText) {
      if (confirmText && !confirmSafe(confirmText)) return;
      try {
        const res = await api('POST', '/pro/queues/action', { site: this.proSite, action, ...extra });
        this.toast(res.ok ? (res.output || 'Done') : (res.error ? this.errLine(res.error) : res.output || 'That didn’t work'), res.ok ? 'ok' : 'err');
        this.loadQueues();
      } catch (e) { this.toast(e.message, 'err'); }
    },
    async startWorker() {
      try {
        await api('POST', '/pro/workers', { site: this.proSite, action: 'start', connection: this.jb.qConn, queue: this.jb.workerQueue, tries: Number(this.jb.workerTries) || 1 });
        this.toast('Worker started on ' + this.jb.workerQueue, 'ok');
        this.loadQueues();
      } catch (e) { this.toast(e.message, 'err'); }
    },
    async stopWorker(id) {
      try { await api('POST', '/pro/workers', { action: 'stop', id }); this.toast('Worker stopped', 'ok'); this.loadQueues(); }
      catch (e) { this.toast(e.message, 'err'); }
    },
    async showWorkerLog(id) {
      if (this.jb.logId === id && this.jb.log) { this.jb.log = null; this.jb.logId = ''; return; }
      try { this.jb.log = (await api('GET', '/pro/workers/log?id=' + encodeURIComponent(id))).lines; this.jb.logId = id; }
      catch (e) { this.toast(e.message, 'err'); }
    },
    totalWaiting() { return (this.jb.queues?.queues || []).reduce((n, q) => n + (q.size || 0), 0); },

    // ---------- monitor ----------
    monAlerts() { return (this.mon?.recommendations || []).filter(r => r.level !== 'tip').length; },
    async loadMonitor() {
      try { this.mon = await api('GET', '/monitor'); this.monError = ''; }
      catch (e) { this.monError = e.message; }
    },
    bytes(b) {
      if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + ' GB';
      if (b >= 1 << 20) return Math.round(b / (1 << 20)) + ' MB';
      return Math.max(0, Math.round(b / 1024)) + ' KB';
    },
    pct(v) { return v >= 10 || v === 0 ? Math.round(v) + '%' : v.toFixed(1) + '%'; },
    // Sparkline path for one series of the history, scaled to its own range.
    sparkPath(key, w, h, area) {
      const pts = this.mon?.history || [];
      if (pts.length < 2) return '';
      const vals = pts.map(p => p[key]);
      const max = key.endsWith('cpu') ? Math.max(5, ...vals) * 1.15 : Math.max(...vals) * 1.15;
      const min = key.endsWith('cpu') ? 0 : Math.min(...vals) * 0.85;
      const x = i => (i / (historyMax() - 1)) * w + (historyMax() - pts.length) / (historyMax() - 1) * w;
      const y = v => h - 2 - ((v - min) / ((max - min) || 1)) * (h - 4);
      let d = vals.map((v, i) => (i ? 'L' : 'M') + x(i).toFixed(1) + ' ' + y(v).toFixed(1)).join(' ');
      if (area) d += ` L${x(vals.length - 1).toFixed(1)} ${h} L${x(0).toFixed(1)} ${h} Z`;
      return d;
    },
    sparkHover(key, e) {
      const pts = this.mon?.history || [];
      if (!pts.length) return;
      const r = e.currentTarget.getBoundingClientRect();
      const frac = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
      const first = historyMax() - pts.length;
      const i = Math.round(frac * (historyMax() - 1)) - first;
      this.spark = { key, i: Math.max(0, Math.min(pts.length - 1, i)) };
    },
    sparkPoint(key) {
      if (this.spark.key !== key || this.spark.i < 0) return null;
      const p = this.mon?.history?.[this.spark.i];
      if (!p) return null;
      const ago = Math.round((this.mon.at - p.t) / 1000);
      const val = key.endsWith('cpu') ? this.pct(p[key]) : this.bytes(p[key]);
      return { left: ((this.spark.i + historyMax() - this.mon.history.length) / (historyMax() - 1)) * 100, text: val + ' · ' + (ago ? ago + ' s ago' : 'now') };
    },
    groupIcon(g) { return { engine: 'zap', app: 'monitor', php: 'code', service: 'db', server: 'server', jobs: 'box' }[g.kind] || 'box'; },
    groupMax(field) { return Math.max(...(this.mon?.groups || []).map(g => g[field]), 0.0001); },
    recIcon(r) { return { critical: 'alert', warning: 'alert', tip: 'info' }[r.level]; },
    recLabel(r) { return { critical: 'Critical', warning: 'Warning', tip: 'Tip' }[r.level]; },
    async recDo(a) {
      switch (a.kind) {
        case 'stop-service': {
          const sv = this.s.services.find(x => x.name === a.service) || { name: a.service, title: a.service };
          await this.serviceDo(sv, 'stop');
          break;
        }
        case 'sites-off': {
          if (a.sites.length > 1 && !confirmSafe('Switch off ' + a.sites.length + ' sites? You can switch them on again from Sites.')) return;
          await this.act(api('POST', '/sites/enable-many', { names: a.sites, on: false }), a.sites.length === 1 ? a.sites[0] + ' is switched off' : a.sites.length + ' sites switched off');
          break;
        }
        case 'site': {
          const site = this.s.sites.find(x => x.name === a.site);
          if (site) this.openSitePanel(site);
          return;
        }
        case 'sites-filter':
          this.setSiteFilter(a.filter);
          location.hash = 'sites';
          return;
      }
      this.loadMonitor();
    },
    siteOff(name) { this.recDo({ kind: 'sites-off', sites: [name] }); },
    openSiteByName(name) { this.recDo({ kind: 'site', site: name }); },

    // ---------- services & tools ----------
    async serviceDo(sv, action, body) {
      this.busy[sv.name] = true;
      const labels = { start: sv.title + ' started', stop: sv.title + ' stopped', restart: sv.title + ' restarted', enable: body?.on ? sv.title + ' will start with LaraMonster' : sv.title + ' won’t start automatically', port: sv.title + ' now uses port ' + body?.port };
      await this.act(api('POST', '/services/' + sv.name + '/' + action, body || {}), labels[action]);
      this.busy[sv.name] = false;
    },
    servicePort(sv, port) { this.serviceDo(sv, 'port', { port: Number(port) }); },
    showLog(name) { this.logName = name; location.hash = 'logs'; this.loadLog(); },
    async loadTools(fresh) {
      if (this.toolsLoading) return;
      this.toolsLoading = true;
      try { const r = await api('GET', '/tools' + (fresh ? '?fresh=1' : '')); this.tools = r.tools; this.toolsManager = r.manager; }
      catch (e) { if (this.page === 'tools') this.toast('Couldn’t check tools: ' + e.message, 'err'); }
      finally { this.toolsLoading = false; }
    },
    toolFor(service) { return this.tools.find(t => t.service === service); },
    toolCategories() { return [...new Set(this.tools.map(t => t.category))]; },
    toolIcon(t) { return { Runtime: 'code', Database: 'db', Cache: 'cache', Mail: 'mail', Search: 'search' }[t.category] || 'wrench'; },
    methodText(t) {
      if (!t) return '';
      if (t.method === 'download') return 'Portable download · no password';
      if (t.method && t.method !== 'page') return 'via ' + t.method + (t.admin ? ' · asks for your password' : '');
      return 'Install it with your system’s package manager, then re-check.';
    },
    toolError(t) {
      const j = (this.s?.jobs || []).find(j => j.title === 'Install ' + t.name);
      return j && j.done && j.error && !t.installed ? j.error : '';
    },
    installTool(t) {
      if (!t) return;
      this.act(api('POST', '/tools/' + t.id + '/install'), 'Installing ' + t.name + '…');
    },

    // ---------- mail ----------
    async setupMail() { await this.act(api('POST', '/mail/setup'), 'Setting up the mail inbox…'); },
    mailEnv() { return this.s?.services.find(x => x.name === 'mailpit')?.env || ''; },
    async loadMail(quiet) {
      if (!this.s?.mail.running) return;
      if (!quiet) this.mail.loading = true;
      try {
        const r = await api('GET', '/mail/messages?q=' + encodeURIComponent(this.mail.q));
        this.mail.list = r.messages || [];
        this.mail.total = r.messages_count ?? r.total ?? this.mail.list.length;
        this.mail.error = '';
      } catch (e) { if (!quiet) this.mail.error = e.message; }
      finally { this.mail.loading = false; }
    },
    async openMail(id) {
      this.mail.opening = true;
      this.mail.raw = '';
      try {
        const m = await api('GET', '/mail/messages/' + encodeURIComponent(id));
        this.mail.msg = m;
        this.mail.tab = m.HTML ? 'html' : 'text';
        const item = this.mail.list.find(x => x.ID === id);
        if (item) item.Read = true;
        this.refresh();
      } catch (e) { this.toast(e.message, 'err'); }
      finally { this.mail.opening = false; }
    },
    closeMail() { this.mail.msg = null; },
    mailHTML() {
      const m = this.mail.msg;
      if (!m?.HTML) return '';
      let html = m.HTML;
      for (const part of m.Inline || []) {
        if (part.ContentID) html = html.split('cid:' + part.ContentID).join(location.origin + '/mail-part/' + encodeURIComponent(m.ID) + '/' + encodeURIComponent(part.PartID));
      }
      // Links open in a new tab; scripts never run (sandboxed iframe).
      return '<base target="_blank"><meta name="color-scheme" content="light">' + html;
    },
    addr(a) { return a ? (a.Name ? a.Name + ' <' + a.Address + '>' : a.Address) : '—'; },
    async loadRaw() {
      this.mail.tab = 'raw';
      if (this.mail.raw) return;
      try { this.mail.raw = (await api('GET', '/mail/messages/' + encodeURIComponent(this.mail.msg.ID) + '/raw')).raw; }
      catch (e) { this.mail.raw = 'Couldn’t load the source: ' + e.message; }
    },
    async markUnread() {
      const id = this.mail.msg.ID;
      if (await this.act(api('POST', '/mail/read', { IDs: [id], Read: false }), 'Marked as unread')) {
        const item = this.mail.list.find(x => x.ID === id);
        if (item) item.Read = false;
        this.mail.msg = null;
      }
    },
    async deleteMail(id) {
      if (await this.act(api('POST', '/mail/delete', { IDs: [id] }), 'Email deleted')) {
        this.mail.msg = null;
        this.loadMail(true);
      }
    },
    async deleteAllMail() {
      if (!confirmSafe('Delete all ' + this.mail.total + ' emails? This can’t be undone.')) return;
      if (await this.act(api('POST', '/mail/delete', {}), 'Inbox emptied')) {
        this.mail.msg = null;
        this.loadMail();
      }
    },

    // ---------- new project wizard ----------
    async openWizard() {
      this.drawer = false;
      const paths = this.s?.config.paths || [];
      this.wizard = {
        open: true, step: 0, maxStep: 0, touched: false, domainEdited: false, submitting: false, job: null,
        f: blankProject(paths[0]),
      };
      // Prefer a PHP that can actually use SQLite (the default database).
      const def = this.s?.php.find(p => p.version === this.s.default_php);
      if (def && !(def.pdo || []).includes('sqlite')) {
        const ok = this.s.php.find(p => (p.pdo || []).includes('sqlite'));
        if (ok) this.wizard.f.php = ok.version;
      }
      this.pk = { q: '', cat: 'Recommended', results: [], top: this.pk.top || [], loading: false, error: '' };
      this.$nextTick(() => this.$refs.wizName?.focus());
      if (!this.catalog.kits.length) {
        try { this.catalog = await api('GET', '/projects/catalog'); }
        catch (e) { this.toast('Couldn’t load the package catalog: ' + e.message, 'err'); }
      }
      if (!this.tools.length) this.loadTools();
    },
    closeWizard() { this.wizard.open = false; },
    syncDomain() { if (!this.wizard.domainEdited) this.wizard.f.domain = this.slug(this.wizard.f.name); },
    nameError() {
      const n = this.slug(this.wizard.f.name);
      if (!n) return 'Enter a project name (letters, numbers and dashes).';
      return '';
    },
    domainError() {
      const d = this.slug(this.wizard.f.domain || this.wizard.f.name);
      if (!d) return 'Enter a domain.';
      if (this.s.sites.some(x => x.name === d)) return d + '.' + this.s.config.tld + ' is already used by another site.';
      return '';
    },
    wizardPHP() {
      const v = this.wizard.f.php || this.s?.default_php;
      return this.s?.php.find(p => p.version === v);
    },
    // Why the chosen PHP can't use a database ("" when it can).
    dbBlocker(db) {
      const p = this.wizardPHP();
      if (!p || !p.pdo) return '';
      const drv = { sqlite: 'sqlite', mysql: 'mysql', pgsql: 'pgsql' }[db];
      if (p.pdo.includes(drv)) return '';
      const alt = this.s.php.find(x => (x.pdo || []).includes(drv));
      return 'PHP ' + p.version + ' is missing pdo_' + drv + (alt ? ': pick PHP ' + alt.version + ' in step 1' : ': install a PHP version from the PHP page');
    },
    targetDir() { return this.wizard.f.dirChoice === '__custom' ? this.wizard.f.dir : this.wizard.f.dirChoice; },
    previewURL() {
      const c = this.s?.config;
      if (!c) return '';
      const d = this.slug(this.wizard.f.domain || this.wizard.f.name) || 'my-shop';
      return this.wizard.f.secure
        ? 'https://' + d + '.' + c.tld + (c.https_port === 443 ? '' : ':' + c.https_port)
        : 'http://' + d + '.' + c.tld + (c.http_port === 80 ? '' : ':' + c.http_port);
    },
    nextStep() {
      if (this.wizard.step === 0) {
        this.wizard.touched = true;
        if (this.nameError() || this.domainError()) return;
        if (!this.targetDir()?.trim()) { this.toast('Choose where to create the project', 'err'); return; }
      }
      if (this.wizard.step === 1 && this.wizard.f.kit !== 'blank' && this.dbBlocker(this.wizard.f.database)) {
        this.toast(this.dbBlocker(this.wizard.f.database), 'err');
        return;
      }
      this.wizard.step++;
      this.wizard.maxStep = Math.max(this.wizard.maxStep, this.wizard.step);
    },
    wiringText() {
      const f = this.wizard.f, on = [];
      if (f.mail) on.push('Mail inbox');
      if (f.redis) on.push('Redis');
      if (f.secure) on.push('HTTPS');
      if (f.npm) on.push('frontend build');
      if (f.git) on.push('Git');
      return on.join(', ') || 'Nothing extra';
    },
    // The package picker serves the wizard and the site panel's Packages tab.
    pkSel() { return this.panel.open && this.panel.tab === 'packages' ? this.sitePk.sel : this.wizard.f.packages; },
    pkPHP() { return this.panel.open && this.panel.tab === 'packages' ? this.sitePk.php : this.wizardPHP(); },
    // Entries may carry a version: "vendor/name:^2.0".
    pkIndex(name) { return this.pkSel().findIndex(x => x === name || x.startsWith(name + ':')); },
    pkOn(name) { return this.pkIndex(name) >= 0; },
    pkInstalled(name) { return this.panel.open && this.panel.tab === 'packages' && this.sitePk.installed.includes(name); },
    togglePkg(name) {
      if (this.pkInstalled(name)) return;
      const list = this.pkSel();
      const i = this.pkIndex(name.split(':')[0]);
      if (i >= 0) list.splice(i, 1); else list.push(name);
    },
    // The PHP extension a package can't run without that the chosen PHP lacks.
    pkMissingExt(p) {
      const php = this.pkPHP();
      if (!php?.exts?.length) return '';
      return (p.ext || []).find(e => !php.exts.includes(e)) || '';
    },
    // A name typed into the search box ("vendor/name" or "vendor/name:^2.0")
    // can be added as it is, even when Packagist search doesn't list it.
    typedPkg() {
      const q = this.pk.q.trim();
      const m = q.match(/^([a-z0-9]([_.-]?[a-z0-9]+)*\/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*)(:\s*[0-9A-Za-z.*^~|<>=!@, -]{1,64})?$/i);
      if (!m) return null;
      const name = m[1].toLowerCase(), version = (m[5] || '').slice(1).trim();
      return { name, version, arg: version ? name + ':' + version : name };
    },
    addTypedPkg() {
      const t = this.typedPkg();
      if (!t || this.pkInstalled(t.name)) return;
      const list = this.pkSel(), i = this.pkIndex(t.name);
      if (i >= 0) list.splice(i, 1, t.arg); else list.push(t.arg);
      this.pk.q = '';
      this.pk.results = [];
    },
    catCount(c) {
      if (c === 'Top on Packagist') return '';
      return this.catalog.packages.filter(p => c === 'Recommended' ? p.recommended : p.category === c).length;
    },
    visiblePackages() {
      if (this.pk.q.trim()) return this.pk.results;
      if (this.pk.cat === 'Top on Packagist') return this.pk.top;
      return this.catalog.packages.filter(p => this.pk.cat === 'Recommended' ? p.recommended : p.category === this.pk.cat);
    },
    async searchPackages() {
      const q = this.pk.q.trim();
      if (!q && this.pk.cat !== 'Top on Packagist') { this.pk.results = []; this.pk.error = ''; return; }
      if (!q && this.pk.top.length) return;
      this.pk.loading = true;
      this.pk.error = '';
      try {
        const res = await api('GET', '/packages/search?q=' + encodeURIComponent(q));
        // Prefer the curated title/description when a result is in the catalog.
        const merged = res.map(r => ({ ...r, ...(this.catalog.packages.find(p => p.name === r.name) || {}) }));
        if (q) this.pk.results = merged; else this.pk.top = merged;
      } catch (e) { this.pk.error = 'Packagist search failed: ' + e.message; }
      finally { this.pk.loading = false; }
    },
    async createProject() {
      const f = this.wizard.f;
      this.wizard.submitting = true;
      const body = {
        name: this.slug(f.name), domain: this.slug(f.domain || f.name), dir: this.targetDir(), kit: f.kit, php: f.php,
        database: f.database, packages: f.kit === 'blank' ? [] : f.packages, secure: f.secure, mail: f.mail, redis: f.redis, npm: f.npm, git: f.git,
      };
      try {
        const job = await api('POST', '/projects/new', body);
        this.wizard.job = job.id;
        this.refresh();
      } catch (e) { this.toast(e.message, 'err'); }
      finally { this.wizard.submitting = false; }
    },
    currentJob() { return (this.s?.jobs || []).find(j => j.id === this.wizard.job); },
    setLogOpen(open) { this.logOpen = open; storeSet('lm.logOpen', open ? '1' : ''); },
    // Warnings of a finished job; older engines sent plain strings.
    jobWarnings(j) { return (j?.result?.warnings || []).map(w => typeof w === 'string' ? { message: w } : w); },
    fixKey(site, w) { return site + '|' + w.message; },
    fixJob(site, w) { const id = this.fixJobs[this.fixKey(site, w)]; return id ? (this.s?.jobs || []).find(j => j.id === id) : null; },
    // fixed, failed, running, or '' (not tried)
    fixState(site, w) {
      const j = this.fixJob(site, w);
      if (!j) return '';
      if (!j.done) return 'running';
      return j.error || this.jobWarnings(j).length ? 'failed' : 'fixed';
    },
    fixError(site, w) {
      const j = this.fixJob(site, w);
      if (!j?.done) return '';
      return j.error || this.jobWarnings(j).map(x => x.message + (x.detail ? '\n' + x.detail : '')).join('\n');
    },
    async runFix(site, w) {
      try {
        const job = await api('POST', '/projects/fix', { name: site, fix: w.fix });
        this.fixJobs = { ...this.fixJobs, [this.fixKey(site, w)]: job.id };
        this.refresh();
      } catch (e) { this.toast(e.message, 'err'); }
    },
    wizardSite() { return this.slug(this.wizard.f.domain || this.wizard.f.name); },

    // ---------- php.ini settings ----------
    openIni(scope) { this.ini.open = true; this.loadIni(scope); },
    closeIni() {
      if (this.iniDirty() && !confirmSafe('Discard your unsaved PHP settings?')) return;
      this.ini.open = false;
    },
    async loadIni(scope) {
      this.ini = { ...this.ini, scope, loading: true, error: '', edits: {}, q: '', newName: '', newVal: '' };
      try {
        const v = await api('GET', '/php/settings?scope=' + encodeURIComponent(scope));
        if (this.ini.scope === scope) this.ini.view = v;
      } catch (e) { if (this.ini.scope === scope) { this.ini.error = e.message; this.ini.view = null; } }
      finally { this.ini.loading = false; }
    },
    iniLayer() { const sc = this.ini.scope; return sc === 'global' ? 'global' : sc.startsWith('site:') ? 'site' : 'version'; },
    iniTitle() {
      const sc = this.ini.scope;
      if (sc === 'global') return 'PHP settings for every version';
      if (sc.startsWith('site:')) return 'PHP settings for ' + sc.slice(5);
      return 'PHP ' + sc + ' settings';
    },
    iniSourceLabel(src) {
      return { 'php.ini': 'php.ini', global: 'All versions', version: 'PHP ' + (this.ini.view?.version || ''), site: 'This site' }[src] || src;
    },
    // The value set at this scope (pending edits first), or ''.
    iniOwn(r) { return r.name in this.ini.edits ? (this.ini.edits[r.name] ?? '') : (r.layers[this.iniLayer()] ?? ''); },
    // What applies without this scope's own value.
    iniInherited(r) {
      const order = this.iniLayer() === 'site' ? ['version', 'global'] : this.iniLayer() === 'version' ? ['global'] : [];
      for (const l of order) if (l in r.layers) return { value: r.layers[l], from: this.iniSourceLabel(l) };
      return { value: r.base, from: 'php.ini' };
    },
    iniShow(r, v) {
      if (r.kind === 'onoff') return ['1', 'on', 'true', 'yes'].includes(String(v).toLowerCase()) ? 'On' : 'Off';
      return v === '' ? '(empty)' : v;
    },
    iniSet(r, v) { this.ini.edits = { ...this.ini.edits, [r.name]: v === '' ? null : v }; },
    iniReset(r) { this.ini.edits = { ...this.ini.edits, [r.name]: null }; },
    iniChanged(r) { return r.name in this.ini.edits && (this.ini.edits[r.name] ?? '') !== (r.layers[this.iniLayer()] ?? ''); },
    iniDirty() { return (this.ini.view?.rows || []).some(r => this.iniChanged(r)) || Object.keys(this.ini.edits).some(k => !(this.ini.view?.rows || []).some(r => r.name === k)); },
    iniChangeCount() { return Object.keys(this.ini.edits).filter(k => { const r = (this.ini.view?.rows || []).find(x => x.name === k); return !r || this.iniChanged(r); }).length; },
    iniInvalid(r) {
      const v = this.iniOwn(r);
      if (v === '') return '';
      if (r.kind === 'size' && !/^-?\d+\s*[KMG]?$/i.test(v)) return 'Use a number with K, M or G, like 512M (or -1 for no limit).';
      if ((r.kind === 'seconds' || r.kind === 'number') && !/^-?\d+$/.test(v)) return 'Use a whole number.';
      return '';
    },
    iniRows(group) {
      const q = this.ini.q.trim().toLowerCase();
      return (this.ini.view?.rows || []).filter(r => r.group === group && (!q || r.name.includes(q) || r.label.toLowerCase().includes(q)));
    },
    iniAdd() {
      const name = this.ini.newName.trim().toLowerCase(), val = this.ini.newVal.trim();
      if (!/^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$/.test(name)) { this.toast('Setting names look like memory_limit or xdebug.mode', 'err'); return; }
      if (!this.ini.view.rows.some(r => r.name === name)) {
        this.ini.view.rows.push({ name, label: name, group: 'Other', help: '', kind: 'text', value: '', base: '', source: 'php.ini', layers: {} });
      }
      this.iniSet({ name }, val);
      this.ini.newName = ''; this.ini.newVal = '';
    },
    async saveIni() {
      const bad = (this.ini.view?.rows || []).find(r => this.iniInvalid(r));
      if (bad) { this.toast(bad.label + ': ' + this.iniInvalid(bad), 'err'); return; }
      this.ini.saving = true;
      try {
        const v = await api('POST', '/php/settings', { scope: this.ini.scope, values: this.ini.edits });
        this.ini.view = v;
        this.ini.edits = {};
        this.toast(this.iniLayer() === 'site' ? 'Saved: the site uses its new PHP settings now' : 'Saved: PHP restarted with the new settings');
        this.refresh();
      } catch (e) { this.toast(e.message, 'err'); }
      finally { this.ini.saving = false; }
    },

    // ---------- packages of an existing project ----------
    async loadSitePackages() {
      const name = this.panel.name;
      this.sitePk.loading = true;
      this.sitePk.error = '';
      this.pk = { q: '', cat: 'Recommended', results: [], top: this.pk.top || [], loading: false, error: '' };
      try {
        if (!this.catalog.kits.length) this.catalog = await api('GET', '/projects/catalog');
        const r = await api('GET', '/sites/packages?name=' + encodeURIComponent(name));
        if (this.panel.name !== name) return;
        this.sitePk.installed = r.installed || [];
        this.sitePk.php = r.php;
      } catch (e) { this.sitePk.error = e.message; }
      finally { this.sitePk.loading = false; }
    },
    sitePkJob() { return this.sitePk.job ? (this.s?.jobs || []).find(j => j.id === this.sitePk.job) : null; },
    async installSitePackages() {
      try {
        const job = await api('POST', '/sites/packages', { name: this.panel.name, packages: this.sitePk.sel });
        this.sitePk.job = job.id;
        this.refresh();
      } catch (e) { this.toast(e.message, 'err'); }
    },
    sitePkDone() {
      this.sitePk.sel = [];
      this.sitePk.job = null;
      this.loadSitePackages();
    },
    stageState(i) {
      const j = this.currentJob();
      if (!j) return '';
      if (j.done && !j.error) return 'done';
      const cur = Math.floor(Math.max(0, j.progress) * STAGES.length + 1e-6);
      if (i < cur) return 'done';
      if (i === cur) return j.error ? '' : 'now';
      return '';
    },
    openCreated(target) {
      const d = this.slug(this.wizard.f.domain || this.wizard.f.name);
      this.act(api('POST', '/sites/open', { name: d, target }));
    },

    // ---------- logs & settings ----------
    async loadLogs() {
      try {
        this.logNames = await api('GET', '/logs');
        if (!this.logName && this.logNames.length) this.logName = this.logNames.includes('laramonster') ? 'laramonster' : this.logNames[0];
        this.loadLog();
      } catch (e) { this.toast(e.message, 'err'); }
    },
    async loadLog(quiet) {
      if (!this.logName) return;
      try {
        const box = this.$refs.logbox;
        const atBottom = !box || box.scrollHeight - box.scrollTop - box.clientHeight < 40;
        const r = await api('GET', '/logs?name=' + encodeURIComponent(this.logName) + '&lines=500');
        this.logLines = r.lines || [];
        if (atBottom) this.$nextTick(() => { if (box) box.scrollTop = box.scrollHeight; });
      } catch (e) { if (!quiet) this.toast(e.message, 'err'); }
    },
    async saveSettings() {
      const f = this.form;
      if (await this.act(api('POST', '/settings', { tld: f.tld, http_port: Number(f.http_port), https_port: Number(f.https_port), dns_port: Number(f.dns_port), editor: f.editor }), 'Settings saved')) this.syncForm();
    },
    // ---------- first-run setup ----------
    onboardSteps: ['Welcome', 'Projects', 'PHP', 'Your system', 'Done'],
    async openOnboard() {
      Object.assign(this.onboard, { open: true, step: 0, busy: '' });
      await this.loadSetup();
      this.onboard.folder = this.onboard.setup?.folder || '';
      if (!this.s.php.length) this.loadAvailable();
    },
    async loadSetup() {
      try { this.onboard.setup = await api('GET', '/setup'); } catch (e) { this.toast(e.message, 'err'); }
    },
    // Runs one setup action; errors stay on screen as a toast.
    async setupStep(busy, fn) {
      this.onboard.busy = busy;
      try { return await fn(); } catch (e) { this.toast(e.message, 'err'); return false; }
      finally { this.onboard.busy = ''; }
    },
    async onboardFolder() {
      const ok = await this.setupStep('folder', async () => {
        this.apply(await api('POST', '/setup/folder', { path: this.onboard.folder }));
        return true;
      });
      if (ok) { await this.loadSetup(); this.onboard.step++; }
    },
    async onboardPick() {
      try {
        const j = await (await fetch('/app/pick-folder')).json();
        if (j.path) this.onboard.folder = j.path;
      } catch { /* not in the desktop app */ }
    },
    recommendedPHP() {
      const list = this.available.filter(r => !r.installed);
      const num = v => v.split('.').map(Number);
      return list.sort((a, b) => { const [x, y] = [num(a.version), num(b.version)]; return y[0] - x[0] || y[1] - x[1]; })[0];
    },
    phpJob() { return (this.s?.jobs || []).find(j => j.title.startsWith('Install PHP') && !j.done); },
    onboardTerminal() {
      this.setupStep('terminal', async () => {
        this.onboard.setup = await api('POST', '/setup/terminal');
        this.toast('Open a new terminal to use php, composer and laramonster', 'ok');
      });
    },
    async onboardAutostart(input) {
      await this.setupStep('autostart', async () => { this.onboard.setup = await api('POST', '/setup/autostart', { on: input.checked }); });
      // Show the real state, also when the change failed.
      input.checked = !!this.onboard.setup?.autostart;
    },
    onboardTrust() { this.setupStep('trust', () => this.trustCA()); },
    async finishOnboard(then) {
      try { this.apply(await api('POST', '/setup/done')); } catch { /* shown again next time */ }
      this.onboard.open = false;
      if (then === 'project') this.openWizard();
    },
    sampleSiteURL() {
      const c = this.s.config, port = c.http_port === 80 ? '' : ':' + c.http_port;
      return 'http://blog.' + c.tld + port;
    },

    async setBrowserDashboard(on) {
      if (!on && !this.desktop && !confirmSafe('Turn off the browser dashboard? This page will stop working; use the LaraMonster app or `laramonster dashboard on` to bring it back.')) {
        this.refresh();
        return;
      }
      await this.act(api('POST', '/settings', { browser_dashboard: on }), on ? 'Browser dashboard on' : 'Browser dashboard off');
    },
    dnsSetup() { this.act(api('POST', '/dns/setup'), 'DNS configured'); },
    async trustCA() {
      this.trusting = true;
      await this.act(api('POST', '/tls/trust'), 'Certificate authority trusted. Restart your browser once to pick it up.');
      this.trusting = false;
    },
    stopEngine() {
      if (!confirmSafe('Stop LaraMonster? All sites and services will go offline.')) return;
      api('POST', '/stop').catch(() => {}).finally(() => { this.offline = true; });
    },
  }));
});
