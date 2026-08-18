(function() {
  'use strict';

  let ws = null;
  let term = null;
  let reconnectTimer = null;
  let selectedDir = '';
  let isComposing = false;
  let syncTimer = null;
  let termCols = 50; // calculated from screen width at runtime
  let termRows = 30; // calculated from the visible output height

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
      return true;
    }
    setStatus(false, 'Not connected — message not sent');
    if (typeof diagnose === 'function') diagnose();
    return false;
  }

  // Phones have no room for a full project path; keep the meaningful tail.
  function shortenPath(path, keep) {
    if (!path) return '';
    const parts = path.split('/').filter(Boolean);
    if (parts.length <= keep) return path;
    return '…/' + parts.slice(-keep).join('/');
  }

  function setDirLabel(id, path) {
    const el = document.getElementById(id);
    if (!el) return;
    el.textContent = shortenPath(path, 3);
    el.title = path;
  }

  function showScreen(id) {
    document.querySelectorAll('.screen').forEach(s => s.classList.remove('active'));
    document.getElementById(id).classList.add('active');
  }

  // Measure the height of one rendered line.
  function lineHeightPx() {
    const measure = document.createElement('div');
    measure.className = 'output-line';
    measure.textContent = 'M';
    measure.style.cssText = 'position:absolute;visibility:hidden;';
    document.body.appendChild(measure);
    const h = measure.getBoundingClientRect().height;
    document.body.removeChild(measure);
    return h > 0 ? h : 18;
  }

  // Calculate terminal rows from the visible output area.
  //
  // This must match what the phone can actually show. Hardcoding 50 rows made
  // Claude draw its input box at the bottom of a 50-row screen while only ~25
  // rows were visible, so the phone showed a blank gap and nothing else.
  function calcRows() {
    const scrollEl = document.getElementById('output-scroll');
    const h = (scrollEl && scrollEl.clientHeight) || window.innerHeight * 0.6;
    const rows = Math.floor((h - 8) / lineHeightPx());
    return Math.max(14, Math.min(60, rows));
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
  let pickerInitialized = false;
  function initPicker() {
    const container = document.getElementById('quick-dirs');
    if (!pickerInitialized) {
      quickDirs.forEach(d => {
        const btn = document.createElement('button');
        btn.className = 'quick-btn';
        btn.textContent = d.icon + ' ' + d.name;
        btn.addEventListener('click', () => browseDir('~/' + d.name));
        container.appendChild(btn);
      });
      pickerInitialized = true;
    }
    browseDir('~/Desktop');
  }

  async function browseDir(path) {
    try {
      const resp = await fetch('/api/files?path=' + encodeURIComponent(path));
      if (resp.status === 401) {
        document.getElementById('dir-list').innerHTML =
          '<div style="padding:20px 12px;color:#f87171;text-align:center;">' +
          'Not authenticated. Scan QR code or run <b>claude-remote setup</b> to connect.</div>';
        document.getElementById('start-bar').classList.add('hidden');
        return;
      }
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
      empty.textContent = 'No subdirectories';
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
    btn.textContent = 'Starting...';
    btn.disabled = true;

    try {
      const resp = await fetch('/api/claude/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dir: selectedDir })
      });
      const data = await resp.json();
      if (data.error) {
        alert('Error: ' + data.error);
        btn.textContent = 'Start Claude';
        btn.disabled = false;
        return;
      }
      showScreen('screen-chat');
      setDirLabel('chat-dir', selectedDir);
      // Clear old output
      document.getElementById('output-text').innerHTML = '';
      initTerminal();
      connectWS();
    } catch (e) {
      alert('Connection error: ' + e.message);
      btn.textContent = 'Start Claude';
      btn.disabled = false;
    }
  });

  // --- Back button ---
  document.getElementById('btn-back').addEventListener('click', () => {
    if (confirm('Disconnect from this session? Claude keeps running on your Mac.')) {
      cleanup();
      showScreen('screen-picker');
      document.getElementById('btn-start').textContent = 'Start Claude';
      document.getElementById('btn-start').disabled = false;
    }
  });

  function cleanup() {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    if (syncTimer) { clearTimeout(syncTimer); syncTimer = null; }
    if (ws) { ws.onclose = null; ws.close(); ws = null; }
    if (term) { term.dispose(); term = null; }
    document.getElementById('output-text').innerHTML = '';
    resetRenderState();
  }

  // --- Terminal (hidden xterm for escape sequence processing) ---
  function initTerminal() {
    if (term) { term.dispose(); }
    termCols = calcCols();
    termRows = calcRows();
    term = new Terminal({
      cols: termCols,
      rows: termRows,
      scrollback: 10000,
    });
    term.open(document.getElementById('xterm-hidden'));
    resetRenderState();
  }

  // ANSI color map
  const ANSI_COLORS = [
    '#000','#c00','#0a0','#a50','#00a','#a0a','#0aa','#ccc', // 0-7
    '#555','#f55','#5f5','#ff5','#55f','#f5f','#5ff','#fff', // 8-15
  ];

  // 256-color palette (xterm)
  function color256(n) {
    if (n < 16) return ANSI_COLORS[n];
    if (n < 232) {
      n -= 16;
      const r = Math.floor(n / 36) * 51;
      const g = Math.floor((n % 36) / 6) * 51;
      const b = (n % 6) * 51;
      return 'rgb(' + r + ',' + g + ',' + b + ')';
    }
    const v = (n - 232) * 10 + 8;
    return 'rgb(' + v + ',' + v + ',' + v + ')';
  }

  // Extract style from a single xterm cell
  function getCellStyle(cell) {
    const fg = cell.getFgColor();
    const bg = cell.getBgColor();
    const fgMode = cell.getFgColorMode();
    const bgMode = cell.getBgColorMode();
    let s = [];
    // fgMode: 1=palette(16), 2=RGB(truecolor), 3=palette(256)
    if (fgMode === 1 && fg >= 0 && fg < 16) s.push('color:' + ANSI_COLORS[fg]);
    else if (fgMode === 2) s.push('color:#' + fg.toString(16).padStart(6, '0'));
    else if (fgMode === 3) s.push('color:' + color256(fg));
    if (bgMode === 1 && bg >= 0 && bg < 16) s.push('background:' + ANSI_COLORS[bg]);
    else if (bgMode === 2) s.push('background:#' + bg.toString(16).padStart(6, '0'));
    else if (bgMode === 3) s.push('background:' + color256(bg));
    if (cell.isBold()) s.push('font-weight:bold');
    if (cell.isItalic()) s.push('font-style:italic');
    if (cell.isUnderline()) s.push('text-decoration:underline');
    if (cell.isDim()) s.push('opacity:0.6');
    return s.length > 0 ? s.join(';') : '';
  }

  // Render a single xterm line to HTML by iterating cells directly
  // (avoids translateToString + cell index mismatch bugs)
  function renderLine(line) {
    if (!line) return '';
    let html = '';
    let prevStyle = '';
    let spanOpen = false;
    let trailingSpaces = 0;

    for (let col = 0; col < line.length; col++) {
      const cell = line.getCell(col);
      if (!cell || cell.getWidth() === 0) continue; // skip second cell of wide chars

      const ch = cell.getChars();
      if (ch === '' || ch === ' ') {
        trailingSpaces++;
        continue;
      }

      // Flush accumulated spaces (they're not trailing since we found a non-space)
      if (trailingSpaces > 0) {
        if (spanOpen) { html += ' '.repeat(trailingSpaces); }
        else { html += ' '.repeat(trailingSpaces); }
        trailingSpaces = 0;
      }

      const style = getCellStyle(cell);
      const escaped = escapeHtml(ch);

      if (style !== prevStyle) {
        if (spanOpen) { html += '</span>'; spanOpen = false; }
        if (style) {
          html += '<span style="' + style + '">';
          spanOpen = true;
        }
        prevStyle = style;
      }
      html += escaped;
    }
    if (spanOpen) html += '</span>';
    // trailingSpaces intentionally dropped (trim right)
    return html;
  }

  // State for incremental rendering
  let renderedLines = []; // cached HTML per logical line currently in the DOM
  let lineElements = [];  // DOM elements per logical line
  let prefixLines = [];   // logical lines that scrolled off the live screen
  let prefixRows = 0;     // absolute buffer row where prefixLines ends

  function resetRenderState() {
    renderedLines = [];
    lineElements = [];
    prefixLines = [];
    prefixRows = 0;
  }

  // Build logical lines from the xterm buffer.
  // A logical line = one or more physical rows (wrapped rows joined).
  //
  // Rows above the live screen have scrolled off and can never change, so they
  // are rendered once and cached. The live screen must be re-rendered every
  // time: Claude Code repaints it in place, and the previous version of this
  // code both stopped at the cursor row (hiding menus drawn below it) and only
  // re-checked the last 3 lines for changes — which left the phone staring at
  // a blank screen while Claude was busy redrawing.
  function collectRows(buf, from, to, out) {
    for (let i = from; i <= to; i++) {
      const line = buf.getLine(i);
      if (!line) { out.push(''); continue; }
      const html = renderLine(line);
      if (line.isWrapped && out.length > 0) {
        out[out.length - 1] += html;
      } else {
        out.push(html);
      }
    }
  }

  function getLogicalLines() {
    if (!term) return [];
    const buf = term.buffer.active;
    const liveStart = Math.max(0, buf.baseY);

    if (prefixRows < liveStart) {
      collectRows(buf, prefixRows, liveStart - 1, prefixLines);
      prefixRows = liveStart;
    }

    const liveEnd = Math.min(buf.length, liveStart + term.rows) - 1;
    const live = [];
    if (liveEnd >= liveStart) {
      collectRows(buf, liveStart, liveEnd, live);
    }
    // The TUI leaves the bottom of the screen empty; do not render padding.
    while (live.length > 0 && live[live.length - 1] === '') live.pop();

    return prefixLines.concat(live);
  }

  // Sync xterm buffer to visible output — true incremental DOM updates
  function syncOutput() {
    if (syncTimer) return;
    syncTimer = setTimeout(() => {
      syncTimer = null;
      if (!term) return;

      const outputEl = document.getElementById('output-text');
      const scrollEl = document.getElementById('output-scroll');
      const wasNearBottom = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight < 80;

      const logicalLines = getLogicalLines();
      const newCount = logicalLines.length;
      const oldCount = renderedLines.length;

      // Remove excess lines if buffer shrunk (screen clear etc)
      while (lineElements.length > newCount) {
        const el = lineElements.pop();
        el.remove();
      }
      renderedLines.length = Math.min(renderedLines.length, newCount);

      // Diff every line. Cached prefix lines are the same string references,
      // so the comparison stays cheap even with a long scrollback.
      for (let i = 0; i < Math.min(oldCount, newCount); i++) {
        if (renderedLines[i] !== logicalLines[i]) {
          lineElements[i].innerHTML = logicalLines[i];
          renderedLines[i] = logicalLines[i];
        }
      }

      // Append new lines
      if (newCount > lineElements.length) {
        const frag = document.createDocumentFragment();
        for (let i = lineElements.length; i < newCount; i++) {
          const div = document.createElement('div');
          div.className = 'output-line';
          div.innerHTML = logicalLines[i];
          frag.appendChild(div);
          lineElements.push(div);
          renderedLines.push(logicalLines[i]);
        }
        outputEl.appendChild(frag);
      }

      if (wasNearBottom) {
        scrollEl.scrollTop = scrollEl.scrollHeight;
      }
    }, 60);
  }

  // Force full re-sync (used on visibility change, resize)
  function forceSyncOutput() {
    if (!term) return;
    // Re-wrapping after a resize invalidates every cached line.
    prefixLines = [];
    prefixRows = 0;
    const outputEl = document.getElementById('output-text');
    const scrollEl = document.getElementById('output-scroll');
    const wasNearBottom = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight < 80;

    const logicalLines = getLogicalLines();

    // Clear and rebuild all line elements
    outputEl.innerHTML = '';
    lineElements = [];
    renderedLines = [];

    const frag = document.createDocumentFragment();
    for (let i = 0; i < logicalLines.length; i++) {
      const div = document.createElement('div');
      div.className = 'output-line';
      div.innerHTML = logicalLines[i];
      frag.appendChild(div);
      lineElements.push(div);
      renderedLines.push(logicalLines[i]);
    }
    outputEl.appendChild(frag);

    if (wasNearBottom) {
      scrollEl.scrollTop = scrollEl.scrollHeight;
    }
  }

  // --- Connection feedback ---
  const banner = document.getElementById('conn-banner');
  const bannerMsg = document.getElementById('conn-msg');
  const bannerHint = document.getElementById('conn-hint');
  const btnRetry = document.getElementById('btn-retry');

  function showProblem(msg, hint) {
    bannerMsg.textContent = msg;
    bannerHint.textContent = hint || '';
    banner.classList.remove('hidden');
    btnRetry.classList.remove('hidden');
  }

  function clearProblem() {
    banner.classList.add('hidden');
    btnRetry.classList.add('hidden');
  }

  // Ask the server why the socket will not open: /health needs no auth,
  // /api/claude/status needs a valid cookie. That tells apart "no network"
  // from "session expired" from "server down".
  async function diagnose() {
    try {
      const health = await fetch('/health', { cache: 'no-store' });
      if (!health.ok) {
        showProblem('Server reachable but unhealthy.', 'Run `claude-remote doctor` on your Mac.');
        return;
      }
    } catch (e) {
      showProblem('Cannot reach the server.',
        'The Mac may be asleep or the tunnel is down. Run `claude-remote doctor` on your Mac.');
      return;
    }
    try {
      const resp = await fetch('/api/claude/status', { cache: 'no-store' });
      if (resp.status === 401) {
        showProblem('Session expired — scan a new QR code.',
          'Run `claude-remote qr` on your Mac, then scan it.');
        return;
      }
      const data = await resp.json();
      if (!data.running) {
        showProblem('No Claude session is running.', 'Go back and pick a folder to start one.');
        return;
      }
      showProblem('Connection dropped.', 'Tap Retry to reconnect.');
    } catch (e) {
      showProblem('Connection problem: ' + e.message, 'Tap Retry to reconnect.');
    }
  }

  btnRetry.addEventListener('click', () => {
    clearProblem();
    failedAttempts = 0;
    setStatus(false, 'Reconnecting...');
    connectWS();
  });

  // Rolling tail of raw terminal output, used to spot selection prompts.
  let rawTail = '';

  // --- WebSocket ---
  let failedAttempts = 0;
  const MAX_FAILED_ATTEMPTS = 5;

  function connectWS() {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + '/ws/term');
    ws.binaryType = 'arraybuffer';
    let opened = false;

    ws.onopen = () => {
      opened = true;
      failedAttempts = 0;
      clearProblem();
      setStatus(true);
      // The server replays its whole backlog on every connect, so start
      // from a clean buffer — otherwise a reconnect duplicates the session.
      if (term) {
        term.reset();
        document.getElementById('output-text').innerHTML = '';
        resetRenderState();
        rawTail = '';
      }
      termCols = calcCols();
      termRows = calcRows();
      if (term) {
        term.resize(termCols, termRows);
      }
      ws.send(JSON.stringify({ type: 'resize', rows: termRows, cols: termCols }));
    };

    ws.onmessage = (evt) => {
      if (!term) return;
      const data = typeof evt.data === 'string' ? evt.data : new TextDecoder().decode(evt.data);
      term.write(data);
      rawTail = (rawTail + data).slice(-4000);
      updatePromptHint(rawTail);
      syncOutput();
    };

    ws.onclose = (evt) => {
      if (!opened) {
        // Never completed the handshake: usually an expired cookie (the
        // upgrade is rejected with 401) or no route to the Mac at all.
        failedAttempts++;
        if (failedAttempts === 2) {
          diagnose(); // tell the user the real reason instead of spinning
        }
        if (failedAttempts >= MAX_FAILED_ATTEMPTS) {
          setStatus(false, 'Not connected');
          diagnose();
          return; // stop the retry loop instead of hammering forever
        }
      }
      setStatus(false, 'Reconnecting...');
      reconnectTimer = setTimeout(connectWS, 3000);
    };

    ws.onerror = () => { if (ws) ws.close(); };
  }

  function setStatus(connected, msg) {
    document.getElementById('status-dot').className = 'dot ' + (connected ? 'connected' : 'disconnected');
    document.getElementById('status-text').textContent = msg || (connected ? 'Connected' : 'Reconnecting...');
  }

  // Handle resize (debounced to avoid rapid re-renders during keyboard show/hide)
  let resizeTimer = null;
  window.addEventListener('resize', () => {
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      resizeTimer = null;
      const newCols = calcCols();
      const newRows = calcRows();
      if ((newCols !== termCols || newRows !== termRows) && term) {
        termCols = newCols;
        termRows = newRows;
        term.resize(termCols, termRows);
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'resize', rows: termRows, cols: termCols }));
        }
        // Force re-render after resize reflows xterm buffer
        forceSyncOutput();
      }
    }, 250);
  });

  // Handle app switch: re-sync output when returning to app
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && term) {
      // Small delay to let browser finish layout
      setTimeout(forceSyncOutput, 100);
    }
  });

  // --- Quick Action Buttons ---
  // HTML data-key contains literal strings like \r \x1b \x03 — parse to real chars
  function parseKey(raw) {
    return raw
      .replace(/\\r/g, '\r')
      .replace(/\\t/g, '\t')
      .replace(/\\x1b/g, '\x1b')
      .replace(/\\x03/g, '\x03');
  }

  document.querySelectorAll('.action-btn').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.preventDefault();
      sendRaw(parseKey(btn.dataset.key));
    });
  });

  // Claude ignores typed text while a selection prompt is open, which looks
  // exactly like "the phone cannot send anything". Say what to press instead.
  const promptHint = document.getElementById('prompt-hint');

  function updatePromptHint(text) {
    // Strip escape sequences first: the raw stream interleaves colour codes
    // between the cursor marker and the option number, so patterns like
    // "❯ 2." never match the unstripped text.
    const tail = text.slice(-4000).replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '').replace(/\x1b[()][A-Za-z0-9]/g, '');
    let msg = '';
    if (/Do you trust|trust this folder|trust this configuration|safety check|adds \d+ director/i.test(tail)) {
      msg = 'Claude is asking you to confirm this folder — press Enter ↵ to accept (↑ ↓ to change the choice).';
    } else if (/Enter to confirm|Esc to cancel|Yes, proceed|No, exit/i.test(tail)) {
      msg = 'Claude is waiting for a choice — use ↑ ↓ then Enter ↵. Typed text is ignored here.';
    } else if (/❯\s*\d\./.test(tail)) {
      msg = 'A menu is open — pick with ↑ ↓ and Enter ↵.';
    }
    if (msg) {
      promptHint.textContent = msg;
      promptHint.classList.remove('hidden');
    } else {
      promptHint.classList.add('hidden');
    }
  }

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
    // Keep the text in the box if the socket is down, so it is not lost.
    if (!sendRaw(text + '\r')) return;
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
    setDirLabel('handoff-dir', dir);
    showScreen('screen-handoff');
  }

  async function attachToSession(dir) {
    showScreen('screen-chat');
    setDirLabel('chat-dir', dir);
    document.getElementById('output-text').innerHTML = '';
    // Check if session is running before attaching
    try {
      const resp = await fetch('/api/claude/status');
      if (resp.status === 401) {
        alert('Not authenticated. Please scan QR code again.');
        showScreen('screen-picker');
        return;
      }
      const data = await resp.json();
      if (!data.running) {
        // No session running — start one instead
        const startResp = await fetch('/api/claude/start', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ dir: dir, resume: false })
        });
        if (startResp.status === 401) {
          alert('Not authenticated. Please scan QR code again.');
          showScreen('screen-picker');
          return;
        }
      }
    } catch (e) {
      // Network error — try connecting anyway
    }
    initTerminal();
    connectWS();
  }

  async function startContinueSession(dir) {
    showScreen('screen-chat');
    setDirLabel('chat-dir', dir);
    document.getElementById('output-text').innerHTML = '';
    try {
      const resp = await fetch('/api/claude/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dir: dir, resume: true })
      });
      if (resp.status === 401) {
        alert('Not authenticated. Please scan QR code again.');
        showScreen('screen-picker');
        return;
      }
      const data = await resp.json();
      if (data.error) {
        alert('Error: ' + data.error);
        showScreen('screen-picker');
        return;
      }
    } catch (e) {
      alert('Connection error: ' + e.message);
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
        document.getElementById('chat-dir').textContent = 'Session running';
        initTerminal();
        connectWS();
      }
    }).catch(() => {});
  }
})();
