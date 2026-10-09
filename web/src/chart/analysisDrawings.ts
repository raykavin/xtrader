import type { CanvasRenderingTarget2D } from "fancy-canvas";
import type {
  IPrimitivePaneRenderer,
  IPrimitivePaneView,
  ISeriesPrimitive,
  PrimitivePaneViewZOrder,
  SeriesAttachedParameter,
  Time,
  UTCTimestamp,
} from "lightweight-charts";
import { type AnalysisShapes, noShapes, type ZoneKind } from "../lib/analysis";

/** A shape of the analysis in the coordinates of the pane: x2 null up to its right edge. */
interface Box {
  kind: ZoneKind;
  state: string | null;
  x1: number;
  x2: number | null;
  y1: number;
  y2: number;
  color: string;
  label: string;
  ended: boolean;
  labelAt: "top" | "bottom";
}

interface Line {
  x1: number;
  x2: number | null;
  y: number;
  color: string;
  label: string;
  ended: boolean;
  intent: boolean;
}

interface Point {
  x: number;
  y: number;
  color: string;
  label: string;
}

interface Geometry {
  boxes: Box[];
  lines: Line[];
  points: Point[];
}

const noGeometry: Geometry = { boxes: [], lines: [], points: [] };

/** The layers of the analysis: its shapes behind the candles, their labels and markers over them. */
type Layer = "shapes" | "labels";

class AnalysisRenderer implements IPrimitivePaneRenderer {
  constructor(
    private readonly geometry: Geometry,
    private readonly layer: Layer,
    private readonly font: string,
    private readonly background: string,
  ) {}

  draw(target: CanvasRenderingTarget2D): void {
    const { boxes, lines, points } = this.geometry;
    if (boxes.length + lines.length + points.length === 0) return;
    // biome-ignore lint/correctness/useHookAtTopLevel: fancy-canvas API, not a React hook
    target.useMediaCoordinateSpace(({ context, mediaSize }) => {
      context.save();
      if (this.layer === "shapes") {
        drawShapes(context, this.geometry, mediaSize.width);
      } else {
        context.font = `10px ${this.font}`;
        drawLabels(context, this.geometry, mediaSize.width, this.background);
      }
      context.restore();
    });
  }
}

/** The zones as boxes and the levels as lines, from their candle to their end or the right edge. */
function drawShapes(context: CanvasRenderingContext2D, { boxes, lines }: Geometry, right: number) {
  for (const box of boxes) {
    if (box.kind === "zone") drawZone(context, box, right);
    else drawSupplyDemand(context, box, right);
  }
  for (const line of lines) {
    context.globalAlpha = line.ended ? 0.45 : 0.9;
    context.strokeStyle = line.color;
    context.lineWidth = line.intent ? 1.5 : 1;
    context.setLineDash(line.intent ? [6, 4] : line.ended ? [2, 3] : []);
    context.beginPath();
    context.moveTo(line.x1, line.y);
    context.lineTo(line.x2 ?? right, line.y);
    context.stroke();
  }
}

/** A zone of a structure, eg: an FVG: a box, faded and its border dashed once ended. */
function drawZone(context: CanvasRenderingContext2D, box: Box, right: number) {
  const x2 = box.x2 ?? right;
  const top = Math.min(box.y1, box.y2);
  const height = Math.max(Math.abs(box.y2 - box.y1), 1);
  context.globalAlpha = box.ended ? 0.06 : 0.13;
  context.fillStyle = box.color;
  context.fillRect(box.x1, top, x2 - box.x1, height);
  context.globalAlpha = box.ended ? 0.35 : 0.6;
  context.strokeStyle = box.color;
  context.lineWidth = 1;
  context.setLineDash(box.ended ? [3, 3] : []);
  context.strokeRect(box.x1, top, x2 - box.x1, height);
}

/** The space between the lines of the hatch of a zone of supply or demand, in pixels. */
const HATCH = 8;

