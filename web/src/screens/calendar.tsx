import { useMemo, useState, type ReactNode } from "react";
import { CalendarDays, ChevronLeft, ChevronRight } from "lucide-react";
import { api } from "../api";
import { useI18n } from "../i18n";
import { Link, useParams } from "../router";
import { useLoader } from "../state";
import type { Item, Project } from "../types";
import { Button, EmptyState, ErrorState, Loading, PageHead } from "../components/ui";
import { itemHref, TypeIcon } from "../components/items";
import { todayISO } from "./my-work";

function dateKey(date: Date) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

function CalendarView({
  items,
  loading,
  error,
  onRetry,
  title,
  lede,
  actions,
}: {
  items: Item[];
  loading?: boolean;
  error?: string;
  onRetry?: () => void;
  title: string;
  lede: string;
  actions?: ReactNode;
}) {
  const { t, locale, formatDate } = useI18n();
  const [month, setMonth] = useState(() => {
    const now = new Date();
    return new Date(now.getFullYear(), now.getMonth(), 1);
  });

  const byDate = useMemo(() => {
    const map = new Map<string, Item[]>();
    for (const item of items) {
      if (!item.due_on) {
        continue;
      }
      const list = map.get(item.due_on) ?? [];
      list.push(item);
      map.set(item.due_on, list);
    }
    return map;
  }, [items]);

  const cells = useMemo(() => {
    const year = month.getFullYear();
    const zeroMonth = month.getMonth();
    const firstWeekday = (new Date(year, zeroMonth, 1).getDay() + 6) % 7;
    const start = new Date(year, zeroMonth, 1 - firstWeekday);
    return Array.from({ length: 42 }, (_, index) => {
      const date = new Date(start.getFullYear(), start.getMonth(), start.getDate() + index);
      return {
        date,
        key: dateKey(date),
        outside: date.getMonth() !== zeroMonth,
      };
    });
  }, [month]);

  const monthItems = useMemo(() => {
    const year = month.getFullYear();
    const zeroMonth = month.getMonth();
    return items
      .filter((item) => {
        if (!item.due_on) {
          return false;
        }
        const date = new Date(`${item.due_on}T00:00:00`);
        return date.getFullYear() === year && date.getMonth() === zeroMonth;
      })
      .sort((a, b) => a.due_on.localeCompare(b.due_on));
  }, [items, month]);

  const weekdays = useMemo(() => {
    const formatter = new Intl.DateTimeFormat(locale === "it" ? "it-IT" : "en-GB", { weekday: "short" });
    return Array.from({ length: 7 }, (_, index) => {
      const date = new Date(2024, 0, 1 + index);
      return formatter.format(date);
    });
  }, [locale]);

  const monthLabel = new Intl.DateTimeFormat(locale === "it" ? "it-IT" : "en-GB", {
    month: "long",
    year: "numeric",
  }).format(month);
  const today = todayISO();

  return (
    <>
      <PageHead title={title} lede={lede} actions={actions} />
      {loading ? <Loading /> : null}
      {error ? <ErrorState message={error} onRetry={onRetry} /> : null}

      {!loading && !error ? (
        <>
          <div className="card" style={{ overflow: "hidden" }}>
            <div className="calendar-head">
              <div className="cluster">
                <Button variant="secondary" size="sm" aria-label={t("Previous month")} onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}>
                  <ChevronLeft />
                </Button>
                <Button variant="secondary" size="sm" aria-label={t("Next month")} onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}>
                  <ChevronRight />
                </Button>
                <span className="calendar-month">{monthLabel}</span>
              </div>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  const now = new Date();
                  setMonth(new Date(now.getFullYear(), now.getMonth(), 1));
                }}
              >
                {t("This month")}
              </Button>
            </div>

            <div className="calendar-grid-wrap">
              <div className="calendar-grid">
                {weekdays.map((weekday) => (
                  <div key={weekday} className="calendar-weekday">
                    {weekday}
                  </div>
                ))}
                {cells.map((cell) => {
                  const dayItems = byDate.get(cell.key) ?? [];
                  const isToday = cell.key === today;
                  return (
                    <div
                      key={cell.key}
                      className={[
                        "calendar-day",
                        cell.outside ? "calendar-day--outside" : "",
                        isToday ? "calendar-day--today" : "",
                      ]
                        .filter(Boolean)
                        .join(" ")}
                    >
                      <div className="calendar-day-head">
                        <span className="calendar-day-number">{cell.date.getDate()}</span>
                        {dayItems.length > 0 ? <span className="calendar-day-count">{dayItems.length}</span> : null}
                      </div>
                      {dayItems.slice(0, 3).map((item) => (
                        <Link
                          key={item.id}
                          to={itemHref(item)}
                          className={`calendar-chip ${
                            item.state_category === "done"
                              ? "calendar-chip--done"
                              : item.due_on < today
                                ? "calendar-chip--bad"
                                : ""
                          }`}
                          title={item.title}
                        >
                          {item.title}
                        </Link>
                      ))}
                      {dayItems.length > 3 ? <span className="calendar-day-count">+{dayItems.length - 3}</span> : null}
                    </div>
                  );
                })}
              </div>
            </div>

            <div className="calendar-agenda">
              {monthItems.length === 0 ? (
                <EmptyState icon={<CalendarDays />} title={t("Nothing scheduled for this month.")} />
              ) : (
                monthItems.map((item) => (
                  <div key={item.id} className="agenda-day">
                    <div className="agenda-date">
                      <span>{formatDate(item.due_on)}</span>
                      <span>{item.project_name}</span>
                    </div>
                    <Link to={itemHref(item)} className="agenda-item">
                      <TypeIcon type={item.type} />
                      <span className="grow small">{item.title}</span>
                      <span className="tag">{item.state_name}</span>
                    </Link>
                  </div>
                ))
              )}
            </div>
          </div>

          {monthItems.length === 0 ? (
            <div className="empty">
              <h3 className="small">{t("No due dates")}</h3>
              <p>{t("Nothing scheduled for this month.")}</p>
            </div>
          ) : null}
        </>
      ) : null}
    </>
  );
}

