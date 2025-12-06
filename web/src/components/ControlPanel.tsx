import { type ReactNode, useEffect, useRef, useState } from "react";
import { createOrder } from "../api/client";
import type { ControlsResponse } from "../api/types";
import { formatNumber, formatPercent, formatPrice, formatQuantity } from "../lib/format";
import {
  defaultProfit,
  defaultStop,
  levelsError,
  noLevels,
  priceStep,
  roundPrice,
  type Side,
  type TicketLevels,
} from "../lib/ticket";
import { Switch } from "./Switch";

interface Props {
  /** The agent whose bot the panel operates. */
  agent: string;
  pair: string;
  asset: string;
  quote: string;
  /** The last close, where a market order enters. */
  lastPrice: number;
  controls: ControlsResponse;
  /** The levels of the order ticket, shared with the chart that draws them. */
  levels: TicketLevels;
  onLevelsChange: (levels: TicketLevels) => void;
}

type Unit = "quote" | "percent" | "lot";
type OrderType = "market" | "limit";
type Confirm = { kind: "order" };
type Feedback = { tone: "ok" | "error"; text: string };

const shares = [25, 50, 75, 100];

/**
 * A price field that keeps what is being typed, eg: "2400.", and takes the
 * value set from outside, eg: a line dragged on the chart.
 */
function PriceInput({
  value,
  onChange,
  label,
  disabled,
}: {
  value: number | null;
  onChange: (value: number | null) => void;
  label: string;
  disabled: boolean;
}) {
  const [text, setText] = useState(value === null ? "" : String(value));
  // biome-ignore lint/correctness/useExhaustiveDependencies: follows the value set from outside only
  useEffect(() => {
    const typed = text.trim() === "" ? null : Number(text);
    if (typed !== value) setText(value === null ? "" : String(value));
  }, [value]);

  return (
    <input
      type="number"
      inputMode="decimal"
      min="0"
      step="any"
      placeholder="0"
      value={text}
      disabled={disabled}
      aria-label={label}
      onChange={(e) => {
        setText(e.target.value);
        const parsed = Number(e.target.value);
        onChange(
          e.target.value.trim() !== "" && Number.isFinite(parsed) && parsed > 0 ? parsed : null,
        );
      }}
    />
  );
}

/** A price field with buttons moving it a step down and up. */
function PriceField({
  value,
  onChange,
  label,
  disabled,
  step,
}: {
  value: number | null;
  onChange: (value: number | null) => void;
  label: string;
  disabled: boolean;
  step: number;
}) {
  const nudge = (steps: number) => {
    if (value === null) return;
    onChange(roundPrice(value + steps * step, value));
  };
  return (
    <div className="field-group">
      <button
        type="button"
        className="field-step"
        aria-label={`${label}: diminuir`}
        disabled={disabled || value === null}
        onClick={() => nudge(-1)}
      >
        −
      </button>
      <PriceInput value={value} onChange={onChange} label={label} disabled={disabled} />
      <button
        type="button"
        className="field-step"
        aria-label={`${label}: aumentar`}
        disabled={disabled || value === null}
        onClick={() => nudge(1)}
      >
        +
      </button>
    </div>
  );
}

/** A field of the ticket: its label, an aside on the right, and its input. */
function Field({
  label,
  aside,
  children,
}: {
  label: ReactNode;
  aside?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="ticket-field">
      <div className="ticket-field-head">
        {typeof label === "string" ? <span className="ticket-field-label">{label}</span> : label}
        {aside}
      </div>
      {children}
    </div>
  );
}

/** How far level is from the entry: in price and in %. */
function distance(level: number | null, entry: number | null): string | null {
  if (level === null || entry === null || entry <= 0) return null;
  const delta = level - entry;
  return `${delta > 0 ? "+" : ""}${formatPrice(delta)} (${formatPercent(delta / entry)})`;
}

