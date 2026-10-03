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

const frameStyles = 'html{color-scheme:light}body{margin:16px;color:#202124;background:#fff;font:14px/1.55 system-ui,sans-serif;overflow-wrap:anywhere}a{color:#0b57d0}'
// SHA-256 of frameStyles. The production response CSP repeats this hash so
// srcdoc can use the component stylesheet without enabling arbitrary <style>.
const frameStyleHash = "'sha256-tz0SsdZR/Dt8LtcpYurICpqvmiePU32suqdbNReHtdI='"

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
