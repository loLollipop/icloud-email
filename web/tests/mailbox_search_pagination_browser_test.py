"""Browser regression for shared pagination, full inbox search and reading.

Run after npm run build. The production SPA/CSP is served locally and only
JSON responses are mocked; layout assertions exercise actual Chromium CSS.
"""

from __future__ import annotations

import json
import sys
sys.dont_write_bytecode = True
from pathlib import Path
from threading import Thread
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import Route, expect, sync_playwright

from mail_html_frame_browser_test import ACCOUNT, MESSAGE, SPAHandler
from http.server import ThreadingHTTPServer


ACCOUNTS = [{**ACCOUNT, "id": f"acc_{i}", "name": f"账号 {i}", "icloud_email": f"owner{i}@icloud.test"} for i in range(1, 24)]
ALIASES = [{"email": f"alias{i:02d}@icloud.test", "anonymousId": f"anon{i}", "label": f"项目 {i}", "active": True, "createdAt": f"2026-09-{i:02d}T10:00:00Z"} for i in range(1, 28)]
MESSAGES = [{**MESSAGE, "id": str(i), "from": "notifications_from_a_very_long_sender_address@example.test", "to": "alias01@icloud.test", "subject": f"历史合同审批邮件 {i}", "date": "2025-01-01T10:00:00Z", "preview": f"合同正文关键词 status {i}"} for i in range(1, 126)]
REQUESTS: list[dict[str, list[str]]] = []


