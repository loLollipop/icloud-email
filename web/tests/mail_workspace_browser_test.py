"""Viewport, mail navigation and visual regression with synthetic mail only."""
from __future__ import annotations

import json
import os
from pathlib import Path
from threading import Thread
from http.server import ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse
from playwright.sync_api import expect, sync_playwright
from mail_html_frame_browser_test import ACCOUNT, SPAHandler

OUT = Path(os.environ.get('MAIL_WORKSPACE_SCREENSHOTS', 'D:/DevCache/Temp/icloud-workspace-redesign-20261007/screenshots'))
TITLES = ['你的申请已通过审核', '十月产品更新与使用指南', '登录验证码', '本月账单已准备就绪', '欢迎加入设计协作空间', '你的订阅即将续费', '活动报名确认', '本周阅读清单']
MESSAGES = [{'id': str(i), 'subject': TITLES[(i-1) % len(TITLES)], 'from': 'Workspace <hello@example.test>', 'to': ['design.notes@icloud.test', 'daily.work@icloud.test', 'paper.reading@icloud.test'][(i-1) % 3], 'date': f'2026-10-07T{18-i%10:02d}:30:00+08:00', 'preview': ['感谢你的耐心等待，你现在可以使用全部工作区功能。', '本次更新带来了更清晰的邮件视图与更顺畅的阅读体验。', '请使用邮件中的验证码完成登录。'][i % 3]} for i in range(1, 26)]
SHORT_HTML = '<h2 style="font-size:24px;font-weight:600;margin:0 0 20px">你的申请已通过审核</h2><p>你好，</p><p>你的申请已经通过审核。感谢你与我们一起，让好的想法继续发生。</p><p>现在可以前往工作区，查看你的账户权益和接下来的使用说明。</p><p style="margin:28px 0"><a href="https://example.test/workspace" style="display:inline-block;background:#0969ed;color:white;text-decoration:none;padding:11px 24px;border-radius:7px">前往工作区 →</a></p><p style="font-size:12px;color:#748094;border-top:1px solid #eee;padding-top:20px">Workspace 团队 · 这封邮件发送至你的隐藏邮箱</p>'
LONG_HTML = SHORT_HTML + ''.join(f'<h3>更新记录 {i}</h3><p>清楚地阅读每一封邮件，将时间留给更重要的事情。' + '邮件内容与链接保持原有语义，长内容在正文区域自然滚动。' * 8 + '</p>' for i in range(1, 13)) + '<p id="mail-end">邮件结束</p>'
WIDE_HTML = '<table width="1200" style="width:1200px;min-width:1200px"><tr><td><div style="width:1000px;min-width:1000px">' + 'long-unbroken-content-' * 20 + '</div></td></tr></table><p id="wide-end">宽邮件结束</p>'

