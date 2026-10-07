"""Real-Chromium regression test for production CSP + MailHtmlFrame.

Run after ``npm run build`` from ``web``. The test serves the built SPA with
the literal CSP declared by the Go middleware and mocks only the JSON API.
"""

from __future__ import annotations

import json
import os
import re
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Thread
from urllib.parse import urlparse

from playwright.sync_api import Route, sync_playwright


REPO_ROOT = Path(__file__).resolve().parents[2]
DIST = REPO_ROOT / "internal" / "webui" / "dist"


def production_csp() -> str:
    source = (REPO_ROOT / "internal" / "server" / "middleware.go").read_text(encoding="utf-8")
    match = re.search(r'"Content-Security-Policy":\s*"([^"]+)"', source)
    if match is None:
        raise AssertionError("production Content-Security-Policy was not found")
    return match.group(1)


class SPAHandler(SimpleHTTPRequestHandler):
    csp = production_csp()
    extensions_map = {
        **SimpleHTTPRequestHandler.extensions_map,
        ".css": "text/css",
        ".js": "text/javascript",
    }

    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(DIST), **kwargs)

    def end_headers(self) -> None:
        self.send_header("Content-Security-Policy", self.csp)
        super().end_headers()

    def do_GET(self) -> None:
        requested = DIST / self.path.lstrip("/").split("?", 1)[0]
        if self.path.startswith("/api/") or requested.is_file():
            super().do_GET()
            return
        self.path = "/index.html"
        super().do_GET()

    def log_message(self, _format: str, *args: object) -> None:
        return


ACCOUNT = {
    "id": "acc_1",
    "name": "Browser test",
    "real_email": "owner@example.test",
    "icloud_email": "owner@icloud.test",
    "host": "icloud.com",
    "status": "active",
    "alias_total": 1,
    "alias_active": 1,
    "has_cookies": False,
    "has_app_password": True,
    "has_proxy": False,
    "last_validated": "2026-10-03T09:00:00+08:00",
    "created_at": "2026-10-03T09:00:00+08:00",
}

MESSAGE = {
    "id": "1",
    "from": "sender@example.test",
    "to": "owner@icloud.test",
    "subject": "Goodstack browser probe",
    "date": "2026-10-03T10:00:00+08:00",
    "preview": "Check status",
}

MAIL_HTML = """
<div><style id="mail-style">
  #mail-style-probe { color: rgb(1, 2, 3); background-image: url('https://style.invalid/pixel'); }
</style></div>
<div id="mail-style-probe">Mail style probe</div>
<div id="inline-url-probe"
     style="background-image:url('https://style.invalid/inline')">Inline URL probe</div>
<a id="goodstack" href="https://goodstack.example/status"
   style="display:inline-block;background:#1677ff;color:white">Check status</a>
<script>window.__mailScriptRan = true; fetch('https://connect.invalid/probe')</script>
<form action="https://form.invalid/submit"><button>Submit</button></form>
<img src="https://image.invalid/pixel">
"""


def mock_api(route: Route) -> None:
    path = urlparse(route.request.url).path
    if path == "/api/auth/session":
        data = {"csrf_token": "browser-test", "expires_at": "2026-10-03T11:00:00+08:00"}
    elif path == "/api/accounts":
        data = [ACCOUNT]
    elif path == "/api/aliases":
        data = {"account_id": "acc_1", "count": 0, "aliases": []}
    elif path == "/api/inbox/1":
        data = {**MESSAGE, "body": "Check status", "html_body": MAIL_HTML, "content_type": "text/html"}
    elif path == "/api/inbox":
        data = {"account_id": "acc_1", "count": 1, "method": "imap", "messages": [MESSAGE]}
    else:
        route.fulfill(status=404, content_type="application/json", body='{"success":false}')
        return
    route.fulfill(status=200, content_type="application/json", body=json.dumps({"success": True, "data": data}))


