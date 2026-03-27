import type { Plan } from "@howlerops/iron-rain";
import { useKeyboard } from "@opentui/solid";
import { createEffect, createSignal } from "solid-js";
import { createStore } from "solid-js/store";
import { ironRainTheme } from "../../theme/theme.js";
import { ConfirmStep } from "./confirm-step.js";
import { OptionsStep } from "./options-step.js";
import { OverviewStep } from "./overview-step.js";
import { TasksStep } from "./tasks-step.js";
import type { MakeWizardOptions, MakeWizardStep } from "./types.js";

export interface MakeWizardProps {
  plan: Plan;
  onApprove: (plan: Plan, options: MakeWizardOptions) => void;
  onCancel: () => void;
  onRegeneratePlan: (feedback: string) => void;
}

const STEPS: MakeWizardStep[] = ["overview", "tasks", "options", "confirm"];

const STEP_LABELS: Record<MakeWizardStep, string> = {
  overview: "Overview",
  tasks: "Tasks",
  options: "Options",
  confirm: "Confirm",
};

interface WizardState {
  step: MakeWizardStep;
  removedIndices: Set<number>;
  options: MakeWizardOptions;
}

export function MakeWizard(props: MakeWizardProps) {
  const [state, setState] = createStore<WizardState>({
    step: "overview",
    removedIndices: new Set(),
    options: {
      autoCommit: true,
      maxIterations: 10,
      notes: "",
      useLoop: false,
    },
  });

  // Per-step cursors
  const [taskCursor, setTaskCursor] = createSignal(0);
  const [optionCursor, setOptionCursor] = createSignal(0);

  // Overview editing
  const [editing, setEditing] = createSignal(false);
  const [editValue, setEditValue] = createSignal("");

  // Notes editing
  const [editingNotes, setEditingNotes] = createSignal(false);
  const [notesEditValue, setNotesEditValue] = createSignal("");

  // Reset when plan changes (after regeneration)
  createEffect(() => {
    const _id = props.plan.id;
    setState("step", "overview");
    setState("removedIndices", new Set());
    setTaskCursor(0);
    setOptionCursor(0);
    setEditing(false);
    setEditValue("");
  });

  function nextStep() {
    const idx = STEPS.indexOf(state.step);
    if (idx < STEPS.length - 1) {
      setState("step", STEPS[idx + 1]!);
    }
  }

  function prevStep() {
    const idx = STEPS.indexOf(state.step);
    if (idx > 0) {
      setState("step", STEPS[idx - 1]!);
    } else {
      props.onCancel();
    }
  }

  const remainingCount = () =>
    props.plan.tasks.length - state.removedIndices.size;
  const removedCount = () => state.removedIndices.size;

  useKeyboard((e) => {
    const step = state.step;
    const keyName = e.name;
    const char = e.raw;

    switch (step) {
      case "overview": {
        if (editing()) {
          if (keyName === "return") {
            const feedback = editValue().trim();
            if (feedback) {
              setEditing(false);
              props.onRegeneratePlan(feedback);
            }
          } else if (keyName === "escape") {
            setEditing(false);
            setEditValue("");
          } else if (keyName === "backspace") {
            setEditValue((v) => v.slice(0, -1));
          } else if (char && char.length === 1) {
            setEditValue((v) => v + char);
          }
          return;
        }

        if (keyName === "return") {
          nextStep();
        } else if (char === "e") {
          setEditing(true);
          setEditValue("");
        } else if (keyName === "escape" || keyName === "backspace") {
          props.onCancel();
        }
        break;
      }

      case "tasks": {
        const total = props.plan.tasks.length;
        if (keyName === "up") {
          setTaskCursor((c) => Math.max(0, c - 1));
        } else if (keyName === "down") {
          setTaskCursor((c) => Math.min(total - 1, c + 1));
        } else if (char === "d") {
          const idx = taskCursor();
          const next = new Set(state.removedIndices);
          if (next.has(idx)) {
            next.delete(idx);
          } else {
            next.add(idx);
          }
          setState("removedIndices", next);
        } else if (keyName === "return") {
          nextStep();
        } else if (keyName === "backspace") {
          prevStep();
        }
        break;
      }

      case "options": {
        if (editingNotes()) {
          if (keyName === "escape") {
            setState("options", "notes", notesEditValue());
            setEditingNotes(false);
          } else if (keyName === "backspace") {
            setNotesEditValue((v) => v.slice(0, -1));
          } else if (keyName === "return") {
            setState("options", "notes", notesEditValue());
            setEditingNotes(false);
          } else if (char && char.length === 1) {
            setNotesEditValue((v) => v + char);
          }
          return;
        }

        if (keyName === "up") {
          setOptionCursor((c) => Math.max(0, c - 1));
        } else if (keyName === "down") {
          setOptionCursor((c) => Math.min(3, c + 1));
        } else if (char === " ") {
          const cursor = optionCursor();
          if (cursor === 0) {
            setState("options", "autoCommit", (v) => !v);
          } else if (cursor === 2) {
            setState("options", "useLoop", (v) => !v);
          }
        } else if (keyName === "left") {
          if (optionCursor() === 1) {
            setState("options", "maxIterations", (v) => Math.max(5, v - 5));
          }
        } else if (keyName === "right") {
          if (optionCursor() === 1) {
            setState("options", "maxIterations", (v) => Math.min(50, v + 5));
          }
        } else if (keyName === "return") {
          if (optionCursor() === 3) {
            setEditingNotes(true);
            setNotesEditValue(state.options.notes);
          } else {
            nextStep();
          }
        } else if (keyName === "backspace") {
          prevStep();
        }
        break;
      }

      case "confirm": {
        if (keyName === "return") {
          // Build filtered plan
          const filteredTasks = props.plan.tasks.filter(
            (_, i) => !state.removedIndices.has(i),
          );
          const finalPlan: Plan = {
            ...props.plan,
            tasks: filteredTasks.map((t, i) => ({ ...t, index: i })),
          };
          props.onApprove(finalPlan, { ...state.options });
        } else if (keyName === "backspace") {
          prevStep();
        }
        break;
      }
    }
  });

  return (
    <box flexDirection="column" width="100%" height="100%">
      {/* Step indicator */}
      <box flexDirection="row" paddingX={4} paddingY={0} gap={0}>
        {STEPS.map((s, i) => {
          const isCurrent = () => s === state.step;
          const isPast = () => STEPS.indexOf(state.step) > i;
          const color = () =>
            isCurrent()
              ? ironRainTheme.brand.primary
              : isPast()
                ? ironRainTheme.status.success
                : ironRainTheme.chrome.dimFg;
          return (
            <box flexDirection="row" gap={0}>
              <text fg={color()}>
                {isPast() ? (
                  `\u2713 ${STEP_LABELS[s]}`
                ) : isCurrent() ? (
                  <b>{`${i + 1} ${STEP_LABELS[s]}`}</b>
                ) : (
                  `${i + 1} ${STEP_LABELS[s]}`
                )}
              </text>
              <text fg={ironRainTheme.chrome.dimFg}>
                {i < STEPS.length - 1 ? "  \u203A  " : ""}
              </text>
            </box>
          );
        })}
      </box>

      {/* Current step */}
      <scrollbox flexGrow={1} stickyScroll stickyStart="bottom" paddingX={1}>
        {state.step === "overview" && (
          <OverviewStep
            plan={props.plan}
            editing={editing()}
            editValue={editValue()}
          />
        )}
        {state.step === "tasks" && (
          <TasksStep
            tasks={props.plan.tasks}
            cursor={taskCursor()}
            removedIndices={state.removedIndices}
          />
        )}
        {state.step === "options" && (
          <OptionsStep
            options={state.options}
            cursor={optionCursor()}
            editingNotes={editingNotes()}
            notesEditValue={notesEditValue()}
          />
        )}
        {state.step === "confirm" && (
          <ConfirmStep
            plan={props.plan}
            options={state.options}
            removedCount={removedCount()}
            remainingCount={remainingCount()}
          />
        )}
      </scrollbox>
    </box>
  );
}
