"""HTML for the OAuth screens: consent, error and the hop page. Every user and client value is escaped."""
from html import escape

STYLE = '''
:root{color-scheme:light dark;--bg:#f6f7f9;--card:#fff;--text:#16181d;--muted:#5b6270;--line:#d9dde4;--accent:#2457d6;--accent-text:#fff;--danger:#8f1d1d}
@media (prefers-color-scheme:dark){:root{--bg:#101216;--card:#1a1d24;--text:#eceef2;--muted:#a0a7b4;--line:#2f343e;--accent:#6b97ff;--accent-text:#0b1020;--danger:#ff9a9a}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:16px;background:var(--bg);color:var(--text);font:16px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{width:100%;max-width:440px;background:var(--card);border:1px solid var(--line);border-radius:12px;padding:24px}
h1{margin:0 0 12px;font-size:1.25rem;line-height:1.3}
p{margin:0 0 12px}
.muted{color:var(--muted);font-size:.9rem}
dl{margin:0 0 16px;display:grid;grid-template-columns:auto 1fr;gap:6px 12px}
dt{color:var(--muted)}dd{margin:0;overflow-wrap:anywhere}
.actions{display:flex;gap:12px}
button{flex:1;height:44px;padding:0 16px;border-radius:8px;border:1px solid var(--line);background:transparent;color:var(--text);font:inherit;cursor:pointer}
button.primary{background:var(--accent);border-color:var(--accent);color:var(--accent-text);font-weight:600}
.error{color:var(--danger)}
'''


def _page(title, body):
    return ('<!doctype html><html lang="en"><head><meta charset="utf-8">'
            '<meta name="viewport" content="width=device-width,initial-scale=1">'
            f'<meta name="robots" content="noindex"><title>{escape(title)}</title><style>{STYLE}</style></head>'
            f'<body><main>{body}</main></body></html>')


def consent(user_name, client_name, return_host, req_id, csrf):
    return _page('Connect to Multica', f'''
<h1>Allow access to Multica?</h1>
<dl>
<dt>You</dt><dd>{escape(user_name)}</dd>
<dt>Application</dt><dd>{escape(client_name)}</dd>
<dt>Returns to</dt><dd>{escape(return_host or '')}</dd>
<dt>Workspaces</dt><dd>all workspaces you belong to, including ones you join later</dd>
<dt>Actions</dt><dd>issues, projects and read-only lookups in Multica as you, limited to the commands this server allows</dd>
</dl>
<p class="muted">A separate token valid for 90 days will be created for this access. You can revoke it in your Multica token settings.</p>
<form method="post" action="/mcp/oauth/consent">
<input type="hidden" name="req" value="{escape(req_id)}"><input type="hidden" name="csrf" value="{escape(csrf)}">
<div class="actions"><button type="submit" name="decision" value="deny">Deny</button>
<button type="submit" name="decision" value="approve" class="primary">Allow</button></div>
</form>''')


def error(message):
    return _page('Connect to Multica', f'<h1 class="error">Could not connect</h1><p>{escape(message)}</p>'
                 '<p class="muted">Close this window and try connecting again from the application.</p>')


def continue_page(url):
    """A hop from our own page: the Multica session cookie (SameSite=Strict) is not sent on a navigation started elsewhere."""
    return ('<!doctype html><html lang="en"><head><meta charset="utf-8">'
            f'<meta http-equiv="refresh" content="0;url={escape(url)}"><meta name="robots" content="noindex">'
            f'<title>Multica</title></head><body><p><a href="{escape(url)}">Continue</a></p></body></html>')