def main() -> None:
    if not (DIST / "index.html").is_file():
        raise AssertionError("built SPA not found; run `npm run build` from web first")

    server = ThreadingHTTPServer(("127.0.0.1", 0), SPAHandler)
    Thread(target=server.serve_forever, daemon=True).start()
    remote_requests: list[str] = []
    remote_failures: list[tuple[str, str | None]] = []
    remote_responses: list[str] = []
    try:
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True, executable_path=os.environ.get("PLAYWRIGHT_CHROMIUM_EXECUTABLE"))
            page = browser.new_page()
            diagnostics: list[str] = []
            page.on("console", lambda message: diagnostics.append(f"console {message.type}: {message.text}"))
            page.on("pageerror", lambda error: diagnostics.append(f"pageerror: {error}"))
            page.route("**/api/**", mock_api)
            page.on(
                "request",
                lambda request: remote_requests.append(request.url)
                if urlparse(request.url).hostname
                in {"connect.invalid", "image.invalid", "form.invalid", "style.invalid"}
                else None,
            )
            page.on(
                "requestfailed",
                lambda request: remote_failures.append((request.url, request.failure))
                if urlparse(request.url).hostname == "style.invalid"
                else None,
            )
            page.on(
                "response",
                lambda response: remote_responses.append(response.url)
                if urlparse(response.url).hostname == "style.invalid"
                else None,
            )
            page.goto(f"http://127.0.0.1:{server.server_port}/inbox")
            page.wait_for_load_state("networkidle")
            if page.get_by_role("button", name="Goodstack browser probe").count() == 0:
                raise AssertionError(
                    f"message button missing at {page.url}; body={page.locator('body').inner_text()!r}; "
                    f"diagnostics={diagnostics!r}"
                )
            page.get_by_role("button", name="Goodstack browser probe").click()

            iframe = page.locator('iframe[title="邮件 HTML 正文"]')
            iframe.wait_for()
            sandbox = iframe.get_attribute("sandbox") or ""
            assert sandbox == "allow-popups allow-popups-to-escape-sandbox"
            for forbidden in ("allow-scripts", "allow-same-origin", "allow-forms"):
                assert forbidden not in sandbox

            frame = page.frame_locator('iframe[title="邮件 HTML 正文"]')
            button = frame.locator("#goodstack")
            button.wait_for()
            computed = button.evaluate(
                "element => ({display: getComputedStyle(element).display, "
                "background: getComputedStyle(element).backgroundColor, "
                "color: getComputedStyle(element).color})"
            )
            assert computed == {
                "display": "inline-block",
                "background": "rgb(22, 119, 255)",
                "color": "rgb(255, 255, 255)",
            }
            style_probe = frame.locator("#mail-style-probe")
            assert frame.locator("#mail-style").count() == 1
            assert style_probe.evaluate("element => getComputedStyle(element).color") != "rgb(1, 2, 3)"
            assert style_probe.evaluate("element => getComputedStyle(element).backgroundImage") == "none"
            assert "style.invalid/inline" in frame.locator("#inline-url-probe").evaluate(
                "element => getComputedStyle(element).backgroundImage"
            )
            assert frame.locator("body").evaluate("element => getComputedStyle(element).paddingTop") == "24px"
            assert frame.locator("script, form, button, img").count() == 0
            assert frame.locator("body").evaluate("() => window.__mailScriptRan === true") is False
            page.wait_for_timeout(250)
            # Chrome versions differ on whether a CSP-blocked fetch emits a
            # network event. Both must explicitly reject it before any response.
            assert any('https://style.invalid/inline' in item and 'Content Security Policy' in item and 'blocked' in item for item in diagnostics), diagnostics
            assert remote_requests in ([], ["https://style.invalid/inline"]), remote_requests
            assert remote_failures in ([], [("https://style.invalid/inline", "csp")]), remote_failures
            assert remote_responses == []
            browser.close()
    finally:
        server.shutdown()
        server.server_close()

    print("MailHtmlFrame Chromium CSP test passed")


if __name__ == "__main__":
    main()