export function ControlPanel({
  agent,
  pair,
  asset,
  quote,
  lastPrice,
  controls,
  levels,
  onLevelsChange,
}: Props) {
  const [side, setSide] = useState<Side>("buy");
  const [type, setType] = useState<OrderType>("market");
  const [amount, setAmount] = useState("");
  const [unit, setUnit] = useState<Unit>("lot");
  const [stopOn, setStopOn] = useState(false);
  const [profitOn, setProfitOn] = useState(false);
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const feedbackTimer = useRef<number | undefined>(undefined);

  // without the controls of the bot, the ticket shows, locked
  const enabled = controls.enabled;
  const locked = busy || !enabled;
  const parsedAmount = Number(amount);
  const validAmount =
    Number.isFinite(parsedAmount) &&
    parsedAmount > 0 &&
    (unit !== "percent" || parsedAmount <= 100);

  // where the order enters: its limit, or the market
  const entry = type === "limit" ? levels.entry : lastPrice > 0 ? lastPrice : null;
  const stop = stopOn ? levels.stop : null;
  const profit = profitOn ? levels.profit : null;
  const missing =
    entry === null || (stopOn && levels.stop === null) || (profitOn && levels.profit === null);
  const levelError = missing || entry === null ? null : levelsError(side, entry, stop, profit);
  const validTicket = validAmount && !missing && levelError === null;
  const step = priceStep(entry ?? lastPrice);

  // the estimates of the summary: the size in the asset is known for a lot or
  // an amount in quote, a share of the balance is sized by the broker
  let size: number | null = null;
  if (validAmount && entry !== null && unit === "lot") size = parsedAmount;
  if (validAmount && entry !== null && unit === "quote") size = parsedAmount / entry;
  const notional = size !== null && entry !== null ? size * entry : null;
  const risk =
    size !== null && entry !== null && stop !== null ? size * Math.abs(entry - stop) : null;
  const reward =
    size !== null && entry !== null && profit !== null ? size * Math.abs(profit - entry) : null;

  // Changing the pair starts a new ticket.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on pair change only
  useEffect(() => {
    setConfirm(null);
    setType("market");
    setStopOn(false);
    setProfitOn(false);
  }, [pair]);

  useEffect(() => {
    return () => window.clearTimeout(feedbackTimer.current);
  }, []);

  const showFeedback = (next: Feedback) => {
    setFeedback(next);
    window.clearTimeout(feedbackTimer.current);
    feedbackTimer.current = window.setTimeout(() => setFeedback(null), 8000);
  };

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    try {
      await action();
    } catch (e) {
      showFeedback({ tone: "error", text: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
      setConfirm(null);
    }
  };

  // the stop loss and take profit on, placed from the price of the order
  const reference = entry ?? lastPrice;
  const changeSide = (next: Side) => {
    setSide(next);
    setConfirm(null);
    onLevelsChange({
      ...levels,
      stop: stopOn ? defaultStop(next, reference) : null,
      profit: profitOn ? defaultProfit(next, reference) : null,
    });
  };
  const changeType = (next: OrderType) => {
    setType(next);
    setConfirm(null);
    onLevelsChange({
      ...levels,
      entry: next === "limit" && lastPrice > 0 ? roundPrice(lastPrice) : null,
    });
  };
  const toggleStop = (on: boolean) => {
    setStopOn(on);
    setConfirm(null);
    onLevelsChange({ ...levels, stop: on && reference > 0 ? defaultStop(side, reference) : null });
  };
  const toggleProfit = (on: boolean) => {
    setProfitOn(on);
    setConfirm(null);
    onLevelsChange({
      ...levels,
      profit: on && reference > 0 ? defaultProfit(side, reference) : null,
    });
  };
  const setLevel = (field: keyof TicketLevels) => (value: number | null) => {
    setConfirm(null);
    onLevelsChange({ ...levels, [field]: value });
  };

  const handleOrder = () =>
    run(async () => {
      const order = await createOrder(agent, {
        pair,
        side,
        amount: parsedAmount,
        percent: unit === "percent",
        lot: unit === "lot",
        type,
        price: type === "limit" ? (levels.entry ?? 0) : 0,
        stop_loss: stop ?? 0,
        take_profit: profit ?? 0,
      });
      showFeedback({
        tone: "ok",
        text: `Ordem de ${side === "buy" ? "compra" : "venda"} #${order.id} criada — ${formatQuantity(
          order.quantity,
        )} ${asset}`,
      });
      setAmount("");
      setType("market");
      setStopOn(false);
      setProfitOn(false);
      onLevelsChange(noLevels);
    });

  const amountLabel = {
    quote: `${formatQuantity(parsedAmount)} ${quote}`,
    percent: `${formatQuantity(parsedAmount)}% do saldo`,
    lot: `${formatQuantity(parsedAmount)} ${Math.abs(parsedAmount) < 2 ? "lote" : "lotes"}`,
  }[unit];
  const drawn = type === "limit" || stopOn || profitOn;

  return (
    <section className={`card ticket ${side} ${enabled ? "" : "locked"}`} aria-label="Boleta">
      <header className="ticket-head">
        <div className="ticket-title">
          <span>Boleta</span>
          <strong>{pair}</strong>
        </div>
      </header>

      <div className="ticket-quote">
        <span className="ticket-quote-label">Último</span>
        <span className="ticket-quote-price">{lastPrice > 0 ? formatPrice(lastPrice) : "—"}</span>
        <span className="ticket-quote-unit">{quote}</span>
      </div>

      <fieldset className="ticket-sides" aria-label="Lado da ordem">
        <button
          type="button"
          className={`ticket-side buy ${side === "buy" ? "active" : ""}`}
          aria-pressed={side === "buy"}
          disabled={locked}
          onClick={() => changeSide("buy")}
        >
          Compra
        </button>
        <button
          type="button"
          className={`ticket-side sell ${side === "sell" ? "active" : ""}`}
          aria-pressed={side === "sell"}
          disabled={locked}
          onClick={() => changeSide("sell")}
        >
          Venda
        </button>
      </fieldset>

      <fieldset className="segmented" aria-label="Tipo da ordem">
        {(["market", "limit"] as const).map((kind) => (
          <button
            key={kind}
            type="button"
            className={type === kind ? "active" : ""}
            aria-pressed={type === kind}
            disabled={locked}
            onClick={() => changeType(kind)}
          >
            {kind === "market" ? "Mercado" : "Limite"}
          </button>
        ))}
      </fieldset>

      <Field label="Quantidade">
        <div className="field-group">
          <input
            type="number"
            inputMode="decimal"
            min="0"
            step="any"
            placeholder="0"
            value={amount}
            disabled={locked}
            onChange={(e) => {
              setAmount(e.target.value);
              setConfirm(null);
            }}
            aria-label="Valor da ordem"
          />
          <select
            className="field-unit"
            value={unit}
            disabled={locked}
            onChange={(e) => {
              setUnit(e.target.value as Unit);
              setConfirm(null);
            }}
            aria-label="Unidade do valor"
          >
            <option value="lot">lote</option>
            <option value="quote">{quote}</option>
            <option value="percent">% saldo</option>
          </select>
        </div>
        {unit === "percent" && (
          <div className="chips">
            {shares.map((share) => (
              <button
                key={share}
                type="button"
                className={parsedAmount === share ? "active" : ""}
                disabled={locked}
                onClick={() => {
                  setAmount(String(share));
                  setConfirm(null);
                }}
              >
                {share}%
              </button>
            ))}
          </div>
        )}
      </Field>

      {type === "limit" && (
        <Field
          label="Preço limite"
          aside={
            <button
              type="button"
              className="link-btn"
              disabled={locked || lastPrice <= 0}
              onClick={() => setLevel("entry")(roundPrice(lastPrice))}
            >
              usar último
            </button>
          }
        >
          <PriceField
            value={levels.entry}
            onChange={setLevel("entry")}
            label="Preço limite"
            disabled={locked}
            step={step}
          />
        </Field>
      )}

      <div className={`ticket-level stop ${stopOn ? "on" : ""}`}>
        <Field
          label={
            <Switch checked={stopOn} onChange={toggleStop} label="Stop loss" disabled={locked} />
          }
          aside={stopOn && <span className="ticket-field-info">{distance(stop, entry)}</span>}
        >
          {stopOn && (
            <PriceField
              value={levels.stop}
              onChange={setLevel("stop")}
              label="Stop loss"
              disabled={locked}
              step={step}
            />
          )}
        </Field>
      </div>

      <div className={`ticket-level profit ${profitOn ? "on" : ""}`}>
        <Field
          label={
            <Switch
              checked={profitOn}
              onChange={toggleProfit}
              label="Take profit"
              disabled={locked}
            />
          }
          aside={profitOn && <span className="ticket-field-info">{distance(profit, entry)}</span>}
        >
          {profitOn && (
            <PriceField
              value={levels.profit}
              onChange={setLevel("profit")}
              label="Take profit"
              disabled={locked}
              step={step}
            />
          )}
        </Field>
      </div>

      <div className="ticket-footer">
        <dl className={`ticket-summary ${confirm?.kind === "order" ? "confirming" : ""}`}>
          <dt>Entrada</dt>
          <dd>
            {entry === null ? "—" : formatPrice(entry)}
            {type === "market" && entry !== null && <small>mercado</small>}
          </dd>
          <dt>Tamanho</dt>
          <dd>
            {size !== null
              ? `${formatQuantity(size)} ${asset}`
              : unit === "percent"
                ? "na corretora"
                : "—"}
          </dd>
          <dt>Valor</dt>
          <dd>{notional === null ? "—" : `${formatNumber(notional)} ${quote}`}</dd>
          <dt>Risco</dt>
          <dd className={risk === null ? "" : "neg"}>
            {risk === null ? "—" : `−${formatNumber(risk)} ${quote}`}
          </dd>
          <dt>Retorno</dt>
          <dd className={reward === null ? "" : "pos"}>
            {reward === null ? "—" : `+${formatNumber(reward)} ${quote}`}
          </dd>
          <dt>Risco : retorno</dt>
          <dd>{risk && reward ? `1 : ${formatNumber(reward / risk)}` : "—"}</dd>
        </dl>

        {levelError && <p className="ticket-error">{levelError}</p>}

        {confirm?.kind === "order" ? (
          <div className="ticket-actions">
            <button
              type="button"
              className="ticket-back"
              disabled={locked}
              onClick={() => setConfirm(null)}
            >
              Voltar
            </button>
            <button
              type="button"
              className={`ticket-submit ${side}`}
              disabled={locked}
              onClick={handleOrder}
            >
              Confirmar {side === "buy" ? "compra" : "venda"}
            </button>
          </div>
        ) : (
          <button
            type="button"
            className={`ticket-submit ${side}`}
            disabled={locked || !validTicket}
            onClick={() => setConfirm({ kind: "order" })}
          >
            {side === "buy" ? "Comprar" : "Vender"}
            {validAmount && ` ${amountLabel}`}
          </button>
        )}

        {enabled && (
          <p className="ticket-hint">
            {drawn
              ? "Arraste as linhas tracejadas para ajustar."
              : "Arraste as ordens no gráfico para movê-las."}
          </p>
        )}
        {feedback && <p className={`control-feedback ${feedback.tone}`}>{feedback.text}</p>}
      </div>
    </section>
  );
}
