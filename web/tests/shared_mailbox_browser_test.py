"""Customer-link UI/CSP regression with synthetic messages and no credentials."""
from __future__ import annotations

import json
import os
from http.server import ThreadingHTTPServer
from pathlib import Path
from threading import Thread
from urllib.parse import urlparse

from playwright.sync_api import expect, sync_playwright
from mail_html_frame_browser_test import SPAHandler
from mail_workspace_browser_test import mock_api

OUT = Path(os.environ.get('SHARED_MAIL_SCREENSHOTS', 'D:/DevCache/Temp/icloud-mail-sharing-20261008/screenshots'))
INFO = {'email': 'customer.notes@icloud.test', 'created_at': '2026-10-08T06:00:00Z'}
MAIL = [{'id': str(i), 'subject': '你的申请已通过审核' if i == 1 else f'工作区更新 {i}', 'from': 'Workspace <hello@example.test>', 'to': INFO['email'], 'date': '2026-10-08T06:15:00Z', 'preview': '感谢你的耐心等待，可以打开查看完整邮件。'} for i in range(1, 21)]
HTML = '<h2>你的申请已通过审核</h2><p>你好，感谢你的耐心等待。</p><a href="https://example.test/workspace">前往工作区</a>' + ''.join(f'<p>阅读记录 {i}：' + '邮件在正文区域滚动。' * 20 + '</p>' for i in range(35))


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), SPAHandler)
    Thread(target=server.serve_forever, daemon=True).start()
    base = f'http://127.0.0.1:{server.server_port}'
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True, executable_path=os.environ.get('PLAYWRIGHT_CHROMIUM_EXECUTABLE'))
            page = browser.new_page(viewport={'width': 1440, 'height': 900}, color_scheme='light')
            revoked = False
            errors = []
            page.on('pageerror', lambda error: errors.append(str(error)))

            def api(route):
                path = urlparse(route.request.url).path
                assert not route.request.headers.get('cookie')
                assert not route.request.headers.get('x-csrf-token')
                assert route.request.headers.get('authorization') == 'Bearer synthetic-customer-token'
                assert route.request.method == 'GET'
                assert path.startswith('/api/shared'), f'Customer called admin endpoint: {path}'
                if revoked:
                    route.fulfill(status=404, content_type='application/json', body=json.dumps({'success': False, 'code': 'SHARE_UNAVAILABLE', 'message': '分发已终止'}))
                    return
                if path == '/api/shared':
                    data = INFO
                elif path == '/api/shared/inbox':
                    data = {**INFO, 'count': len(MAIL), 'total': len(MAIL), 'page': 1, 'page_size': 20, 'messages': MAIL}
                else:
                    data = {**MAIL[0], 'body': '纯文本邮件正文', 'html_body': HTML, 'content_type': 'text/html'}
                route.fulfill(content_type='application/json', body=json.dumps({'success': True, 'data': data}))

            page.route('**/api/**', api)
            for width, height in [(1440, 900), (900, 720), (390, 844), (320, 740)]:
                page.set_viewport_size({'width': width, 'height': height})
                page.goto(base + '/share#synthetic-customer-token')
                page.wait_for_load_state('networkidle')
                expect(page.locator('.mail-list-item')).to_have_count(20)
                assert page.evaluate('document.documentElement.scrollWidth <= innerWidth + 1 && document.documentElement.scrollHeight <= innerHeight + 1')
                page.screenshot(path=str(OUT / f'customer-{width}.png'))
                page.get_by_role('button', name='你的申请已通过审核', exact=True).click()
                expect(page.locator('iframe')).to_be_visible()
                expect(page.get_by_role('button', name='删除邮件')).to_have_count(0)
                expect(page.get_by_role('link', name='配置邮件读取')).to_have_count(0)
                expect(page.frame_locator('iframe').get_by_role('link', name='前往工作区')).to_be_visible()
                assert page.evaluate('document.documentElement.scrollWidth <= innerWidth + 1 && document.documentElement.scrollHeight <= innerHeight + 1')
                page.screenshot(path=str(OUT / f'customer-reader-{width}.png'))
                page.get_by_role('button', name='← 返回全部邮件', exact=True).click()
                expect(page.get_by_role('button', name='你的申请已通过审核', exact=True)).to_be_focused()
            revoked = True
            page.evaluate('window.dispatchEvent(new Event("focus"))')
            expect(page.get_by_text('此链接无法继续使用')).to_be_visible()
            expect(page.locator('.mail-list-item')).to_have_count(0)
            expect(page.locator('iframe')).to_have_count(0)
            page.screenshot(path=str(OUT / 'customer-revoked.png'))
            assert not errors, errors

            admin = browser.new_page(viewport={'width': 1440, 'height': 900})
            active = False
            generation = 0

            def admin_api(route):
                nonlocal active, generation
                path = urlparse(route.request.url).path
                if path != '/api/aliases/0/share':
                    mock_api(route)
                    return
                if route.request.method != 'GET':
                    assert route.request.post_data_json == {'account_id': 'acc_1'}
                    assert route.request.headers.get('x-csrf-token') == 'preview-only'
                if route.request.method == 'POST':
                    active = True
                    generation += 1
                elif route.request.method == 'DELETE':
                    active = False
                data = {'active': active}
                if active:
                    data['created_at'] = INFO['created_at']
                if route.request.method == 'POST':
                    data['token'] = f'synthetic-new-link-{generation}'
                route.fulfill(content_type='application/json', body=json.dumps({'success': True, 'data': data}))

            admin.route('**/api/**', admin_api)
            admin.goto(base + '/aliases')
            admin.wait_for_load_state('networkidle')
            admin.get_by_role('button', name='分发邮箱 · design.notes@icloud.test').click()
            dialog = admin.get_by_role('dialog', name='分发邮箱')
            dialog.get_by_role('button', name='生成分发链接', exact=True).click()
            expect(dialog.get_by_label('客户访问链接')).to_have_value(base + '/share#synthetic-new-link-1')
            for width, height in [(1440, 900), (390, 844)]:
                admin.set_viewport_size({'width': width, 'height': height})
                expect(dialog.get_by_role('button', name='终止分发', exact=True)).to_be_visible()
                assert admin.evaluate('document.documentElement.scrollWidth <= innerWidth + 1 && document.documentElement.scrollHeight <= innerHeight + 1')
                admin.screenshot(path=str(OUT / f'admin-distribute-{width}.png'))
            dialog.get_by_role('button', name='关闭', exact=True).click()
            admin.get_by_role('button', name='分发邮箱 · design.notes@icloud.test').click()
            expect(dialog.get_by_role('button', name='重新生成', exact=True)).to_be_visible()
            expect(dialog.get_by_label('客户访问链接')).to_have_count(0)
            dialog.get_by_role('button', name='重新生成', exact=True).click()
            dialog.get_by_role('button', name='确认重新生成', exact=True).click()
            expect(dialog.get_by_label('客户访问链接')).to_have_value(base + '/share#synthetic-new-link-2')
            dialog.get_by_role('button', name='终止分发', exact=True).click()
            dialog.get_by_role('button', name='确认终止', exact=True).click()
            expect(dialog.get_by_text('未分发', exact=True)).to_be_visible()
            expect(dialog.get_by_label('客户访问链接')).to_have_count(0)
            admin.close()
            browser.close()
    finally:
        server.shutdown()
    print('PASS: customer isolation, read-only reader, CSP links, scrolling, revocation; admin generation, reopening, rotation and termination')


if __name__ == '__main__':
    main()
