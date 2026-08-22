// ── Password visibility toggle ────────────────────────────────────────────────

document.addEventListener('click', function(evt) {
  var btn = evt.target.closest('[data-toggle-pwd]');
  if (!btn) return;
  var input = document.getElementById(btn.getAttribute('data-toggle-pwd'));
  if (!input) return;
  var show = input.type === 'password';
  input.type = show ? 'text' : 'password';
  btn.querySelector('.eye').classList.toggle('hidden', show);
  btn.querySelector('.eye-off').classList.toggle('hidden', !show);
});

// ── Token expiry handling ─────────────────────────────────────────────────────

// When the server fires the token-expired event, attempt a silent refresh.
// If the refresh succeeds, replay the original request.
// If it fails, redirect to login.
document.body.addEventListener('token-expired', async function () {
  try {
    var res = await fetch('/auth/refresh', { method: 'POST' });
    if (res.ok) {
      // Replay by re-triggering HTMX on the active element.
      var active = document.querySelector('[hx-trigger]');
      if (active) htmx.trigger(active, 'retry');
    } else {
      window.location.href = '/login';
    }
  } catch (_) {
    window.location.href = '/login';
  }
});

// ── Concept2 Bluetooth connection ─────────────────────────────────────────────

(function () {
  var connectBtn = document.getElementById('bt-connect-btn');
  if (!connectBtn) return;

  var connectionInfo = document.getElementById('bt-connection-info');
  var concept2ServiceUuid = 'ce060000-43e5-11e4-916c-0800200c9a66';

  function showConnectionInfo(message) {
    if (!connectionInfo) return;
    connectionInfo.textContent = message;
    connectionInfo.classList.remove('hidden');
  }

  connectBtn.addEventListener('click', async function () {
    if (!window.isSecureContext || !navigator.bluetooth) {
      showConnectionInfo('Web Bluetooth is not available in this browser or this page is not a secure context. Use a supported browser over HTTPS or localhost.');
      return;
    }

    showConnectionInfo('Searching for a Concept2 rowing machine...');

    try {
      var device = await navigator.bluetooth.requestDevice({
        filters: [{ services: [concept2ServiceUuid] }],
        optionalServices: [concept2ServiceUuid]
      });

      device.addEventListener('gattserverdisconnected', function () {
        showConnectionInfo((device.name || 'Concept2 rowing machine') + ' — Disconnected');
      });

      var server = await device.gatt.connect();
      var status = server.connected ? 'Connected' : 'Connection status unknown';
      showConnectionInfo((device.name || 'Concept2 rowing machine') + ' — ' + status);
    } catch (err) {
      if (err && err.name === 'NotFoundError') {
        showConnectionInfo('No rowing machine selected. Choose a Concept2 rowing machine to connect.');
        return;
      }

      showConnectionInfo('Could not connect to the rowing machine. Please try again.');
    }
  });
})();
