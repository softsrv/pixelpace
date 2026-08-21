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
