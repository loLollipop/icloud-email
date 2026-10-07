import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ThemeControls from './ThemeControls'

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
  delete document.documentElement.dataset.theme
})

it('跟随系统变化更新切换目标，手动主题不被系统变化覆盖', async () => {
  let listener: ((event: { matches: boolean }) => void) | undefined
  const remove = vi.fn()
  vi.stubGlobal('matchMedia', vi.fn(() => ({
    matches: true,
    addEventListener: (_type: string, callback: typeof listener) => { listener = callback },
    removeEventListener: remove,
  })))
  const user = userEvent.setup()
  const { unmount } = render(<ThemeControls />)
  expect(screen.getByRole('button', { name: '跟随系统' })).toHaveAttribute('aria-pressed', 'true')
  await user.click(screen.getByRole('button', { name: '切换为浅色' }))
  expect(document.documentElement).toHaveAttribute('data-theme', 'light')
  act(() => listener?.({ matches: false }))
  expect(document.documentElement).toHaveAttribute('data-theme', 'light')
  await user.click(screen.getByRole('button', { name: '跟随系统' }))
  expect(document.documentElement).not.toHaveAttribute('data-theme')
  act(() => listener?.({ matches: true }))
  expect(screen.getByRole('button', { name: '切换为浅色' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: '切换为浅色' }))
  expect(localStorage.getItem('icloud-mail-theme')).toBe('light')
  unmount()
  expect(remove).toHaveBeenCalledWith('change', listener)
})

describe('主题偏好恢复', () => {
  it('无效存储偏好回退系统，键盘可直接切换', async () => {
    localStorage.setItem('icloud-mail-theme', 'invalid')
    render(<ThemeControls />)
    const user = userEvent.setup()
    const toggle = screen.getByRole('button', { name: '切换为深色' })
    toggle.focus()
    await user.keyboard('{Enter}')
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    await user.keyboard(' ')
    expect(document.documentElement).toHaveAttribute('data-theme', 'light')
  })
})
