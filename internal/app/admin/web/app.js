const API = '/admin/api';

// ========== 主题切换 ==========
function getTheme() {
  return localStorage.getItem('theme') || 'dark';
}
function applyTheme(t) {
  if (t === 'dark') {
    document.documentElement.setAttribute('data-theme', 'dark');
    const ic = document.getElementById('themeIcon');
    if (ic) ic.textContent = '🌙';
  } else {
    document.documentElement.setAttribute('data-theme', 'light');
    const ic = document.getElementById('themeIcon');
    if (ic) ic.textContent = '☀️';
  }
}
function toggleTheme() {
  const cur = getTheme();
  const next = cur === 'dark' ? 'light' : 'dark';
  localStorage.setItem('theme', next);
  applyTheme(next);
}
applyTheme(getTheme());

const _ = id => document.getElementById(id);
const esc = s => { const d=document.createElement('div'); d.textContent=s||''; return d.innerHTML; };
const fmtNum = n => (n || 0).toLocaleString('zh-CN');
const fmtTokens = n => {
  n = n || 0;
  if (n >= 1000000) return (n / 1000000).toFixed(2).replace(/\.?0+$/, '') + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'K';
  return String(n);
};
if (window.location.host) _('footerApiAddr').textContent = 'http://' + window.location.host;

function toast(msg, t, duration) {
  const el = _('toast');
  el.textContent = msg;
  el.style.whiteSpace = 'pre-line';
  el.className = 'toast ' + (t || 'info') + ' show';
  clearTimeout(el._timer);
  el._timer = setTimeout(() => el.classList.remove('show'), duration || 3500);
}

// ========== 导航（单一入口：switchTab） ==========
document.querySelectorAll('.nav-item').forEach(el => {
  el.addEventListener('click', () => { if (el.dataset.tab) switchTab(el.dataset.tab); });
});

function switchTab(name) {
  document.querySelectorAll('.nav-item').forEach(e => {
    e.classList.toggle('active', e.dataset.tab === name);
  });
  document.querySelectorAll('.tab-panel').forEach(e => e.style.display = 'none');
  const panel = _('tab-' + name);
  if (panel) panel.style.display = 'block';
  if (name === 'dashboard') { loadStats(); loadAccounts(); }
  if (name === 'accounts') loadAccounts();
  if (name === 'settings') { loadKeys(); loadModels(); loadConfig(); }
  if (name === 'logs') loadLogs();
  if (name === 'opencode') { loadOcConfig(); loadOcModels(); loadOcStats(); }
  if (name === 'channels') { loadChannels(); }
  if (name === 'auto') { loadAutoConfig(); }
  if (name === 'balance') { loadBalanceConfig(); }
}

// 导入子标签
document.querySelectorAll('#importTabs .tab').forEach(el => {
  el.addEventListener('click', () => {
    document.querySelectorAll('#importTabs .tab').forEach(e => e.classList.remove('active'));
    el.classList.add('active');
    document.querySelectorAll('#import-oauth,#import-token,#import-batch').forEach(e => e.classList.remove('active'));
    _('import-' + el.dataset.tab).classList.add('active');
  });
});

// ========== API 请求（管理员令牌：Bearer；401 时引导输入一次并重试） ==========
const ADMIN_TOKEN_KEY = 'cline_proxy_admin_token';
function getAdminToken() { try { return localStorage.getItem(ADMIN_TOKEN_KEY) || ''; } catch (e) { return ''; } }
function setAdminToken(t) { try { localStorage.setItem(ADMIN_TOKEN_KEY, t); } catch (e) { /* ignore */ } }

