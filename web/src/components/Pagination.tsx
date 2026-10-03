interface PaginationProps {
  page: number
  pageSize: number
  totalItems: number
  onPageChange: (page: number) => void
  onPageSizeChange: (pageSize: number) => void
  pageSizeOptions?: number[]
  label?: string
}

function pageNumbers(currentPage: number, totalPages: number): number[] {
  if (totalPages <= 7) return Array.from({ length: totalPages }, (_, index) => index + 1)
  const start = Math.max(1, Math.min(currentPage - 2, totalPages - 4))
  return Array.from({ length: 5 }, (_, index) => start + index)
}

export default function Pagination({
  page,
  pageSize,
  totalItems,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = [10, 20, 50],
  label = '分页',
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
          disabled={currentPage === 1}
          aria-label="上一页"
        >
          上一页
        </button>
        {pages[0] > 1 && <span className="pagination-ellipsis" aria-hidden="true">…</span>}
        {pages.map((pageNumber) => (
          <button
            type="button"
            key={pageNumber}
            className={pageNumber === currentPage ? 'is-current' : undefined}
            aria-label={`第 ${pageNumber} 页`}
            aria-current={pageNumber === currentPage ? 'page' : undefined}
            onClick={() => onPageChange(pageNumber)}
          >
            {pageNumber}
          </button>
        ))}
        {pages[pages.length - 1] < totalPages && <span className="pagination-ellipsis" aria-hidden="true">…</span>}
        <button
          type="button"
          onClick={() => onPageChange(currentPage + 1)}
          disabled={currentPage === totalPages}
          aria-label="下一页"
        >
          下一页
        </button>
      </div>
    </nav>
  )
}
