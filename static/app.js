(function() {
  'use strict';

  let ws = null;
  let term = null;
  let fitAddon = null;
  let currentPath = '';
  let reconnectTimer = null;

  // Tab Switching
  document.querySelectorAll('.tab').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.tab').forEach(b => b.classList.remove('active'));
      document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
      btn.classList.add('active');
      document.getElementById(btn.dataset.tab).classList.add('active');
      if (btn.dataset.tab === 'tab-terminal' && fitAddon) fitAddon.fit();
    });
  });

  // Terminal
  function initTerminal() {
    term = new Terminal({
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, monospace',
      theme: { background: '#1a1a2e' },
      cursorBlink: true,
      allowProposedApi: true,
    });
    fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(new WebLinksAddon.WebLinksAddon());
    term.open(document.getElementById('terminal-container'));
    fitAddon.fit();

    term.onData(data => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(data));
      }
    });

    window.addEventListener('resize', () => fitAddon.fit());
    term.onResize(({ rows, cols }) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', rows, cols }));
      }
    });
  }

  function connectWS() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + '/ws/term');
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      setStatus(true);
      if (fitAddon) {
        fitAddon.fit();
        ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }));
      }
    };

    ws.onmessage = (evt) => {
      const data = typeof evt.data === 'string' ? evt.data : new TextDecoder().decode(evt.data);
      term.write(data);
    };

    ws.onclose = () => {
      setStatus(false);
      reconnectTimer = setTimeout(connectWS, 3000);
    };

    ws.onerror = () => ws.close();
  }

  function setStatus(connected) {
    document.getElementById('status-dot').className = 'dot ' + (connected ? 'connected' : 'disconnected');
    document.getElementById('status-text').textContent = connected ? 'Connected' : 'Reconnecting...';
  }

  // File Browser
  async function loadDir(path) {
    try {
      const resp = await fetch('/api/files?path=' + encodeURIComponent(path));
      if (resp.status === 401) { location.reload(); return; }
      const data = await resp.json();
      if (data.error) { alert(data.error); return; }
      currentPath = data.path;
      renderBreadcrumb(data.path);
      renderFiles(data.entries, data.path);
    } catch (e) {
      console.error('loadDir error:', e);
    }
  }

  function renderBreadcrumb(path) {
    const el = document.getElementById('breadcrumb');
    const parts = path.split('/').filter(Boolean);
    el.innerHTML = '';
    let accumulated = '/';
    parts.forEach((part, i) => {
      const span = document.createElement('span');
      span.textContent = part;
      accumulated += part + '/';
      const p = accumulated;
      span.addEventListener('click', () => loadDir(p));
      el.appendChild(span);
    });
  }

  function renderFiles(entries, parentPath) {
    const el = document.getElementById('file-list');
    el.innerHTML = '';
    if (!entries) return;
    entries.sort((a, b) => {
      if (a.type !== b.type) return a.type === 'dir' ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
    entries.forEach(entry => {
      const div = document.createElement('div');
      div.className = 'file-entry';
      const fullPath = parentPath.replace(/\/$/, '') + '/' + entry.name;
      div.innerHTML =
        '<span class="file-icon">' + (entry.type === 'dir' ? '📁' : '📄') + '</span>' +
        '<div class="file-info">' +
        '<div class="file-name">' + entry.name + '</div>' +
        '<div class="file-meta">' + (entry.type === 'file' ? formatSize(entry.size) : '') + '</div>' +
        '</div>';
      div.addEventListener('click', () => {
        if (entry.type === 'dir') loadDir(fullPath);
        else openFile(fullPath, entry.name);
      });
      el.appendChild(div);
    });
  }

  async function openFile(path, name) {
    try {
      const resp = await fetch('/api/files/read?path=' + encodeURIComponent(path));
      const data = await resp.json();
      if (data.error) { alert(data.error); return; }
      document.getElementById('modal-filename').textContent = name;
      document.getElementById('file-content').textContent = data.content;
      document.getElementById('file-modal').classList.remove('hidden');

      document.getElementById('open-in-claude').onclick = () => {
        document.querySelector('[data-tab="tab-terminal"]').click();
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(new TextEncoder().encode(path + '\n'));
        }
        document.getElementById('file-modal').classList.add('hidden');
      };
    } catch (e) {
      console.error('openFile error:', e);
    }
  }

  document.getElementById('modal-close').addEventListener('click', () => {
    document.getElementById('file-modal').classList.add('hidden');
  });

  function formatSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / 1048576).toFixed(1) + ' MB';
  }

  // Init
  initTerminal();
  connectWS();
  loadDir('');
})();