async function api(method, path, body, _retried) {
  const opts = { method, headers: {} };
  const tok = getAdminToken();
  if (tok) opts.headers['Authorization'] = 'Bearer ' + tok;
  if (body) { opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
  const res = await fetch(API + path, opts);
  if (res.status === 401 && !_retried) {
    const t = (prompt('请输入管理令牌（见服务启动日志或 .admin-token 文件）') || '').trim();
    if (t) { setAdminToken(t); return api(method, path, body, true); }
    throw new Error('未授权：缺少管理令牌');
  }
  let data;
  try { data = await res.json(); } catch (e) { throw new Error('HTTP ' + res.status); }
  if (!data.success && data.error) throw new Error(data.error);
  return data;
}

// ========== 仪表盘 ==========
async function loadStats() {
  try {
    const d = await api('GET', '/stats');
    const s = d.data;
    _('statTotal').textContent = s.total;
    _('statActive').textContent = s.active;
    _('statCooldown').textContent = s.cooldown;
    _('statExpired').textContent = s.expired;
    if (s.version) _('settingVersion').value = s.version;
    if (s.strategy) _('settingStrategy').value = s.strategy;
  } catch (e) { /* ignore */ }
}

// ========== 账号管理 ==========
async function loadAccounts() {
  try {
    const d = await api('GET', '/accounts');
    const list = d.data.accounts;
    const tbody = _('accountTableBody');
    if (!list || list.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">暂无账号，前往 <a href="#" onclick="switchTab(\'import\')" style="color:var(--accent);cursor:pointer">导入账号</a> 页添加</td></tr>';
      return;
    }
    const sn = { active: '活跃', cooldown: '冷却', expired: '已过期' };
    tbody.innerHTML = list.map(a => {
      const lu = a.lastUsed ? new Date(a.lastUsed).toLocaleString('zh-CN') : '-';
      const cr = a.createdAt ? new Date(a.createdAt).toLocaleString('zh-CN') : '-';
      // 冷却标签：展示预计恢复时间
      let statusExtra = '';
      if (a.status === 'cooldown') {
        const until = a.cooldownUntil ? new Date(a.cooldownUntil).toLocaleString('zh-CN') : '';
        statusExtra = until ? '<div style="font-size:10px;color:var(--text3);margin-top:2px">预计 ' + esc(until) + ' 恢复</div>' : '';
      }
      return '<tr>' +
        '<td>' + esc(a.email) + '</td>' +
        '<td><span class="status ' + a.status + '"><span class="status-dot ' + a.status + '"></span>' + (sn[a.status] || a.status) + '</span>' + statusExtra + '</td>' +
          '<td title="今日 ' + fmtNum(a.tokensToday) + ' / 累计 ' + fmtNum(a.tokensTotal) + ' tokens（上游返回 usage 时精确，否则为估算值）">' + fmtTokens(a.tokensToday) + ' / ' + fmtTokens(a.tokensTotal) + '</td>' +
        '<td class="mono" style="font-size:11px">' + lu + '</td>' +
        '<td class="mono" style="font-size:11px">' + cr + '</td>' +
        '<td style="white-space:nowrap">' +
          '<button class="btn btn-sm" onclick="testAccount(\'' + a.accountId + '\', this)" title="测试账号是否可用（成功会清除冷却/过期状态）">⚡</button> ' +
          '<button class="btn btn-sm" onclick="resetAccount(\'' + a.accountId + '\', this)" title="检测限流并解除：探测上游，若仍限流则保持冷却并提示恢复时间">↻</button> ' +
          '<button class="btn btn-sm btn-danger" onclick="deleteAccount(\'' + a.accountId + '\')" title="删除">✕</button>' +
        '</td></tr>';
    }).join('');
  } catch (e) { toast('加载账号失败: ' + e.message, 'error'); }
}

async function testAccount(id, btn) {
  const original = btn ? btn.innerHTML : '';
  if (btn) { btn.disabled = true; btn.innerHTML = '<span class="loading"></span>测试中'; }
  try {
    const d = await api('POST', '/accounts/test', { accountId: id });
    const r = d.data || {};
    const statusMap = { active: '可用', cooldown: '冷却', expired: '已失效', error: '错误' };
    const label = statusMap[r.status] || r.status;
    const prevMap = { active: '活跃', cooldown: '冷却', expired: '已过期', '': '' };
    let msg = '账号 ' + esc(r.email || '') + ' — ' + label;
    if (r.prevStatus && r.prevStatus !== r.status) msg += '（原状态: ' + (prevMap[r.prevStatus] || r.prevStatus) + '）';
    if (r.cooldownUntil) msg += '\n预计恢复: ' + esc(r.cooldownUntil);
    if (r.remaining) msg += '（剩余 ' + esc(r.remaining) + '）';
    if (r.reason) msg += '\n原因: ' + esc(r.reason);
    if (r.httpStatus) msg += '\nHTTP: ' + r.httpStatus;
    const type = r.status === 'active' ? 'success' : (r.status === 'cooldown' ? 'warning' : 'error');
    toast(msg, type, 6000);
    loadAccounts(); loadStats();
  } catch (e) {
    toast('测试失败: ' + e.message, 'error');
  } finally {
    if (btn) { btn.disabled = false; btn.innerHTML = original; }
  }
}

async function deleteAccount(id) {
  if (!confirm('确定删除此账号？')) return;
  try {
    await api('POST', '/accounts/delete', { accountId: id });
    toast('账号已删除', 'success');
    loadAccounts(); loadStats();
  } catch (e) { toast('删除失败: ' + e.message, 'error'); }
}

async function resetAccount(id, btn) {
  const original = btn ? btn.innerHTML : '';
  if (btn) { btn.disabled = true; btn.innerHTML = '<span class="loading"></span>检测中'; }
  try {
    const d = await api('POST', '/accounts/reset', { accountId: id });
    const r = d.data || {};
    const type = d.success ? 'success' : (r.status === 'cooldown' ? 'warning' : 'error');
    let msg = d.message || '检测完成';
    if (r.remaining && r.status !== 'active') msg += '（剩余 ' + esc(r.remaining) + '）';
    toast(msg, type, 6000);
    loadAccounts(); loadStats();
  } catch (e) {
    toast('检测失败: ' + e.message, 'error');
  } finally {
    if (btn) { btn.disabled = false; btn.innerHTML = original; }
  }
}

async function deleteAllAccounts() {
  if (!confirm('⚠️ 确定删除所有账号？不可撤销！')) return;
  try {
    await api('POST', '/accounts/delete-all', {});
    toast('全部账号已删除', 'success');
    loadAccounts(); loadStats();
  } catch (e) { toast('删除失败: ' + e.message, 'error'); }
}

async function refreshAllTokens() {
  try {
    await api('POST', '/accounts/refresh-all', {});
    toast('全部 Token 已刷新', 'success');
    loadAccounts(); loadStats();
  } catch (e) { toast('刷新失败: ' + e.message, 'error'); }
}

// ========== OAuth 登录 ==========
async function startOAuth() {
  const btn = _('oauthBtn');
  btn.disabled = true;
  btn.innerHTML = '<span class="loading"></span> 启动中...';
  _('oauthProgress').style.display = 'block';
  _('oauthResult').style.display = 'none';
  _('oauthStatus').textContent = '正在连接 WorkOS...';
  try {
    const d = await api('POST', '/oauth/start');
    const s = d.data;
    _('oauthStatus').textContent = '请在浏览器中打开链接并输入代码';
    const u = _('oauthUrl');
    u.textContent = s.verificationUri;
    u.href = s.verificationUri;
    _('oauthUserCode').textContent = s.userCode;
    const poll = setInterval(async () => {
      try {
        const r = await api('GET', '/oauth/status?sessionId=' + s.sessionId);
        if (r.data.done) {
          clearInterval(poll);
          btn.disabled = false;
          btn.innerHTML = '🚀 开始 OAuth 登录';
          if (r.data.success) {
            _('oauthProgress').style.display = 'none';
            _('oauthResult').innerHTML = '<div style="color:var(--accent2);font-weight:600;font-size:14px">✓ 账号添加成功: ' + esc(r.data.email) + '</div>';
            _('oauthResult').style.display = 'block';
            loadAccounts(); loadStats();
            toast('账号添加成功！', 'success');
          } else {
            _('oauthStatus').textContent = '失败: ' + (r.data.error || '未知错误');
            toast('OAuth 失败', 'error');
          }
        }
      } catch(e) {}
    }, 2000);
  } catch (e) {
    btn.disabled = false;
    btn.innerHTML = '🚀 开始 OAuth 登录';
    _('oauthStatus').textContent = '错误: ' + e.message;
    toast('OAuth 失败: ' + e.message, 'error');
  }
}

// ========== Token 导入 ==========
async function addByToken() {
  const token = _('tokenInput').value.trim();
  if (!token) { toast('请输入 refreshToken', 'error'); return; }
  const email = _('tokenEmail').value.trim();
  try {
    const d = await api('POST', '/accounts/add', { refreshToken: token, email: email || undefined });
    toast('账号添加成功: ' + (d.data.email || ''), 'success');
    _('tokenInput').value = '';
    _('tokenEmail').value = '';
    loadAccounts(); loadStats();
  } catch (e) { toast('添加失败: ' + e.message, 'error'); }
}

// ========== 批量导入 ==========
async function batchImport() {
  const raw = _('batchInput').value.trim();
  if (!raw) { toast('请输入账号数据', 'error'); return; }
  let tokens;
  try { tokens = JSON.parse(raw); if (!Array.isArray(tokens)) tokens = [tokens]; }
  catch { tokens = raw.split('\n').filter(t => t.trim()).map(t => ({ refreshToken: t.trim() })); }
  try {
    const d = await api('POST', '/batch-import', { tokens });
    toast(d.message || '导入完成', 'success');
    _('batchInput').value = '';
    loadAccounts(); loadStats();
  } catch (e) { toast('导入失败: ' + e.message, 'error'); }
}

async function handleFileImport(event) {
  const file = event.target.files[0];
  if (!file) return;
  const text = await file.text();
  let tokens;
  try { tokens = JSON.parse(text); if (!Array.isArray(tokens)) tokens = [tokens]; }
  catch { tokens = text.split('\n').filter(t => t.trim()).map(t => ({ refreshToken: t.trim() })); }
  try {
    const d = await api('POST', '/batch-import', { tokens });
    toast(d.message || '导入了 ' + tokens.length + ' 个账号', 'success');
    loadAccounts(); loadStats();
  } catch (e) { toast('导入失败: ' + e.message, 'error'); }
  event.target.value = '';
}

// ========== API 密钥管理 ==========
async function loadKeys() {
  try {
    const d = await api('GET', '/keys');
    const keys = d.data.keys;
    const el = _('keysList');
    if (!keys || keys.length === 0) {
      el.innerHTML = '<div class="empty-state"><span class="icon">🔑</span>暂无 API 密钥</div>';
      return;
    }
    el.innerHTML = keys.map(k =>
      '<div class="flex" style="margin-bottom:8px">' +
        '<span class="key-display" style="flex:1" onclick="copyText(\'' + k + '\')" title="点击复制">' + esc(k) + '</span>' +
        '<button class="btn btn-sm btn-danger" onclick="deleteKey(\'' + k + '\')">✕</button>' +
      '</div>'
    ).join('');
  } catch (e) { _('keysList').innerHTML = '<div class="empty">加载失败</div>'; }
}

async function generateKey() {
  try {
    const d = await api('POST', '/keys/generate');
    const key = d.data.key;
    _('keyGenResult').innerHTML =
      '<div style="background:rgba(52,211,153,.08);border:1px solid rgba(52,211,153,.4);border-radius:10px;padding:12px">' +
        '<div style="color:var(--accent2);font-weight:600;margin-bottom:8px">✓ 新密钥已生成（点击复制）</div>' +
        '<div class="key-display" onclick="copyText(\'' + key + '\')">' + esc(key) + '</div>' +
      '</div>';
    loadKeys();
    toast('密钥已生成', 'success');
    setTimeout(() => _('keyGenResult').innerHTML = '', 8000);
  } catch (e) { toast('生成失败: ' + e.message, 'error'); }
}

async function deleteKey(key) {
  if (!confirm('确定删除此密钥？')) return;
  try {
    await api('POST', '/keys/delete', { key });
    toast('密钥已删除', 'success');
    loadKeys();
  } catch (e) { toast('删除失败: ' + e.message, 'error'); }
}

async function deleteAllKeys() {
  if (!confirm('确定删除所有 API 密钥？')) return;
  try {
    const d = await api('GET', '/keys');
    const keys = d.data.keys || [];
    for (const k of keys) await api('POST', '/keys/delete', { key: k });
    toast('全部密钥已删除', 'success');
    loadKeys();
  } catch (e) { toast('删除失败: ' + e.message, 'error'); }
}

function copyText(t) {
  navigator.clipboard.writeText(t).then(() => toast('已复制到剪贴板', 'success')).catch(() => {
    const ta = document.createElement('textarea');
    ta.value = t; document.body.appendChild(ta); ta.select(); document.execCommand('copy'); document.body.removeChild(ta);
    toast('已复制到剪贴板', 'success');
  });
}

// ========== 请求日志 ==========
const ROUTE_LABEL = { zen: 'opencode', cline: 'cline 池', admin: '管理', meta: '元信息', other: '其他' };
const STATUS_CLASS = s => s >= 500 ? 'color:var(--danger)' : (s >= 400 ? 'color:var(--amber)' : 'color:var(--accent2)');

async function loadLogs() {
  try {
    const d = await api('GET', '/logs');
    const logs = d.data.logs || [];
    const tbody = _('logsTableBody');
    if (!logs.length) { tbody.innerHTML = '<tr><td colspan="8" class="empty">暂无请求记录</td></tr>'; return; }
    tbody.innerHTML = logs.map(l => {
      const t = l.time ? new Date(l.time).toLocaleString('zh-CN') : '-';
      const route = ROUTE_LABEL[l.route] || l.route || '-';
      const st = l.status || 0;
      return '<tr>' +
        '<td class="mono" style="font-size:11px">' + t + '</td>' +
        '<td class="mono" style="font-size:11px">' + esc(l.client || '-') + '</td>' +
        '<td>' + esc(l.method || '-') + '</td>' +
        '<td class="mono" style="font-size:11px">' + esc(l.path || '-') + '</td>' +
        '<td class="mono" style="font-size:12px">' + esc(l.model || '-') + '</td>' +
        '<td><span class="model-tag">' + esc(route) + '</span></td>' +
        '<td style="font-weight:600;color:' + STATUS_CLASS(st) + '">' + st + '</td>' +
        '<td class="mono" style="font-size:11px">' + (l.durationMs != null ? l.durationMs + ' ms' : '-') + '</td>' +
      '</tr>';
    }).join('');
  } catch (e) { tbody.innerHTML = '<tr><td colspan="8" class="empty">加载失败</td></tr>'; }
}

// ========== 导出账号 ==========
async function exportAccounts() {
  try {
    const res = await fetch(API + '/accounts/export');
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = 'cline-accounts-export.json';
    document.body.appendChild(a); a.click(); document.body.removeChild(a);
    URL.revokeObjectURL(url);
    toast('账号已导出（JSON）', 'success');
  } catch (e) { toast('导出失败: ' + e.message, 'error'); }
}

// ========== 配置管理 ==========
async function updateConfig() {
  const strategy = _('settingStrategy').value;
  try {
    await api('POST', '/config/update', { strategy });
    toast('策略已更新为: ' + strategy, 'success');
  } catch (e) { toast('更新失败: ' + e.message, 'error'); }
}

function addHeaderRow() {
  const tbody = _('headersTableBody');
  const tr = document.createElement('tr');
  tr.innerHTML =
    '<td><input type="text" class="header-key" placeholder="Header-Name" style="font-size:12px;font-family:monospace"></td>' +
    '<td><input type="text" class="header-val" placeholder="value" style="font-size:12px;font-family:monospace"></td>' +
    '<td><button class="btn btn-sm btn-danger" onclick="this.closest(\'tr\').remove()">✕</button></td>';
  tbody.appendChild(tr);
}

async function saveHeaders() {
  const tbody = _('headersTableBody');
  const rows = tbody.querySelectorAll('tr');
  const headers = {};
  let hasEmpty = false;
  rows.forEach(tr => {
    const keyInput = tr.querySelector('.header-key');
    const valInput = tr.querySelector('.header-val');
    if (keyInput && valInput) {
      const k = keyInput.value.trim();
      const v = valInput.value.trim();
      if (k) { headers[k] = v; }
      else if (v) { hasEmpty = true; }
    }
  });
  if (hasEmpty) { toast('存在有值无键的行，已忽略', 'info'); }
  try {
    const d = await api('POST', '/config/update', { headers });
    toast('请求头已保存', 'success');
    _('headerSaveResult').innerHTML =
      '<div style="color:var(--accent2);font-size:12px">✓ 已保存 ' + Object.keys(d.data.headers).length + ' 个请求头</div>';
    setTimeout(() => _('headerSaveResult').innerHTML = '', 5000);
    loadConfig();
  } catch (e) { toast('保存失败: ' + e.message, 'error'); }
}

const MODEL_STYLE = {
  active:  { label: '可用', css: 'color:var(--accent2);border:1px solid rgba(52,211,153,.5);background:rgba(52,211,153,.08)' },
  empty:   { label: '响应为空', css: 'color:var(--amber);border:1px solid rgba(245,158,11,.5);background:rgba(245,158,11,.08)' },
  pass:    { label: '需订阅', css: 'color:var(--amber);border:1px solid rgba(245,158,11,.5);background:rgba(245,158,11,.08)' },
  removed: { label: '已下架', css: 'color:var(--text3);border:1px solid var(--border)' },
  error:   { label: '异常', css: 'color:var(--danger);border:1px solid rgba(248,113,113,.5);background:rgba(248,113,113,.08)' },
  unknown: { label: '未探测', css: 'color:var(--text3);border:1px dashed var(--border-strong)' }
};
const COST_LABEL = { free: '免费', pass: '订阅', quota: '消耗额度' };

async function loadModels() {
  try {
    const d = await api('GET', '/models');
    const models = d.data.models || [];
    let info = '';
    if (d.data.lastSync) info += '· 官方清单: ' + new Date(d.data.lastSync).toLocaleTimeString('zh-CN');
    _('modelsProbeInfo').textContent = info;
    if (!models.length) { _('modelsList').innerHTML = '<div class="empty">暂无模型</div>'; return; }
    _('modelsList').innerHTML = models.map(m => {
      const st = MODEL_STYLE[m.status] || MODEL_STYLE.unknown;
      const cost = COST_LABEL[m.cost] || m.cost || '';
      const synced = m.syncedAt ? new Date(m.syncedAt).toLocaleTimeString('zh-CN') : '-';
      return '<div style="display:flex;align-items:center;gap:10px;padding:8px 12px;margin:5px 0;background:rgba(148,163,184,.06);border:1px solid var(--border);border-radius:10px;transition:.15s">' +
        '<span style="font-family:\'JetBrains Mono\',monospace;font-size:13px;flex:1">' + esc(m.id) + '</span>' +
        (m.cost === 'free' ? '<span style="font-size:11px;color:var(--accent2)">不扣费</span>' : '') +
        (cost ? '<span class="model-tag">' + esc(cost) + '</span>' : '') +
        '<span class="model-tag" style="' + st.css + '">' + st.label + '</span>' +
        '<span style="font-size:11px;color:var(--text3);min-width:60px;text-align:right">' + synced + '</span>' +
        '</div>';
    }).join('');
  } catch (e) { _('modelsList').textContent = '加载失败'; }
}

async function refreshModels() {
  try {
    _('modelsProbeInfo').textContent = '· 同步中...';
    const d = await api('POST', '/models/refresh');
    toast(d.message || (d.data && d.data.message) || '同步已开始', 'info');
    setTimeout(loadModels, 3000);
  } catch (e) { toast('刷新失败: ' + e.message, 'error'); _('modelsProbeInfo').textContent = ''; }
}

async function loadModelOptions() {
  try {
    const d = await api('GET', '/models');
    const models = d.data.models || [];
    const sel = _('settingDefModel');
    if (!sel) return;
    sel.innerHTML = models.map(m => {
      const st = MODEL_STYLE[m.status] || MODEL_STYLE.unknown;
      return '<option value="' + esc(m.id) + '">' + esc(m.id) + ' (' + st.label + ')</option>';
    }).join('');
    const c = await api('GET', '/config');
    if (c.data.defaultModel) sel.value = c.data.defaultModel;
    if (!sel.value && models.length) sel.value = models[0].id;
  } catch (e) { /* ignore */ }
}

async function saveDefaultModel() {
  const v = _('settingDefModel').value;
  if (!v) { toast('请选择模型', 'error'); return; }
  try {
    const d = await api('POST', '/config/update', { defaultModel: v });
    toast('默认模型已保存: ' + d.data.defaultModel, 'success');
  } catch (e) { toast('保存失败: ' + e.message, 'error'); }
}

// ========== 配置加载 ==========
async function loadConfig() {
  try {
    const d = await api('GET', '/config');
    const c = d.data;
    if (c.address) _('settingAddr').value = c.address;
    if (c.strategy) _('settingStrategy').value = c.strategy;
    if (c.version) _('settingVersion').value = c.version;
    if (c.poolPath) _('settingPoolPath').value = c.poolPath;
    loadModelOptions();
    if (c.headers) {
      const tbody = _('headersTableBody');
      tbody.innerHTML = Object.entries(c.headers).map(([k, v]) =>
        '<tr>' +
          '<td><input type="text" class="header-key" value="' + esc(k) + '" style="font-size:12px;font-family:monospace;width:100%"></td>' +
          '<td><input type="text" class="header-val" value="' + esc(v) + '" style="font-size:12px;font-family:monospace;width:100%"></td>' +
          '<td><button class="btn btn-sm btn-danger" onclick="this.closest(\'tr\').remove()">✕</button></td>' +
        '</tr>'
      ).join('');
    }
  } catch (e) { /* ignore */ }
}

// ========== opencode 免费模型 ==========
async function loadOcConfig() {
  try {
    const d = await api('GET', '/opencode/config');
    const c = d.data;
    _('ocEnabled').value = String(c.enabled);
    _('ocKey').value = c.key || 'public';
    _('ocBaseURL').value = c.baseURL || '';
    _('ocProxies').value = (c.proxies || []).join('\n');
    _('ocStrategy').value = c.proxyStrategy || 'round_robin';
    _('ocMaxConc').value = c.maxConcurrency || 8;
    _('ocRetries').value = c.retries || 3;
    _('ocFailover').value = String(c.failover);
    _('ocFailoverCount').value = c.failoverCount || 3;
    _('ocFailoverMinutes').value = c.failoverMinutes || 5;
    _('ocCompactAuto').value = String(c.compaction ? c.compaction.auto : true);
    _('ocCompactBuffer').value = c.compaction ? c.compaction.buffer : 20000;
    _('ocKeepTokens').value = c.compaction ? c.compaction.keepTokens : 8000;
    _('ocSummaryModel').value = c.compaction ? (c.compaction.summaryModel || '') : '';
    _('ocMaxSummary').value = c.compaction ? c.compaction.maxSummary : 4096;
    const rt = c.runtime || {};
    _('ocFailoverInfo').innerHTML = rt.failoverActive
      ? '<span style="color:var(--danger)">🔴 故障转移中 (opencode 不可用, 请求走 cline 池)</span>'
      : '<span style="color:var(--accent2)">🟢 正常</span>';
    const cd = rt.proxyCooldowns || {};
    const keys = Object.keys(cd);
    _('ocCooldownInfo').textContent = keys.length
      ? keys.map(k => k + ' 冷却至 ' + cd[k]).join('; ')
      : '暂无冷却中的代理';
  } catch (e) { /* ignore */ }
}

async function saveOcConfig() {
  const proxies = _('ocProxies').value.split('\n').map(s => s.trim()).filter(Boolean);
  const PROXY_RE = /^(https?|socks5h?):\/\/[^\s]+:\d+/;
  const bad = proxies.find(p => !PROXY_RE.test(p));
  if (bad) { toast('代理格式无效: ' + bad + '（需 http(s)://host:port 或 socks5://host:port）', 'error'); return; }
  const body = {
    enabled: _('ocEnabled').value === 'true',
    key: _('ocKey').value.trim(),
    baseURL: _('ocBaseURL').value.trim(),
    proxies: proxies,
    proxyStrategy: _('ocStrategy').value,
    maxConcurrency: parseInt(_('ocMaxConc').value) || 8,
    retries: parseInt(_('ocRetries').value) || 3,
    failover: _('ocFailover').value === 'true',
    failoverCount: parseInt(_('ocFailoverCount').value) || 3,
    failoverMinutes: parseInt(_('ocFailoverMinutes').value) || 5,
    compaction: {
      auto: _('ocCompactAuto').value === 'true',
      buffer: parseInt(_('ocCompactBuffer').value) || 20000,
      keepTokens: parseInt(_('ocKeepTokens').value) || 8000,
      summaryModel: _('ocSummaryModel').value.trim(),
      maxSummary: parseInt(_('ocMaxSummary').value) || 4096
    }
  };
  try {
    const d = await api('POST', '/opencode/config/update', body);
    toast('opencode 配置已保存', 'success');
    loadOcConfig();
  } catch (e) { toast('保存失败: ' + e.message, 'error'); }
}

async function loadOcModels() {
  try {
    const d = await api('GET', '/opencode/models');
    const models = d.data.models || [];
    _('ocModelsList').innerHTML = '<div class="table-wrap"><table><thead><tr><th style="text-align:left">模型 ID</th><th>上下文</th><th>输出</th><th>来源</th></tr></thead><tbody>' +
      models.map(m => '<tr><td style="text-align:left;font-family:monospace">' + esc(m.id) + '</td><td>' + m.context + '</td><td>' + m.output + '</td><td>' + m.source + '</td></tr>').join('') +
      '</tbody></table></div><div class="hint">共 ' + models.length + ' 个免费模型（每 10 分钟自动同步）</div>';
  } catch (e) { _('ocModelsList').textContent = '加载失败'; }
}

async function refreshOcModels() {
  try {
    const d = await api('POST', '/opencode/models/refresh');
    toast(d.message || '同步完成', 'success');
    loadOcModels();
  } catch (e) { toast('同步失败: ' + e.message, 'error'); }
}

async function loadOcStats() {
  try {
    const d = await api('GET', '/opencode/stats');
    const t = d.data.today || {}, s = d.data.total || {};
    _('ocStatsBox').innerHTML = '<table><thead><tr><th style="text-align:left"></th><th>请求数</th><th>输入 tokens</th><th>输出 tokens</th><th>压缩消耗</th><th>限流命中</th></tr></thead><tbody>' +
      '<tr><td style="text-align:left">今日</td><td>' + (t.requests || 0) + '</td><td>' + (t.promptTokens || 0) + '</td><td>' + (t.completionTokens || 0) + '</td><td>' + (t.compaction || 0) + '</td><td>' + (t.rateLimited || 0) + '</td></tr>' +
      '<tr><td style="text-align:left">累计</td><td>' + (s.requests || 0) + '</td><td>' + (s.promptTokens || 0) + '</td><td>' + (s.completionTokens || 0) + '</td><td>' + (s.compaction || 0) + '</td><td>' + (s.rateLimited || 0) + '</td></tr>' +
      '</tbody></table>';
    const bm = t.byModel || {};
    const rows = Object.keys(bm).map(k => '<tr><td style="text-align:left;font-family:monospace">' + esc(k) + '</td><td>' + bm[k].requests + '</td><td>' + bm[k].promptTokens + '</td><td>' + bm[k].completionTokens + '</td></tr>').join('');
    _('ocModelStatsBox').innerHTML = '<div style="font-size:13px;font-weight:600;margin-bottom:6px">按模型分布（今日）</div>' +
      '<div class="table-wrap"><table><thead><tr><th style="text-align:left">模型</th><th>请求数</th><th>输入 tokens</th><th>输出 tokens</th></tr></thead><tbody>' +
      (rows || '<tr><td colspan="4" style="text-align:left;color:var(--text2)">暂无数据</td></tr>') + '</tbody></table></div>';
  } catch (e) { /* ignore */ }
}

// ========== 初始化 ==========
loadStats();
loadAccounts();
loadKeys();
loadModels();
loadConfig();
setInterval(() => { loadStats(); }, 10000);
setInterval(() => { loadOcStats(); }, 15000);
setInterval(() => { if (_('tab-logs').style.display !== 'none') loadLogs(); }, 8000);

// 点击模态框外部 / 按 ESC 关闭
document.addEventListener('click', function(e) {
  if (e.target === _('channelModal')) closeChannelModal();
  if (e.target === _('groupModal')) closeGroupModal();
});
document.addEventListener('keydown', function(e) {
  if (e.key === 'Escape') {
    const cm = _('channelModal'), gm = _('groupModal');
    if (cm && cm.style.display !== 'none') closeChannelModal();
    if (gm && gm.style.display !== 'none') closeGroupModal();
  }
});

// ========== M6 WP6.1: 渠道组管理面板 ==========
let channelsCache = [];
let groupsCache = [];

// ----- Channels Panel -----
async function loadChannels() {
  try {
    const d = await api('GET', '/channels');
    const snap = d.data || {};
    channelsCache = snap.channels || [];
    groupsCache = snap.groups || [];
    renderChannelsTable();
    renderGroupsTable();
    if (_('channelCount')) _('channelCount').textContent = channelsCache.length;
    if (_('groupCount')) _('groupCount').textContent = groupsCache.length;
  } catch (e) { toast('加载渠道组失败: ' + e.message, 'error'); }
}

function renderChannelsTable() {
  const tbody = _('channelsTableBody');
  if (!tbody) return;
  if (!channelsCache.length) {
    tbody.innerHTML = '<tr><td colspan=8 class="empty">暂无渠道，点击下方"添加渠道"按钮</td></tr>';
    return;
  }
  tbody.innerHTML = channelsCache.map(ch => {
    const statusClass = ch.disabled ? 'expired' : 'active';
    const statusLabel = ch.disabled ? '已禁用' : '启用中';
    return '<tr>' +
      '<td>' + esc(ch.id) + '</td>' +
      '<td><span class="model-tag">' + esc(ch.provider) + '</span></td>' +
      '<td><span class="model-tag" style="' + (ch.disabled ? 'border:1px solid var(--border)' : 'background:rgba(52,211,153,.08);border:1px solid rgba(52,211,153,.5);color:var(--accent2)') + '">' + statusLabel + '</span></td>' +
      '<td class="mono" style="font-size:11px">' + (ch.baseURL ? esc(ch.baseURL) : '-') + '</td>' +
      '<td>' + (ch.weight || 1) + '</td>' +
      '<td style="white-space:nowrap">' +
        '<button class="btn btn-sm" onclick="probeChannel(\'' + esc(ch.id) + '\', this)" title="探测余额/健康"><span title="探测">🔍</span></button> ' +
        '<button class="btn btn-sm" onclick="editChannel(\'' + esc(ch.id) + '\')" title="编辑">✎</button> ' +
        '<button class="btn btn-sm btn-danger" onclick="deleteChannel(\'' + esc(ch.id) + '\')" title="删除">✕</button>' +
      '</td>' +
    '</tr>';
  }).join('');
}

async function probeChannel(id, btn) {
  const ch = channelsCache.find(c => c.id === id);
  if (!ch) { toast('渠道不存在', 'error'); return; }
  const orig = btn.innerHTML;
  btn.disabled = true; btn.innerHTML = '<span class="loading"></span>';
  try {
    const d = await api('POST', '/channels/probe', {
      provider: ch.provider,
      api_key: ch.apiKey || '',
      base_url: ch.baseURL || ''
    });
    const bal = d.data || {};
    let msg = '渠道 ' + esc(ch.id) + ': ';
    if (bal.total != null) msg += '$' + parseFloat(bal.total).toFixed(2);
    if (bal.currency) msg += ' (' + esc(bal.currency) + ')';
    toast(msg, 'success', 5000);
  } catch (e) {
    toast('探测失败: ' + e.message, 'error');
  } finally {
    btn.disabled = false; btn.innerHTML = orig;
  }
}

async function deleteChannel(id) {
  if (!confirm('确定删除渠道 "' + id + '"？此操作不可撤销！')) return;
  try {
    await api('POST', '/channels/delete', { id });
    toast('渠道已删除', 'success');
    loadChannels();
  } catch (e) { toast('删除失败: ' + e.message, 'error'); }
}

function editChannel(id) {
  const ch = channelsCache.find(c => c.id === id);
  if (!ch) { toast('渠道不存在', 'error'); return; }
  showChannelModal(ch);
}

function showChannelModal(ch = {}) {
  const modal = _('channelModal');
  if (!modal) { alert('模态框模板缺失，请检查 index.html'); return; }
  _('chId').value = ch.id || '';
  _('chProvider').value = ch.provider || '';
  _('chBaseURL').value = ch.baseURL || '';
  _('chAPIKey').value = ch.apiKey || '';
  _('chWeight').value = ch.weight || 1;
  _('chDisabled').checked = !!ch.disabled;
  modal.style.display = 'flex';
  const isNew = !ch.id;
  _('chModalTitle').textContent = isNew ? '➕ 添加渠道' : '✎ 编辑渠道';
  _('chSubmitBtn').textContent = isNew ? '添加' : '保存';
}

function closeChannelModal() {
  _('channelModal').style.display = 'none';
  _('chId').value = '';
  _('chProvider').value = '';
  _('chBaseURL').value = '';
  _('chAPIKey').value = '';
  _('chWeight').value = 1;
  _('chDisabled').checked = false;
}

async function submitChannel() {
  const body = {
    id: _('chId').value.trim(),
    provider: _('chProvider').value.trim(),
    baseURL: _('chBaseURL').value.trim(),
    apiKey: _('chAPIKey').value.trim(),
    weight: parseInt(_('chWeight').value) || 1,
    disabled: _('chDisabled').checked
  };
  if (!body.id || !body.provider) { toast('ID 和 Provider 必填', 'error'); return; }
  try {
    await api('POST', '/channels/upsert', body);
    toast(body.id ? '渠道已更新' : '渠道已添加', 'success');
    closeChannelModal();
    loadChannels();
  } catch (e) { toast('保存失败: ' + e.message, 'error'); }
}

// ----- Groups Panel -----
function renderGroupsTable() {
  const tbody = _('groupsTableBody');
  if (!tbody) return;
  if (!groupsCache.length) {
    tbody.innerHTML = '<tr><td colspan=6 class="empty">暂无模型组，点击下方"添加组"按钮</td></tr>';
    return;
  }
  tbody.innerHTML = groupsCache.map(grp => {
    const strategyLabels = { round_robin: '轮询', weighted: '加权', least_used: '最少使用', lowest_latency: '最低延迟', auto: 'Auto' };
    return '<tr>' +
      '<td>' + esc(grp.name) + '</td>' +
      '<td><span class="stat-mini">' + grp.members.length + ' 个成员</span></td>' +
      '<td><span class="model-tag">' + (strategyLabels[grp.strategy] || grp.strategy) + '</span></td>' +
      '<td class="mono" style="font-size:10px">' + grp.members.map(esc).join(', ') + '</td>' +
      '<td style="white-space:nowrap">' +
        '<button class="btn btn-sm" onclick="editGroup(\'' + esc(grp.name) + '\')" title="编辑">✎</button> ' +
        '<button class="btn btn-sm btn-danger" onclick="deleteGroup(\'' + esc(grp.name) + '\')" title="删除">✕</button>' +
      '</td>' +
    '</tr>';
  }).join('');
}

async function deleteGroup(name) {
  if (!confirm('确定删除模型组 "' + name + '"？')) return;
  try {
    await api('POST', '/groups/delete', { name });
    toast('组已删除', 'success');
    loadChannels();
  } catch (e) { toast('删除失败: ' + e.message, 'error'); }
}

function editGroup(name) {
  const grp = groupsCache.find(g => g.name === name);
  if (!grp) { toast('组不存在', 'error'); return; }
  showGroupModal(grp);
}

function showGroupModal(grp = {}) {
  const modal = _('groupModal');
  if (!modal) { alert('模态框模板缺失'); return; }
  _('grpName').value = grp.name || '';
  _('grpMembers').value = (grp.members || []).join('\n');
  _('grpStrategy').value = grp.strategy || 'round_robin';
  modal.style.display = 'flex';
  const isNew = !grp.name;
  _('grpModalTitle').textContent = isNew ? '➕ 添加模型组' : '✎ 编辑模型组';
  _('grpSubmitBtn').textContent = isNew ? '添加' : '保存';
  _('grpName').disabled = !isNew; // 组名不可改
}

function closeGroupModal() {
  _('groupModal').style.display = 'none';
  _('grpName').value = '';
  _('grpMembers').value = '';
  _('grpStrategy').value = 'round_robin';
  _('grpName').disabled = false;
}

async function submitGroup() {
  const name = _('grpName').value.trim();
  const members = _('grpMembers').value.split('\n').map(s => s.trim()).filter(Boolean);
  const strategy = _('grpStrategy').value;
  if (!name) { toast('组名必填', 'error'); return; }
  if (members.length === 0) { toast('至少需要一个成员', 'error'); return; }
  const body = { name, members, strategy };
  try {
    await api('POST', '/groups/upsert', body);
    toast(name ? '组已更新' : '组已添加', 'success');
    closeGroupModal();
    loadChannels();
  } catch (e) { toast('保存失败: ' + e.message, 'error'); }
}

// ========== M6 WP6.2: Auto 路由面板（只读视图；启用方式=组的策略设为 auto） ==========
async function loadAutoConfig() {
  try {
    const d = await api('GET', '/channels');
    const snap = d.data || {};
    channelsCache = snap.channels || [];
    renderAutoCandidates();
    renderAutoGroups(snap.groups || []);
  } catch (e) {
    _('autoCandidatesBody').innerHTML = '<tr><td colspan="5" class="empty">加载失败: ' + esc(e.message) + '</td></tr>';
  }
}

function capsLabels(caps) {
  if (!caps) return [];
  const out = [];
  if (caps.tier) out.push('Tier ' + caps.tier);
  if (caps.vision) out.push('视觉');
  if (caps.tool_call) out.push('工具调用');
  if (caps.reasoning) out.push('推理');
  if (caps.context_window) out.push(caps.context_window + ' ctx');
  if (caps.cost_per_1m_in) out.push('$' + caps.cost_per_1m_in + '/1M in');
  return out;
}

function renderAutoCandidates() {
  const tbody = _('autoCandidatesBody');
  if (!tbody) return;
  if (!channelsCache.length) {
    tbody.innerHTML = '<tr><td colspan="5" class="empty">暂无渠道，请先在「渠道组」添加</td></tr>';
    return;
  }
  tbody.innerHTML = channelsCache.map(ch => {
    const labels = capsLabels(ch.caps);
    const capTxt = labels.length
      ? labels.map(l => '<span class="model-tag">' + esc(l) + '</span>').join(' ')
      : '<span class="probe-pill">未标注（按「能力未知但可用」处理）</span>';
    const statusClass = ch.disabled ? 'expired' : 'active';
    const statusTxt = ch.disabled ? '禁用' : '就绪';
    return '<tr>' +
      '<td>' + esc(ch.id) + '</td>' +
      '<td><span class="model-tag">' + esc(ch.provider) + '</span></td>' +
      '<td style="white-space:normal">' + capTxt + '</td>' +
      '<td>' + (ch.weight || 1) + '</td>' +
      '<td><span class="status ' + statusClass + '"><span class="status-dot ' + statusClass + '"></span>' + statusTxt + '</span></td>' +
    '</tr>';
  }).join('');
}

function renderAutoGroups(groups) {
  const tbody = _('autoGroupsBody');
  if (!tbody) return;
  const auto = groups.filter(g => g.strategy === 'auto');
  if (!auto.length) {
    tbody.innerHTML = '<tr><td colspan="3" class="empty">暂无 auto 组——在「渠道组」中新建组并选择策略 <b>auto</b> 即可启用</td></tr>';
    return;
  }
  tbody.innerHTML = auto.map(g =>
    '<tr>' +
      '<td>' + esc(g.name) + '</td>' +
      '<td><span class="model-tag free">auto</span></td>' +
      '<td class="mono" style="font-size:11px;white-space:normal">' + (g.members || []).map(esc).join(', ') + '</td>' +
    '</tr>'
  ).join('');
}

// ========== M6 WP6.3: 余额探测面板（按需真实探测；自动探测=浏览器本地定时器） ==========
const balanceCache = new Map(); // channelId -> {text, at}
let balTimerHandle = null;

function fmtBalance(r) {
  if (!r || r.total == null) return '—';
  const cur = r.currency ? ' ' + r.currency : '';
  return '$' + Number(r.total).toFixed(2) + cur;
}

async function loadBalanceConfig() {
  try {
    const d = await api('GET', '/channels');
    const snap = d.data || {};
    channelsCache = snap.channels || [];
    const sel = _('balTimer');
    if (sel) {
      sel.value = localStorage.getItem('balance_auto_min') || '0';
      sel.onchange = applyBalTimer;
    }
    applyBalTimer();
    renderBalanceOverview();
  } catch (e) {
    _('balanceContent').innerHTML = '<tr><td colspan="6" class="empty">加载失败: ' + esc(e.message) + '</td></tr>';
  }
}

function applyBalTimer() {
  if (balTimerHandle) { clearInterval(balTimerHandle); balTimerHandle = null; }
  const sel = _('balTimer');
  if (!sel) return;
  const min = parseInt(sel.value) || 0;
  localStorage.setItem('balance_auto_min', String(min));
  if (min > 0) {
    balTimerHandle = setInterval(() => { runBalanceProbe(null, true); }, min * 60000);
  }
}

function renderBalanceOverview() {
  const tbody = _('balanceContent');
  if (!tbody) return;
  if (!channelsCache.length) {
    tbody.innerHTML = '<tr><td colspan="6" class="empty">暂无渠道，请先在「渠道组」添加</td></tr>';
    return;
  }
  tbody.innerHTML = channelsCache.map(ch => {
    const c = balanceCache.get(ch.id);
    const statusCls = ch.disabled ? 'expired' : 'active';
    const statusTxt = ch.disabled ? '禁用' : '就绪';
    return '<tr>' +
      '<td>' + esc(ch.id) + '</td>' +
      '<td><span class="model-tag">' + esc(ch.provider) + '</span></td>' +
      '<td><span class="status ' + statusCls + '"><span class="status-dot ' + statusCls + '"></span>' + statusTxt + '</span></td>' +
      '<td class="mono">' + (c ? c.text : '—') + '</td>' +
      '<td class="mono" style="font-size:11px">' + (c ? c.at : '—') + '</td>' +
      '<td><button class="btn btn-sm" onclick="probeOne(\'' + esc(ch.id) + '\', this)">🔍 探测</button></td>' +
    '</tr>';
  }).join('');
}

async function probeOne(id, btn) {
  const ch = channelsCache.find(c => c.id === id);
  if (!ch) return;
  const orig = btn ? btn.innerHTML : '';
  if (btn) { btn.disabled = true; btn.innerHTML = '<span class="loading"></span>'; }
  try {
    const d = await api('POST', '/channels/probe', { provider: ch.provider, api_key: ch.apiKey || '', base_url: ch.baseURL || '' });
    balanceCache.set(id, { text: fmtBalance(d.data), at: new Date().toLocaleTimeString('zh-CN') });
    toast('渠道 ' + id + ' 余额: ' + fmtBalance(d.data), 'success');
  } catch (e) {
    balanceCache.set(id, { text: '探测失败', at: new Date().toLocaleTimeString('zh-CN') });
    toast('渠道 ' + id + ' 探测失败: ' + e.message, 'error');
  } finally {
    if (btn) { btn.disabled = false; btn.innerHTML = orig; }
    renderBalanceOverview();
  }
}

async function runBalanceProbe(btn, silent) {
  const orig = btn ? btn.innerHTML : '';
  if (btn) { btn.disabled = true; btn.innerHTML = '<span class="loading"></span>探测中'; }
  let ok = 0, fail = 0;
  for (const ch of channelsCache) {
    if (ch.disabled) continue;
    try {
      const d = await api('POST', '/channels/probe', { provider: ch.provider, api_key: ch.apiKey || '', base_url: ch.baseURL || '' });
      balanceCache.set(ch.id, { text: fmtBalance(d.data), at: new Date().toLocaleTimeString('zh-CN') });
      ok++;
    } catch (e) {
      balanceCache.set(ch.id, { text: '探测失败', at: new Date().toLocaleTimeString('zh-CN') });
      fail++;
    }
  }
  renderBalanceOverview();
  if (btn) { btn.disabled = false; btn.innerHTML = orig; }
  if (!silent) toast('探测完成：' + ok + ' 成功 / ' + fail + ' 失败', ok && !fail ? 'success' : 'info', 5000);
}
