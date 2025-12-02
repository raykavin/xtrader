import echos from "../assets/echos.png";

interface Props {
  className?: string;
}

/** The foot of the pages: who made the dashboard, small. */
export function Footer({ className = "" }: Props) {
  return (
    <footer className={`footer ${className}`.trim()}>
      <img className="footer-logo" src={echos} alt="ECHOS - Soluções em Softwares Web" />
    </footer>
  );
}