def mock_api(route: Route) -> None:
    parsed = urlparse(route.request.url)
    params = parse_qs(parsed.query)
    path = parsed.path
    if path == "/api/auth/session":
        data = {"csrf_token": "browser-test", "expires_at": "2099-10-03T11:00:00+08:00"}
    elif path == "/api/accounts":
        data = ACCOUNTS
    elif path == "/api/aliases":
        data = {"account_id": "acc_1", "count": len(ALIASES), "aliases": ALIASES}
    elif path == "/api/inbox":
        REQUESTS.append(params)
        assert "limit" not in params and "days" not in params, params
        assert params["scope"] == ["hme_aliases"], params
        q = params.get("q", [""])[0]
        field = params.get("field", ["all"])[0]
        def matches(message):
            values = [message.get(field, "")] if field != "all" else [message["subject"], message["from"], message["to"], message["preview"]]
            if field == "body":
                values = [message["preview"]]
            return any(q.lower() in value.lower() for value in values)
        rows = [message for message in MESSAGES if matches(message)]
        size = int(params.get("page_size", ["20"])[0])
        page = min(int(params.get("page", ["1"])[0]), max(1, (len(rows) + size - 1) // size))
        selected = rows[(page - 1) * size:page * size]
        data = {"account_id": "acc_1", "total": len(rows), "count": len(selected), "page": page, "page_size": size, "method": "imap", "messages": selected}
    elif path.startswith("/api/inbox/"):
        uid = path.rsplit("/", 1)[1]
        message = next(message for message in MESSAGES if message["id"] == uid)
        data = {**message, "body": f"完整历史邮件正文 {uid}", "content_type": "text/plain"}
    else:
        route.fulfill(status=404, content_type="application/json", body='{"success":false}')
        return
    route.fulfill(status=200, content_type="application/json", body=json.dumps({"success": True, "data": data}))


def assert_layout(page, label: str, width: int) -> None:
    nav = page.get_by_role("navigation", name=label, exact=True)
    expect(nav).to_be_visible()
    nav.scroll_into_view_if_needed()
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth"), (label, width)
    buttons = nav.locator(".pagination-pages button")
    boxes = [button.bounding_box() for button in buttons.all()]
    # Include the ellipsis entries when measuring spacing: skipped page
    # numbers intentionally have an ellipsis between their buttons.
    entries = [entry.bounding_box() for entry in nav.locator(".pagination-pages > *").all()]
    for before, after in zip(entries, entries[1:]):
        assert before and after
        if abs(before["y"] - after["y"]) < 2:
            assert after["x"] - before["x"] - before["width"] <= 12, (label, width, entries)
    if width >= 900:
        status = nav.locator(".pagination-status").bounding_box()
        first = boxes[0]
        assert status and first and abs(status["y"] + status["height"] / 2 - first["y"] - first["height"] / 2) < 3, (label, width)


def main() -> None:
    server = ThreadingHTTPServer(("127.0.0.1", 0), SPAHandler)
    Thread(target=server.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{server.server_port}"
    screenshots = Path("D:/DevCache/Temp")
    try:
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True)
            page = browser.new_page(viewport={"width": 1440, "height": 1000})
            failures = []
            page.on("pageerror", lambda error: failures.append(str(error)))
            page.route("**/api/**", mock_api)
            for width in (1920, 1440, 900, 390, 320):
                page.set_viewport_size({"width": width, "height": 1000})
                for path, label in (("accounts", "账号列表分页"), ("aliases?account_id=acc_1", "别名列表分页"), ("inbox?account_id=acc_1", "邮件列表分页")):
                    page.goto(f"{base}/{path}")
                    page.wait_for_load_state("networkidle")
                    assert_layout(page, label, width)
                    nav = page.get_by_role("navigation", name=label, exact=True)
                    nav.get_by_role("button", name="下一页", exact=True).click()
                    expect(nav.get_by_role("button", name="第 2 页", exact=True)).to_have_attribute("aria-current", "page")
                    assert_layout(page, label, width)
                    nav.get_by_role("button", name="上一页", exact=True).click()
                    expect(nav.get_by_role("button", name="第 1 页", exact=True)).to_have_attribute("aria-current", "page")
                    nav.get_by_label("每页条数").select_option("20")
                    assert_layout(page, label, width)

            page.set_viewport_size({"width": 1440, "height": 1000})
            page.goto(f"{base}/inbox?account_id=acc_1")
            page.wait_for_load_state("networkidle")
            search = page.get_by_role("searchbox", name="搜索邮件")
            search.fill("历史合同审批邮件 125")
            expect(page.get_by_role("button", name="历史合同审批邮件 125", exact=True)).to_be_visible()
            assert REQUESTS[-1].get("q") == ["历史合同审批邮件 125"]
            assert REQUESTS[-1].get("page") == ["1"]
            page.get_by_role("button", name="历史合同审批邮件 125", exact=True).click()
            expect(page.get_by_text("完整历史邮件正文 125", exact=True)).to_be_visible()
            expect(page.get_by_label("邮件列表", exact=True)).to_have_count(0)
            page.get_by_role("button", name="← 返回全部邮件").click()
            expect(search).to_have_value("历史合同审批邮件 125")

            search.fill("合同")
            search.press("Enter")
            nav = page.get_by_role("navigation", name="邮件列表分页", exact=True)
            expect(nav.get_by_text("第 1-20 项，共 125 项")).to_be_visible()
            nav.get_by_role("button", name="下一页", exact=True).click()
            expect(page.get_by_role("button", name="历史合同审批邮件 21", exact=True)).to_be_visible()
            page.get_by_role("button", name="历史合同审批邮件 21", exact=True).click()
            expect(page.get_by_text("完整历史邮件正文 21", exact=True)).to_be_visible()
            page.get_by_role("button", name="← 返回全部邮件").click()
            expect(nav.get_by_role("button", name="第 2 页", exact=True)).to_have_attribute("aria-current", "page")
            page.screenshot(path=str(screenshots / "icloud-inbox-search-desktop.png"), full_page=True)
            page.set_viewport_size({"width": 390, "height": 1000})
            assert_layout(page, "邮件列表分页", 390)
            page.evaluate("window.scrollTo(0, 0)")
            page.screenshot(path=str(screenshots / "icloud-inbox-search-mobile.png"), full_page=True)
            search.fill("不存在的邮件关键词")
            expect(page.get_by_text("没有找到匹配的邮件，试试其他关键词", exact=True)).to_be_visible()
            assert not failures, failures
            browser.close()
    finally:
        server.shutdown()
        server.server_close()
    print("Chromium: all three pagination layouts pass at 1920/1440/900/390/320px; server search, real pages and detail return pass")


if __name__ == "__main__":
    main()
