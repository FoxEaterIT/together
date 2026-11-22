import { useMemo, useState, type FormEvent } from "react";
import {
  ArrowDown,
  ArrowUp,
  BellRing,
  Check,
  Pencil,
  Plus,
  Trash2,
  Zap,
} from "lucide-react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { useApp, useLoader } from "../state";
import type { AutomationAction, AutomationRule, Person, Project, WorkflowStateRef } from "../types";
import {
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  InlineError,
  Input,
  Loading,
  Modal,
  PageHead,
  Select,
  Switch,
  Textarea,
  useToast,
} from "../components/ui";

type ActionDraft = {
  kind: AutomationAction["kind"];
  stateID: string;
  userID: string;
  body: string;
  mentionIDs: number[];
};

const triggerKinds: { value: AutomationRule["trigger_kind"]; label: string }[] = [
  { value: "state_changed", label: "State changed" },
  { value: "assigned", label: "Item assigned" },
  { value: "comment_added", label: "Comment added" },
  { value: "due_passed", label: "Due date passed" },
];

const actionKinds: { value: AutomationAction["kind"]; label: string }[] = [
  { value: "set_state", label: "Set state" },
  { value: "assign_user", label: "Assign person" },
  { value: "add_comment", label: "Add comment" },
  { value: "notify", label: "Notify assignees" },
];

function emptyAction(kind: AutomationAction["kind"] = "set_state"): ActionDraft {
  return { kind, stateID: "", userID: "", body: "", mentionIDs: [] };
}

function triggerLabel(rule: AutomationRule, t: (text: string) => string) {
  switch (rule.trigger_kind) {
    case "state_changed":
      return `${t("State changed")} → ${rule.trigger_state_name ?? `#${rule.trigger_state_id}`}`;
    case "assigned":
      return t("Item assigned");
    case "comment_added":
      return t("Comment added");
    case "due_passed":
      return t("Due date passed");
    default:
      return rule.trigger_kind;
  }
}

