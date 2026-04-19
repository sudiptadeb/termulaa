// ignorei18n_start
/**
 * app.js — Tab-ownership-aware split pane manager for terminal-agent v2.
 *
 * Architecture:
 *   Browser → /ws/tab/<tabID>           (tab ownership WS — control channel)
 *           → /ws/session/<sessionID>   (per-pane PTY I/O, one per pane)
 *
 * - Tab WS is connected FIRST and owns the lifecycle.
 * - Split/close/resize commands go through the tab WS.
 * - Session WSes carry raw PTY data (binary) per pane.
 */
(function() {
  'use strict';

  var RECONNECT_DELAY = 2000;

  // ── Tab ID from URL ─────────────────────────────────────────────────────────
  window.TAB_ID = window.location.pathname.split('/').pop();

  // ── State ───────────────────────────────────────────────────────────────────
  var root = null;          // Root of the split tree (PaneNode or SplitNode)
  var activePane = null;    // Currently focused PaneNode
  var nextPaneId = 1;       // Auto-increment pane ID (local only, for DOM)
  var rootContainer = null; // The #terminal-container div
  var tabWS = null;         // Tab-level ownership WebSocket
  var ownerToken = '';      // Active tab-owner token for session WS auth
  var serverSettings = {};  // Server-backed settings from /api/settings

  // ── Settings ────────────────────────────────────────────────────────────────

  async function loadServerSettings() {
    try {
      var response = await fetch('/api/settings');
      if (response.ok) {
        serverSettings = await response.json();
      }
    } catch (e) {
      console.warn('Failed to load settings, using defaults:', e);
      serverSettings = {};
    }
  }

  function getTerminalOptions() {
    var settings = serverSettings || {};
    var themeName = settings.theme || (window.TERMINAL_DEFAULTS && window.TERMINAL_DEFAULTS.theme) || 'dark';
    var themes = window.TERMINAL_THEMES || {};
    var theme = themes[themeName] || themes.dark || {
      background: '#1e1e1e',
      foreground: '#d4d4d4',
      cursor: '#d4d4d4',
      selectionBackground: '#264f78',
    };

    return {
      cursorBlink: settings.cursorBlink !== false,
      cursorStyle: settings.cursorStyle || 'block',
      fontSize: settings.fontSize || (window.TERMINAL_DEFAULTS && window.TERMINAL_DEFAULTS.fontSize) || 14,
      fontFamily: settings.fontFamily ||
        (window.TERMINAL_DEFAULTS && window.TERMINAL_DEFAULTS.fontFamily) ||
        "'JetBrains Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
      theme: theme,
      allowProposedApi: true,
    };
  }

  // ── Scrollback Cleanup ──────────────────────────────────────────────────────

  function stripQueryResponses(bytes) {
    var text = '';
    for (var i = 0; i < bytes.length; i++) text += String.fromCharCode(bytes[i]);
    // DA1 (device attributes) responses
    text = text.replace(/\x1b\[\?[\d;]*c/g, '');
    // DA2 responses
    text = text.replace(/\x1b\[>[\d;]*c/g, '');
    // DECRPM (mode report) responses: CSI ? <mode> ; <value> $ y
    text = text.replace(/\x1b\[\?[\d;]*\$y/g, '');
    // CPR (cursor position report): CSI <row> ; <col> R
    text = text.replace(/\x1b\[\d+;\d+R/g, '');
    // OSC responses (color queries, title, etc.) terminated by BEL or ST
    text = text.replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '');
    // DCS responses terminated by ST
    text = text.replace(/\x1bP[^\x1b]*\x1b\\/g, '');
    // Focus events
    text = text.replace(/\x1b\[I/g, '');
    text = text.replace(/\x1b\[O/g, '');
    var result = new Uint8Array(text.length);
    for (var j = 0; j < text.length; j++) result[j] = text.charCodeAt(j);
    return result;
  }

  // ── Pane Creation ───────────────────────────────────────────────────────────

  function createPane(container, sessionID) {
    var id = nextPaneId++;

    var el = document.createElement('div');
    el.className = 'pane';
    el.dataset.paneId = id;
    el.dataset.sessionId = sessionID;
    el.style.flex = '1 1 0%';
    el.style.minWidth = '0';
    el.style.minHeight = '0';
    el.style.position = 'relative';
    el.style.overflow = 'hidden';
    container.appendChild(el);

    var term = new Terminal(getTerminalOptions());
    var fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);

    try {
      var webgl = new WebglAddon.WebglAddon();
      term.loadAddon(webgl);
    } catch (e) {
      console.warn('Pane ' + id + ': WebGL addon failed, using canvas:', e);
    }

    try {
      var unicode11 = new Unicode11Addon.Unicode11Addon();
      term.loadAddon(unicode11);
      term.unicode.activeVersion = '11';
    } catch (e) {
      console.warn('Pane ' + id + ': Unicode11 addon failed:', e);
    }

    term.open(el);

    var pane = {
      type: 'pane',
      id: id,
      sessionID: sessionID,
      term: term,
      fitAddon: fitAddon,
      ws: null,
      element: el,
      active: false,
      reconnectTimer: null,
      disposed: false,
      scrollbackLoaded: false,
      replayingScrollback: false,
    };

    // Click to focus
    el.addEventListener('mousedown', function() {
      setActivePane(pane);
    });

    // Wire terminal input, but defer until scrollback replay completes.
    term.onData(function(data) {
      if (pane.replayingScrollback) return;
      if (pane.ws && pane.ws.readyState === WebSocket.OPEN) {
        pane.ws.send(new TextEncoder().encode(data));
      }
    });

    term.onBinary(function(data) {
      if (pane.replayingScrollback) return;
      if (pane.ws && pane.ws.readyState === WebSocket.OPEN) {
        var bytes = new Uint8Array(data.length);
        for (var i = 0; i < data.length; i++) bytes[i] = data.charCodeAt(i);
        pane.ws.send(bytes);
      }
    });

    term.onResize(function(size) {
      if (pane.ws && pane.ws.readyState === WebSocket.OPEN) {
        pane.ws.send(JSON.stringify({ type: 'resize', cols: size.cols, rows: size.rows }));
      }
    });

    term.onTitleChange(function(title) {
      if (pane.active && title) {
        document.title = title;
      }
    });

    // Clipboard paste
    el.addEventListener('paste', function(e) {
      if (!e.clipboardData || !e.clipboardData.items) return;
      var items = e.clipboardData.items;
      for (var i = 0; i < items.length; i++) {
        var item = items[i];
        if (item.type.indexOf('image/') === 0) {
          e.preventDefault();
          var blob = item.getAsFile();
          if (blob) sendFileToAgent(pane, blob, 'clipboard.png');
          return;
        }
        if (item.kind === 'file' && item.type) {
          var file = item.getAsFile();
          if (file && file.type.indexOf('text/') !== 0) {
            e.preventDefault();
            sendFileToAgent(pane, file, file.name);
            return;
          }
        }
      }
    });

    // Drag & drop
    el.addEventListener('dragover', function(e) {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
      el.classList.add('drag-over');
    });
    el.addEventListener('dragleave', function() {
      el.classList.remove('drag-over');
    });
    el.addEventListener('drop', function(e) {
      e.preventDefault();
      el.classList.remove('drag-over');
      setActivePane(pane);
      var files = e.dataTransfer.files;
      for (var i = 0; i < files.length; i++) {
        sendFileToAgent(pane, files[i], files[i].name);
      }
    });

    // Fit after a tick
    requestAnimationFrame(function() {
      try { fitAddon.fit(); } catch (e) { /* ignore */ }
    });

    // Connect session WebSocket
    connectPaneWS(pane);

    return pane;
  }

  // ── File Transfer ───────────────────────────────────────────────────────────

  function sendFileToAgent(pane, blob, filename) {
    if (!pane.ws || pane.ws.readyState !== WebSocket.OPEN) {
      pane.term.writeln('\r\n\x1b[31mCannot send file: not connected\x1b[0m');
      return;
    }

    var reader = new FileReader();
    reader.onload = function() {
      var b64 = reader.result.split(',')[1];
      pane.ws.send(JSON.stringify({
        type: 'file',
        name: filename || 'file',
        data: b64,
      }));
    };
    reader.onerror = function() {
      pane.term.writeln('\r\n\x1b[31mFailed to read file\x1b[0m');
    };
    reader.readAsDataURL(blob);
  }

  // ── Session WebSocket per pane ──────────────────────────────────────────────

  function connectPaneWS(pane) {
    if (pane.disposed) return;

    if (pane.reconnectTimer) {
      clearTimeout(pane.reconnectTimer);
      pane.reconnectTimer = null;
    }

    var protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    var wsUrl = protocol + '//' + window.location.host + '/ws/session/' + pane.sessionID +
      '?tab_id=' + encodeURIComponent(window.TAB_ID) +
      '&token=' + encodeURIComponent(ownerToken);

    var ws = new WebSocket(wsUrl);
    ws.binaryType = 'arraybuffer';
    pane.ws = ws;

    ws.onopen = function() {
      if (pane.disposed) { ws.close(); return; }
      if (pane.active) pane.term.focus();
      try { pane.fitAddon.fit(); } catch (e) { /* ignore */ }
      var cols = pane.term.cols;
      var rows = pane.term.rows;
      ws.send(JSON.stringify({ type: 'resize', cols: cols, rows: rows }));
    };

    ws.onmessage = function(event) {
      if (pane.disposed) return;
      if (typeof event.data === 'string') {
        try {
          var msg = JSON.parse(event.data);
          if (msg.type === 'scrollback' && msg.data) {
            // Skip if already loaded — a reconnect to a now-alive session
            // would send its (small) ring buffer, wiping the richer old scrollback.
            if (pane.scrollbackLoaded) return;
            pane.term.reset();
            pane.replayingScrollback = true;
            var binary = atob(msg.data);
            var bytes = new Uint8Array(binary.length);
            for (var j = 0; j < binary.length; j++) bytes[j] = binary.charCodeAt(j);
            var cleaned = stripQueryResponses(bytes);
            pane.term.write(cleaned, function() {
              pane.replayingScrollback = false;
            });
            pane.scrollbackLoaded = true;
            return;
          }
          if (msg.type === 'exit') {
            pane.term.writeln('\r\n\x1b[90m[Process exited with code ' + msg.code + ']\x1b[0m');
            schedulePaneReconnect(pane);
            return;
          }
        } catch (e) {
          pane.term.write(event.data);
        }
      } else {
        pane.term.write(new Uint8Array(event.data));
      }
    };

    ws.onclose = function() {
      if (pane.disposed) return;
      pane.ws = null;
      schedulePaneReconnect(pane);
    };

    ws.onerror = function() { /* onclose fires after this */ };
  }

  function schedulePaneReconnect(pane) {
    if (pane.disposed) return;
    if (!pane.reconnectTimer) {
      pane.reconnectTimer = setTimeout(function() {
        pane.reconnectTimer = null;
        connectPaneWS(pane);
      }, RECONNECT_DELAY);
    }
  }

  // ── Layout Reconstruction ───────────────────────────────────────────────────

  function reconstructLayout(container, node) {
    if (!node) return null;

    if (node.type === 'pane') {
      return createPane(container, node.session_id);
    }

    if (node.type === 'split') {
      var splitEl = document.createElement('div');
      splitEl.className = 'split-container ' + node.direction;
      splitEl.style.flex = '1 1 0%';
      splitEl.style.minWidth = '0';
      splitEl.style.minHeight = '0';
      container.appendChild(splitEl);

      var first = reconstructLayout(splitEl, node.first);

      var handle = document.createElement('div');
      handle.className = 'resize-handle ' + node.direction;
      splitEl.appendChild(handle);

      var second = reconstructLayout(splitEl, node.second);

      var splitNode = {
        type: 'split',
        direction: node.direction,
        ratio: node.ratio || 0.5,
        first: first,
        second: second,
        element: splitEl,
      };

      applyRatio(splitNode);
      initResizeHandle(handle, splitNode);

      return splitNode;
    }

    return null;
  }

  // ── Layout Serialization ────────────────────────────────────────────────────

  function serializeTree(node) {
    if (!node) return null;

    if (node.type === 'pane') {
      return { type: 'pane', session_id: node.sessionID };
    }

    if (node.type === 'split') {
      return {
        type: 'split',
        direction: node.direction,
        ratio: node.ratio,
        first: serializeTree(node.first),
        second: serializeTree(node.second),
      };
    }

    return null;
  }

  /**
   * Sync layout to server via tab WS (replaces REST PUT).
   */
  function syncLayout() {
    if (!tabWS || tabWS.readyState !== WebSocket.OPEN) return;

    var layout = serializeTree(root);
    if (!layout) return;

    tabWS.send(JSON.stringify({ type: 'update_layout', layout: layout }));
  }

  // ── Split / Close (via tab WS) ─────────────────────────────────────────────

  /**
   * Request a split from the server via tab WS.
   * The server creates the session and broadcasts session_created.
   */
  function splitPane(paneNode, direction) {
    if (!tabWS || tabWS.readyState !== WebSocket.OPEN) return;

    tabWS.send(JSON.stringify({
      type: 'split',
      session_id: paneNode.sessionID,
      direction: direction,
    }));
  }

  /**
   * Request a pane close from the server via tab WS.
   * The server kills the session and broadcasts pane_closed.
   */
  function requestClosePane(paneNode) {
    if (!tabWS || tabWS.readyState !== WebSocket.OPEN) return;

    tabWS.send(JSON.stringify({
      type: 'close_pane',
      session_id: paneNode.sessionID,
    }));
  }

  /**
   * Apply a split locally in the DOM tree when the server confirms it.
   */
  function applySplit(targetSessionID, newSessionID, direction) {
    var targetPane = findPaneBySessionID(targetSessionID);
    if (!targetPane) return;

    var container = document.createElement('div');
    container.className = 'split-container ' + direction;
    container.style.flex = '1 1 0%';
    container.style.minWidth = '0';
    container.style.minHeight = '0';

    var handle = document.createElement('div');
    handle.className = 'resize-handle ' + direction;

    var parentInfo = findParent(targetPane);

    if (parentInfo) {
      var parentContainer = parentInfo.parent.element;
      parentContainer.insertBefore(container, targetPane.element);
      parentContainer.removeChild(targetPane.element);
    } else {
      rootContainer.removeChild(targetPane.element);
      rootContainer.appendChild(container);
    }

    targetPane.element.style.flex = '1 1 0%';
    container.appendChild(targetPane.element);
    container.appendChild(handle);

    var newPane = createPane(container, newSessionID);

    var splitNode = {
      type: 'split',
      direction: direction,
      ratio: 0.5,
      first: targetPane,
      second: newPane,
      element: container,
    };

    if (parentInfo) {
      parentInfo.parent[parentInfo.which] = splitNode;
    } else {
      root = splitNode;
    }

    applyRatio(splitNode);
    initResizeHandle(handle, splitNode);
    refitAll();
    setActivePane(newPane);
  }

  /**
   * Apply a pane close locally in the DOM tree when the server confirms it.
   * serverLayout: the layout from the server event (used for last-pane replacement).
   */
  function applyClosePane(sessionID, serverLayout) {
    var paneNode = findPaneBySessionID(sessionID);
    if (!paneNode) return;

    disposePane(paneNode);

    var allPanes = getAllPanes();
    if (allPanes.length === 0) {
      // Last pane was closed — server created a replacement.
      // Rebuild from the server layout.
      rootContainer.innerHTML = '';
      root = null;
      if (serverLayout) {
        root = reconstructLayout(rootContainer, serverLayout);
        var newPanes = getAllPanes();
        if (newPanes.length > 0) setActivePane(newPanes[0]);
      }
      return;
    }

    var parentInfo = findParent(paneNode);
    if (!parentInfo) {
      // Was the root pane — rebuild from server layout.
      rootContainer.innerHTML = '';
      root = null;
      if (serverLayout) {
        root = reconstructLayout(rootContainer, serverLayout);
        var rebuiltPanes = getAllPanes();
        if (rebuiltPanes.length > 0) setActivePane(rebuiltPanes[0]);
      }
      return;
    }

    var parentSplit = parentInfo.parent;
    var siblingKey = parentInfo.which === 'first' ? 'second' : 'first';
    var sibling = parentSplit[siblingKey];
    var grandInfo = findParent(parentSplit);
    var splitEl = parentSplit.element;

    if (sibling.element.parentNode === splitEl) {
      splitEl.removeChild(sibling.element);
    }

    if (grandInfo) {
      var grandContainer = grandInfo.parent.element;
      grandContainer.insertBefore(sibling.element, splitEl);
      grandContainer.removeChild(splitEl);
      grandInfo.parent[grandInfo.which] = sibling;
    } else {
      rootContainer.removeChild(splitEl);
      sibling.element.style.flex = '1 1 0%';
      rootContainer.appendChild(sibling.element);
      root = sibling;
    }

    refitAll();

    if (sibling.type === 'pane') {
      setActivePane(sibling);
    } else {
      var leaves = [];
      walkLeaves(sibling, function(p) { leaves.push(p); });
      if (leaves.length > 0) setActivePane(leaves[0]);
    }
  }

  function disposePane(pane) {
    pane.disposed = true;
    if (pane.reconnectTimer) {
      clearTimeout(pane.reconnectTimer);
      pane.reconnectTimer = null;
    }
    if (pane.ws) {
      try { pane.ws.close(); } catch (e) { /* ignore */ }
      pane.ws = null;
    }
    try { pane.term.dispose(); } catch (e) { /* ignore */ }
    if (pane.element && pane.element.parentNode) {
      pane.element.parentNode.removeChild(pane.element);
    }
  }

  // ── Focus Management ────────────────────────────────────────────────────────

  function setActivePane(pane) {
    if (!pane || pane.disposed) return;

    if (activePane && activePane !== pane) {
      activePane.active = false;
      activePane.element.classList.remove('active');
    }

    activePane = pane;
    pane.active = true;
    pane.element.classList.add('active');
    pane.term.focus();
  }

  function getActivePane() {
    return activePane;
  }

  function getAllPanes() {
    var panes = [];
    if (root) walkLeaves(root, function(p) { if (!p.disposed) panes.push(p); });
    return panes;
  }

  function findPaneBySessionID(sessionID) {
    var found = null;
    if (root) walkLeaves(root, function(p) {
      if (!p.disposed && p.sessionID === sessionID) found = p;
    });
    return found;
  }

  // ── Layout ──────────────────────────────────────────────────────────────────

  function applyRatio(splitNode) {
    var r = splitNode.ratio;
    var firstPct = (r * 100).toFixed(2) + '%';
    var secondPct = ((1 - r) * 100).toFixed(2) + '%';

    splitNode.first.element.style.flex = '0 0 calc(' + firstPct + ' - 2px)';
    splitNode.second.element.style.flex = '0 0 calc(' + secondPct + ' - 2px)';
  }

  var refitPending = false;
  function refitAll() {
    if (refitPending) return;
    refitPending = true;
    requestAnimationFrame(function() {
      refitPending = false;
      if (!root) return;
      walkLeaves(root, function(pane) {
        if (!pane.disposed) {
          try { pane.fitAddon.fit(); } catch (e) { /* ignore */ }
        }
      });
    });
  }

  // ── Tree Helpers ────────────────────────────────────────────────────────────

  function findParent(node) {
    if (!root || root === node) return null;
    return _findParentRecursive(root, node);
  }

  function _findParentRecursive(current, target) {
    if (current.type !== 'split') return null;
    if (current.first === target) return { parent: current, which: 'first' };
    if (current.second === target) return { parent: current, which: 'second' };
    var result = _findParentRecursive(current.first, target);
    if (result) return result;
    return _findParentRecursive(current.second, target);
  }

  function walkLeaves(node, callback) {
    if (!node) return;
    if (node.type === 'pane') {
      callback(node);
    } else if (node.type === 'split') {
      walkLeaves(node.first, callback);
      walkLeaves(node.second, callback);
    }
  }

  // ── Resize Handle Drag ────────────────────────────────────────────────────

  function initResizeHandle(handle, splitNode) {
    var dragging = false;
    var rafId = null;

    handle.addEventListener('mousedown', function(e) {
      e.preventDefault();
      e.stopPropagation();
      dragging = true;
      document.body.style.cursor = splitNode.direction === 'vertical'
        ? 'col-resize' : 'row-resize';
      document.body.style.userSelect = 'none';

      var overlay = document.createElement('div');
      overlay.id = 'resize-overlay';
      overlay.style.cssText = 'position:fixed;top:0;left:0;right:0;bottom:0;z-index:9999;cursor:' +
        (splitNode.direction === 'vertical' ? 'col-resize' : 'row-resize');
      document.body.appendChild(overlay);

      function onMouseMove(e2) {
        if (!dragging) return;
        if (rafId) return;
        rafId = requestAnimationFrame(function() {
          rafId = null;
          var rect = splitNode.element.getBoundingClientRect();
          var ratio;
          if (splitNode.direction === 'vertical') {
            ratio = (e2.clientX - rect.left) / rect.width;
          } else {
            ratio = (e2.clientY - rect.top) / rect.height;
          }
          ratio = Math.max(0.1, Math.min(0.9, ratio));
          splitNode.ratio = ratio;
          applyRatio(splitNode);
          refitAll();
        });
      }

      function onMouseUp() {
        dragging = false;
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        var ov = document.getElementById('resize-overlay');
        if (ov) ov.parentNode.removeChild(ov);
        document.removeEventListener('mousemove', onMouseMove);
        document.removeEventListener('mouseup', onMouseUp);
        if (rafId) {
          cancelAnimationFrame(rafId);
          rafId = null;
        }
        refitAll();
        syncLayout();
      }

      document.addEventListener('mousemove', onMouseMove);
      document.addEventListener('mouseup', onMouseUp);
    });
  }

  // ── Keyboard Shortcuts ──────────────────────────────────────────────────────

  var isMac = navigator.platform.indexOf('Mac') !== -1;

  var keybindings = {
    splitVertical:   { mod: true, shift: false, key: 'd' },
    splitHorizontal: { mod: true, shift: true,  key: 'd' },
    closePane:       { mod: true, shift: false, key: 'w' },
    nextPane:        { mod: true, shift: false, key: ']' },
    prevPane:        { mod: true, shift: false, key: '[' },
  };

  function loadKeybindings() {
    try {
      var stored = localStorage.getItem('terminalKeybindings');
      if (stored) {
        var custom = JSON.parse(stored);
        for (var action in custom) {
          keybindings[action] = custom[action];
        }
      }
    } catch (e) { /* ignore */ }
  }

  function matchesKeybinding(e, kb) {
    if (!kb) return false;
    var modPressed = isMac ? e.metaKey : e.ctrlKey;
    if (kb.mod && !modPressed) return false;
    if (!kb.mod && modPressed) return false;
    if (kb.shift && !e.shiftKey) return false;
    if (!kb.shift && e.shiftKey) return false;
    if (kb.alt && !e.altKey) return false;
    if (!kb.alt && e.altKey) return false;
    var eventKey = e.key.length === 1 ? e.key.toLowerCase() : e.key;
    return eventKey === kb.key;
  }

  function cyclePanes(delta) {
    var panes = getAllPanes();
    if (panes.length <= 1) return;
    var idx = panes.indexOf(activePane);
    if (idx === -1) idx = 0;
    idx = (idx + delta + panes.length) % panes.length;
    setActivePane(panes[idx]);
  }

  function handleKeyboardShortcuts(e) {
    if (matchesKeybinding(e, keybindings.splitVertical)) {
      e.preventDefault();
      if (activePane) splitPane(activePane, 'vertical');
      return;
    }
    if (matchesKeybinding(e, keybindings.splitHorizontal)) {
      e.preventDefault();
      if (activePane) splitPane(activePane, 'horizontal');
      return;
    }
    if (matchesKeybinding(e, keybindings.closePane)) {
      e.preventDefault();
      if (activePane) requestClosePane(activePane);
      return;
    }
    if (matchesKeybinding(e, keybindings.nextPane)) {
      e.preventDefault();
      cyclePanes(1);
      return;
    }
    if (matchesKeybinding(e, keybindings.prevPane)) {
      e.preventDefault();
      cyclePanes(-1);
      return;
    }
  }

  // ── Window Resize ───────────────────────────────────────────────────────────

  var resizeDebounce = null;
  function onWindowResize() {
    if (resizeDebounce) clearTimeout(resizeDebounce);
    resizeDebounce = setTimeout(function() {
      resizeDebounce = null;
      refitAll();
    }, 100);
  }

  // ── beforeunload ───────────────────────────────────────────────────────────

  window.addEventListener('beforeunload', function() {
    var panes = getAllPanes();
    for (var i = 0; i < panes.length; i++) {
      var p = panes[i];
      if (p.ws && p.ws.readyState === WebSocket.OPEN) {
        try { p.ws.send(JSON.stringify({ type: 'closing' })); } catch (e) { /* ignore */ }
      }
    }

    if (serverSettings.deleteOnClose && ownerToken) {
      try {
        fetch('/api/tabs/' + window.TAB_ID, { method: 'DELETE', keepalive: true });
      } catch (e) { /* ignore */ }
    }
  });

  // ── Tab WS Message Handling ─────────────────────────────────────────────────

  function handleTabWSMessage(event) {
    var msg;
    try {
      msg = JSON.parse(event.data);
    } catch (e) {
      return;
    }

    switch (msg.type) {
      case 'session_created':
        // A split was confirmed by the server.
        applySplit(msg.pane_session_id, msg.id, msg.direction);
        break;

      case 'pane_closed':
        // A pane close was confirmed by the server.
        applyClosePane(msg.id, msg.layout);
        break;

      case 'disconnected':
        // We've been taken over.
        showTakeoverBanner();
        break;

      case 'error':
        console.error('Tab WS error:', msg.message);
        break;
    }
  }

  // ── Conflict Modal ──────────────────────────────────────────────────────────

  var conflictResolver = null;

  function showConflictModal() {
    document.getElementById('conflict-modal').style.display = '';
    return new Promise(function(resolve) {
      conflictResolver = resolve;
    });
  }

  function hideConflictModal() {
    document.getElementById('conflict-modal').style.display = 'none';
  }

  window._resolveConflict = function(choice) {
    hideConflictModal();
    if (conflictResolver) {
      conflictResolver(choice);
      conflictResolver = null;
    }
  };

  // ── Takeover Banner ─────────────────────────────────────────────────────────

  function showTakeoverBanner() {
    // Dispose all panes and show the takeover message.
    if (root) {
      walkLeaves(root, function(p) { disposePane(p); });
    }
    rootContainer.innerHTML = '';
    root = null;
    activePane = null;

    if (tabWS) {
      try { tabWS.close(); } catch (e) { /* ignore */ }
      tabWS = null;
    }
    ownerToken = '';

    document.getElementById('takeover-banner').style.display = '';
  }

  // ── Tab WS Connection ──────────────────────────────────────────────────────

  function connectTabWS() {
    return new Promise(function(resolve, reject) {
      var protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      var wsUrl = protocol + '//' + window.location.host + '/ws/tab/' + window.TAB_ID;

      var ws = new WebSocket(wsUrl);
      tabWS = ws;

      // We handle the first message specially (connected or conflict).
      var initialized = false;

      ws.onmessage = function(event) {
        var msg;
        try {
          msg = JSON.parse(event.data);
        } catch (e) {
          return;
        }

        if (!initialized) {
          initialized = true;

          if (msg.type === 'conflict') {
            // Show modal, get user choice, send resolution.
            showConflictModal().then(function(choice) {
              ws.send(JSON.stringify({ type: choice }));

              // Wait for connected response after resolution.
              ws.onmessage = function(event2) {
                var msg2;
                try {
                  msg2 = JSON.parse(event2.data);
                } catch (e) {
                  return;
                }

                if (msg2.type === 'connected') {
                  ownerToken = msg2.token || '';
                  // Switch to normal message handler.
                  ws.onmessage = handleTabWSMessage;
                  resolve(msg2);
                } else if (msg2.type === 'error') {
                  reject(new Error(msg2.message));
                }
              };
            });
            return;
          }

          if (msg.type === 'connected') {
            ownerToken = msg.token || '';
            // Switch to normal message handler.
            ws.onmessage = handleTabWSMessage;
            resolve(msg);
            return;
          }
        }
      };

      ws.onerror = function() {
        if (!initialized) reject(new Error('Tab WS connection failed'));
      };

      ws.onclose = function() {
        tabWS = null;
        if (!initialized) reject(new Error('Tab WS closed before init'));
      };
    });
  }

  // ── Init ────────────────────────────────────────────────────────────────────

  async function init() {
    rootContainer = document.getElementById('terminal-container');
    if (!rootContainer) {
      console.error('Missing #terminal-container element');
      return;
    }

    loadKeybindings();
    await loadServerSettings();

    // Step 1: Connect tab WS and wait for ownership resolution.
    var tabData;
    try {
      tabData = await connectTabWS();
    } catch (e) {
      console.error('Failed to connect tab WS:', e);
      return;
    }

    // Step 2: Build layout from the connected message.
    if (tabData.layout) {
      root = reconstructLayout(rootContainer, tabData.layout);
    }

    // Step 3: Focus first pane.
    var panes = getAllPanes();
    if (panes.length > 0) {
      setActivePane(panes[0]);
    }

    // Keyboard shortcuts
    document.addEventListener('keydown', handleKeyboardShortcuts, true);

    // Window resize
    window.addEventListener('resize', onWindowResize);
  }

  // ── Boot ────────────────────────────────────────────────────────────────────
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  // Export for external use
  window.SplitTerminal = {
    splitPane: splitPane,
    closePane: requestClosePane,
    getActivePane: getActivePane,
    getAllPanes: getAllPanes,
    setActivePane: setActivePane,
    refitAll: refitAll,
    cyclePanes: cyclePanes,
    syncLayout: syncLayout,
  };

})();

// ignorei18n_end
