import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import Pagination from './Pagination'

describe('Pagination', () => {
  it('显示准确范围并支持页码与每页条数', async () => {
    const onPageChange = vi.fn()
    const onPageSizeChange = vi.fn()
    render(<Pagination page={2} pageSize={10} totalItems={25} onPageChange={onPageChange} onPageSizeChange={onPageSizeChange} />)

    expect(screen.getByText('第 11-20 项，共 25 项')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '第 2 页' })).toHaveAttribute('aria-current', 'page')
    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(onPageChange).toHaveBeenCalledWith(3)
    await userEvent.selectOptions(screen.getByLabelText('每页条数'), '20')
    expect(onPageSizeChange).toHaveBeenCalledWith(20)
  })

  it('只有一页和空列表仍显示状态并禁用边界按钮', () => {
    render(<Pagination page={1} pageSize={10} totalItems={0} onPageChange={() => undefined} onPageSizeChange={() => undefined} />)
    expect(screen.getByText('第 0-0 项，共 0 项')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled()
  })

  it('很多页保留首尾页并限制按钮数量，最后一页禁用下一页', () => {
    render(<Pagination page={100} pageSize={10} totalItems={1000} onPageChange={() => undefined} onPageSizeChange={() => undefined} />)
    expect(screen.getByRole('button', { name: '第 1 页' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '第 100 页' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getAllByRole('button')).toHaveLength(7)
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled()
  })
})
