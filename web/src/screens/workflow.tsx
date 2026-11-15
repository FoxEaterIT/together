import { useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import {
  Check,
  CircleDot,
  GitBranch,
  GripVertical,
  Pencil,
  Plus,
  Trash2,
} from "lucide-react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { useLoader } from "../state";
import type { Workflow, WorkflowCondition, WorkflowState, WorkflowTransition } from "../types";
import {
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  InlineError,
  Input,
  Loading,
  Modal,
  PageHead,
  Select,
  useToast,
} from "../components/ui";

type ConditionDraft = {
  kind: WorkflowCondition["kind"];
  value: string;
};

const NODE_W = 168;
const NODE_H = 62;
const GAP_X = 78;
const GAP_Y = 66;
const PAD = 32;
const COLUMNS = 3;

const conditionKinds: { value: WorkflowCondition["kind"]; label: string }[] = [
  { value: "role", label: "Role" },
  { value: "assignee", label: "Assignee" },
  { value: "priority", label: "Priority" },
  { value: "item_type", label: "Type" },
];

function conditionValues(kind: WorkflowCondition["kind"]): { value: string; label: string }[] {
  switch (kind) {
    case "role":
      return [
        { value: "admin", label: "Admin role" },
        { value: "manager", label: "Manager" },
        { value: "member", label: "Member" },
        { value: "guest", label: "Guest" },
      ];
    case "assignee":
      return [{ value: "yes", label: "Yes" }];
    case "priority":
      return [
        { value: "none", label: "None" },
        { value: "low", label: "Low" },
        { value: "medium", label: "Medium" },
        { value: "high", label: "High" },
        { value: "urgent", label: "Urgent" },
      ];
    case "item_type":
      return [
        { value: "observer", label: "Observer" },
        { value: "action", label: "Action" },
        { value: "micro", label: "Micro" },
        { value: "bug", label: "Bug" },
      ];
    default:
      return [];
  }
}

function nodePosition(index: number) {
  const column = index % COLUMNS;
  const row = Math.floor(index / COLUMNS);
  return { x: PAD + column * (NODE_W + GAP_X), y: PAD + row * (NODE_H + GAP_Y) };
}

function edgePoint(cx: number, cy: number, tx: number, ty: number, width: number, height: number) {
  const dx = tx - cx;
  const dy = ty - cy;
  if (dx === 0 && dy === 0) {
    return { x: cx, y: cy };
  }
  const scale = Math.min(
    dx !== 0 ? width / 2 / Math.abs(dx) : Number.POSITIVE_INFINITY,
    dy !== 0 ? height / 2 / Math.abs(dy) : Number.POSITIVE_INFINITY,
  );
  return { x: cx + dx * scale, y: cy + dy * scale };
}

export function WorkflowScreen() {
  const { t } = useI18n();
  const { notify } = useToast();
  const state = useLoader(() => api<Workflow>("/api/workflow"), []);
  const canvasRef = useRef<HTMLDivElement>(null);

  const [drag, setDrag] = useState<{ from: number; x: number; y: number } | null>(null);

  const [transitionOpen, setTransitionOpen] = useState(false);
  const [editingTransition, setEditingTransition] = useState<WorkflowTransition | null>(null);
  const [fromState, setFromState] = useState("");
  const [toState, setToState] = useState("");
  const [transitionName, setTransitionName] = useState("");
  const [conditions, setConditions] = useState<ConditionDraft[]>([]);
  const [transitionError, setTransitionError] = useState("");
  const [transitionBusy, setTransitionBusy] = useState(false);

  const [stateOpen, setStateOpen] = useState(false);
  const [editingState, setEditingState] = useState<WorkflowState | null>(null);
  const [stateName, setStateName] = useState("");
  const [stateCategory, setStateCategory] = useState<"open" | "done">("open");
  const [statePosition, setStatePosition] = useState("");
  const [stateError, setStateError] = useState("");
  const [stateBusy, setStateBusy] = useState(false);

  const [deleteTransition, setDeleteTransition] = useState<WorkflowTransition | null>(null);
  const [deleteState, setDeleteState] = useState<WorkflowState | null>(null);
  const [busy, setBusy] = useState(false);

  const workflow = state.data;
  const states = workflow?.states ?? [];
  const transitions = workflow?.transitions ?? [];

  const positions = useMemo(() => {
    const map = new Map<number, { x: number; y: number }>();
    states.forEach((entry, index) => {
      map.set(entry.id, nodePosition(index));
    });
    return map;
  }, [states]);

  const graphSize = useMemo(() => {
    const rows = Math.max(1, Math.ceil(states.length / COLUMNS));
    const width = Math.max(720, PAD * 2 + COLUMNS * NODE_W + (COLUMNS - 1) * GAP_X);
    const height = Math.max(360, PAD * 2 + rows * NODE_H + Math.max(0, rows - 1) * GAP_Y);
    return { width, height };
  }, [states.length]);

  function openCreateTransition(fromID?: number, toID?: number) {
    setEditingTransition(null);
    setFromState(fromID ? String(fromID) : states[0] ? String(states[0].id) : "");
    setToState(toID ? String(toID) : states[1] ? String(states[1].id) : "");
    setTransitionName("");
    setConditions([]);
    setTransitionError("");
    setTransitionOpen(true);
  }

  function openEditTransition(transition: WorkflowTransition) {
    setEditingTransition(transition);
    setFromState(String(transition.from_state_id));
    setToState(String(transition.to_state_id));
    setTransitionName(transition.name);
    setConditions(transition.conditions.map((condition) => ({ kind: condition.kind, value: condition.value })));
    setTransitionError("");
    setTransitionOpen(true);
  }

  function openCreateState() {
    setEditingState(null);
    setStateName("");
    setStateCategory("open");
    setStatePosition(String((states[states.length - 1]?.position ?? 0) + 1));
    setStateError("");
    setStateOpen(true);
  }

  function openEditState(entry: WorkflowState) {
    setEditingState(entry);
    setStateName(entry.name);
    setStateCategory(entry.category);
    setStatePosition(String(entry.position));
    setStateError("");
    setStateOpen(true);
  }

  async function saveTransition() {
    setTransitionError("");
    if (!fromState || !toState) {
      setTransitionError(t("Choose a state."));
      return;
    }
    if (fromState === toState) {
      setTransitionError(t("A transition needs two different states."));
      return;
    }
    setTransitionBusy(true);
    try {
      const body = {
        from_state_id: Number(fromState),
        to_state_id: Number(toState),
        name: transitionName.trim(),
        conditions,
      };
      if (editingTransition) {
        await api(`/api/workflow/transitions/${editingTransition.id}`, { method: "PUT", body });
      } else {
        await api("/api/workflow/transitions", { method: "POST", body });
      }
      notify(t("Transition saved."), "good");
      setTransitionOpen(false);
      state.reload();
    } catch (err) {
      setTransitionError(errorMessage(err));
    } finally {
      setTransitionBusy(false);
    }
  }

  async function saveState() {
    setStateError("");
    if (stateName.trim() === "") {
      setStateError(t("State name is required."));
      return;
    }
    setStateBusy(true);
    try {
      const body: Record<string, unknown> = {
        name: stateName.trim(),
        category: stateCategory,
      };
      if (!editingState) {
        body.position = statePosition === "" ? undefined : Number(statePosition);
      } else if (statePosition !== "") {
        body.position = Number(statePosition);
      }
      if (editingState) {
        await api(`/api/workflow/states/${editingState.id}`, { method: "PUT", body });
      } else {
        await api("/api/workflow/states", { method: "POST", body });
      }
      notify(t("State saved."), "good");
      setStateOpen(false);
      state.reload();
    } catch (err) {
      setStateError(errorMessage(err));
    } finally {
      setStateBusy(false);
    }
  }

  async function setInitial(entry: WorkflowState) {
    setBusy(true);
    try {
      await api("/api/workflow/initial-state", { method: "PUT", body: { state_id: entry.id } });
      notify(t("Initial state saved."), "good");
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBusy(false);
    }
  }

  async function removeTransition() {
    if (!deleteTransition) {
      return;
    }
    setBusy(true);
    try {
      await api(`/api/workflow/transitions/${deleteTransition.id}`, { method: "DELETE" });
      notify(t("Transition deleted."), "good");
      setDeleteTransition(null);
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBusy(false);
    }
  }

  async function removeState() {
    if (!deleteState) {
      return;
    }
    setBusy(true);
    try {
      await api(`/api/workflow/states/${deleteState.id}`, { method: "DELETE" });
      notify(t("State deleted."), "good");
      setDeleteState(null);
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBusy(false);
    }
  }

  function beginDrag(event: ReactPointerEvent<HTMLDivElement>, stateID: number) {
    if (event.button !== 0) {
      return;
    }
    const canvas = canvasRef.current;
    if (!canvas) {
      return;
    }
    event.preventDefault();
    const rect = canvas.getBoundingClientRect();
    const scroller: HTMLDivElement = canvas;
    const pointer = () => ({
      x: event.clientX - rect.left + scroller.scrollLeft,
      y: event.clientY - rect.top + scroller.scrollTop,
    });
    setDrag({ from: stateID, ...pointer() });

    function onMove(moveEvent: PointerEvent) {
      setDrag((current) =>
        current
          ? {
              ...current,
              x: moveEvent.clientX - rect.left + scroller.scrollLeft,
              y: moveEvent.clientY - rect.top + scroller.scrollTop,
            }
          : current,
      );
    }

    function onUp(upEvent: PointerEvent) {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      const target = document.elementFromPoint(upEvent.clientX, upEvent.clientY);
      const node = target instanceof Element ? target.closest("[data-state-id]") : null;
      const targetID = node instanceof HTMLElement ? Number(node.dataset.stateId) : 0;
      setDrag(null);
      if (targetID && targetID !== stateID) {
        openCreateTransition(stateID, targetID);
      }
    }

    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  }

  function updateCondition(index: number, next: Partial<ConditionDraft>) {
    setConditions((current) =>
      current.map((condition, position) => {
        if (position !== index) {
          return condition;
        }
        if (next.kind && next.kind !== condition.kind) {
          const options = conditionValues(next.kind);
          return { kind: next.kind, value: options[0]?.value ?? "" };
        }
        return { ...condition, ...next };
      }),
    );
  }

  return (
    <>
      <PageHead
        title={t("Workflow")}
        lede={t("One workflow for the whole installation.")}
        actions={
          <>
            <Button variant="secondary" icon={<CircleDot />} onClick={openCreateState}>
              {t("Add state")}
            </Button>
            <Button variant="primary" icon={<Plus />} onClick={() => openCreateTransition()} disabled={states.length < 2}>
              {t("Add transition")}
            </Button>
          </>
        }
      />

      {state.loading ? <Loading /> : null}
      {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}

      {workflow ? (
        <div className="workflow-layout">
          <section className="card">
            <div className="card-head">
              <div>
                <h2 className="card-title">{t("States")}</h2>
                <p className="card-sub">{t("Order of the workflow")}</p>
              </div>
              <Badge tone="mono">{states.length}</Badge>
            </div>
            <div className="card-body state-list">
              {states.length === 0 ? (
                <EmptyState icon={<CircleDot />} title={t("No states yet.")} />
              ) : (
                states.map((entry) => (
                  <div
                    key={entry.id}
                    className={`state-card ${entry.initial ? "state-card--initial" : ""}`}
                  >
                    <div className="state-card-head">
                      <GripVertical aria-hidden="true" style={{ width: 16, height: 16, color: "var(--color-muted)" }} />
                      <span className="state-name grow">{entry.name}</span>
                      {entry.initial ? <Badge tone="accent">{t("Initial")}</Badge> : null}
                      <Badge tone={entry.category === "done" ? "good" : "mono"}>
                        {entry.category === "done" ? t("Done") : t("Open")}
                      </Badge>
                    </div>
                    <div className="state-card-meta">
                      <span>
                        {entry.item_count} {t("items")}
                      </span>
                      <span>
                        {entry.active_item_count} {t("active")}
                      </span>
                    </div>
                    <div className="state-card-actions">
                      {!entry.initial ? (
                        <Button size="sm" variant="ghost" icon={<Check />} disabled={busy} onClick={() => setInitial(entry)}>
                          {t("Set as initial")}
                        </Button>
                      ) : null}
                      <Button size="sm" variant="ghost" icon={<Pencil />} onClick={() => openEditState(entry)}>
                        {t("Edit")}
                      </Button>
                      <IconButton label={t("Delete state")} onClick={() => setDeleteState(entry)}>
                        <Trash2 />
                      </IconButton>
                    </div>
                  </div>
                ))
              )}
            </div>
          </section>

          <div>
            <div className="graph-shell">
              <div className="graph-head">
                <div>
                  <h2 className="card-title">{t("Graph")}</h2>
                  <p className="card-sub">{t("Drag from a state to another to create a transition.")}</p>
                </div>
                <Badge tone="mono">
                  {transitions.length} {t("Transitions")}
                </Badge>
              </div>
              <div className="graph-canvas" ref={canvasRef} style={{ minWidth: graphSize.width }}>
                <div style={{ position: "relative", width: graphSize.width, height: graphSize.height }}>
                  <svg className="graph-svg" viewBox={`0 0 ${graphSize.width} ${graphSize.height}`} aria-hidden="true">
                    <defs>
                      <marker id="wf-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                        <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--color-accent)" />
                      </marker>
                    </defs>
                    {transitions.map((transition) => {
                      const from = positions.get(transition.from_state_id);
                      const to = positions.get(transition.to_state_id);
                      if (!from || !to) {
                        return null;
                      }
                      const startCenter = { x: from.x + NODE_W / 2, y: from.y + NODE_H / 2 };
                      const endCenter = { x: to.x + NODE_W / 2, y: to.y + NODE_H / 2 };
                      const start = edgePoint(startCenter.x, startCenter.y, endCenter.x, endCenter.y, NODE_W, NODE_H);
                      const end = edgePoint(endCenter.x, endCenter.y, startCenter.x, startCenter.y, NODE_W, NODE_H);
                      const reverse = transitions.some(
                        (other) =>
                          other.id !== transition.id &&
                          other.from_state_id === transition.to_state_id &&
                          other.to_state_id === transition.from_state_id,
                      );
                      const midX = (start.x + end.x) / 2;
                      const midY = (start.y + end.y) / 2;
                      const dx = end.x - start.x;
                      const dy = end.y - start.y;
                      const length = Math.hypot(dx, dy) || 1;
                      const bend = reverse ? 26 : 10;
                      const controlX = midX - (dy / length) * bend;
                      const controlY = midY + (dx / length) * bend;
                      const d = `M ${start.x} ${start.y} Q ${controlX} ${controlY} ${end.x} ${end.y}`;
                      return (
                        <path
                          key={transition.id}
                          d={d}
                          fill="none"
                          stroke="var(--color-accent)"
                          strokeWidth={1.6}
                          strokeLinecap="round"
                          markerEnd="url(#wf-arrow)"
                          opacity={0.85}
                        />
                      );
                    })}
                    {drag ? (
                      <line
                        x1={(positions.get(drag.from)?.x ?? 0) + NODE_W / 2}
                        y1={(positions.get(drag.from)?.y ?? 0) + NODE_H / 2}
                        x2={drag.x}
                        y2={drag.y}
                        stroke="var(--color-accent)"
                        strokeWidth={1.6}
                        strokeDasharray="5 5"
                      />
                    ) : null}
                  </svg>

                  {states.map((entry) => {
                    const position = positions.get(entry.id) ?? { x: 0, y: 0 };
                    return (
                      <div
                        key={entry.id}
                        data-state-id={entry.id}
                        className={[
                          "graph-node",
                          entry.initial ? "graph-node--initial" : "",
                          entry.category === "done" ? "graph-node--done" : "",
                        ]
                          .filter(Boolean)
                          .join(" ")}
                        style={{ left: position.x, top: position.y }}
                        onPointerDown={(event) => beginDrag(event, entry.id)}
                        title={t("Drag to connect")}
                      >
                        <span className="graph-node-name">{entry.name}</span>
                        <span className="graph-node-meta">
                          {entry.initial ? `${t("Initial")} · ` : ""}
                          {entry.item_count} {t("items")}
                        </span>
                      </div>
                    );
                  })}

                  {states.length === 0 ? (
                    <div className="empty" style={{ position: "absolute", inset: 0 }}>
                      <EmptyState icon={<CircleDot />} title={t("No states yet.")} />
                    </div>
                  ) : null}
                </div>
              </div>
            </div>

            <section className="card" style={{ marginTop: "var(--space-4)" }}>
              <div className="card-head">
                <div>
                  <h2 className="card-title">{t("Transitions")}</h2>
                  <p className="card-sub">{t("Who can move an item between states.")}</p>
                </div>
                <Button size="sm" variant="secondary" icon={<Plus />} onClick={() => openCreateTransition()} disabled={states.length < 2}>
                  {t("Add transition")}
                </Button>
              </div>
              <div className="card-body" style={{ padding: 0 }}>
                {transitions.length === 0 ? (
                  <div className="empty" style={{ padding: 32 }}>
                    <EmptyState icon={<GitBranch />} title={t("No transitions yet.")}>
                      {t("Drag from a state to another to create a transition.")}
                    </EmptyState>
                  </div>
                ) : (
                  <div className="transition-list">
                    {transitions.map((transition) => (
                      <div key={transition.id} className="transition-row">
                        <div className="transition-route grow">
                          <Badge tone="mono">{transition.from_state_name}</Badge>
                          <span aria-hidden="true">→</span>
                          <Badge tone="mono">{transition.to_state_name}</Badge>
                          <span className="small">{transition.name}</span>
                          {transition.conditions.length > 0 ? (
                            <Badge tone="accent">
                              {transition.conditions.length} {t("Conditions")}
                            </Badge>
                          ) : (
                            <span className="tiny muted">{t("No conditions.")}</span>
                          )}
                        </div>
                        <div className="list-actions">
                          <Button size="sm" variant="ghost" icon={<Pencil />} onClick={() => openEditTransition(transition)}>
                            {t("Edit")}
                          </Button>
                          <IconButton label={t("Delete transition")} onClick={() => setDeleteTransition(transition)}>
                            <Trash2 />
                          </IconButton>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </section>
          </div>
        </div>
      ) : null}

      <Modal
        open={transitionOpen}
        title={editingTransition ? t("Edit transition") : t("Add transition")}
        onClose={() => setTransitionOpen(false)}
        wide
        footer={
          <>
            <Button variant="ghost" onClick={() => setTransitionOpen(false)} disabled={transitionBusy}>
              {t("Cancel")}
            </Button>
            <Button variant="primary" onClick={saveTransition} disabled={transitionBusy} icon={<Check />}>
              {t("Save")}
            </Button>
          </>
        }
      >
        <div className="form">
          {transitionError ? <InlineError message={transitionError} /> : null}
          <div className="form-grid">
            <Field label={t("From state")} htmlFor="transition-from">
              <Select id="transition-from" value={fromState} onChange={(event) => setFromState(event.target.value)}>
                <option value="">{t("Choose a state")}</option>
                {states.map((entry) => (
                  <option key={entry.id} value={entry.id}>
                    {entry.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label={t("To state")} htmlFor="transition-to">
              <Select id="transition-to" value={toState} onChange={(event) => setToState(event.target.value)}>
                <option value="">{t("Choose a state")}</option>
                {states.map((entry) => (
                  <option key={entry.id} value={entry.id}>
                    {entry.name}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <Field label={t("Transition name")} htmlFor="transition-name" hint={t("Leave blank to use the state names.")}>
            <Input
              id="transition-name"
              value={transitionName}
              maxLength={120}
              onChange={(event) => setTransitionName(event.target.value)}
            />
          </Field>

          <div className="spread">
            <h3 className="card-title">{t("Conditions")}</h3>
            <Button
              size="sm"
              variant="secondary"
              icon={<Plus />}
              onClick={() => setConditions((current) => [...current, { kind: "role", value: "member" }])}
            >
              {t("Add condition")}
            </Button>
          </div>

          {conditions.length === 0 ? (
            <p className="small muted">{t("No conditions. Everyone who can write may use this transition.")}</p>
          ) : (
            <div className="stack-sm">
              {conditions.map((condition, index) => (
                <div key={index} className="condition-row">
                  <Field label={t("Condition type")} className="grow">
                    <Select
                      value={condition.kind}
                      onChange={(event) =>
                        updateCondition(index, { kind: event.target.value as WorkflowCondition["kind"] })
                      }
                    >
                      {conditionKinds.map((kind) => (
                        <option key={kind.value} value={kind.value}>
                          {t(kind.label)}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <Field label={t("Condition value")} className="grow">
                    <Select
                      value={condition.value}
                      onChange={(event) => updateCondition(index, { value: event.target.value })}
                    >
                      {conditionValues(condition.kind).map((option) => (
                        <option key={option.value} value={option.value}>
                          {t(option.label)}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <IconButton
                    label={t("Remove")}
                    onClick={() => setConditions((current) => current.filter((_, position) => position !== index))}
                  >
                    <Trash2 />
                  </IconButton>
                </div>
              ))}
            </div>
          )}

          <p className="tiny muted">{t("Same condition type is combined with OR, different types with AND.")}</p>
        </div>
      </Modal>

      <Modal
        open={stateOpen}
        title={editingState ? t("Edit state") : t("Add state")}
        onClose={() => setStateOpen(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setStateOpen(false)} disabled={stateBusy}>
              {t("Cancel")}
            </Button>
            <Button variant="primary" onClick={saveState} disabled={stateBusy} icon={<Check />}>
              {t("Save")}
            </Button>
          </>
        }
      >
        <div className="form">
          {stateError ? <InlineError message={stateError} /> : null}
          <Field label={t("State name")} required htmlFor="state-name">
            <Input
              id="state-name"
              value={stateName}
              maxLength={80}
              autoFocus
              onChange={(event) => setStateName(event.target.value)}
            />
          </Field>
          <div className="form-grid">
            <Field label={t("Category")} htmlFor="state-category">
              <Select
                id="state-category"
                value={stateCategory}
                onChange={(event) => setStateCategory(event.target.value as "open" | "done")}
              >
                <option value="open">{t("Open category")}</option>
                <option value="done">{t("Done category")}</option>
              </Select>
            </Field>
            <Field label={t("Position")} htmlFor="state-position" hint={t("Lower numbers come first.")}>
              <Input
                id="state-position"
                type="number"
                min={1}
                value={statePosition}
                onChange={(event) => setStatePosition(event.target.value)}
              />
            </Field>
          </div>
        </div>
      </Modal>

      <ConfirmDialog
        open={deleteTransition !== null}
        title={t("Delete transition")}
        body={t("Delete this transition? Items already in a state are not changed.")}
        confirmLabel={t("Delete")}
        busy={busy}
        onConfirm={removeTransition}
        onClose={() => setDeleteTransition(null)}
      />

      <ConfirmDialog
        open={deleteState !== null}
        title={t("Delete state")}
        body={t("Delete this state? It can only be removed when no item uses it.")}
        confirmLabel={t("Delete")}
        busy={busy}
        onConfirm={removeState}
        onClose={() => setDeleteState(null)}
      />
    </>
  );
}
