(function() {
  'use strict';

  let ws = null;
  let term = null;
  let reconnectTimer = null;
  let selectedDir = '';
  let isComposing = false;
  let syncTimer = null;
  let termCols = 50; // will be calculated from screen width

  // --- Handoff URL param detection ---
  const urlParams = new URLSearchParams(location.search);
  const handoffDir = urlParams.get('dir');
  const handoffMode = urlParams.get('mode');

  const quickDirs = [
    { name: 'Desktop', icon: '🖥️' },
    { name: 'Downloads', icon: '📥' },
    { name: 'Documents', icon: '📂' }
  ];

  function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }

  function sendRaw(data) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(new TextEncoder().encode(data));
    }
  }

  function showScreen(id) {
    document.querySelectorAll('.screen').forEach(s => s.classList.remove('active'));
    document.getElementById(id).classList.add('active');
  }

  // Calculate terminal cols from screen width
  function calcCols() {
    const scrollEl = document.getElementById('output-scroll');
    if (!scrollEl) return 50;
    // Measure actual monospace char width
    const measure = document.createElement('span');
    measure.style.cssText = 'font-family:Menlo,Monaco,Courier New,monospace;font-size:13px;position:absolute;visibility:hidden;';
    measure.textContent = 'M';
    document.body.appendChild(measure);
    const charW = measure.getBoundingClientRect().width;
    document.body.removeChild(measure);
    const padding = 24; // 12px padding on each side
    const availableWidth = (scrollEl.clientWidth || window.innerWidth) - padding;
    return Math.max(30, Math.floor(availableWidth / charW));
  }

  // --- Folder Picker ---
  function initPicker() {
    const container = document.getElementById('quick-dirs');
    quickDirs.forEach(d => {
      const btn = document.createElement('button');
      btn.className = 'quick-btn';
      btn.textContent = d.icon + ' ' + d.name;
      btn.addEventListener('click', () => browseDir('~/' + d.name));
      container.appendChild(btn);
    });
    browseDir('~/Desktop');
  }

  async function browseDir(path) {
    try {
      const resp = await fetch('/api/files?path=' + encodeURIComponent(path));
      if (resp.status === 401) { location.reload(); return; }
      const data = await resp.json();
      if (data.error) { alert(data.error); return; }
      selectedDir = data.path;
      document.getElementById('selected-dir').textContent = data.path;
      document.getElementById('start-bar').classList.remove('hidden');
      renderBreadcrumb(data.path);
      renderDirList(data.entries || [], data.path);
    } catch (e) {
      console.error('browseDir error:', e);
    }
  }

  function renderBreadcrumb(path) {
    const el = document.getElementById('dir-breadcrumb');
    const parts = path.split('/').filter(Boolean);
    el.innerHTML = '';
    let accumulated = '/';
    parts.forEach((part) => {
      const span = document.createElement('span');
      span.textContent = part;
      accumulated += part + '/';
      const p = accumulated;
      span.addEventListener('click', () => browseDir(p));
      el.appendChild(span);
    });
  }

  function renderDirList(entries, parentPath) {
    const el = document.getElementById('dir-list');
    el.innerHTML = '';
    const dirs = (entries || []).filter(e => e.type === 'dir');
    dirs.sort((a, b) => a.name.localeCompare(b.name));

    if (dirs.length === 0) {
      const empty = document.createElement('div');
      empty.style.cssText = 'padding: 20px 12px; color: #666; text-align: center;';
      empty.textContent = 'Không có thư mục con';
      el.appendChild(empty);
      return;
    }

    dirs.forEach(entry => {
      const div = document.createElement('div');
      div.className = 'dir-entry';
      const fullPath = parentPath.replace(/\/$/, '') + '/' + entry.name;
      div.innerHTML = '<span class="dir-icon">📁</span><span class="dir-name">' + escapeHtml(entry.name) + '</span>';
      div.addEventListener('click', () => browseDir(fullPath));
      el.appendChild(div);
    });
  }

  // --- Start Claude ---
  document.getElementById('btn-start').addEventListener('click', async () => {
    if (!selectedDir) return;
    const btn = document.getElementById('btn-start');
    btn.textContent = 'Đang khởi động...';
    btn.disabled = true;

    try {
      const resp = await fetch('/api/claude/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dir: selectedDir })
      });
      const data = await resp.json();
      if (data.error) {
        alert('Lỗi: ' + data.error);
        btn.textContent = 'Bắt đầu Claude';
        btn.disabled = false;
        return;
      }
      showScreen('screen-chat');
      document.getElementById('chat-dir').textContent = selectedDir;
      // Clear old output
      document.getElementById('output-text').innerHTML = '';
      initTerminal();
      connectWS();
    } catch (e) {
      alert('Lỗi kết nối: ' + e.message);
      btn.textContent = 'Bắt đầu Claude';
      btn.disabled = false;
    }
  });

  // --- Back button ---
  document.getElementById('btn-back').addEventListener('click', () => {
    if (confirm('Quay lại sẽ dừng phiên Claude hiện tại. Tiếp tục?')) {
      cleanup();
      showScreen('screen-picker');
      document.getElementById('btn-start').textContent = 'Bắt đầu Claude';
      document.getElementById('btn-start').disabled = false;
    }
  });

  function cleanup() {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    if (ws) { ws.onclose = null; ws.close(); ws = null; }
    if (term) { term.dispose(); term = null; }
    document.getElementById('output-text').innerHTML = '';
  }

  // --- Terminal (hidden xterm for escape sequence processing) ---
  function initTerminal() {
    if (term) { term.dispose(); }
    termCols = calcCols();
    term = new Terminal({
      cols: termCols,
      rows: 50,
      scrollback: 10000,
    });
    term.open(document.getElementById('xterm-hidden'));
  }

  // ANSI color map
  const ANSI_COLORS = [
    '#000','#c00','#0a0','#a50','#00a','#a0a','#0aa','#ccc', // 0-7
    '#555','#f55','#5f5','#ff5','#55f','#f5f','#5ff','#fff', // 8-15
  ];

  // Extract colored HTML from xterm buffer
  function getCellStyle(cell) {
    const fg = cell.getFgColor();
    const bg = cell.getBgColor();
    const fgMode = cell.getFgColorMode();
    const bgMode = cell.getBgColorMode();
    let s = [];
    if (fgMode === 1 && fg >= 0 && fg < 16) s.push('color:' + ANSI_COLORS[fg]);
    else if (fgMode === 2) s.push('color:#' + fg.toString(16).padStart(6, '0'));
    if (bgMode === 1 && bg >= 0 && bg < 16) s.push('background:' + ANSI_COLORS[bg]);
    else if (bgMode === 2) s.push('background:#' + bg.toString(16).padStart(6, '0'));
    if (cell.isBold()) s.push('font-weight:bold');
    if (cell.isItalic()) s.push('font-style:italic');
    if (cell.isUnderline()) s.push('text-decoration:underline');
    if (cell.isDim()) s.push('opacity:0.6');
    return s.length > 0 ? s.join(';') : '';
  }

  function serializeBuffer() {
    if (!term) return '';
    const buf = term.buffer.active;
    const totalLines = buf.baseY + buf.cursorY + 1;
    let html = '';

    for (let i = 0; i < totalLines; i++) {
      const line = buf.getLine(i);
      if (!line) { html += '\n'; continue; }

      if (!line.isWrapped && i > 0) html += '\n';

      // Use translateToString to get correct text (with spaces)
      const text = line.translateToString(true);
      if (text.length === 0) continue;

      // Map text chars to cell styles
      let cellIdx = 0;
      for (let ci = 0; ci < text.length; ci++) {
        const ch = text[ci];
        // Find the cell for this character
        let style = '';
        if (cellIdx < line.length) {
          const cell = line.getCell(cellIdx);
          if (cell) {
            style = getCellStyle(cell);
            cellIdx++;
            // Skip width-0 cells (second cell of wide char)
            while (cellIdx < line.length) {
              const next = line.getCell(cellIdx);
              if (!next || next.getWidth() !== 0) break;
              cellIdx++;
            }
          }
        }
        const escaped = escapeHtml(ch);
        if (style) {
          html += '<span style="' + style + '">' + escaped + '</span>';
        } else {
          html += escaped;
        }
      }
    }
    return html;
  }

  // Sync xterm buffer to visible native-scroll div
  function syncOutput() {
    if (syncTimer) return;
    syncTimer = requestAnimationFrame(() => {
      syncTimer = null;
      if (!term) return;
      const outputEl = document.getElementById('output-text');
      const scrollEl = document.getElementById('output-scroll');
      const wasNearBottom = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight < 80;

      outputEl.innerHTML = serializeBuffer();

      if (wasNearBottom) {
        scrollEl.scrollTop = scrollEl.scrollHeight;
      }
    });
  }

  // --- WebSocket ---
  function connectWS() {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + '/ws/term');
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      setStatus(true);
      termCols = calcCols();
      if (term) {
        term.resize(termCols, 50);
      }
      ws.send(JSON.stringify({ type: 'resize', rows: 50, cols: termCols }));
    };

    ws.onmessage = (evt) => {
      if (!term) return;
      const data = typeof evt.data === 'string' ? evt.data : new TextDecoder().decode(evt.data);
      term.write(data);
      syncOutput();
    };

    ws.onclose = () => {
      setStatus(false);
      reconnectTimer = setTimeout(connectWS, 3000);
    };

    ws.onerror = () => { if (ws) ws.close(); };
  }

  function setStatus(connected) {
    document.getElementById('status-dot').className = 'dot ' + (connected ? 'connected' : 'disconnected');
    document.getElementById('status-text').textContent = connected ? 'Đã kết nối' : 'Đang kết nối lại...';
  }

  // Handle resize
  window.addEventListener('resize', () => {
    const newCols = calcCols();
    if (newCols !== termCols && term) {
      termCols = newCols;
      term.resize(termCols, 50);
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', rows: 50, cols: termCols }));
      }
    }
  });

  // --- Quick Action Buttons ---
  // HTML data-key contains literal strings like \r \x1b \x03 — parse to real chars
  function parseKey(raw) {
    return raw.replace(/\\r/g, '\r').replace(/\\x1b/g, '\x1b').replace(/\\x03/g, '\x03');
  }

  document.querySelectorAll('.action-btn').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.preventDefault();
      sendRaw(parseKey(btn.dataset.key));
    });
  });

  // --- Chat Input with Vietnamese IME support ---
  const chatInput = document.getElementById('chat-input');
  const btnSend = document.getElementById('btn-send');

  chatInput.addEventListener('compositionstart', () => { isComposing = true; });
  chatInput.addEventListener('compositionend', () => { isComposing = false; });

  chatInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      if (isComposing) return;
      e.preventDefault();
      sendMessage();
    }
  });

  btnSend.addEventListener('click', (e) => {
    e.preventDefault();
    // Reset composing flag in case it got stuck
    isComposing = false;
    setTimeout(sendMessage, 50);
  });

  // Hide keyboard button
  document.getElementById('btn-hide-kb').addEventListener('click', (e) => {
    e.preventDefault();
    chatInput.blur();
    document.activeElement.blur();
  });

  chatInput.addEventListener('input', () => {
    chatInput.style.height = 'auto';
    chatInput.style.height = Math.min(chatInput.scrollHeight, 120) + 'px';
  });

  function sendMessage() {
    if (isComposing) return;
    const text = chatInput.value;
    if (text === '') return;
    sendRaw(text + '\r');
    chatInput.value = '';
    chatInput.style.height = 'auto';
    setTimeout(() => chatInput.focus(), 50);
  }

  // --- Handoff Mode Selector ---
  function showHandoffScreen(dir, mode) {
    history.replaceState({}, '', '/');
    if (mode === 'attach') {
      attachToSession(dir);
      return;
    }
    if (mode === 'continue') {
      startContinueSession(dir);
      return;
    }
    document.getElementById('handoff-dir').textContent = dir;
    showScreen('screen-handoff');
  }

  function attachToSession(dir) {
    showScreen('screen-chat');
    document.getElementById('chat-dir').textContent = dir;
    document.getElementById('output-text').innerHTML = '';
    initTerminal();
    connectWS();
  }

  async function startContinueSession(dir) {
    showScreen('screen-chat');
    document.getElementById('chat-dir').textContent = dir;
    document.getElementById('output-text').innerHTML = '';
    try {
      const resp = await fetch('/api/claude/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dir: dir, resume: true })
      });
      const data = await resp.json();
      if (data.error) {
        alert('Lỗi: ' + data.error);
        showScreen('screen-picker');
        return;
      }
    } catch (e) {
      alert('Lỗi kết nối: ' + e.message);
      showScreen('screen-picker');
      return;
    }
    initTerminal();
    connectWS();
  }

  document.getElementById('btn-attach').addEventListener('click', () => {
    attachToSession(handoffDir || selectedDir);
  });
  document.getElementById('btn-continue').addEventListener('click', () => {
    startContinueSession(handoffDir || selectedDir);
  });
  document.getElementById('btn-new-folder').addEventListener('click', () => {
    history.replaceState({}, '', '/');
    showScreen('screen-picker');
    initPicker();
  });

  // --- Init ---
  if (handoffDir && handoffMode) {
    showHandoffScreen(handoffDir, handoffMode);
  } else {
    initPicker();
    fetch('/api/claude/status').then(r => r.json()).then(data => {
      if (data.running) {
        showScreen('screen-chat');
        document.getElementById('chat-dir').textContent = 'Phiên đang chạy';
        initTerminal();
        connectWS();
      }
    }).catch(() => {});
  }
})();
