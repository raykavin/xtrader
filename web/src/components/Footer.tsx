import echos from "../assets/echos.png";

interface Props {
  className?: string;
}

/** The foot of the pages: the copyright, then the logo of who made the dashboard. */
export function Footer({ className = "" }: Props) {
  return (
    <footer className={`footer ${className}`.trim()}>
      <span className="footer-copyright">
        © 2026 ECHOS Tecnologia. Todos os direitos reservados.
      </span>
      <img className="footer-logo" src={echos} alt="ECHOS - Soluções em Softwares Web" />
    </footer>
  );
}
