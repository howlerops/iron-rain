import type { Plan } from "@howlerops/iron-rain";
import { loadConfig, parseReferences } from "@howlerops/iron-rain";
import { useKeyboard } from "@opentui/solid";
import {
  batch,
  createMemo,
  createSignal,
  ErrorBoundary,
  Match,
  Show,
  Switch,
} from "solid-js";
import { PlanReview } from "../components/plan-review.js";
import { PlanView } from "../components/plan-view.js";
import {
  formatDuration,
  formatTokens,
  SessionView,
} from "../components/session-view.js";
import { Settings } from "../components/settings.js";
import { getFilteredCommands, SlashMenu } from "../components/slash-menu.js";
import { WelcomeScreen } from "../components/welcome-screen.js";
import { useSlate } from "../context/slate-context.js";
import type { SessionContext, SessionMode } from "../controllers/context.js";
import { LoopController } from "../controllers/loop-controller.js";
import { PlanController } from "../controllers/plan-controller.js";
import { handleSlashCommand as handleSlashCommandController } from "../controllers/slash-commands.js";
import { handleSystemCommand } from "../controllers/system-commands.js";
import { ironRainTheme, slotLabel } from "../theme/theme.js";

export function SessionRoute(props: { version?: string; onQuit?: () => void }) {
  const [state, actions] = useSlate();
  const [inputValue, setInputValue] = createSignal("");
  const [inputFocused, setInputFocused] = createSignal(true);
  const [menuIndex, setMenuIndex] = createSignal(0);
  const [mode, setMode] = createSignal<SessionMode>("chat");
  let inputRef: any = undefined;

  const showSlashMenu = createMemo(() => {
    const val = inputValue();
    return val.startsWith("/") && val.length >= 1;
  });

  const skillCommands = createMemo(() => actions.skillCommands());

  const filteredCommands = createMemo(() => {
    if (!showSlashMenu()) return [];
    return getFilteredCommands(inputValue(), skillCommands());
  });

  function addSystemMessage(content: string) {
    actions.addMessage({
      id: `sys-${Date.now()}`,
      role: "assistant",
      content,
      slot: "main",
      timestamp: Date.now(),
    });
  }

  const sessionContext = (): SessionContext => ({
    state,
    actions,
    setMode,
    addSystemMessage,
    version: props.version,
    onQuit: props.onQuit,
    skillCommands,
  });

  const planController = new PlanController(sessionContext());
  const loopController = new LoopController(sessionContext());

  async function handleSlashCommand(text: string): Promise<boolean> {
    const context = sessionContext();

    if (
      await handleSystemCommand({
        text,
        addSystemMessage,
        version: props.version,
      })
    ) {
      return true;
    }

    if (await planController.handleCommand(text)) {
      return true;
    }

    if (await loopController.handleCommand(text)) {
      return true;
    }

    return handleSlashCommandController(text, context);
  }

  function clearInput() {
    setInputValue("");
    setMenuIndex(0);
    if (inputRef) {
      try {
        inputRef.selectAll();
        inputRef.deleteCharBackward();
      } catch {}
    }
  }

  async function handleSubmit(_event?: unknown) {
    const text = (typeof _event === "string" ? _event : inputValue()).trim();
    if (!text) return;

    if (actions.isLoading() && !text.startsWith("/")) {
      actions.addMessage({
        id: crypto.randomUUID?.() ?? `${Date.now()}`,
        role: "user",
        content: text,
        slot: "main",
        timestamp: Date.now(),
      });
      clearInput();
      actions.injectContext(text);
      return;
    }

    if (mode() === "plan-review") {
      const plan = actions.activePlan();
      if (plan) {
        if (text.toLowerCase() === "approve" || text.toLowerCase() === "a") {
          clearInput();
          await planController.executePlan(plan);
          return;
        }
        if (text.toLowerCase() === "reject" || text.toLowerCase() === "r") {
          clearInput();
          setMode("chat");
          actions.setActivePlan(null);
          addSystemMessage("Plan rejected.");
          return;
        }
        clearInput();
        addSystemMessage(`Regenerating plan with feedback: *${text}*`);
        await planController.generatePlan(
          `${plan.description}\n\nFeedback: ${text}`,
        );
        return;
      }
    }

    if (showSlashMenu() && filteredCommands().length > 0) {
      const selected = filteredCommands()[menuIndex()];
      if (selected) {
        clearInput();
        await handleSlashCommand(selected.name);
        return;
      }
    }

    if (text.startsWith("/")) {
      clearInput();
      if (await handleSlashCommand(text)) {
        return;
      }
    }

    const { targetSlot, prompt, references } = await parseReferences(
      text,
      process.cwd(),
      state.contextDirectories,
    );

    const imageRefs = references.filter((r) => r.type === "image");
    const images =
      imageRefs.length > 0
        ? imageRefs.map((r) => {
            const name = r.path.split("/").pop() ?? r.path;
            const sizeMatch = r.content.match(/size="(\d+)KB"/);
            const sizeKB = sizeMatch ? parseInt(sizeMatch[1], 10) : 0;
            return { path: r.path, name, sizeKB };
          })
        : undefined;

    actions.addMessage({
      id: crypto.randomUUID?.() ?? `${Date.now()}`,
      role: "user",
      content: text,
      slot: targetSlot ?? "main",
      timestamp: Date.now(),
      images,
    });

    clearInput();
    actions.dispatch(
      prompt,
      targetSlot,
      references.length > 0 ? references : undefined,
    );
  }

  useKeyboard((e) => {
    if (mode() === "settings") {
      if (e.name === "escape") {
        setMode("chat");
        e.preventDefault();
      }
      return;
    }

    if (mode() === "plan-review") {
      if (e.name === "a" && !e.ctrl && !e.meta) {
        const plan = actions.activePlan();
        if (plan) {
          e.preventDefault();
          planController.executePlan(plan);
          return;
        }
      }
      if (e.name === "r" && !e.ctrl && !e.meta) {
        e.preventDefault();
        setMode("chat");
        actions.setActivePlan(null);
        addSystemMessage("Plan rejected.");
        return;
      }
    }

    if (e.name === "return" && !e.meta && !e.ctrl) {
      const val = inputValue();
      if (val.trim()) {
        handleSubmit(val);
      }
      e.preventDefault();
      return;
    }

    if (e.name === "escape" && actions.isLoading()) {
      actions.cancelDispatch();
      return;
    }

    if (e.name === "escape" && mode() !== "chat") {
      setMode("chat");
      return;
    }

    if (!showSlashMenu()) return;
    const cmds = filteredCommands();
    if (cmds.length === 0) return;

    if (e.name === "up") {
      setMenuIndex((i) => Math.max(0, i - 1));
    } else if (e.name === "down") {
      setMenuIndex((i) => Math.min(cmds.length - 1, i + 1));
    } else if (e.name === "tab") {
      const selected = cmds[menuIndex()];
      if (selected) {
        setInputValue(selected.name);
      }
    }
  });

  const hasMessages = createMemo(() => state.messages.length > 0);

  return (
    <Switch>
      <Match when={mode() === "settings"}>
        <ErrorBoundary
          fallback={(err: any) => (
            <box flexDirection="column" paddingX={2} paddingY={1}>
              <text fg="red">{`Settings Error: ${String(err)}`}</text>
              <text fg={ironRainTheme.chrome.dimFg}>Press Esc to go back</text>
            </box>
          )}
        >
          <Settings
            config={loadConfig()}
            initialSection={
              Object.keys(loadConfig().providers ?? {}).length === 0
                ? "providers"
                : undefined
            }
            onSave={(cfg) => {
              if (cfg.slots) actions.updateSlots(cfg.slots);
              actions.clearMessages();
              setMode("chat");
            }}
            onClose={() => setMode("chat")}
          />
        </ErrorBoundary>
      </Match>
      <Match when={mode() === "plan-review"}>
        <box flexDirection="column" flexGrow={1}>
          <Show when={actions.activePlan()}>
            {(plan: () => Plan) => (
              <scrollbox
                flexGrow={1}
                stickyScroll
                stickyStart="bottom"
                paddingX={1}
              >
                <PlanReview
                  plan={plan()}
                  onApprove={() => planController.executePlan(plan())}
                  onReject={() => {
                    setMode("chat");
                    actions.setActivePlan(null);
                  }}
                  onEdit={(feedback) =>
                    planController.generatePlan(
                      `${plan().description}\n\nFeedback: ${feedback}`,
                    )
                  }
                />
              </scrollbox>
            )}
          </Show>
          <box paddingX={1}>
            <input
              ref={(el: any) => {
                inputRef = el;
              }}
              width="100%"
              focused={inputFocused()}
              value={inputValue()}
              placeholder="Type approve, reject, or feedback..."
              placeholderColor={ironRainTheme.chrome.muted}
              textColor={ironRainTheme.chrome.fg}
              backgroundColor={ironRainTheme.chrome.bg}
              focusedBackgroundColor={ironRainTheme.chrome.bg}
              focusedTextColor={ironRainTheme.chrome.fg}
              keyBindings={[{ name: "return", action: "submit" }]}
              onSubmit={handleSubmit}
              onInput={(val: string) => {
                setInputValue(val);
              }}
            />
          </box>
        </box>
      </Match>
      <Match
        when={
          mode() === "chat" ||
          mode() === "plan-generating" ||
          mode() === "plan-executing" ||
          mode() === "loop-running"
        }
      >
        <box flexDirection="column" flexGrow={1} height="100%">
          <Show
            when={hasMessages()}
            fallback={<WelcomeScreen model={state.slots.main.model} />}
          >
            <scrollbox
              flexGrow={1}
              flexShrink={1}
              stickyScroll
              stickyStart="bottom"
              paddingX={1}
            >
              <box flexDirection="column">
                <SessionView
                  messages={state.messages}
                  stats={state.sessionStats}
                  isLoading={actions.isLoading()}
                  activeSlot={actions.activeSlot()}
                  streamingContent={actions.streamingContent()}
                  streamingThinking={actions.streamingThinking()}
                  streamingSystemPrompt={actions.streamingSystemPrompt()}
                  streamingToolCalls={actions.streamingToolCalls()}
                  streamingTask={actions.streamingTask()}
                  loadingStartTime={actions.loadingStartTime()}
                />

                <Show
                  when={mode() === "plan-executing" && actions.activePlan()}
                >
                  {(plan: () => Plan) => (
                    <PlanView
                      plan={plan()}
                      streamingContent={actions.streamingContent()}
                    />
                  )}
                </Show>
              </box>
            </scrollbox>
          </Show>

          <Show when={showSlashMenu() && filteredCommands().length > 0}>
            <scrollbox maxHeight={12} flexShrink={0}>
              <SlashMenu
                filter={inputValue()}
                selectedIndex={menuIndex()}
                onSelect={(cmd) => {
                  setInputValue("");
                  setMenuIndex(0);
                  handleSlashCommand(cmd.name);
                }}
                extraCommands={skillCommands()}
              />
            </scrollbox>
          </Show>

          {/* ── Input row with prompt indicator ───── */}
          <box
            flexDirection="row"
            paddingX={1}
            gap={1}
            flexShrink={0}
            border
            borderStyle="rounded"
            borderColor={ironRainTheme.chrome.border}
          >
            <text fg={ironRainTheme.brand.primary}>
              <b>{"\u276F"}</b>
            </text>
            <textarea
              ref={(el: any) => {
                inputRef = el;
              }}
              flexGrow={1}
              minHeight={1}
              maxHeight={6}
              focused={inputFocused()}
              initialValue=""
              placeholder={
                actions.isLoading()
                  ? "Add context or press Esc to cancel..."
                  : "Type a message..."
              }
              placeholderColor={ironRainTheme.chrome.muted}
              textColor={ironRainTheme.chrome.fg}
              backgroundColor={ironRainTheme.chrome.bg}
              focusedBackgroundColor={ironRainTheme.chrome.bg}
              focusedTextColor={ironRainTheme.chrome.fg}
              keyBindings={[{ name: "return", action: "submit" }]}
              onSubmit={() => handleSubmit()}
              onContentChange={
                (() => {
                  batch(() => {
                    const text = inputRef?.plainText ?? "";
                    setInputValue(text);
                    setMenuIndex(0);
                  });
                }) as any
              }
            />
          </box>

          {/* ── Status bar: model (left) · stats (right) ── */}
          <box
            flexDirection="row"
            paddingX={1}
            flexShrink={0}
            justifyContent="space-between"
          >
            <box flexDirection="row" gap={2}>
              <text fg={ironRainTheme.chrome.muted}>
                {state.slots.main.model}
              </text>
              {actions.isLoading() && (
                <text fg={ironRainTheme.brand.primary}>
                  {`${slotLabel(actions.activeSlot())} working...`}
                </text>
              )}
              <Show when={mode() === "loop-running"}>
                <text fg={ironRainTheme.status.warning}>LOOP RUNNING</text>
              </Show>
              <Show when={mode() === "plan-executing"}>
                <text fg={ironRainTheme.status.warning}>PLAN EXECUTING</text>
              </Show>
            </box>
            <Show
              when={state.sessionStats && state.sessionStats.requestCount > 0}
            >
              <text fg={ironRainTheme.chrome.dimFg}>
                {`${formatDuration(state.sessionStats!.totalDuration)} \u00B7 ${state.sessionStats!.requestCount} req${state.sessionStats!.requestCount !== 1 ? "s" : ""} \u00B7 ${formatTokens(state.sessionStats!.totalTokens)} tokens`}
              </text>
            </Show>
          </box>
        </box>
      </Match>
    </Switch>
  );
}
