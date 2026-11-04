// The assets typed on the dashboard, as MT5 names them: in the windows of the
// trading hours and in the rules of the exposure limit, each a chip.

/** An asset as MT5 names it, eg: XAUUSDm, US500.cash, BTC# (agentapi's symbol). */
const symbol = /^[A-Za-z0-9][A-Za-z0-9._#-]{0,31}$/;

/** Whether text is an asset as MT5 names it. */
export function isSymbol(text: string): boolean {
  return symbol.test(text);
}

/**
 * The assets of text typed or pasted: split by ;, by , and by blanks (as a
 * list of before, "XAUUSDm;BTCUSDm", or one copied), trimmed, the blanks left
 * out, the case kept (MT5 tells them apart).
 */
export function splitSymbols(text: string): string[] {
  return text.split(/[;,\s]+/).filter((part) => part !== "");
}

/** values with the assets of text added after them, each once (the ones already there left out). */
export function withSymbols(values: string[], text: string): string[] {
  const next = [...values];
  for (const value of splitSymbols(text)) {
    if (!next.includes(value)) next.push(value);
  }
  return next;
}
