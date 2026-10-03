import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import MailHtmlFrame from './MailHtmlFrame'

function frameDocument(): Document {
  const frame = screen.getByTitle('邮件 HTML 正文')
  const source = frame.getAttribute('srcdoc') ?? ''
  return new DOMParser().parseFromString(source, 'text/html')
}

describe('MailHtmlFrame', () => {
  it('使用严格 sandbox、no-referrer 和 iframe CSP', () => {
    render(<MailHtmlFrame html="<p>Hello</p>" />)
    const frame = screen.getByTitle('邮件 HTML 正文')
    expect(frame).toHaveAttribute('sandbox', 'allow-popups allow-popups-to-escape-sandbox')
    expect(frame).not.toHaveAttribute('sandbox', expect.stringContaining('allow-scripts'))
    expect(frame).not.toHaveAttribute('sandbox', expect.stringContaining('allow-same-origin'))
    expect(frame).toHaveAttribute('referrerpolicy', 'no-referrer')
    expect(frame).toHaveAttribute('tabindex', '0')

    const policy = frameDocument().querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute('content')
    expect(policy).toContain("default-src 'none'")
    expect(policy).toContain("script-src 'none'")
    expect(policy).toContain("connect-src 'none'")
    expect(policy).toContain("form-action 'none'")
    expect(policy).toContain("style-src 'none'")
    expect(policy).toContain("style-src-attr 'unsafe-inline'")
    const style = frameDocument().querySelector('style')?.textContent ?? ''
    expect(style).toBe('html{color-scheme:light}body{margin:16px;color:#202124;background:#fff;font:14px/1.55 system-ui,sans-serif;overflow-wrap:anywhere}a{color:#0b57d0}')
    expect(policy).toContain("style-src-elem 'sha256-tz0SsdZR/Dt8LtcpYurICpqvmiePU32suqdbNReHtdI='")
    expect(policy).toContain('img-src data:')
  })

  it('保留带样式的 HTTPS 按钮链接并安全重写', () => {
    render(
      <MailHtmlFrame html={'<a href="https://goodstack.example/status" style="display:inline-block;background:#1677ff;color:white">Check status</a>'} />,
    )
    const anchor = frameDocument().querySelector('a')
    expect(anchor?.textContent).toContain('Check status')
    expect(anchor?.getAttribute('href')).toBe('https://goodstack.example/status')
    expect(anchor?.getAttribute('target')).toBe('_blank')
    expect(anchor?.getAttribute('rel')).toBe('noopener noreferrer')
    expect(anchor?.getAttribute('style')).toContain('background')
  })

  it('移除可执行元素、事件、表单、远程图片和危险链接', () => {
    render(
      <MailHtmlFrame
        html={`
          <script>alert(1)</script>
          <form action="https://evil.example"><input><button>send</button></form>
          <img src="https://tracker.example/pixel" onerror="alert(2)" srcset="https://tracker.example/2x 2x">
          <a id="js" href="javascript:alert(3)" ping="https://tracker.example">js</a>
          <a id="data" href="data:text/html,bad" download>data</a>
          <a id="blob" href="blob:https://example.com/id">blob</a>
          <a id="relative" href="/account">relative</a>
          <map name="navigation"><area id="area-link" href="https://evil.example" shape="rect"></map>
        `}
      />,
    )
    const doc = frameDocument()
    expect(doc.querySelector('script, form, input, button, iframe, object, embed, svg, math, area')).toBeNull()
    expect(doc.querySelector('img')).toBeNull()
    expect(doc.body.innerHTML).not.toMatch(/onerror|srcset|ping|download/i)
    for (const id of ['js', 'data', 'blob', 'relative']) {
      expect(doc.getElementById(id)?.hasAttribute('href')).toBe(false)
    }
    expect(screen.getByText(/已阻止邮件中的远程图片/)).toBeInTheDocument()
  })
})