/**
 * A zone of supply or demand, apart from the zones of the structures: a box
 * hatched, down to the right for supply and up for demand, its edge nearest
 * to the price, where it would be met (the bottom of a supply zone, the top
 * of a demand one), stronger; faded as tested, and faded, dashed, once broken.
 */
function drawSupplyDemand(context: CanvasRenderingContext2D, box: Box, right: number) {
  const x2 = box.x2 ?? right;
  const top = Math.min(box.y1, box.y2);
  const bottom = Math.max(box.y1, box.y2, top + 1);
  const width = x2 - box.x1;
  const tested = box.state === "tested";
  context.globalAlpha = box.ended ? 0.05 : tested ? 0.1 : 0.15;
  context.fillStyle = box.color;
  context.fillRect(box.x1, top, width, bottom - top);

  context.save();
  context.beginPath();
  context.rect(box.x1, top, width, bottom - top);
  context.clip();
  context.globalAlpha = box.ended ? 0.1 : tested ? 0.18 : 0.26;
  context.strokeStyle = box.color;
  context.lineWidth = 1;
  context.setLineDash([]);
  context.beginPath();
  const height = bottom - top;
  const demand = box.kind === "demand";
  for (let x = box.x1 - height; x < x2; x += HATCH) {
    context.moveTo(x, demand ? bottom : top);
    context.lineTo(x + height, demand ? top : bottom);
  }
  context.stroke();
  context.restore();

  context.globalAlpha = box.ended ? 0.35 : 0.7;
  context.strokeStyle = box.color;
  context.lineWidth = 1;
  context.setLineDash(box.ended ? [3, 3] : []);
  context.strokeRect(box.x1, top, width, bottom - top);
  const near = box.kind === "supply" ? bottom : top;
  context.globalAlpha = box.ended ? 0.4 : 0.95;
  context.lineWidth = 2;
  context.beginPath();
  context.moveTo(box.x1, near);
  context.lineTo(x2, near);
  context.stroke();
}

/**
 * The labels of the zones and of the levels, and the markers with theirs, over
 * the candles: each label outlined in the color of the background, legible
 * over the candles and the grid.
 */
function drawLabels(
  context: CanvasRenderingContext2D,
  { boxes, lines, points }: Geometry,
  right: number,
  background: string,
) {
  const label = (text: string, x: number, y: number, color: string, alpha: number) => {
    context.globalAlpha = alpha;
    context.lineJoin = "round";
    context.lineWidth = 3;
    context.strokeStyle = background;
    context.strokeText(text, x, y);
    context.fillStyle = color;
    context.fillText(text, x, y);
  };
  context.setLineDash([]);
  context.textBaseline = "top";
  for (const box of boxes) {
    // from the start of the box, kept in the pane: a zone of the last candles
    // shifted left to show it whole
    const width = context.measureText(box.label).width;
    const x = Math.max(Math.min(Math.max(box.x1, 0) + 3, right - width - 3), 0);
    const alpha = box.ended ? 0.55 : 1;
    if (box.labelAt === "bottom") {
      context.textBaseline = "bottom";
      label(box.label, x, Math.max(box.y1, box.y2) - 2, box.color, alpha);
    } else {
      context.textBaseline = "top";
      label(box.label, x, Math.min(box.y1, box.y2) + 2, box.color, alpha);
    }
  }
  context.textBaseline = "bottom";
  for (const line of lines) {
    const x2 = line.x2 ?? right;
    const width = context.measureText(line.label).width;
    // the label at the start of the line, kept in the pane
    const x = Math.min(Math.max(line.x1, 0) + 3, Math.max(x2 - width - 3, 0));
    label(line.label, x, line.y - 2, line.color, line.ended ? 0.55 : 1);
  }
  for (const point of points) {
    context.globalAlpha = 1;
    context.fillStyle = point.color;
    context.beginPath();
    context.moveTo(point.x, point.y - 4);
    context.lineTo(point.x + 4, point.y);
    context.lineTo(point.x, point.y + 4);
    context.lineTo(point.x - 4, point.y);
    context.closePath();
    context.fill();
    const width = context.measureText(point.label).width;
    label(point.label, point.x - width / 2, point.y - 6, point.color, 1);
  }
}

