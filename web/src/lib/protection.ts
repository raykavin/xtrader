// The protection of the open positions of an agent on the dashboard: its
// breakeven and its trailing stop, their states on a position, their moves,
// and the form of its settings, checked as the controller checks them.

import type {
  PositionProtection,
  ProtectionFeature,
  ProtectionMove,
  ProtectionSettings,
} from "../api/types";
import { formatPrice } from "./format";

/** The unit of every distance: multiples of the ATR(14) of the closed candles of the timeframe of the agent. */
export const unitLabel = "× ATR(14)";

/** The longest distance the controller takes, in ATR. */
export const maxDistance = 50;

/** A feature, in words. */
export function featureLabel(feature: "breakeven" | "trailing" | string): string {
  return feature === "trailing" ? "Trailing stop" : "Breakeven";
}

/** A tone of the chips of the dashboard. */
export type ProtectionTone = "on" | "off" | "warn" | "error" | "info";

/** A label and its tone. */
export interface ProtectionLabel {
  text: string;
  tone: ProtectionTone;
}

/** A distance in ATR, eg: 0,4 ATR. */
export function atr(value: number): string {
  return `${value.toLocaleString("pt-BR", { maximumFractionDigits: 2 })} ATR`;
}

/** The state of a feature on a position, eg: aguardando o gatilho (faltam 0,4 ATR). */
export function featureState(feature: ProtectionFeature): ProtectionLabel {
  switch (feature.state) {
    case "waiting":
      return {
        text: feature.detail
          ? feature.detail
          : `aguardando o gatilho, faltam ${atr(feature.remaining ?? 0)}`,
        tone: "info",
      };
    case "active":
      return {
        text: feature.level ? `ativo, stop em ${formatPrice(feature.level)}` : "ativo",
        tone: "on",
      };
    case "unavailable":
      return { text: feature.detail ?? "indisponível", tone: "warn" };
  }
  return {
    text: feature.armed ? (feature.detail ?? "armado, sem permissão") : "desligado",
    tone: "off",
  };
}

/** The last move of a feature: solicitado, confirmado pela corretora, recusado (o stop mantido) or desconhecido. */
export function moveLabel(move: ProtectionMove): ProtectionLabel {
  switch (move.status) {
    case "requested":
      return { text: `solicitado: stop em ${formatPrice(move.requested)}…`, tone: "info" };
    case "confirmed":
      return {
        text: `confirmado pela corretora: stop em ${formatPrice(move.confirmed || move.requested)}`,
        tone: "on",
      };
    case "rejected":
      return {
        text: `recusado pela corretora; stop mantido em ${move.previous ? formatPrice(move.previous) : "nenhum"}`,
        tone: "error",
      };
  }
  return { text: "sem resposta da corretora: relendo o stop", tone: "warn" };
}

/** The features of a position worth showing: the ones not off, or armed. */
export function shownFeatures(
  protection: PositionProtection | undefined,
): { name: "breakeven" | "trailing"; feature: ProtectionFeature }[] {
  if (!protection) return [];
  const features: { name: "breakeven" | "trailing"; feature: ProtectionFeature }[] = [
    { name: "breakeven", feature: protection.breakeven },
    { name: "trailing", feature: protection.trailing },
  ];
  return features.filter(({ feature }) => feature.state !== "off" || feature.armed);
}

/** The fields of the form of the protection, as typed. */
export interface ProtectionForm {
  breakevenEnabled: boolean;
  breakevenSupervisor: boolean;
  breakevenTrigger: string;
  breakevenMargin: string;
  trailingEnabled: boolean;
  trailingSupervisor: boolean;
  trailingTrigger: string;
  trailingDistance: string;
  trailingStep: string;
}

/** The form of settings, the distances left blank when 0. */
export function protectionForm(settings: ProtectionSettings): ProtectionForm {
  const text = (value: number) => (value ? String(value) : "");
  return {
    breakevenEnabled: settings.breakeven.enabled,
    breakevenSupervisor: settings.breakeven.supervisor,
    breakevenTrigger: text(settings.breakeven.trigger),
    breakevenMargin: text(settings.breakeven.margin),
    trailingEnabled: settings.trailing.enabled,
    trailingSupervisor: settings.trailing.supervisor,
    trailingTrigger: text(settings.trailing.trigger),
    trailingDistance: text(settings.trailing.distance),
    trailingStep: text(settings.trailing.step),
  };
}

/** A distance typed, with a comma or a dot, 0 when blank; NaN when not a number. */
export function parseDistance(text: string): number {
  const trimmed = text.trim().replace(",", ".");
  if (trimmed === "") return 0;
  return /^\d+(\.\d+)?$/.test(trimmed) ? Number(trimmed) : Number.NaN;
}

/** The settings of a form, its distances parsed. */
export function protectionSettings(form: ProtectionForm): ProtectionSettings {
  return {
    unit: "atr",
    breakeven: {
      enabled: form.breakevenEnabled,
      supervisor: form.breakevenSupervisor,
      trigger: parseDistance(form.breakevenTrigger),
      margin: parseDistance(form.breakevenMargin),
    },
    trailing: {
      enabled: form.trailingEnabled,
      supervisor: form.trailingSupervisor,
      trigger: parseDistance(form.trailingTrigger),
      distance: parseDistance(form.trailingDistance),
      step: parseDistance(form.trailingStep),
    },
  };
}

/** Why each field is refused, by field, as the controller refuses it; empty when the form is taken. */
export function protectionErrors(
  form: ProtectionForm,
): Partial<Record<keyof ProtectionForm, string>> {
  const errors: Partial<Record<keyof ProtectionForm, string>> = {};
  const settings = protectionSettings(form);
  const check = (field: keyof ProtectionForm, value: number) => {
    if (Number.isNaN(value)) errors[field] = "Digite um número, ex.: 0,5.";
    else if (value > maxDistance) errors[field] = `No máximo ${maxDistance} ATR.`;
  };
  check("breakevenTrigger", settings.breakeven.trigger);
  check("breakevenMargin", settings.breakeven.margin);
  check("trailingTrigger", settings.trailing.trigger);
  check("trailingDistance", settings.trailing.distance);
  check("trailingStep", settings.trailing.step);

  const b = settings.breakeven;
  if ((b.enabled || b.supervisor) && !errors.breakevenTrigger && !errors.breakevenMargin) {
    if (b.trigger <= 0) errors.breakevenTrigger = "Defina o gatilho.";
    else if (b.margin >= b.trigger) errors.breakevenMargin = "Deve ser menor que o gatilho.";
  }
  const t = settings.trailing;
  if (
    (t.enabled || t.supervisor) &&
    !errors.trailingTrigger &&
    !errors.trailingDistance &&
    !errors.trailingStep
  ) {
    if (t.trigger <= 0) errors.trailingTrigger = "Defina o gatilho.";
    if (t.distance <= 0) errors.trailingDistance = "Defina a distância.";
    if (t.step <= 0) errors.trailingStep = "Defina o passo.";
    else if (t.distance > 0 && t.step > t.distance) errors.trailingStep = "No máximo a distância.";
  }
  return errors;
}
