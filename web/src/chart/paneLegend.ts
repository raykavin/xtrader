import type { CanvasRenderingTarget2D } from "fancy-canvas";
import type {
  IPanePrimitive,
  IPanePrimitivePaneView,
  IPrimitivePaneRenderer,
  PaneAttachedParameter,
  PrimitivePaneViewZOrder,
  Time,
} from "lightweight-charts";

/** An item of the legend of a pane: a series drawn in it, with its color. */
export interface LegendItem {
  label: string;
  color: string;
}

/** The look of a legend: the face of the chart, its text and the background behind it. */
export interface LegendStyle {
  fontFamily: string;
  text: string;
  background: string;
}

/** Where the legend sits in its pane, and its sizes, in CSS pixels. */
const MARGIN = 8;
const PADDING = 7;
const HEIGHT = 20;
const DOT = 6;
const DOT_GAP = 5;
const ITEM_GAP = 11;

class LegendRenderer implements IPrimitivePaneRenderer {
  constructor(
    private readonly items: readonly LegendItem[],
    private readonly style: LegendStyle,
  ) {}

  draw(target: CanvasRenderingTarget2D): void {
    if (this.items.length === 0) return;
    // biome-ignore lint/correctness/useHookAtTopLevel: fancy-canvas API, not a React hook
    target.useMediaCoordinateSpace(({ context, mediaSize }) => {
      context.save();
      context.font = `500 11px ${this.style.fontFamily}`;
      context.textBaseline = "middle";
      const widths = this.items.map((item) => context.measureText(item.label).width);
      const content =
        widths.reduce((sum, width) => sum + DOT + DOT_GAP + width, 0) +
        ITEM_GAP * (this.items.length - 1);
      // never wider than the pane: cut at its edge
      const width = Math.min(content + 2 * PADDING, mediaSize.width - 2 * MARGIN);
      if (width <= 2 * PADDING || mediaSize.height < HEIGHT + MARGIN) {
        context.restore();
        return;
      }
      context.fillStyle = this.style.background;
      context.beginPath();
      context.roundRect(MARGIN, MARGIN, width, HEIGHT, 6);
      context.fill();
      context.beginPath();
      context.rect(MARGIN, MARGIN, width, HEIGHT);
      context.clip();

      const y = MARGIN + HEIGHT / 2;
      let x = MARGIN + PADDING;
      for (const [index, item] of this.items.entries()) {
        context.fillStyle = item.color;
        context.beginPath();
        context.arc(x + DOT / 2, y, DOT / 2, 0, 2 * Math.PI);
        context.fill();
        x += DOT + DOT_GAP;
        context.fillStyle = this.style.text;
        context.fillText(item.label, x, y + 0.5);
        x += (widths[index] ?? 0) + ITEM_GAP;
      }
      context.restore();
    });
  }
}

class LegendView implements IPanePrimitivePaneView {
  constructor(private readonly source: PaneLegendPrimitive) {}

  zOrder(): PrimitivePaneViewZOrder {
    return "top";
  }

  renderer(): IPrimitivePaneRenderer {
    return new LegendRenderer(this.source.items, this.source.style);
  }
}

/**
 * The legend of a pane of the chart: the names of the series drawn in it, each
 * after a dot of its color, at its top left corner, eg: EMA 20 and EMA 50 over
 * the candles, RSI 14 in the pane of the RSI. It follows its pane as it is
 * resized or moved; the screen readers read the list of the page instead.
 */
export class PaneLegendPrimitive implements IPanePrimitive<Time> {
  items: readonly LegendItem[] = [];
  private readonly views: readonly IPanePrimitivePaneView[] = [new LegendView(this)];
  private requestUpdate?: () => void;

  constructor(public style: LegendStyle) {}

  attached({ requestUpdate }: PaneAttachedParameter<Time>): void {
    this.requestUpdate = requestUpdate;
  }

  detached(): void {
    this.requestUpdate = undefined;
  }

  paneViews(): readonly IPanePrimitivePaneView[] {
    return this.views;
  }

  /** Shows items, drawn again only when they changed. */
  setItems(items: readonly LegendItem[]): void {
    const same =
      items.length === this.items.length &&
      items.every(
        (item, i) => item.label === this.items[i]?.label && item.color === this.items[i]?.color,
      );
    if (same) return;
    this.items = items;
    this.requestUpdate?.();
  }

  /** Draws with style, eg: of the other theme. */
  setStyle(style: LegendStyle): void {
    this.style = style;
    this.requestUpdate?.();
  }
}

/** The color hex, eg: #0f1723, at alpha, for the background behind a legend. */
export function withAlpha(hex: string, alpha: number): string {
  const match = /^#([0-9a-f]{6})$/i.exec(hex);
  if (!match?.[1]) return hex;
  const value = Number.parseInt(match[1], 16);
  return `rgba(${(value >> 16) & 255}, ${(value >> 8) & 255}, ${value & 255}, ${alpha})`;
}
