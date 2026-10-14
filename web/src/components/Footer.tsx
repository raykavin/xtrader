import echos from "../assets/echos.svg";

interface Props {
  className?: string;
}

/** The foot of the pages: the logo of who made the dashboard, then the copyright. */
export function Footer({ className = "" }: Props) {
  return (
    <footer className={`footer ${className}`.trim()}>
      <img className="footer-logo" src={echos} alt="ECHOS - Soluções em Softwares Web" />
      <span className="footer-copyright">
        © 2026 ECHOS Tecnologia. Todos os direitos reservados.
      </span>
    </footer>
  );
}