export function CalendarScreen() {
  const { t } = useI18n();
  const state = useLoader(() => api<{ items: Item[] }>("/api/items?assignee_id=me&limit=500"), []);
  const items = (state.data?.items ?? []).filter((item) => item.due_on);
  return (
    <CalendarView
      items={items}
      loading={state.loading}
      error={state.error}
      onRetry={state.reload}
      title={t("Calendar")}
      lede={t("Your deadlines, month by month.")}
    />
  );
}

export function ProjectCalendarScreen() {
  const { t } = useI18n();
  const params = useParams();
  const projectID = params.id;

  const projectState = useLoader(() => api<Project>(`/api/projects/${projectID}`), [projectID]);
  const itemsState = useLoader(
    () => api<{ items: Item[] }>(`/api/items?project_id=${projectID}&limit=500`),
    [projectID],
  );

  if (projectState.loading) {
    return <Loading />;
  }
  if (projectState.error || !projectState.data) {
    return (
      <>
        <PageHead title={t("Project calendar")} />
        <ErrorState message={projectState.error || t("Project not found")} onRetry={projectState.reload} />
        <Link to="/projects" className="btn btn--secondary" style={{ marginTop: 16 }}>
          {t("All projects")}
        </Link>
      </>
    );
  }

  const items = (itemsState.data?.items ?? []).filter((item) => item.due_on);
  return (
    <CalendarView
      items={items}
      loading={itemsState.loading}
      error={itemsState.error}
      onRetry={itemsState.reload}
      title={projectState.data.name}
      lede={t("All items in this project with a due date.")}
      actions={
        <Link to={`/projects/${projectID}`} className="btn btn--secondary">
          {t("Back to project")}
        </Link>
      }
    />
  );
}