def mock_api(route):
    parsed = urlparse(route.request.url)
    p = parse_qs(parsed.query)
    if parsed.path == '/api/auth/session':
        data = {'csrf_token': 'preview-only', 'expires_at': '2099-01-01T00:00:00Z'}
    elif parsed.path == '/api/accounts':
        data = [{**ACCOUNT, 'name': '我的 iCloud', 'icloud_email': 'my.mail@icloud.test', 'alias_total': 68, 'alias_active': 65, 'has_cookies': True}, {**ACCOUNT, 'id': 'acc_2', 'name': '备用邮箱', 'icloud_email': 'work.mail@icloud.test', 'alias_total': 12, 'alias_active': 12, 'status': 'pending', 'has_app_password': False}]
        data.extend({**ACCOUNT, 'id': f'acc_{i}', 'name': f'工作邮箱 {i}', 'icloud_email': f'work.{i}@icloud.test'} for i in range(3, 27))
    elif parsed.path == '/api/aliases':
        data = {'aliases': [{'email': f'{name}@icloud.test', 'anonymousId': str(i), 'label': label, 'active': i != 4, 'createdAt': '2026-10-01T00:00:00Z'} for i, (name, label) in enumerate([('design.notes', '设计与协作'), ('daily.work', '日常工作'), ('paper.reading', '阅读订阅'), ('shopping.list', '购物与账单'), ('archive.box', '历史项目')])]}
        data['aliases'].extend({'email': f'private.{i}@icloud.test', 'anonymousId': str(i), 'label': f'订阅 {i}', 'active': True, 'createdAt': '2026-10-01T00:00:00Z'} for i in range(5, 68))
    elif parsed.path == '/api/auth/logout':
        data = {}
    elif parsed.path == '/api/inbox':
        rows = [m for m in MESSAGES if p.get('q', [''])[0].lower() in m['subject'].lower()]
        size = int(p.get('page_size', ['20'])[0]); page = int(p.get('page', ['1'])[0])
        data = {'account_id': 'acc_1', 'count': len(rows[(page-1)*size:page*size]), 'total': len(rows), 'page': page, 'page_size': size, 'method': 'imap', 'messages': rows[(page-1)*size:page*size]}
    elif parsed.path.startswith('/api/inbox/'):
        uid = int(parsed.path.rsplit('/', 1)[1])
        data = {**MESSAGES[uid-1], 'body': '纯文本邮件正文\n' + ('这里是一段完整的邮件正文，支持选中复制。\n' * 80 if uid == 4 else '感谢你的耐心等待。'), 'content_type': 'text/html'}
        if uid != 4: data['html_body'] = LONG_HTML if uid == 2 else WIDE_HTML if uid == 3 else SHORT_HTML
    else:
        route.fulfill(status=404, body='{"success":false}')
        return
    route.fulfill(content_type='application/json', body=json.dumps({'success': True, 'data': data}))

def no_outer_scroll(page):
    if not page.evaluate('document.documentElement.scrollHeight <= innerHeight + 1 && document.documentElement.scrollWidth <= innerWidth + 1'):
        page.screenshot(path=str(OUT / 'overflow-failure.png'))
        raise AssertionError((page.url, page.viewport_size, page.evaluate('Array.from(document.querySelectorAll("body *")).filter(e => e.getBoundingClientRect().right > innerWidth + 1).slice(0,10).map(e => [e.tagName,e.className,e.getBoundingClientRect().right])')))

