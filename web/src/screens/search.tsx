import { useMemo, useState, type FormEvent } from "react";
import { Search as SearchIcon, SlidersHorizontal, X } from "lucide-react";
import { api, queryString } from "../api";
import { useI18n } from "../i18n";
import { useRouter, useQueryState } from "../router";
import { useLoader } from "../state";
import type { Item, Person, WorkflowStateRef } from "../types";
import { ItemTable } from "../components/items";
import { Button, EmptyState, ErrorState, Field, Input, Loading, PageHead, Select } from "../components/ui";
import { addDaysISO, todayISO } from "./my-work";

const RECENT_KEY = "work:recent-searches";
const RECENT_LIMIT = 8;

function loadRecent(): string[] {
  try {
    const raw = window.localStorage.getItem(RECENT_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed.filter((value): value is string => typeof value === "string").slice(0, RECENT_LIMIT);
  } catch {
    return [];
  }
}

function storeRecent(value: string) {
  try {
    window.localStorage.setItem(RECENT_KEY, JSON.stringify(value));
  } catch {
    // Private browsing can disable storage; recent searches are optional.
  }
}

export function SearchScreen() {
  const { t } = useI18n();
  const { navigate } = useRouter();
  const params = useQueryState();
  const [query, setQuery] = useState(params.get("q") ?? "");
  const [recent, setRecent] = useState<string[]>(loadRecent);

  const q = params.get("q") ?? "";
  const type = params.get("type") ?? "";
  const stateID = params.get("state_id") ?? "";
  const stateCategory = params.get("state_category") ?? "";
  const priority = params.get("priority") ?? "";
  const projectID = params.get("project_id") ?? "";
  const assigneeID = params.get("assignee_id") ?? "";
  const hasAssignee = params.get("has_assignee") ?? "";
  const due = params.get("due") ?? "";
  const createdFrom = params.get("created_from") ?? "";
  const createdTo = params.get("created_to") ?? "";
  const updatedFrom = params.get("updated_from") ?? "";
  const updatedTo = params.get("updated_to") ?? "";
  const sort = params.get("sort") ?? "";

  const peopleState = useLoader(() => api<{ people: Person[] }>("/api/people"), []);
  const workflowState = useLoader(() => api<{ states: WorkflowStateRef[] }>("/api/workflow/states"), []);
  const stateGroups = useMemo(() => {
    const groups = new Map<number, { projectName: string; states: WorkflowStateRef[] }>();
    for (const state of workflowState.data?.states ?? []) {
      const group = groups.get(state.project_id) ?? { projectName: state.project_name, states: [] };
      group.states.push(state);
      groups.set(state.project_id, group);
    }
    return Array.from(groups.entries()).sort((a, b) => a[1].projectName.localeCompare(b[1].projectName));
  }, [workflowState.data]);

  const search = useMemo(() => {
    const today = todayISO();
    const query: Record<string, string | number> = {
      q,
      type,
      state_id: stateID,
      state_category: stateCategory,
      priority,
      project_id: projectID,
      assignee_id: assigneeID,
      has_assignee: hasAssignee,
      created_from: createdFrom,
      created_to: createdTo,
      updated_from: updatedFrom,
      updated_to: updatedTo,
      sort,
      limit: 500,
      total: 1,
    };
    if (due === "overdue") {
      query.has_due = 1;
      query.due_to = addDaysISO(today, -1);
      // "Open only" is part of the meaning of overdue; keep an explicit
      // category filter from the URL if the user chose one.
      query.state_category = stateCategory || "open";
    } else if (due === "today") {
      query.due_from = today;
      query.due_to = today;
    } else if (due === "week") {
      query.due_from = today;
      query.due_to = addDaysISO(today, 7);
    } else if (due === "none") {
      query.no_due = 1;
    }
    return queryString(query);
  }, [
    q,
    type,
    stateID,
    stateCategory,
    priority,
    projectID,
    assigneeID,
    hasAssignee,
    due,
    createdFrom,
    createdTo,
    updatedFrom,
    updatedTo,
    sort,
  ]);

  const itemsState = useLoader(
    () => api<{ items: Item[]; total?: number }>(`/api/items${search}`),
    [search],
  );

  const items = itemsState.data?.items ?? [];
  const total = itemsState.data?.total ?? items.length;

  function update(next: Record<string, string>) {
    const merged = new URLSearchParams(params);
    for (const [key, value] of Object.entries(next)) {
      if (value) {
        merged.set(key, value);
      } else {
        merged.delete(key);
      }
    }
    const text = merged.toString();
    navigate(text ? `/search?${text}` : "/search");
  }

  function remember(value: string) {
    const trimmed = value.trim();
    if (!trimmed) {
      return;
    }
    setRecent((current) => {
      const next = [trimmed, ...current.filter((entry) => entry.toLowerCase() !== trimmed.toLowerCase())].slice(
        0,
        RECENT_LIMIT,
      );
      storeRecent(JSON.stringify(next));
      return next;
    });
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = query.trim();
    update({ q: trimmed });
    remember(trimmed);
  }

  function useRecent(value: string) {
    setQuery(value);
    update({ q: value });
    remember(value);
  }

  function clearFilters() {
    setQuery("");
    navigate("/search");
  }

  const hasFilters = Boolean(
    q ||
      type ||
      stateID ||
      stateCategory ||
      priority ||
      projectID ||
      assigneeID ||
      hasAssignee ||
      due ||
      createdFrom ||
      createdTo ||
      updatedFrom ||
      updatedTo ||
      sort,
  );

  return (
    <>
      <PageHead title={t("Search")} lede={t("Find work across every project you can read.")} />

      <form className="toolbar" role="search" onSubmit={submit}>
        <Field label={t("Search work")} className="field--grow">
          <div className="search-field">
            <SearchIcon />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("Search titles, descriptions and comments…")}
              type="search"
              autoFocus
            />
          </div>
        </Field>
        <Button type="submit" variant="primary" icon={<SearchIcon />}>
          {t("Search")}
        </Button>
      </form>

      {recent.length > 0 ? (
        <div className="cluster recent-searches" aria-label={t("Recent searches")}>
          <span className="tiny muted">{t("Recent searches")}</span>
          {recent.map((entry) => (
            <button key={entry} type="button" className="chip chip-button" onClick={() => useRecent(entry)}>
              {entry}
            </button>
          ))}
        </div>
      ) : null}

      <div className="toolbar">
        <Field label={t("Type")}>
          <Select value={type} onChange={(event) => update({ type: event.target.value })}>
            <option value="">{t("All types")}</option>
            <option value="observer">{t("Observer")}</option>
            <option value="action">{t("Action")}</option>
            <option value="micro">{t("Micro")}</option>
            <option value="bug">{t("Bug")}</option>
          </Select>
        </Field>
        <Field label={t("Category")}>
          <Select
            value={stateCategory}
            onChange={(event) => update({ state_category: event.target.value })}
          >
            <option value="">{t("All categories")}</option>
            <option value="open">{t("Open")}</option>
            <option value="done">{t("Done")}</option>
          </Select>
        </Field>
        <Field label={t("State")}>
          <Select value={stateID} onChange={(event) => update({ state_id: event.target.value })}>
            <option value="">{t("All states")}</option>
            {stateGroups.map(([projectID, group]) => (
              <optgroup key={projectID} label={group.projectName}>
                {group.states.map((state) => (
                  <option key={state.id} value={state.id}>
                    {state.name} · {state.workflow_name}
                  </option>
                ))}
              </optgroup>
            ))}
          </Select>
        </Field>
        <Field label={t("Priority")}>
          <Select value={priority} onChange={(event) => update({ priority: event.target.value })}>
            <option value="">{t("All priorities")}</option>
            <option value="urgent">{t("Urgent")}</option>
            <option value="high">{t("High")}</option>
            <option value="medium">{t("Medium")}</option>
            <option value="low">{t("Low")}</option>
            <option value="none">{t("None")}</option>
          </Select>
        </Field>
        <Field label={t("Assignee")}>
          <Select value={assigneeID} onChange={(event) => update({ assignee_id: event.target.value })}>
            <option value="">{t("Anyone")}</option>
            {(peopleState.data?.people ?? []).map((person) => (
              <option key={person.id} value={person.id}>
                {person.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("Assignment")}>
          <Select value={hasAssignee} onChange={(event) => update({ has_assignee: event.target.value })}>
            <option value="">{t("Any")}</option>
            <option value="1">{t("Has an assignee")}</option>
          </Select>
        </Field>
        <Field label={t("Due date")}>
          <Select value={due} onChange={(event) => update({ due: event.target.value })}>
            <option value="">{t("Any")}</option>
            <option value="overdue">{t("Overdue")}</option>
            <option value="today">{t("Due today")}</option>
            <option value="week">{t("Next 7 days")}</option>
            <option value="none">{t("No due date")}</option>
          </Select>
        </Field>
        <Field label={t("Sort by")}>
          <Select value={sort} onChange={(event) => update({ sort: event.target.value })}>
            <option value="">{t("Default")}</option>
            <option value="relevance">{t("Relevance")}</option>
            <option value="updated">{t("Recently updated")}</option>
            <option value="created">{t("Recently created")}</option>
            <option value="due">{t("Due date")}</option>
            <option value="priority">{t("Priority")}</option>
          </Select>
        </Field>
      </div>

      <div className="toolbar">
        <Field label={t("Created from")}>
          <Input
            type="date"
            value={createdFrom}
            max={createdTo || undefined}
            onChange={(event) => update({ created_from: event.target.value })}
          />
        </Field>
        <Field label={t("Created to")}>
          <Input
            type="date"
            value={createdTo}
            min={createdFrom || undefined}
            onChange={(event) => update({ created_to: event.target.value })}
          />
        </Field>
        <Field label={t("Updated from")}>
          <Input
            type="date"
            value={updatedFrom}
            max={updatedTo || undefined}
            onChange={(event) => update({ updated_from: event.target.value })}
          />
        </Field>
        <Field label={t("Updated to")}>
          <Input
            type="date"
            value={updatedTo}
            min={updatedFrom || undefined}
            onChange={(event) => update({ updated_to: event.target.value })}
          />
        </Field>
        {hasFilters ? (
          <Button variant="ghost" icon={<X />} onClick={clearFilters}>
            {t("Clear filters")}
          </Button>
        ) : null}
      </div>

      <div className="spread" style={{ marginBottom: 12 }}>
        <span className="small muted">{t("Showing {shown} of {total}", { shown: items.length, total })}</span>
        <span className="tag">
          <SlidersHorizontal /> {t("Filters")}
        </span>
      </div>

      {itemsState.loading ? <Loading /> : null}
      {itemsState.error ? <ErrorState message={itemsState.error} onRetry={itemsState.reload} /> : null}

      {!itemsState.loading && !itemsState.error ? (
        items.length === 0 ? (
          <div className="card">
            <EmptyState icon={<SearchIcon />} title={t("No matching work")}>
              {t("Try a different word or clear the filters.")}
            </EmptyState>
          </div>
        ) : (
          <ItemTable items={items} showProject />
        )
      ) : null}
    </>
  );
}
