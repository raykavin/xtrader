import { useState } from "react";
import type { PositionProtection as Protection, ProtectionFeature } from "../api/types";
import { featureLabel, featureState, moveLabel, shownFeatures } from "../lib/protection";
import { Icon } from "./Icon";

/**
 * The protection of the open position of the pair, on the chart, which draws
 * its stop loss: each feature on (breakeven, trailing stop) with its state,
 * its last move as the broker answered it (a stop never shown applied before
 * the broker confirmed it), and the mark of the XTrader AI when it armed it,
 * its reason a tap away. Nothing without a position or its protection.
 */
export function PositionProtection({ protection }: { protection?: Protection }) {
  const features = shownFeatures(protection);
  if (features.length === 0) return null;
  return (
    <ul className="position-protection" aria-label="Proteção da posição">
      <li className="position-protection-title">
        <Icon name="shield" size={13} />
        Proteção
      </li>
      {features.map(({ name, feature }) => (
        <ProtectionItem key={name} name={name} feature={feature} />
      ))}
    </ul>
  );
}

function ProtectionItem({ name, feature }: { name: string; feature: ProtectionFeature }) {
  const [reason, setReason] = useState(false);
  const state = featureState(feature);
  const last = feature.last ? moveLabel(feature.last) : null;
  return (
    <li className="protection-row">
      <span className={`protection-chip ${state.tone}`}>{featureLabel(name)}</span>
      <span className="protection-state">{state.text}</span>
      {last && <span className={`protection-move ${last.tone}`}>· {last.text}</span>}
      {feature.armed && (
        <button
          type="button"
          className="protection-ai"
          aria-expanded={reason}
          title={
            feature.arm_reason
              ? `Armado pelo XTrader AI: ${feature.arm_reason}`
              : "Armado pelo XTrader AI"
          }
          onClick={() => setReason((shown) => !shown)}
        >
          XTrader AI
        </button>
      )}
      {reason && feature.arm_reason && (
        <span className="protection-reason">Motivo do XTrader AI: {feature.arm_reason}</span>
      )}
    </li>
  );
}
