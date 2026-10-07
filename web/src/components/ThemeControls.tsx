import { useEffect, useState } from 'react'
import { IconMonitor, IconMoon, IconSun } from './icons'

type Theme = 'system' | 'light' | 'dark'
const preferenceKey = 'icloud-mail-theme'

function readTheme(): Theme {
  try {
    const value = localStorage.getItem(preferenceKey)
    if (value === 'light' || value === 'dark') return value
  } catch { /* Theme controls also work without browser storage. */ }
  return 'system'
}

export default function ThemeControls() {
  const [theme, setTheme] = useState<Theme>(readTheme)
  const [systemDark, setSystemDark] = useState(() => window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false)
  const isDark = theme === 'dark' || (theme === 'system' && systemDark)
  const toggleLabel = isDark ? '切换为浅色' : '切换为深色'

  useEffect(() => {
    const media = window.matchMedia?.('(prefers-color-scheme: dark)')
    if (!media) return
    const update = (event: MediaQueryListEvent) => setSystemDark(event.matches)
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])

  useEffect(() => {
    if (theme === 'system') delete document.documentElement.dataset.theme
    else document.documentElement.dataset.theme = theme
    try {
      if (theme === 'system') localStorage.removeItem(preferenceKey)
      else localStorage.setItem(preferenceKey, theme)
    } catch { /* Only an optional appearance preference is persisted. */ }
  }, [theme])

  return (
    <div className="workspace-theme-controls" role="group" aria-label="外观设置">
      <button type="button" className="workspace-icon-button" aria-label={toggleLabel} title={toggleLabel}
        onClick={() => setTheme(isDark ? 'light' : 'dark')}>
        {isDark ? <IconMoon size={18} /> : <IconSun size={18} />}
      </button>
      <button type="button" className="workspace-icon-button workspace-system-theme" aria-label="跟随系统" title="跟随系统"
        aria-pressed={theme === 'system'} onClick={() => setTheme('system')}>
        <IconMonitor size={18} />
      </button>
    </div>
  )
}
