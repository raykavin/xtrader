import { Icon } from "./Icon";

interface Props {
  value: string;
  onChange: (value: string) => void;
  /** What it searches, read to a screen reader, eg: "Buscar prompts". */
  label: string;
  /** The fields it searches, eg: "Buscar por nome, descrição ou agente". */
  placeholder: string;
  /** The ID of the list it filters. */
  controls?: string;
  /** What the search found, see searchNote; none without a search. */
  note?: string | null;
}

/**
 * The search of a list, as the one of the agents: the magnifier in the field,
 * Escape and the button at its end clearing it, and what it found below it.
 */
export function SearchField({ value, onChange, label, placeholder, controls, note }: Props) {
  return (
    <>
      <search className="list-search">
        <Icon name="search" size={16} />
        <input
          type="search"
          className="input list-search-input"
          value={value}
          placeholder={placeholder}
          aria-label={label}
          aria-controls={controls}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") onChange("");
          }}
        />
        {value !== "" && (
          <button
            type="button"
            className="btn btn-sm btn-ghost btn-icon list-search-clear"
            aria-label="Limpar a busca"
            title="Limpar"
            onClick={() => onChange("")}
          >
            <Icon name="close" size={14} />
          </button>
        )}
      </search>
      {note && (
        <p className="list-search-note" aria-live="polite">
          {note}
        </p>
      )}
    </>
  );
}
