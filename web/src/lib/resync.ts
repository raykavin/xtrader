// What the dashboard loads again as the live events are back after a drop
// (see liveStream), and when the decisions shown are cleared rather than
// kept until the new ones come.

import type { Snapshot } from "../api/types";

/**
 * The loads of the dashboard a resync repeats, each one counted: the effect
 * loading it depends on its count.
 */
export interface Resyncs {
  /** The snapshot of the pair. */
  snapshot: number;
  /** The decisions of the language model of the pair. */
  decisions: number;
  /** The state of the bot controls. */
  controls: number;
}

export const noResyncs: Resyncs = { snapshot: 0, decisions: 0, controls: 0 };

/**
 * The events are back after a drop: the ones sent meanwhile were missed, so
 * the snapshot, the decisions and the controls are loaded again.
 */
export function resynced(r: Resyncs): Resyncs {
  return { snapshot: r.snapshot + 1, decisions: r.decisions + 1, controls: r.controls + 1 };
}

/** Whether the strategy of the snapshot, the one of pair, keeps the decisions of a language model. */
export function keepsDecisions(snapshot: Snapshot | null, pair: string | null): boolean {
  return snapshot?.llm?.decisions === true && snapshot.pair === pair;
}

/** What the effect of the decisions does as it runs, see decisionsLoad. */
export interface DecisionsLoad {
  /** The decisions shown are now the ones of key. */
  key: string;
  /** The list shown, and its error, are cleared. */
  clear: boolean;
  /** The decisions are loaded. */
  load: boolean;
}

/**
 * What the effect of the decisions does as it runs, shown the ones of shownFor
 * (null for none yet): the list is cleared when the agent, the pair or whether
 * its strategy keeps decisions changed, and kept otherwise, eg: on a resync,
 * until the new list comes; loaded for a pair whose strategy keeps them.
 */
export function decisionsLoad(
  shownFor: string | null,
  agent: string,
  pair: string | null,
  keeps: boolean,
): DecisionsLoad {
  const key = `${agent}\n${pair}\n${keeps}`;
  return { key, clear: shownFor !== key, load: Boolean(pair) && keeps };
}
