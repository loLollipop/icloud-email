import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import Dialog from './Dialog'

describe('Dialog', () => {
  it('onClose identity 变化不会重新聚焦，Escape 使用最新回调', () => {
    const firstClose = vi.fn()
    const latestClose = vi.fn()
    const { rerender } = render(
      <Dialog title="测试对话框" open onClose={firstClose}>
        <button>第一个</button>
        <button>第二个</button>
      </Dialog>,
    )
    const second = screen.getByRole('button', { name: '第二个' })
    second.focus()

    rerender(
      <Dialog title="测试对话框" open onClose={latestClose}>
        <button>第一个</button>
        <button>第二个</button>
      </Dialog>,
    )

    expect(screen.getByRole('button', { name: '第二个' })).toHaveFocus()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(firstClose).not.toHaveBeenCalled()
    expect(latestClose).toHaveBeenCalledTimes(1)
  })

  it('当前焦点因按钮禁用而离开焦点集合时，Tab 聚焦第一个可用元素', () => {
    const { rerender } = render(
      <Dialog title="测试对话框" open onClose={vi.fn()}>
        <button>第一个</button>
        <button>第二个</button>
        <button>最后一个</button>
      </Dialog>,
    )
    const last = screen.getByRole('button', { name: '最后一个' })
    last.focus()

    rerender(
      <Dialog title="测试对话框" open onClose={vi.fn()}>
        <button>第一个</button>
        <button>第二个</button>
        <button disabled>最后一个</button>
      </Dialog>,
    )

    expect(screen.getByRole('button', { name: '最后一个' })).toHaveFocus()
    const dispatched = fireEvent.keyDown(document, { key: 'Tab' })
    expect(dispatched).toBe(false)
    expect(screen.getByRole('button', { name: '第一个' })).toHaveFocus()
  })

  it('当前焦点因按钮禁用而离开焦点集合时，Shift+Tab 聚焦最后一个可用元素', () => {
    const { rerender } = render(
      <Dialog title="测试对话框" open onClose={vi.fn()}>
        <button>第一个</button>
        <button>第二个</button>
        <button>最后一个</button>
      </Dialog>,
    )
    const last = screen.getByRole('button', { name: '最后一个' })
    last.focus()

    rerender(
      <Dialog title="测试对话框" open onClose={vi.fn()}>
        <button>第一个</button>
        <button>第二个</button>
        <button disabled>最后一个</button>
      </Dialog>,
    )

    expect(screen.getByRole('button', { name: '最后一个' })).toHaveFocus()
    const dispatched = fireEvent.keyDown(document, { key: 'Tab', shiftKey: true })
    expect(dispatched).toBe(false)
    expect(screen.getByRole('button', { name: '第二个' })).toHaveFocus()
  })
})
