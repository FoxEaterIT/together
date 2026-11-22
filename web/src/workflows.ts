import type { Workflow, WorkflowState } from "./types";

// Flows belong to projects, so the screens that filter across projects work on
// the flat state list the API returns instead of assuming one installation
// workflow.
export function allStates(workflows: Workflow[]): WorkflowState[] {
  return workflows.flatMap((workflow) => workflow.states);
}

export function stateOptionLabel(state: WorkflowState, workflows: Workflow[]): string {
  if (workflows.length <= 1) {
    return state.name;
  }
  const workflow = workflows.find((entry) => entry.id === state.workflow_id);
  return workflow ? `${state.name} · ${workflow.name}` : state.name;
}

export function boundWorkflow(
  workflows: Workflow[],
  bindings: Record<string, number>,
  itemType: string,
): Workflow | null {
  const id = bindings[itemType];
  if (!id) {
    return null;
  }
  return workflows.find((workflow) => workflow.id === id) ?? null;
}

export function initialState(
  workflows: Workflow[],
  bindings: Record<string, number>,
  itemType: string,
): WorkflowState | null {
  const workflow = boundWorkflow(workflows, bindings, itemType);
  if (!workflow) {
    return null;
  }
  return workflow.states.find((state) => state.id === workflow.initial_state_id) ?? null;
}
