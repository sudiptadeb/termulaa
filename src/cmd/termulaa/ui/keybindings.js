// Shared by app.js (behaviour) and settings.html (reference) so displayed
// shortcuts are always derived from the live bindings, never a copy.
(function() {
  'use strict';

  var isMac = navigator.platform.indexOf('Mac') !== -1;

  var defaults = {
    splitVertical:   { mod: true, shift: false, key: 'd' },
    splitHorizontal: { mod: true, shift: true,  key: 'd' },
    closePane:       { mod: true, shift: false, key: 'w' },
    nextPane:        { mod: true, shift: false, key: ']' },
    prevPane:        { mod: true, shift: false, key: '[' },
    showShortcuts:   { mod: true, shift: false, key: '/' },
  };

  var labels = {
    splitVertical:   'Split pane vertically',
    splitHorizontal: 'Split pane horizontally',
    closePane:       'Close pane',
    nextPane:        'Focus next pane',
    prevPane:        'Focus previous pane',
    showShortcuts:   'Shortcut guide',
  };

  function load() {
    var kb = {};
    for (var action in defaults) kb[action] = defaults[action];
    try {
      var stored = localStorage.getItem('terminalKeybindings');
      if (stored) {
        var custom = JSON.parse(stored);
        for (var action in custom) kb[action] = custom[action];
      }
    } catch (e) { /* ignore */ }
    return kb;
  }

  function keyName(key) {
    return key.length === 1 ? key.toUpperCase() : key;
  }

  function format(kb) {
    if (!kb || !kb.key) return '';
    if (isMac) {
      return (kb.alt ? '⌥' : '') + (kb.shift ? '⇧' : '') + (kb.mod ? '⌘' : '') + keyName(kb.key);
    }
    var parts = [];
    if (kb.mod) parts.push('Ctrl');
    if (kb.alt) parts.push('Alt');
    if (kb.shift) parts.push('Shift');
    parts.push(keyName(kb.key));
    return parts.join('+');
  }

  window.TerminalKeybindings = {
    isMac: isMac,
    defaults: defaults,
    labels: labels,
    load: load,
    format: format,
  };
})();
