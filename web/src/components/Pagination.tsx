interface PaginationProps {
  page: number
  pageSize: number
  totalItems: number
  onPageChange: (page: number) => void
  onPageSizeChange: (pageSize: number) => void
  pageSizeOptions?: number[]
  label?: string
  disabled?: boolean
}

function pageNumbers(currentPage: number, totalPages: number): number[] {
  if (totalPages <= 5) return Array.from({ length: totalPages }, (_, index) => index + 1)
  const start = Math.max(2, Math.min(currentPage - 1, totalPages - 3))
  return [1, ...Array.from({ length: 3 }, (_, index) => start + index), totalPages]
}

export default function Pagination({
  page,
  pageSize,
  totalItems,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = [10, 20, 50],
  label = '分页',
  disabled = false,
}: PaginationProps) {
  const totalPages = Math.max(1, Math.ceil(totalItems / pageSize))
  const currentPage = Math.min(Math.max(1, page), totalPages)
  const firstItem = totalItems === 0 ? 0 : (currentPage - 1) * pageSize + 1
  const lastItem = Math.min(currentPage * pageSize, totalItems)
  const pages = pageNumbers(currentPage, totalPages)

  return (
    <nav className="pagination" aria-label={label}>
      <span className="pagination-status" aria-live="polite">
        第 {firstItem}-{lastItem} 项，共 {totalItems} 项
      </span>
      <label className="pagination-size">
        <span>每页</span>
        <select
          aria-label="每页条数"
          value={pageSize}
          disabled={disabled}
          onChange={(event) => onPageSizeChange(Number(event.target.value))}
        >
          {pageSizeOptions.map((option) => (
            <option key={option} value={option}>{option}</option>
          ))}
        </select>
      </label>
      <div className="pagination-pages">
        <button
          type="button"
          onClick={() => onPageChange(currentPage - 1)}
          disabled={disabled || currentPage === 1}
          aria-label="上一页"
        >
          上一页
        </button>
        {pages.map((pageNumber, index) => (
          <span className="pagination-page-entry" key={pageNumber}>
            {index > 0 && pageNumber - pages[index - 1] > 1 && <span className="pagination-ellipsis" aria-hidden="true">…</span>}
            <button
              type="button"
              disabled={disabled}
              className={pageNumber === currentPage ? 'is-current' : undefined}
              aria-label={`第 ${pageNumber} 页`}
              aria-current={pageNumber === currentPage ? 'page' : undefined}
              onClick={() => onPageChange(pageNumber)}
            >
              {pageNumber}
            </button>
          </span>
        ))}
        <button
          type="button"
          onClick={() => onPageChange(currentPage + 1)}
          disabled={disabled || currentPage === totalPages}
          aria-label="下一页"
        >
          下一页
        </button>
      </div>
    </nav>
  )
}
