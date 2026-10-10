import { UI_VERSION } from "../api/client";
import echos from "../assets/echos.png";

interface Props {
  className?: string;
}

/** The foot of the pages: who made the dashboard, and its version, small. */
export function Footer({ className = "" }: Props) {
  return (
    <footer className={`footer ${className}`.trim()}>
      <img className="footer-logo" src={echos} alt="ECHOS - Soluções em Softwares Web" />
      <span className="footer-version" title="Versão do painel">
        {/^\d/.test(UI_VERSION) ? `v${UI_VERSION}` : UI_VERSION}
      </span>
    </footer>
  );
}
