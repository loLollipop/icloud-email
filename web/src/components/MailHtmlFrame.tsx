import DOMPurify from 'dompurify'
import { useMemo } from 'react'

const forbiddenTags = [
  'script',
  'form',
  'input',
  'button',
  'iframe',
  'object',
  'embed',
  'base',
  'meta',
  'link',
  'svg',
  'math',
  'area',
]

const frameStyles = 'html{color-scheme:light;overflow-wrap:anywhere}body{box-sizing:border-box;margin:0 auto;padding:24px clamp(16px,4vw,48px);max-width:1000px;color:#202124;background:#fff;font:15px/1.7 -apple-system,BlinkMacSystemFont,Segoe UI,Microsoft YaHei,sans-serif;overflow-wrap:anywhere}img{max-width:100%!important;height:auto!important}table{max-width:100%!important;min-width:0!important;table-layout:fixed}td,th,div{min-width:0!important;max-width:100%!important;overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere}a{color:#0b57d0}@media(max-width:600px){body{padding:18px 14px}table{width:100%!important;min-width:0!important;table-layout:fixed!important}td,th{min-width:0!important;overflow-wrap:anywhere}div{max-width:100%!important;min-width:0!important}}'
// SHA-256 of frameStyles. The production response CSP repeats this hash so
// srcdoc can use the component stylesheet without enabling arbitrary <style>.
const frameStyleHash = "'sha256-eKr+7RSpsPrZqhQZ1gTR40o8Ir1CG8gkheULySs/9+A='"

const csp = [
  "default-src 'none'",
  "script-src 'none'",
  "connect-src 'none'",
  "object-src 'none'",
  "frame-src 'none'",
  "form-action 'none'",
  "base-uri 'none'",
  "style-src 'none'",
  "style-src-attr 'unsafe-inline'",
  `style-src-elem ${frameStyleHash}`,
  'img-src data:',
  'font-src data:',
].join('; ')

function sanitizeMailHtml(raw: string): string {
  const clean = DOMPurify.sanitize(raw, {
    FORBID_TAGS: forbiddenTags,
    FORBID_ATTR: ['ping', 'download', 'srcset'],
  })
  const template = document.createElement('template')
  template.innerHTML = clean

  template.content.querySelectorAll('*').forEach((element) => {
    for (const attribute of Array.from(element.attributes)) {
      if (attribute.name.toLowerCase().startsWith('on')) {
        element.removeAttribute(attribute.name)
      }
    }
  })

  template.content.querySelectorAll('a').forEach((anchor) => {
    const href = anchor.getAttribute('href')
    let safeURL: URL | null = null
    if (href) {
      try {
        const parsed = new URL(href)
        if (parsed.protocol === 'https:') safeURL = parsed
      } catch {
        // Relative and malformed links are intentionally disabled.
      }
    }
    if (safeURL) {
      anchor.setAttribute('href', safeURL.href)
      anchor.setAttribute('target', '_blank')
      anchor.setAttribute('rel', 'noopener noreferrer')
    } else {
      anchor.removeAttribute('href')
      anchor.removeAttribute('target')
      anchor.removeAttribute('rel')
    }
  })

  template.content.querySelectorAll('img').forEach((image) => {
    const source = image.getAttribute('src')?.trim() ?? ''
    if (!source.toLowerCase().startsWith('data:')) image.remove()
  })

  return template.innerHTML
}

function makeSrcDoc(raw: string): string {
  const body = sanitizeMailHtml(raw)
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><meta name="referrer" content="no-referrer"><style>${frameStyles}</style></head><body>${body}</body></html>`
}

interface MailHtmlFrameProps {
  html: string
}

export default function MailHtmlFrame({ html }: MailHtmlFrameProps) {
  const srcDoc = useMemo(() => makeSrcDoc(html), [html])

  return (
    <div className="mail-html-container">
      <p className="mail-privacy-notice">为保护隐私，已阻止邮件中的远程图片。</p>
      <iframe
        className="mail-html-frame"
        title="邮件 HTML 正文"
        sandbox="allow-popups allow-popups-to-escape-sandbox"
        referrerPolicy="no-referrer"
        // The isolated message document must remain keyboard reachable.
        // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
        tabIndex={0}
        srcDoc={srcDoc}
      />
    </div>
  )
}