export function AutomationsScreen() {
  const { t } = useI18n();
  const { user, projects } = useApp();
  const { notify } = useToast();
  const state = useLoader(() => api<{ automations: AutomationRule[] }>("/api/automations"), []);
  const workflowState = useLoader(() => api<{ states: WorkflowStateRef[] }>("/api/workflow/states"), []);
  const peopleState = useLoader(() => api<{ people: Person[] }>("/api/people"), []);

  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<AutomationRule | null>(null);
  const [name, setName] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [triggerKind, setTriggerKind] = useState<AutomationRule["trigger_kind"]>("state_changed");
  const [triggerStateID, setTriggerStateID] = useState("");
  const [projectIDs, setProjectIDs] = useState<number[]>([]);
  const [actions, setActions] = useState<ActionDraft[]>([emptyAction()]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [deleteRule, setDeleteRule] = useState<AutomationRule | null>(null);

  const rules = state.data?.automations ?? [];
  const states = workflowState.data?.states ?? [];
  const people = peopleState.data?.people ?? [];

  const projectOptions = useMemo(() => {
    const map = new Map<number, Project>();
    for (const project of projects) {
      map.set(project.id, project);
    }
    for (const rule of rules) {
      for (const project of rule.projects) {
        if (!map.has(project.id)) {
          map.set(project.id, {
            id: project.id,
            name: project.name,
            description: "",
            visibility: "open",
            created_by: 0,
            created_by_name: "",
            created_at: "",
            member_count: 0,
            team_count: 0,
          });
        }
      }
    }
    return Array.from(map.values()).sort((a, b) => a.name.localeCompare(b.name));
  }, [projects, rules]);

  function openCreate() {
    setEditing(null);
    setName("");
    setEnabled(true);
    setTriggerKind("state_changed");
    setTriggerStateID("");
    setProjectIDs(user.role === "manager" ? [] : []);
    setActions([emptyAction()]);
    setError("");
    setOpen(true);
  }

  function openEdit(rule: AutomationRule) {
    setEditing(rule);
    setName(rule.name);
    setEnabled(rule.enabled);
    setTriggerKind(rule.trigger_kind);
    setTriggerStateID(rule.trigger_state_id ? String(rule.trigger_state_id) : "");
    setProjectIDs(rule.project_ids);
    setActions(
      rule.actions.length > 0
        ? rule.actions.map((action) => ({
            kind: action.kind,
            stateID: action.state_id ? String(action.state_id) : "",
            userID: action.user_id ? String(action.user_id) : "",
            body: action.body ?? "",
            mentionIDs: action.mention_ids ?? [],
          }))
        : [emptyAction()],
    );
    setError("");
    setOpen(true);
  }

  function updateAction(index: number, next: Partial<ActionDraft>) {
    setActions((current) =>
      current.map((action, position) => {
        if (position !== index) {
          return action;
        }
        if (next.kind && next.kind !== action.kind) {
          return emptyAction(next.kind);
        }
        return { ...action, ...next };
      }),
    );
  }

  function moveAction(index: number, direction: -1 | 1) {
    setActions((current) => {
      const target = index + direction;
      if (target < 0 || target >= current.length) {
        return current;
      }
      const copy = [...current];
      const [entry] = copy.splice(index, 1);
      copy.splice(target, 0, entry);
      return copy;
    });
  }

  function validate(): string {
    if (name.trim() === "") {
      return t("Rule name is required.");
    }
    if (triggerKind === "state_changed" && triggerStateID === "") {
      return t("Choose the arrival state for a state changed trigger.");
    }
    if (user.role === "manager" && projectIDs.length === 0) {
      return t("Choose at least one project.");
    }
    if (actions.length === 0) {
      return t("Add at least one action.");
    }
    for (const action of actions) {
      if (action.kind === "set_state" && action.stateID === "") {
        return t("Choose a state for the set state action.");
      }
      if (action.kind === "assign_user" && action.userID === "") {
        return t("Choose a person for the assign action.");
      }
      if (action.kind === "add_comment" && action.body.trim() === "") {
        return t("The comment cannot be empty.");
      }
    }
    return "";
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    const message = validate();
    if (message) {
      setError(message);
      return;
    }
    setError("");
    setBusy(true);
    try {
      const body = {
        name: name.trim(),
        enabled,
        trigger_kind: triggerKind,
        trigger_state_id: triggerKind === "state_changed" ? Number(triggerStateID) : 0,
        project_ids: projectIDs,
        actions: actions.map((action) => ({
          kind: action.kind,
          state_id: action.stateID ? Number(action.stateID) : 0,
          user_id: action.userID ? Number(action.userID) : 0,
          body: action.body,
          mention_ids: action.mentionIDs,
        })),
      };
      if (editing) {
        await api(`/api/automations/${editing.id}`, { method: "PUT", body });
      } else {
        await api("/api/automations", { method: "POST", body });
      }
      notify(t("Rule saved."), "good");
      setOpen(false);
      state.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!deleteRule) {
      return;
    }
    setBusy(true);
    try {
      await api(`/api/automations/${deleteRule.id}`, { method: "DELETE" });
      notify(t("Rule deleted."), "good");
      setDeleteRule(null);
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHead
        title={t("Automations")}
        lede={t("Rules that react to work without scripts.")}
        actions={
          <Button variant="primary" icon={<Plus />} onClick={openCreate}>
            {t("New rule")}
          </Button>
        }
      />

      {state.loading ? <Loading /> : null}
      {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}

      {!state.loading && !state.error ? (
        rules.length === 0 ? (
          <div className="card">
            <EmptyState icon={<Zap />} title={t("No automation rules yet")}>
              {t("Create a rule to set a state, assign a person, comment or notify assignees.")}
            </EmptyState>
          </div>
        ) : (
          <div className="project-grid">
            {rules.map((rule) => (
              <article key={rule.id} className="rule-card">
                <div className="rule-card-head">
                  <div className="grow">
                    <h2 className="rule-name">{rule.name}</h2>
                    <div className="rule-meta">
                      <Badge tone={rule.enabled ? "good" : "neutral"}>
                        {rule.enabled ? t("Enabled") : t("Disabled")}
                      </Badge>
                      <span>{triggerLabel(rule, t)}</span>
                    </div>
                  </div>
                  <Zap aria-hidden="true" style={{ width: 16, height: 16, color: "var(--color-muted)" }} />
                </div>

                <div className="rule-meta">
                  {rule.projects.length === 0 ? (
                    <Badge tone="violet">{t("Global rule")}</Badge>
                  ) : (
                    rule.projects.map((project) => (
                      <Badge key={project.id} tone="mono">
                        {project.name}
                      </Badge>
                    ))
                  )}
                </div>

                <div className="rule-actions">
                  {rule.actions.map((action, index) => (
                    <div key={action.id ?? index} className="cluster" style={{ gap: 6 }}>
                      <Badge tone="neutral">{index + 1}</Badge>
                      <span className="small">{describeAction(action, states, people, t)}</span>
                    </div>
                  ))}
                </div>

                <div className="cluster">
                  <Button size="sm" variant="secondary" icon={<Pencil />} onClick={() => openEdit(rule)}>
                    {t("Edit")}
                  </Button>
                  <Button size="sm" variant="danger-ghost" icon={<Trash2 />} onClick={() => setDeleteRule(rule)}>
                    {t("Delete rule")}
                  </Button>
                </div>
              </article>
            ))}
          </div>
        )
      ) : null}

      <Modal
        open={open}
        title={editing ? t("Edit rule") : t("New rule")}
        onClose={() => setOpen(false)}
        wide
        footer={
          <>
            <Button variant="ghost" onClick={() => setOpen(false)} disabled={busy}>
              {t("Cancel")}
            </Button>
            <Button variant="primary" onClick={save} disabled={busy} icon={<Check />}>
              {t("Save")}
            </Button>
          </>
        }
      >
        <form className="form" onSubmit={save}>
          {error ? <InlineError message={error} /> : null}

          <div className="form-grid">
            <Field label={t("Rule name")} required htmlFor="rule-name">
              <Input
                id="rule-name"
                value={name}
                maxLength={120}
                autoFocus
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            <div className="field">
              <span className="field-label">{t("Rule enabled")}</span>
              <Switch checked={enabled} onChange={setEnabled} label={enabled ? t("Enabled") : t("Disabled")} />
            </div>
          </div>

          <div className="form-grid">
            <Field label={t("Trigger")} htmlFor="rule-trigger">
              <Select
                id="rule-trigger"
                value={triggerKind}
                onChange={(event) => setTriggerKind(event.target.value as AutomationRule["trigger_kind"])}
              >
                {triggerKinds.map((kind) => (
                  <option key={kind.value} value={kind.value}>
                    {t(kind.label)}
                  </option>
                ))}
              </Select>
            </Field>
            {triggerKind === "state_changed" ? (
              <Field label={t("Arrival state")} htmlFor="rule-trigger-state">
                <Select
                  id="rule-trigger-state"
                  value={triggerStateID}
                  onChange={(event) => setTriggerStateID(event.target.value)}
                >
                  <option value="">{t("No state selected")}</option>
                  {states.map((entry) => (
                    <option key={entry.id} value={entry.id}>
                      {entry.name} · {entry.project_name}
                    </option>
                  ))}
                </Select>
              </Field>
            ) : null}
          </div>

          <div className="divider" />

          <div className="spread">
            <div>
              <h3 className="card-title">{t("Applies to")}</h3>
              <p className="card-sub">
                {user.role === "manager"
                  ? t("Choose at least one project.")
                  : t("Leave every project unchecked for a global rule.")}
              </p>
            </div>
            <Badge tone={projectIDs.length === 0 ? "violet" : "mono"}>
              {projectIDs.length === 0 ? t("Global rule") : t("{count} projects", { count: projectIDs.length })}
            </Badge>
          </div>

          <div className="project-check-list">
            {projectOptions.map((project) => (
              <Checkbox
                key={project.id}
                label={project.name}
                checked={projectIDs.includes(project.id)}
                onChange={(event) =>
                  setProjectIDs((current) =>
                    event.target.checked
                      ? [...current, project.id]
                      : current.filter((id) => id !== project.id),
                  )
                }
              />
            ))}
          </div>

          <div className="divider" />

          <div className="spread">
            <div>
              <h3 className="card-title">{t("Actions")}</h3>
              <p className="card-sub">{t("Each action runs in order inside one transaction.")}</p>
            </div>
            <Button
              size="sm"
              variant="secondary"
              icon={<Plus />}
              onClick={() => setActions((current) => [...current, emptyAction()])}
            >
              {t("Add action")}
            </Button>
          </div>

          <div className="stack-sm">
            {actions.map((action, index) => (
              <div key={index} className="action-card">
                <div className="action-card-head">
                  <Badge tone="neutral">{index + 1}</Badge>
                  <Select
                    value={action.kind}
                    aria-label={t("Action")}
                    onChange={(event) => updateAction(index, { kind: event.target.value as AutomationAction["kind"] })}
                  >
                    {actionKinds.map((kind) => (
                      <option key={kind.value} value={kind.value}>
                        {t(kind.label)}
                      </option>
                    ))}
                  </Select>
                  <span className="grow" />
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t("Move up")}
                    disabled={index === 0}
                    onClick={() => moveAction(index, -1)}
                  >
                    <ArrowUp />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t("Move down")}
                    disabled={index === actions.length - 1}
                    onClick={() => moveAction(index, 1)}
                  >
                    <ArrowDown />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t("Remove")}
                    onClick={() => setActions((current) => current.filter((_, position) => position !== index))}
                  >
                    <Trash2 />
                  </Button>
                </div>

                <div className="action-card-body">
                  {action.kind === "set_state" ? (
                    <Field label={t("Set state")}>
                      <Select
                        value={action.stateID}
                        onChange={(event) => updateAction(index, { stateID: event.target.value })}
                      >
                        <option value="">{t("Choose a state")}</option>
                        {states.map((entry) => (
                          <option key={entry.id} value={entry.id}>
                            {entry.name}
                          </option>
                        ))}
                      </Select>
                    </Field>
                  ) : null}

                  {action.kind === "assign_user" ? (
                    <Field label={t("Assign person")}>
                      <Select
                        value={action.userID}
                        onChange={(event) => updateAction(index, { userID: event.target.value })}
                      >
                        <option value="">{t("Choose a person")}</option>
                        {people
                          .filter((person) => !person.disabled)
                          .map((person) => (
                            <option key={person.id} value={person.id}>
                              {person.name}
                            </option>
                          ))}
                      </Select>
                    </Field>
                  ) : null}

                  {action.kind === "add_comment" ? (
                    <>
                      <Field label={t("Comment body")}>
                        <Textarea
                          value={action.body}
                          rows={3}
                          onChange={(event) => updateAction(index, { body: event.target.value })}
                        />
                      </Field>
                      <Field label={t("Mention people")}>
                        <div className="assignee-picker">
                          {people
                            .filter((person) => !person.disabled)
                            .map((person) => (
                              <Checkbox
                                key={person.id}
                                label={person.name}
                                checked={action.mentionIDs.includes(person.id)}
                                onChange={(event) =>
                                  updateAction(index, {
                                    mentionIDs: event.target.checked
                                      ? [...action.mentionIDs, person.id]
                                      : action.mentionIDs.filter((id) => id !== person.id),
                                  })
                                }
                              />
                            ))}
                        </div>
                      </Field>
                    </>
                  ) : null}

                  {action.kind === "notify" ? (
                    <div className="banner banner--info">
                      <BellRing />
                      <span>
                        {t(
                          "The notify action creates an in-app notification for the current assignees and sends email only when the recipient enabled it and SMTP is configured. It never reaches anyone else.",
                        )}
                      </span>
                    </div>
                  ) : null}
                </div>
              </div>
            ))}
          </div>

          <p className="tiny muted">{t("Automation actions never trigger another rule in the same round.")}</p>
        </form>
      </Modal>

      <ConfirmDialog
        open={deleteRule !== null}
        title={t("Delete rule")}
        body={t("Delete this rule?")}
        confirmLabel={t("Delete")}
        busy={busy}
        onConfirm={remove}
        onClose={() => setDeleteRule(null)}
      />
    </>
  );
}

function describeAction(
  action: AutomationAction,
  states: { id: number; name: string }[],
  people: Person[],
  t: (text: string) => string,
) {
  switch (action.kind) {
    case "set_state":
      return `${t("Set state")}: ${states.find((entry) => entry.id === action.state_id)?.name ?? `#${action.state_id}`}`;
    case "assign_user":
      return `${t("Assign person")}: ${people.find((person) => person.id === action.user_id)?.name ?? `#${action.user_id}`}`;
    case "add_comment":
      return `${t("Add comment")}: ${action.body ?? ""}`;
    case "notify":
      return t("Notify assignees only");
    default:
      return action.kind;
  }
}
