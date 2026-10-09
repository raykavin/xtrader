import type { ReactNode } from "react";

interface Props {
  title: ReactNode;
  /** What the page is for, below the title. */
  description?: ReactNode;
  /** The main actions of the page, at its right, eg: creating an item. */
  actions?: ReactNode;
}

/** The head of a page or of a section of the settings: its title, what it is for, its actions. */
export function PageHeader({ title, description, actions }: Props) {
  return (
    <div className="page-header">
      <div className="page-heading">
        <h2 className="page-title">{title}</h2>
        {description && <p className="page-description">{description}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </div>
  );
}
