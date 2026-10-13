import { useEffect, useState } from "react";
import {
  CARD_LABELS,
  clearLayout,
  defaultLayout,
  hasSavedLayout,
  LAYOUT_KEY,
  loadLayout,
  ZONES,
} from "../lib/layout";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import type { SectionIntro } from "./SettingsPage";
import { Alert } from "./States";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
}

/** What the layout saved in this browser changes from the default one, a line each. */
function changesOf(): string[] {
  if (!hasSavedLayout()) return [];
  const layout = loadLayout();
  const fallback = defaultLayout();
  const changes: string[] = [];
  if (ZONES.some((zone) => layout.zones[zone].join() !== fallback.zones[zone].join())) {
    changes.push("Cards em outra ordem ou em outra área");
  }
  if (layout.hidden.length > 0) {
    changes.push(`Ocultos: ${layout.hidden.map((card) => CARD_LABELS[card]).join(", ")}`);
  }
  if (layout.chartHeight !== null) changes.push(`Altura do gráfico: ${layout.chartHeight} px`);
  return changes;
}

/**
 * The layout of the dashboard in this browser: what the customization changed,
 * and its reset, which removes the saved layout alone and gives the default
 * one back at once, the open dashboards included.
 */
export function LayoutSettings({ intro }: Props) {
  const [saved, setSaved] = useState(hasSavedLayout);
  const [changes, setChanges] = useState(changesOf);
  const [restored, setRestored] = useState(false);

  // the dashboard of another tab customizing it meanwhile
  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== LAYOUT_KEY && event.key !== null) return;
      setSaved(hasSavedLayout());
      setChanges(changesOf());
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const restore = () => {
    clearLayout();
    setSaved(hasSavedLayout());
    setChanges(changesOf());
    setRestored(true);
  };

  return (
    <div className="settings-section">
      <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
      <section className="card card-flush" aria-labelledby="layout-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Painel</span>
            <h3 id="layout-title" className="card-title">
              Layout dos cards
            </h3>
          </div>
          <span className={`status-chip ${saved ? "on" : "off"}`}>
            {saved ? "personalizado" : "padrão"}
          </span>
        </header>
        <div className="card-body stack">
          <p className="layout-settings-text">
            No painel, o botão de layout da barra superior (o ícone de grade) move, oculta e
            redimensiona os cards. A disposição fica salva neste navegador.
          </p>
          {saved && changes.length > 0 && (
            <ul className="layout-settings-changes">
              {changes.map((change) => (
                <li key={change}>{change}</li>
              ))}
            </ul>
          )}
          {restored && !saved && (
            <Alert tone="success" title="Layout padrão restaurado">
              O painel volta à disposição original, com todos os cards e a altura padrão do gráfico,
              inclusive nas abas já abertas.
            </Alert>
          )}
          <div>
            <button type="button" className="btn btn-sm" disabled={!saved} onClick={restore}>
              <Icon name="restart" size={14} />
              Restaurar layout padrão
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}
