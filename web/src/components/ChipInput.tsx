import { type ClipboardEvent, type KeyboardEvent, useRef, useState } from "react";
import { withSymbols } from "../lib/symbols";
import { Icon } from "./Icon";

interface Props {
  /** The id of the field, for its label. */
  id: string;
  /** The assets, each a chip, in the order typed. */
  values: string[];
  onChange: (values: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
  /** Whether the field is refused, its border marked. */
  invalid?: boolean;
  /** The id of what tells the field's problem, if any. */
  describedBy?: string;
  /** Whether a value is refused, its chip marked (eg: not an asset as MT5 names it). */
  refused?: (value: string) => boolean;
}

/**
 * The assets of a field as chips: an asset typed becomes a chip on Enter, on
 * ; or , and when the field is left; several pasted at once (eg:
 * "XAUUSDm;BTCUSDm") become a chip each; each once. A chip is removed by its
 * ×, or the last one by Backspace in the empty field. Enter never sends the
 * form around it.
 */
export function ChipInput({
  id,
  values,
  onChange,
  placeholder,
  disabled,
  invalid,
  describedBy,
  refused,
}: Props) {
  const [draft, setDraft] = useState("");
  const input = useRef<HTMLInputElement>(null);

  const commit = (text: string) => {
    setDraft("");
    const next = withSymbols(values, text);
    if (next.length !== values.length) onChange(next);
  };
  const remove = (value: string) => {
    onChange(values.filter((v) => v !== value));
    input.current?.focus();
  };

  const keyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ";" || e.key === ",") {
      e.preventDefault();
      commit(draft);
    } else if (e.key === "Backspace" && draft === "" && values.length > 0) {
      e.preventDefault();
      onChange(values.slice(0, -1));
    }
  };
  const paste = (e: ClipboardEvent<HTMLInputElement>) => {
    const text = e.clipboardData.getData("text");
    if (!/[;,\s]/.test(text.trim())) return; // one asset: typed as any text
    e.preventDefault();
    commit(draft + text);
  };

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents lint/a11y/noStaticElementInteractions: a click on the box focuses its field, reached by the keyboard itself
    <div
      className="chip-input"
      data-invalid={invalid ? "" : undefined}
      data-disabled={disabled ? "" : undefined}
      onClick={(e) => {
        if (e.target === e.currentTarget) input.current?.focus();
      }}
    >
      {values.map((value) => (
        <span
          key={value}
          className="chip-input-item mono"
          data-invalid={refused?.(value) ? "" : undefined}
        >
          {value}
          <button
            type="button"
            className="chip-input-remove"
            aria-label={`Remover ${value}`}
            title="Remover"
            disabled={disabled}
            onClick={() => remove(value)}
          >
            <Icon name="close" size={12} />
          </button>
        </span>
      ))}
      <input
        ref={input}
        id={id}
        className="chip-input-field mono"
        value={draft}
        placeholder={values.length === 0 ? placeholder : undefined}
        autoCapitalize="none"
        autoComplete="off"
        spellCheck={false}
        disabled={disabled}
        aria-invalid={invalid ? true : undefined}
        aria-describedby={describedBy}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={keyDown}
        onPaste={paste}
        onBlur={() => commit(draft)}
      />
    </div>
  );
}
