// SPDX-License-Identifier: GPL-2.0-or-later
import { Tabs } from "./ui";

export interface TabGroup {
  id: string;
  label: string;
  tabs: { id: string; label: string }[];
}

/** The group a tab belongs to (the first group when the tab is unknown). */
export function groupOf(groups: TabGroup[], tab: string): TabGroup {
  return groups.find((g) => g.tabs.some((t) => t.id === tab)) ?? groups[0]!;
}

/**
 * Two levels of tabs: the groups across the top and, under the chosen group, its own tabs. `value`
 * and `onChange` use the id of the inner tab, so the page that shows it does not care about groups.
 * A group with one tab has no second row.
 */
export function GroupedTabs({
  groups,
  value,
  onChange,
  label = "Sections",
}: {
  groups: TabGroup[];
  value: string;
  onChange: (tab: string) => void;
  label?: string;
}) {
  const group = groupOf(groups, value);
  return (
    <div className="stack tab-groups">
      <Tabs
        label={label}
        value={group.id}
        onChange={(id) => {
          const g = groups.find((x) => x.id === id);
          if (g && g.tabs[0]) onChange(g.tabs[0].id);
        }}
        tabs={groups.map((g) => ({ id: g.id, label: g.label }))}
      />
      {group.tabs.length > 1 && (
        <div className="sub-tabs">
          <Tabs label={group.label} value={value} onChange={onChange} tabs={group.tabs} />
        </div>
      )}
    </div>
  );
}
