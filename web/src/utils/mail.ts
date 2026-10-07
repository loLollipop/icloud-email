export function formatMailDate(raw: string, compact = false): string {
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return raw || '日期未知'
  if (!compact) return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date)
  const today = new Date()
  if (date.toDateString() === today.toDateString()) return new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit' }).format(date)
  return new Intl.DateTimeFormat('zh-CN', { ...(date.getFullYear() !== today.getFullYear() ? { year: 'numeric' as const } : {}), month: 'short', day: 'numeric' }).format(date)
}

export function readMailPreference(key: string, fallback: string): string {
  try { return localStorage.getItem(`icloud-mail:${key}`) ?? fallback } catch { return fallback }
}

export function saveMailPreference(key: string, value: string) {
  try { localStorage.setItem(`icloud-mail:${key}`, value) } catch { /* Storage may be disabled. UI still works for this session. */ }
}
