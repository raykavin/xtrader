import type { IPriceLine, ISeriesApi, SeriesType } from "lightweight-charts";

/** A price line of the chart the user can drag vertically. */
export interface DraggableLine {
  key: string;
  line: IPriceLine;
  price: number;
  /** Follows the pointer while dragged, eg: to fill a field with the price. */
  onDrag?: (price: number) => void;
  /** Takes the price the line is dropped at; when it rejects, the line goes back. */
  onDrop: (price: number) => Promise<void> | void;
}

// how far from a line, in pixels, a press grabs it
const grabDistance = 6;

/**
 * Drags the price lines of a series: pressing on one holds it, without the
 * chart scrolling meanwhile, moving follows the pointer and releasing drops it
 * at that price, rounded by round.
 */
export class LineDragger {
  private lines: DraggableLine[] = [];
  private drag: { line: DraggableLine; start: number } | null = null;

  constructor(
    private readonly container: HTMLElement,
    private readonly series: ISeriesApi<SeriesType>,
    private readonly round: (price: number) => number,
  ) {
    // the capture phase comes before the chart: a press on a line never
    // reaches it, and canceling it drops the mouse events that would scroll
    container.addEventListener("pointerdown", this.onPointerDown, true);
    container.addEventListener("mousedown", this.swallow, true);
    container.addEventListener("pointermove", this.onHover);
  }

  /** The lines that can be dragged; the one being dragged keeps its price. */
  setLines(lines: DraggableLine[]): void {
    this.lines = lines;
    const drag = this.drag;
    if (!drag) return;
    const current = lines.find((l) => l.key === drag.line.key);
    if (!current) {
      this.release(); // gone meanwhile, eg: filled
      return;
    }
    current.price = drag.line.price;
    current.line.applyOptions({ price: drag.line.price });
    drag.line = current;
  }

  /** Whether the line of key is being dragged. */
  dragging(key: string): boolean {
    return this.drag?.line.key === key;
  }

  destroy(): void {
    this.release();
    this.container.removeEventListener("pointerdown", this.onPointerDown, true);
    this.container.removeEventListener("mousedown", this.swallow, true);
    this.container.removeEventListener("pointermove", this.onHover);
  }

  private y(event: PointerEvent): number {
    return event.clientY - this.container.getBoundingClientRect().top;
  }

  private lineAt(y: number): DraggableLine | null {
    let found: DraggableLine | null = null;
    let nearest = grabDistance;
    for (const line of this.lines) {
      const coordinate = this.series.priceToCoordinate(line.price);
      if (coordinate === null) continue;
      const distance = Math.abs(coordinate - y);
      if (distance <= nearest) {
        found = line;
        nearest = distance;
      }
    }
    return found;
  }

  private readonly onHover = (event: PointerEvent) => {
    if (this.drag) return;
    this.container.style.cursor = this.lineAt(this.y(event)) ? "ns-resize" : "";
  };

  private readonly swallow = (event: MouseEvent) => {
    if (this.drag) event.stopPropagation();
  };

  private readonly onPointerDown = (event: PointerEvent) => {
    if (event.button !== 0) return;
    const line = this.lineAt(this.y(event));
    if (!line) return;

    event.preventDefault();
    event.stopPropagation();
    this.drag = { line, start: line.price };
    this.container.style.cursor = "ns-resize";
    window.addEventListener("pointermove", this.onMove);
    window.addEventListener("pointerup", this.onUp);
    window.addEventListener("pointercancel", this.onCancel);
  };

  private readonly onMove = (event: PointerEvent) => {
    const drag = this.drag;
    if (!drag) return;
    const price = this.series.coordinateToPrice(this.y(event));
    if (price === null || price <= 0) return;

    const rounded = this.round(price);
    drag.line.price = rounded;
    drag.line.line.applyOptions({ price: rounded });
    drag.line.onDrag?.(rounded);
  };

  private readonly onUp = () => {
    const drag = this.drag;
    this.release();
    if (!drag || drag.line.price === drag.start) return;

    const { line, start } = drag;
    Promise.resolve(line.onDrop(line.price)).catch(() => {
      line.price = start;
      line.line.applyOptions({ price: start });
      line.onDrag?.(start);
    });
  };

  private readonly onCancel = () => {
    const drag = this.drag;
    this.release();
    if (!drag) return;
    drag.line.price = drag.start;
    drag.line.line.applyOptions({ price: drag.start });
    drag.line.onDrag?.(drag.start);
  };

  private release(): void {
    this.drag = null;
    this.container.style.cursor = "";
    window.removeEventListener("pointermove", this.onMove);
    window.removeEventListener("pointerup", this.onUp);
    window.removeEventListener("pointercancel", this.onCancel);
  }
}
