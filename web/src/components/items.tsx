import { Bug, CircleDot, CornerDownRight, Eye } from "lucide-react";
import { Link } from "../router";
import { useI18n } from "../i18n";
import type { Item, ItemType, Priority } from "../types";
import { AvatarStack, Badge } from "./ui";

export function typeLabel(type: ItemType, t: (text: string) => string) {
  switch (type) {
    case "observer":
      return t("Observer");
    case "action":
      return t("Action");
    case "micro":
      return t("Micro");
    case "bug":
      return t("Bug");
    default:
      return type;
  }
}

export function TypeIcon({ type }: { type: ItemType }) {
  const Icon = type === "observer" ? Eye : type === "micro" ? CornerDownRight : type === "bug" ? Bug : CircleDot;
  return (
    <span className={`type-icon type-icon--${type}`} aria-hidden="true">
      <Icon />
    </span>
  );
}

export function TypeTag({ type }: { type: ItemType }) {
  const { t } = useI18n();
  return (
    <span className="cluster" style={{ gap: 6 }}>
      <TypeIcon type={type} />
      <span className="small strong">{typeLabel(type, t)}</span>
    </span>
  );
}

export function PriorityBadge({ priority }: { priority: Priority }) {
  const { t } = useI18n();
  const tone = priority === "urgent" ? "bad" : priority === "high" ? "warn" : priority === "medium" ? "accent" : "neutral";
  const label =
    priority === "urgent"
      ? t("Urgent")
      : priority === "high"
        ? t("High")
        : priority === "medium"
          ? t("Medium")
          : priority === "low"
            ? t("Low")
            : t("None");
  return <Badge tone={priority === "none" ? "mono" : tone}>{label}</Badge>;
}

export function StateBadge({ name, category }: { name: string; category: string }) {
  return <Badge tone={category === "done" ? "good" : "accent"}>{name}</Badge>;
}

export function DueLabel({
  date,
  category,
}: {
  date: string;
  category?: string;
}) {
  const { t, formatDate } = useI18n();
  if (!date) {
    return <span className="due">—</span>;
  }
  const today = new Date();
  const iso = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, "0")}-${String(today.getDate()).padStart(2, "0")}`;
  const done = category === "done";
  const overdue = !done && date < iso;
  const isToday = !done && date === iso;
  const text = isToday ? t("Today") : formatDate(date, { year: undefined });
  return (
    <span className={`due ${overdue ? "due--overdue" : isToday ? "due--today" : ""}`}>
      {overdue ? `${text} · ${t("Overdue")}` : text}
    </span>
  );
}

export function itemHref(item: Pick<Item, "project_id" | "id">) {
  return `/projects/${item.project_id}/items/${item.id}`;
}

// The server returns plain text with «» around matched words, never HTML. The
// split keeps React in charge of escaping, so a title containing markup stays
// text.
function SearchSnippet({ text }: { text: string }) {
  const parts = text.split(/(«[^»]*»)/g).filter(Boolean);
  return (
    <span className="search-snippet">
      {parts.map((part, index) =>
        part.startsWith("«") && part.endsWith("»") ? (
          <mark key={index}>{part.slice(1, -1)}</mark>
        ) : (
          <span key={index}>{part}</span>
        ),
      )}
    </span>
  );
}

export function ItemTable({
  items,
  showProject = false,
}: {
  items: Item[];
  showProject?: boolean;
}) {
  const { t } = useI18n();
  return (
    <div className="table-wrap">
      <table className="data-table">
        <thead>
          <tr>
            <th>{t("Item")}</th>
            {showProject ? <th>{t("Project")}</th> : null}
            <th>{t("State")}</th>
            <th>{t("Priority")}</th>
            <th>{t("Due date")}</th>
            <th>{t("Assignees")}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((item) => (
            <tr key={item.id}>
              <td className="cell-primary">
                <Link to={itemHref(item)} className="cell-title">
                  <TypeIcon type={item.type} />
                  <span className="cell-title-text">{item.title}</span>
                </Link>
                {item.snippet ? <SearchSnippet text={item.snippet} /> : null}
                {item.match === "comments" ? (
                  <span className="tag tag--accent search-hit">{t("Found in comments")}</span>
                ) : null}
              </td>
              {showProject ? (
                <td data-label={t("Project")}>
                  <Link to={`/projects/${item.project_id}`} className="small">
                    {item.project_name}
                  </Link>
                </td>
              ) : null}
              <td data-label={t("State")}>
                <StateBadge name={item.state_name} category={item.state_category} />
              </td>
              <td data-label={t("Priority")}>
                <PriorityBadge priority={item.priority} />
              </td>
              <td data-label={t("Due date")}>
                <DueLabel date={item.due_on} category={item.state_category} />
              </td>
              <td data-label={t("Assignees")}>
                <AvatarStack
                  people={item.assignees.map((assignee) => ({ id: assignee.user_id, name: assignee.name }))}
                />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
