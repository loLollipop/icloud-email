import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import Dialog from './Dialog'

describe('Dialog', () => {
  it('普通弹窗允许 Escape 和遮罩关闭', () => {
    const onClose = vi.fn()
    render(<Dialog title="普通弹窗" open onClose={onClose}><button>关闭</button></Dialog>)
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(screen.getByRole('presentation'))
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  it('busy 切换保持焦点圈定与原触发按钮，完成后恢复关闭', () => {
    const onClose = vi.fn()
    const trigger = document.createElement('button')
    document.body.appendChild(trigger)
    trigger.focus()
    const content = (busy: boolean, open = true) => (
      <Dialog title="保存弹窗" open={open} busy={busy} onClose={onClose}>
        <button>第一个</button><button>最后一个</button>
      </Dialog>
    )
    const { rerender, unmount } = render(content(false))
    const last = screen.getByRole('button', { name: '最后一个' })
    last.focus()
    rerender(content(true))
    expect(last).toHaveFocus()
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(screen.getByRole('presentation'))
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.keyDown(document, { key: 'Tab' })
    expect(screen.getByRole('button', { name: '第一个' })).toHaveFocus()
    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true })
    expect(last).toHaveFocus()
    rerender(content(false))
    expect(last).toHaveFocus()
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(screen.getByRole('presentation'))
    expect(onClose).toHaveBeenCalledTimes(2)
    rerender(content(false, false))
    expect(trigger).toHaveFocus()
    unmount()
    trigger.remove()
  })

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