class AnalysisPaneView implements IPrimitivePaneView {
  private geometry: Geometry = noGeometry;

  constructor(
    private readonly source: AnalysisPrimitive,
    private readonly layer: Layer,
  ) {}

  update(): void {
    this.geometry = this.source.geometry();
  }

  // the shapes behind the candles, the price readable over the zones; their
  // labels and the markers over them
  zOrder(): PrimitivePaneViewZOrder {
    return this.layer === "shapes" ? "bottom" : "top";
  }

  renderer(): IPrimitivePaneRenderer {
    return new AnalysisRenderer(
      this.geometry,
      this.layer,
      this.source.font,
      this.source.background,
    );
  }
}

/**
 * Series primitive that draws the analysis of the language model of the
 * strategy on the price pane, see analysisShapes: its zones as boxes, its
 * levels as lines from their candle, its markers as points with their label,
 * and the prices of its intent dashed: the shapes behind the candles, the
 * labels and the markers over them. None of them takes part in the scale of
 * the prices, which the candles and the orders set.
 */
export class AnalysisPrimitive implements ISeriesPrimitive<Time> {
  private params?: SeriesAttachedParameter<Time>;
  private shapes: AnalysisShapes = noShapes;
  private readonly views = [
    new AnalysisPaneView(this, "shapes"),
    new AnalysisPaneView(this, "labels"),
  ];

  /** font names the font of the labels, and background the color they are outlined in. */
  constructor(
    public font: string,
    public background: string,
  ) {}

  attached(params: SeriesAttachedParameter<Time>): void {
    this.params = params;
  }

  detached(): void {
    this.params = undefined;
  }

  setShapes(shapes: AnalysisShapes): void {
    this.shapes = shapes;
    this.params?.requestUpdate();
  }

  updateAllViews(): void {
    for (const view of this.views) view.update();
  }

  paneViews(): readonly IPrimitivePaneView[] {
    return this.views;
  }

  geometry(): Geometry {
    const params = this.params;
    if (!params) return noGeometry;
    const timeScale = params.chart.timeScale();
    const x = (time: number) => timeScale.timeToCoordinate(time as UTCTimestamp);
    const y = (price: number) => params.series.priceToCoordinate(price);
    const spacing = timeScale.options().barSpacing;
    const geometry: Geometry = { boxes: [], lines: [], points: [] };
    for (const zone of this.shapes.zones) {
      const from = x(zone.from);
      const to = zone.to === null ? null : x(zone.to);
      const y1 = y(zone.high);
      const y2 = y(zone.low);
      if (from === null || y1 === null || y2 === null || (zone.to !== null && to === null))
        continue;
      let x1: number = from;
      let x2: number | null = to;
      if (x2 !== null && x2 - x1 < spacing) {
        // started and ended in one candle of the timeframe shown, eg: a zone
        // of 4h broken the same day on the daily chart: as wide as the candle
        const middle = (x1 + x2) / 2;
        x1 = middle - spacing / 2;
        x2 = middle + spacing / 2;
      }
      geometry.boxes.push({
        kind: zone.kind,
        state: zone.state,
        x1,
        x2,
        y1,
        y2,
        color: zone.color,
        label: zone.label,
        ended: zone.ended,
        labelAt: zone.labelAt,
      });
    }
    for (const level of this.shapes.levels) {
      const x1 = x(level.from);
      const x2 = level.to === null ? null : x(level.to);
      const yy = y(level.price);
      if (x1 === null || yy === null || (level.to !== null && x2 === null)) continue;
      geometry.lines.push({
        x1,
        x2,
        y: yy,
        color: level.color,
        label: level.label,
        ended: level.ended,
        intent: level.intent,
      });
    }
    for (const marker of this.shapes.markers) {
      const xx = x(marker.time);
      const yy = y(marker.price);
      if (xx === null || yy === null) continue;
      geometry.points.push({ x: xx, y: yy, color: marker.color, label: marker.label });
    }
    return geometry;
  }
}