def main():
    OUT.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(('127.0.0.1', 0), SPAHandler)
    Thread(target=server.serve_forever, daemon=True).start()
    base = f'http://127.0.0.1:{server.server_port}'
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True, executable_path=os.environ.get('PLAYWRIGHT_CHROMIUM_EXECUTABLE'))
            page = browser.new_page(viewport={'width': 1440, 'height': 900}, color_scheme='light')
            errors = []
            page.on('pageerror', lambda err: errors.append(str(err)))
            page.route('**/api/**', mock_api)
            page.goto(base + '/inbox')
            expect(page.locator('.mail-list-item')).to_have_count(20)
            page.screenshot(path=str(OUT / 'inbox-desktop.png'))
            no_outer_scroll(page)
            rows = page.locator('.mail-list')
            rows.evaluate('e => e.scrollTop = 230')
            saved_top = rows.evaluate('e => e.scrollTop')
            page.locator('[data-message-id="4"]').click()
            expect(page.locator('.mail-body')).to_be_visible()
            no_outer_scroll(page)
            assert page.locator('.mail-body').evaluate('e => e.scrollHeight > e.clientHeight')
            page.get_by_role('button', name='← 返回全部邮件').click()
            assert rows.evaluate('e => e.scrollTop') == saved_top
            expect(page.locator('[data-message-id="4"]')).to_be_focused()
            page.locator('[data-message-id="1"]').click()
            expect(page.frame_locator('iframe').locator('h2')).to_be_visible()
            page.screenshot(path=str(OUT / 'reader-desktop.png'))
            no_outer_scroll(page)
            box = page.locator('iframe').bounding_box()
            assert box and box['height'] >= 470, box
            # In-page and browser history navigation share the same reading state.
            page.get_by_role('button', name='下一封邮件').click()
            expect(page.frame_locator('iframe').locator('#mail-end')).to_have_count(1)
            page.go_back()
            expect(page.locator('.mail-list')).to_be_visible()
            page.go_forward()
            expect(page.frame_locator('iframe').locator('#mail-end')).to_have_count(1)
            for width, height in [(1440, 900), (1366, 768), (390, 844), (320, 640), (844, 390)]:
                page.set_viewport_size({'width': width, 'height': height})
                no_outer_scroll(page)
                frame = page.frame_locator('iframe')
                end = frame.locator('#mail-end')
                end.scroll_into_view_if_needed()
                expect(end).to_be_visible()
                expect(page.get_by_role('button', name='← 返回全部邮件')).to_be_in_viewport()
                no_outer_scroll(page)
                frame.locator('body').evaluate('() => window.scrollTo(0,0)')
                if width == 390: page.screenshot(path=str(OUT / 'reader-mobile.png'))
            page.set_viewport_size({'width': 390, 'height': 844})
            page.get_by_role('button', name='下一封邮件').click()
            expect(page.frame_locator('iframe').locator('#wide-end')).to_be_visible()
            for width in (1440, 900, 390, 320):
                page.set_viewport_size({'width': width, 'height': 844})
                assert page.frame_locator('iframe').locator('body').evaluate('() => document.documentElement.scrollWidth <= innerWidth + 1'), width
            page.set_viewport_size({'width': 390, 'height': 844})
            page.get_by_role('button', name='专注阅读', exact=True).click()
            expect(page.locator('.workspace-sidebar')).to_be_hidden()
            page.get_by_role('button', name='退出专注阅读').click()
            expect(page.locator('.workspace-sidebar')).to_be_visible()
            page.get_by_role('button', name='← 返回全部邮件').click()
            page.screenshot(path=str(OUT / 'inbox-mobile.png'))
            for path in ['accounts', 'aliases', 'help']:
                sizes = [(1440, 900), (1366, 768), (390, 844), (320, 640)]
                if path == 'accounts': sizes.extend([(1159, 768), (900, 768), (760, 768), (451, 844)])
                for width, height in sizes:
                    page.set_viewport_size({'width': width, 'height': height})
                    page.goto(base + '/' + path)
                    page.wait_for_load_state('networkidle')
                    no_outer_scroll(page)
                    if path in ['accounts', 'aliases']:
                        region = page.locator('.management-scroll-region')
                        pagination = page.locator('.management-content > .pagination')
                        # A full page of data must scroll inside the panel, leaving controls visible.
                        assert region.evaluate('e => e.clientHeight >= 80 && e.scrollHeight > e.clientHeight'), (path, width, region.bounding_box())
                        assert page.locator('.management-page').evaluate('e => e.scrollHeight <= e.clientHeight + 1'), (path, width)
                        before = pagination.bounding_box()
                        region.evaluate('e => e.scrollTop = e.scrollHeight')
                        assert region.evaluate('e => e.scrollTop > 0')
                        assert pagination.bounding_box() == before
                        expect(pagination).to_be_in_viewport()
                        if path == 'accounts':
                            # Every action is directly available without links or an expander.
                            row = page.get_by_role('row', name='我的 iCloud', exact=True)
                            row.scroll_into_view_if_needed()
                            actions = row.get_by_role('group', name='账户操作 · 我的 iCloud')
                            expect(actions.get_by_role('button')).to_have_count(7)
                            expect(row.get_by_role('link')).to_have_count(0)
                            edit = actions.get_by_role('button', name='编辑 · 我的 iCloud', exact=True)
                            edit.focus()
                            page.keyboard.press('Tab')
                            expect(actions.get_by_role('button', name='更新 Cookie · 我的 iCloud', exact=True)).to_be_focused()
                            for _ in range(4): page.keyboard.press('Tab')
                            proxy = actions.get_by_role('button', name='设置代理 · 我的 iCloud', exact=True)
                            expect(proxy).to_be_focused()
                            expect(proxy).to_be_in_viewport()
                            page.keyboard.press('Enter')
                            expect(page.get_by_role('dialog', name='设置代理')).to_be_visible()
                            page.keyboard.press('Escape')
                            expect(page.get_by_role('dialog', name='设置代理')).to_have_count(0)
                            expect(proxy).to_be_focused()
                            no_outer_scroll(page)
                        page.get_by_role('button', name='下一页', exact=True).click()
                        expect(pagination).to_contain_text('第 11-20 项')
                        page.get_by_role('button', name='上一页', exact=True).click()
                        region.evaluate('e => e.scrollTop = 0')
                        no_outer_scroll(page)
                    if path == 'aliases':
                        # Destructive action labels must not match the button fill.
                        assert page.locator('.alias-secondary-actions button.danger').first.evaluate('e => getComputedStyle(e).color !== getComputedStyle(e).backgroundColor')
                    if width != 320: page.screenshot(path=str(OUT / f'{path}-{width}.png'))
            # A single long account stays compact without overflowing any layout breakpoint.
            def single_account_api(route):
                account = {**ACCOUNT, 'name': '工作与订阅专用的 iCloud 邮箱账户', 'icloud_email': 'long.account.address.for.layout.regression@icloud.test'}
                route.fulfill(content_type='application/json', body=json.dumps({'success': True, 'data': [account]}))
            page.route('**/api/accounts', single_account_api)
            for width, height in [(1440, 900), (900, 768), (760, 768), (451, 844), (390, 844)]:
                page.set_viewport_size({'width': width, 'height': height})
                page.goto(base + '/accounts')
                expect(page.locator('.account-row')).to_have_count(1)
                expect(page.locator('.account-identity')).to_contain_text('long.account.address.for.layout.regression@icloud.test')
                no_outer_scroll(page)
                page.screenshot(path=str(OUT / f'account-single-{width}.png'))
            page.unroute('**/api/accounts', single_account_api)
            page.set_viewport_size({'width': 1440, 'height': 900})
            page.goto(base + '/inbox')
            page.get_by_role('button', name='切换为深色', exact=True).click()
            expect(page.locator('.mail-list-item')).to_have_count(20)
            expect(page.get_by_role('button', name='刷新邮件')).to_be_enabled()
            page.screenshot(path=str(OUT / 'inbox-dark.png'))
            assert page.locator('html').get_attribute('data-theme') == 'dark'
            page.locator('[data-message-id="1"]').click()
            expect(page.frame_locator('iframe').locator('h2')).to_be_visible()
            page.get_by_role('button', name='纯文本', exact=True).click()
            expect(page.locator('.mail-body')).to_contain_text('纯文本邮件正文')
            page.get_by_role('button', name='原始排版', exact=True).click()
            expect(page.locator('iframe')).to_be_visible()
            page.get_by_role('button', name='切换为浅色', exact=True).click()
            assert page.locator('html').get_attribute('data-theme') == 'light'
            system = page.get_by_role('button', name='跟随系统', exact=True)
            system.click()
            expect(system).to_have_attribute('aria-pressed', 'true')
            page.emulate_media(color_scheme='dark')
            expect(page.get_by_role('button', name='切换为浅色', exact=True)).to_be_visible()
            assert page.locator('html').get_attribute('data-theme') is None
            page.get_by_role('button', name='切换为浅色', exact=True).click()
            assert page.locator('html').get_attribute('data-theme') == 'light'
            page.reload()
            expect(page.get_by_role('button', name='切换为深色', exact=True)).to_be_visible()
            expect(system).to_have_attribute('aria-pressed', 'false')
            repository = page.get_by_role('link', name='打开 GitHub 仓库 loLollipop/icloud-email')
            expect(repository).to_have_attribute('href', 'https://github.com/loLollipop/icloud-email')
            expect(repository).to_be_in_viewport()
            expect(page.get_by_role('button', name='退出登录', exact=True)).to_be_hidden()
            page.locator('.workspace-user-trigger').click()
            expect(page.get_by_role('button', name='退出登录', exact=True)).to_be_in_viewport()
            page.get_by_role('button', name='退出登录', exact=True).click()
            expect(page.get_by_role('button', name='进入工作区')).to_be_visible()
            assert not errors, errors
            browser.close()
    finally:
        server.shutdown(); server.server_close()
    print(f'Mail workspace: viewport, long/wide HTML, plaintext, return/scroll/focus, history, dark theme passed; screenshots: {OUT}')

if __name__ == '__main__': main()
