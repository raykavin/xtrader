import type { PageInfo } from "../api/types";
import { clampPage, PAGE_SIZE, pageCount, pageList } from "../lib/pagination";
import { Icon } from "./Icon";

interface Props {
  /** The page shown, from 0. */
  page: number;
  /** The rows of the list. */
  total: number;
  onPage: (page: number) => void;
  /** The rows of a page, PAGE_SIZE by default. */
  size?: number;
}

/**
 * The pages of a list, size rows each (PAGE_SIZE by default): the rows shown,
 * the previous and next pages and the ones around the current one. None for a
 * single page.
 */
export function Paginator({ page, total, onPage, size = PAGE_SIZE }: Props) {
  const count = pageCount(total, size);
  if (count <= 1) return null;
  const current = clampPage(page, total, size);
  const first = current * size + 1;
  const last = Math.min(total, first + size - 1);
  const pages = pageList(current, count);

  return (
    <nav className="paginator" aria-label="Páginas">
      <span className="paginator-range">
        {first}–{last} de {total}
      </span>
      <div className="paginator-pages">
        <button
          type="button"
          className="page-btn"
          disabled={current === 0}
          aria-label="Página anterior"
          onClick={() => onPage(current - 1)}
        >
          <Icon name="chevronLeft" size={14} />
        </button>
        {pages.map((p, i) =>
          p === null ? (
            <span key={`gap-after-${pages[i - 1]}`} className="page-gap" aria-hidden="true">
              …
            </span>
          ) : (
            <button
              key={p}
              type="button"
              className={p === current ? "page-btn current" : "page-btn"}
              aria-current={p === current ? "page" : undefined}
              aria-label={`Página ${p + 1}`}
              onClick={() => onPage(p)}
            >
              {p + 1}
            </button>
          ),
        )}
        <button
          type="button"
          className="page-btn"
          disabled={current === count - 1}
          aria-label="Próxima página"
          onClick={() => onPage(current + 1)}
        >
          <Icon name="chevronRight" size={14} />
        </button>
      </div>
    </nav>
  );
}

/**
 * The pages of a list paged by the API, as it answered (see PageInfo): the
 * page asked from 1; none for a single page.
 */
export function ListPages({
  info,
  onPage,
}: {
  info: PageInfo | null;
  onPage: (page: number) => void;
}) {
  if (!info || info.total_pages <= 1) return null;
  return (
    <Paginator
      page={info.page - 1}
      total={info.total}
      size={info.per_page}
      onPage={(page) => onPage(page + 1)}
    />
  );
}
