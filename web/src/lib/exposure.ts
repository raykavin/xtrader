// The exposure limit of all the agents on the dashboard: at most so many
// agents on an asset in the same direction at once, and the form that sets
// it, checked as the controller checks it.

import type { ExposureRequest, ExposureSettings } from "../api/types";

/** The largest limit, of all the assets or of one. */
export const MAX_EXPOSURE = 100;

/** An asset as MT5 names it, eg: XAUUSDm (agentapi's symbol). */
const symbol = /^[A-Za-z0-9][A-Za-z0-9._#-]{0,31}$/;

/** The form of the exposure limit, as typed: the numbers as text, a blank for none. */
export interface ExposureForm {
  /** The limit of every asset; blank or 0 for none. */
  maxAgents: string;
  /** The limits of some assets apart, as "XAUUSDm=2;BTCUSDm=3". */
  pairsText: string;
}

/** The limits of some assets as typed, eg: XAUUSDm=2;BTCUSDm=3; blank for none. */
export function pairLimitsText(pairs: Record<string, number> | undefined): string {
  return Object.entries(pairs ?? {})
    .map(([pair, limit]) => `${pair}=${limit}`)
    .join(";");
}

/** The form of settings: no limit as a blank. */
export function exposureForm(settings: ExposureSettings): ExposureForm {
  return {
    maxAgents: settings.max_agents > 0 ? String(settings.max_agents) : "",
    pairsText: pairLimitsText(settings.pairs),
  };
}

/** The limit of text, an integer from 0 to MAX_EXPOSURE; null when it isn't one. */
function limitOf(text: string): number | null {
  if (!/^\d+$/.test(text)) return null;
  const limit = Number(text);
  return limit <= MAX_EXPOSURE ? limit : null;
}

/**
 * The limits of some assets typed as "XAUUSDm=2;BTCUSDm=3": split by ; and by
 * the first =, trimmed, the blanks left out, the case of each asset kept (MT5
 * tells them apart); and why each entry is refused, in its order.
 */
export function parsePairLimits(text: string): {
  pairs: Record<string, number>;
  errors: string[];
} {
  const pairs: Record<string, number> = {};
  const errors: string[] = [];
  // each problem told once, however many times it is typed
  const refuse = (problem: string) => {
    if (!errors.includes(problem)) errors.push(problem);
  };
  for (const item of text.split(";")) {
    const entry = item.trim();
    if (entry === "") continue;
    const at = entry.indexOf("=");
    const pair = (at < 0 ? entry : entry.slice(0, at)).trim();
    const value = at < 0 ? "" : entry.slice(at + 1).trim();
    if (at < 0 || pair === "" || value === "") {
      refuse(`${entry}: use ATIVO=número, ex.: XAUUSDm=2.`);
      continue;
    }
    if (!symbol.test(pair)) {
      refuse(`${pair}: use o nome do ativo no MT5, ex.: XAUUSDm.`);
      continue;
    }
    const limit = limitOf(value);
    if (limit === null) {
      refuse(`${pair}: use um número inteiro de 0 a ${MAX_EXPOSURE}.`);
      continue;
    }
    if (Object.hasOwn(pairs, pair)) {
      refuse(`${pair}: ativo repetido, deixe um só.`);
      continue;
    }
    pairs[pair] = limit;
  }
  return { pairs, errors };
}

/** Why the fields of the form are refused: of the limit of every asset, and of each entry of the assets apart. */
export interface ExposureErrors {
  maxAgents?: string;
  pairs: string[];
}

/**
 * The request of form, or why its fields are refused, as the controller would
 * refuse them (it checks them again): the limit of every asset an integer
 * from 0 to 100 (blank for 0, none), each asset as MT5 names it with an
 * integer from 0 to 100, none repeated; the assets apart only when some.
 */
export function exposureRequest(form: ExposureForm): {
  request: ExposureRequest | null;
  errors: ExposureErrors;
} {
  const errors: ExposureErrors = { pairs: [] };
  const typed = form.maxAgents.trim();
  const maxAgents = typed === "" ? 0 : limitOf(typed);
  if (maxAgents === null) {
    errors.maxAgents = `Use um número inteiro de 0 a ${MAX_EXPOSURE}.`;
  }
  const parsed = parsePairLimits(form.pairsText);
  errors.pairs = parsed.errors;
  if (maxAgents === null || parsed.errors.length > 0) return { request: null, errors };
  const pairs = Object.keys(parsed.pairs).length > 0 ? parsed.pairs : undefined;
  return {
    request: { settings: { max_agents: maxAgents, ...(pairs ? { pairs } : {}) } },
    errors,
  };
}
