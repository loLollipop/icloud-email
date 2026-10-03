import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import ConfirmDialog from './ConfirmDialog'

describe('ConfirmDialog', () => {
  it('busy 时禁止取消、Escape 和遮罩关闭', () => {
    const onClose = vi.fn()
    render(
      <>
        <button>背景操作</button>
        <ConfirmDialog
          title="删除邮件"
          message="邮件将永久删除"
          open
          busy
          onClose={onClose}
          onConfirm={vi.fn()}
        />
      </>,
    )

    const dialog = screen.getByRole('dialog', { name: '删除邮件' })
    expect(dialog).toHaveFocus()
    expect(screen.getByRole('button', { name: '取消' })).toBeDisabled()
    fireEvent.keyDown(document, { key: 'Tab' })
    expect(dialog).toHaveFocus()
    expect(screen.getByRole('button', { name: '背景操作' })).not.toHaveFocus()
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(screen.getByRole('presentation'))
    expect(onClose).not.toHaveBeenCalled()
  })
})
