import { clampPage, PAGE_SIZE, pageCount, pageList } from "../lib/pagination";
import { Icon } from "./Icon";

interface Props {
  /** The page shown, from 0. */
  page: number;
  /** The rows of the list. */
  total: number;
  onPage: (page: number) => void;
}

/**
 * The pages of a list, PAGE_SIZE rows each: the rows shown, the previous and
 * next pages and the ones around the current one. None for a single page.
 */
export function Paginator({ page, total, onPage }: Props) {
  const count = pageCount(total);
  if (count <= 1) return null;
  const current = clampPage(page, total);
  const first = current * PAGE_SIZE + 1;
  const last = Math.min(total, first + PAGE_SIZE - 1);
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
