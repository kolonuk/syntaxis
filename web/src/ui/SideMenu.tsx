// SPDX-License-Identifier: GPL-2.0-or-later
import type { ReactNode } from "react";

export interface MenuGroup {
  label: string;
  items: { id: string; label: string }[];
}

/**
 * A vertical menu with its items under group headings, and the chosen screen beside it. On a phone
 * the menu sits above the screen and its items wrap.
 */
export function SideMenu({
  groups,
  value,
  onChange,
  label,
  children,
}: {
  groups: MenuGroup[];
  value: string;
  onChange: (id: string) => void;
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="settings">
      <nav className="settings-nav" aria-label={label}>
        {groups.map((g) => (
          <div key={g.label}>
            <h2>{g.label}</h2>
            {g.items.map((i) => (
              <button
                key={i.id}
                aria-current={i.id === value ? "page" : undefined}
                onClick={() => onChange(i.id)}
              >
                {i.label}
              </button>
            ))}
          </div>
        ))}
      </nav>
      <div className="settings-body stack">{children}</div>
    </div>
  );
}
