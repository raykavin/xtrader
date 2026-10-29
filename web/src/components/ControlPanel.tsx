import { type ReactNode, useEffect, useRef, useState } from "react";
import { createOrder } from "../api/client";
import type { ControlsResponse } from "../api/types";
import { formatNumber, formatPercent, formatPrice, formatQuantity } from "../lib/format";
import { settingsHref } from "../lib/nav";
import {
  defaultProfit,
  defaultStop,
  levelsError,
  noLevels,
  orderFeedback,
  priceStep,
  roundPrice,
  type Side,
  type TicketLevels,
} from "../lib/ticket";
import { SectionHead } from "./SectionHead";
import { Switch } from "./Switch";

interface Props {
  /** The agent whose bot the panel operates. */
  agent: string;
  pair: string;
  asset: string;
  quote: string;
  /**
   * The currency of the account, see Snapshot.account_currency: the money of
   * the ticket (an amount, the value, the risk and the return) is in it.
   */
  accountCurrency?: string;
  /** The value in accountCurrency of a move of 1 of the price for 1 unit, see Snapshot.point_value. */
  pointValue?: number;
  /** A paper wallet: the money simulated, in the quote. */
  simulated?: boolean;
  /** The last close, where a market order enters. */
  lastPrice: number;
  /** Null while unknown: the ticket shows, locked. */
  controls: ControlsResponse | null;
  /** The levels of the order ticket, shared with the chart that draws them. */
  levels: TicketLevels;
  onLevelsChange: (levels: TicketLevels) => void;
}

/** The unit of the amount: lots, money (of the account, else the quote) or a share of the balance. */
type Unit = "quote" | "percent" | "lot";

/** Why the ticket is blocked, and where to unblock it, on its badge. */
const blockedHint =
  "o painel de trading está desligado neste agente. Ligue-o em Configurações › Agentes para enviar ordens.";
type OrderType = "market" | "limit";
type Confirm = { kind: "order" };
type Feedback = { tone: "ok" | "warn" | "error"; text: string };

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
        {typeof label === "string" ? <span className="field-label">{label}</span> : label}
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
  accountCurrency,
  pointValue,
  simulated,
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
  const enabled = controls?.enabled === true;
  const locked = busy || !enabled;
  // known off: the ticket sends no order until the trade panel of the agent is on
  const blocked = controls !== null && !enabled;
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

  // the money of the ticket: on an account, in its currency, a move of the
  // price valued by the point value of the pair (unknown until the broker
  // tells it: the estimates left out); else in the quote, simulated
  const money = accountCurrency ?? (simulated ? `${quote} sim.` : quote);
  const point = accountCurrency ? (pointValue && pointValue > 0 ? pointValue : null) : 1;
  // the estimates of the summary: the size in the asset is known for a lot or
  // an amount of money, a share of the balance is sized by the broker
  let size: number | null = null;
  if (validAmount && entry !== null && unit === "lot") size = parsedAmount;
  if (validAmount && entry !== null && unit === "quote" && point !== null) {
    size = parsedAmount / (entry * point);
  }
  const valued = (distance: number | null) =>
    size !== null && distance !== null && point !== null ? size * distance * point : null;
  const notional = valued(entry);
  const risk = valued(entry !== null && stop !== null ? Math.abs(entry - stop) : null);
  const reward = valued(entry !== null && profit !== null ? Math.abs(profit - entry) : null);

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
    // a warning stays until the next feedback: the position may be unprotected
    if (next.tone !== "warn") {
      feedbackTimer.current = window.setTimeout(() => setFeedback(null), 8000);
    }
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
      showFeedback(orderFeedback(order, side, asset));
      setAmount("");
      setType("market");
      setStopOn(false);
      setProfitOn(false);
      onLevelsChange(noLevels);
    });

  const amountLabel = {
    quote: `${formatQuantity(parsedAmount)} ${money}`,
    percent: `${formatQuantity(parsedAmount)}% do saldo`,
    lot: `${formatQuantity(parsedAmount)} ${Math.abs(parsedAmount) < 2 ? "lote" : "lotes"}`,
  }[unit];
  const drawn = type === "limit" || stopOn || profitOn;

  return (
    <section className={`card ticket ${side} ${enabled ? "" : "locked"}`} aria-label="Boleta">
      <SectionHead
        eyebrow="Operar"
        title="Boleta"
        tag={pair}
        inline={
          blocked && (
            <a
              className="badge badge-blocked"
              href={settingsHref("agentes")}
              title={blockedHint}
              aria-label={`Boleta bloqueada: ${blockedHint}`}
            >
              Bloqueada
            </a>
          )
        }
      />

      <div className="ticket-quote">
        <span className="field-label">Último preço</span>
        <span className="ticket-quote-price">
          {lastPrice > 0 ? formatPrice(lastPrice) : "—"}
          <small>{quote}</small>
        </span>
      </div>

      <fieldset className="seg ticket-sides" aria-label="Lado da ordem">
        <button
          type="button"
          className="seg-item ticket-side buy"
          aria-pressed={side === "buy"}
          disabled={locked}
          onClick={() => changeSide("buy")}
        >
          Compra
        </button>
        <button
          type="button"
          className="seg-item ticket-side sell"
          aria-pressed={side === "sell"}
          disabled={locked}
          onClick={() => changeSide("sell")}
        >
          Venda
        </button>
      </fieldset>

      <fieldset className="seg seg-fill ticket-types" aria-label="Tipo da ordem">
        {(["market", "limit"] as const).map((kind) => (
          <button
            key={kind}
            type="button"
            className="seg-item"
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
            <option value="quote">{accountCurrency ?? quote}</option>
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
          <dd>{notional === null ? "—" : `${formatNumber(notional)} ${money}`}</dd>
          <dt>Risco</dt>
          <dd className={risk === null ? "" : "neg"}>
            {risk === null ? "—" : `−${formatNumber(risk)} ${money}`}
          </dd>
          <dt>Retorno</dt>
          <dd className={reward === null ? "" : "pos"}>
            {reward === null ? "—" : `+${formatNumber(reward)} ${money}`}
          </dd>
          <dt>Risco : retorno</dt>
          <dd>{risk && reward ? `1 : ${formatNumber(reward / risk)}` : "—"}</dd>
        </dl>

        {levelError && (
          <p className="msg msg-error" role="alert">
            {levelError}
          </p>
        )}

        {confirm?.kind === "order" ? (
          <div className="ticket-actions">
            <button
              type="button"
              className="btn btn-lg"
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
        {feedback && (
          <p
            className={`msg msg-${feedback.tone}`}
            role={feedback.tone === "error" ? "alert" : "status"}
          >
            {feedback.text}
          </p>
        )}
      </div>
    </section>
  );
}
