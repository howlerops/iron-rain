import { createSignal, createMemo, Show, Switch, Match, ErrorBoundary } from 'solid-js';
import { useKeyboard } from '@opentui/solid';
import {
  loadConfig,
  checkForUpdate,
  performUpdate,
  getVersionInfo,
  runDiagnostics,
  PlanGenerator,
  PlanExecutor,
  PlanStorage,
  RalphLoop,
  SkillExecutor,
  parseReferences,
} from '@howlerops/iron-rain';
import type { Plan, LoopState, LoopConfig } from '@howlerops/iron-rain';
import { useSlate, getSessionDB } from '../context/slate-context.js';
import { SessionView } from '../components/session-view.js';
import { WelcomeScreen } from '../components/welcome-screen.js';
import { Settings } from '../components/settings.js';
import { SlashMenu, getFilteredCommands, SLASH_COMMANDS } from '../components/slash-menu.js';
import { PlanView } from '../components/plan-view.js';
import { PlanReview } from '../components/plan-review.js';
import { SkillPicker } from '../components/skill-picker.js';
import { ironRainTheme, slotLabel } from '../theme/theme.js';

type SessionMode = 'chat' | 'settings' | 'plan-generating' | 'plan-review' | 'plan-executing' | 'loop-running' | 'skills-browse' | 'mcp-status';

export function SessionRoute(props: { version?: string; onQuit?: () => void }) {
  const [state, actions] = useSlate();
  const [inputValue, setInputValue] = createSignal('');
  const [inputFocused, setInputFocused] = createSignal(true);
  const [menuIndex, setMenuIndex] = createSignal(0);
  const [mode, setMode] = createSignal<SessionMode>('chat');
  let inputRef: any = undefined;

  const showSlashMenu = createMemo(() => {
    const val = inputValue();
    return val.startsWith('/') && val.length >= 1;
  });

  // Include skill-derived commands in the menu
  const skillCommands = createMemo(() => actions.skillCommands());

  const filteredCommands = createMemo(() => {
    if (!showSlashMenu()) return [];
    return getFilteredCommands(inputValue(), skillCommands());
  });

  // Helper to add a system message
  function addSystemMessage(content: string) {
    actions.addMessage({
      id: `sys-${Date.now()}`,
      role: 'assistant',
      content,
      slot: 'main',
      timestamp: Date.now(),
    });
  }

  async function handleSlashCommand(text: string): Promise<boolean> {
    if (text === '/quit' || text === '/exit') {
      props.onQuit?.();
      return true;
    }
    if (text === '/clear') {
      actions.clearMessages();
      return true;
    }
    if (text === '/settings') {
      setMode('settings');
      return true;
    }
    if (text === '/new') {
      actions.newSession();
      return true;
    }
    if (text === '/help') {
      const allCmds = [...SLASH_COMMANDS, ...skillCommands()];
      addSystemMessage(
        allCmds.map(c => `**${c.name}** — ${c.description}`).join('\n') +
        '\n\n**@cortex/@scout/@forge** — Route to a specific slot',
      );
      return true;
    }
    if (text === '/lessons') {
      const db = getSessionDB();
      if (!db) {
        addSystemMessage('No database available for lessons.');
      } else {
        const lessons = db.getLessons(20);
        const content = lessons.length === 0
          ? 'No lessons learned yet. Lessons are saved from conversations to improve future responses.'
          : lessons.map((l: any, i: number) => `${i + 1}. ${l.content}${l.tags.length ? ` *(${l.tags.join(', ')})*` : ''}`).join('\n');
        addSystemMessage(`## Lessons Learned\n${content}`);
      }
      return true;
    }

    // --- Auto-update commands ---
    if (text === '/version') {
      const info = getVersionInfo();
      addSystemMessage(
        `**${info.package}** v${info.version}\n` +
        `Bun: ${info.bun}\n` +
        `OS: ${info.os}\n` +
        `Config: ${info.configPath}`,
      );
      return true;
    }

    if (text === '/update') {
      addSystemMessage('Checking for updates...');
      try {
        const result = await checkForUpdate(props.version ?? '0.1.6');
        if (result.updateAvailable) {
          addSystemMessage(`Update available: ${result.currentVersion} -> ${result.latestVersion}\nInstalling...`);
          const updateResult = await performUpdate();
          if (updateResult.success) {
            addSystemMessage('Update installed successfully. Restart iron-rain to use the new version.');
          } else {
            addSystemMessage(`Update failed: ${updateResult.error}`);
          }
        } else {
          addSystemMessage(`Already on latest version (${result.currentVersion}).`);
        }
      } catch (err) {
        addSystemMessage(`Update check failed: ${err instanceof Error ? err.message : String(err)}`);
      }
      return true;
    }

    if (text === '/doctor') {
      addSystemMessage('Running diagnostics...');
      try {
        const config = loadConfig();
        const checks = await runDiagnostics(config);
        const lines = checks.map(c => {
          const icon = c.status === 'ok' ? '\u2713' : c.status === 'warn' ? '!' : '\u2717';
          return `${icon} **${c.name}**: ${c.message}`;
        });
        addSystemMessage(`## Diagnostics\n${lines.join('\n')}`);
      } catch (err) {
        addSystemMessage(`Diagnostics failed: ${err instanceof Error ? err.message : String(err)}`);
      }
      return true;
    }

    // --- Plan commands ---
    if (text.startsWith('/plan ')) {
      const want = text.slice(6).trim();
      if (!want) {
        addSystemMessage('Usage: /plan <description of what you want to build>');
        return true;
      }
      await handlePlanGenerate(want);
      return true;
    }
    if (text === '/plan') {
      addSystemMessage('Usage: /plan <description of what you want to build>');
      return true;
    }

    if (text === '/plans') {
      const storage = new PlanStorage();
      const plans = storage.list();
      if (plans.length === 0) {
        addSystemMessage('No saved plans. Use `/plan <description>` to create one.');
      } else {
        const lines = plans.map(p => `- **${p.title}** (${p.status}) — ${new Date(p.createdAt).toLocaleDateString()}`);
        addSystemMessage(`## Saved Plans\n${lines.join('\n')}`);
      }
      return true;
    }

    if (text === '/resume') {
      const plan = actions.activePlan();
      if (plan && (plan.status === 'paused' || plan.status === 'approved')) {
        await handlePlanExecute(plan);
      } else {
        addSystemMessage('No paused plan to resume. Use `/plans` to see saved plans.');
      }
      return true;
    }

    // --- Loop commands ---
    if (text.startsWith('/loop ')) {
      const rest = text.slice(6).trim();
      const untilMatch = rest.match(/(.+?)\s+--until\s+"([^"]+)"/);
      if (untilMatch) {
        await handleLoopStart(untilMatch[1].trim(), untilMatch[2]);
      } else {
        addSystemMessage('Usage: /loop <description> --until "<condition>"');
      }
      return true;
    }
    if (text === '/loop') {
      addSystemMessage('Usage: /loop <description> --until "<condition>"');
      return true;
    }

    if (text === '/loop-status') {
      const loop = actions.activeLoop();
      if (loop) {
        const lines = [
          `**Goal:** ${loop.config.want}`,
          `**Condition:** ${loop.config.completionPromise}`,
          `**Status:** ${loop.status}`,
          `**Iterations:** ${loop.iterations.length}/${loop.config.maxIterations}`,
        ];
        if (loop.iterations.length > 0) {
          const last = loop.iterations[loop.iterations.length - 1];
          lines.push(`**Last action:** ${last.action.slice(0, 200)}`);
        }
        addSystemMessage(lines.join('\n'));
      } else {
        addSystemMessage('No active loop. Use `/loop <description> --until "<condition>"` to start one.');
      }
      return true;
    }

    if (text === '/loop-pause') {
      addSystemMessage('Loop pause requested. The loop will stop after the current iteration.');
      // The actual pause happens in the loop runner
      return true;
    }

    if (text === '/loop-resume') {
      const loop = actions.activeLoop();
      if (loop && loop.status === 'paused') {
        await handleLoopResume(loop);
      } else {
        addSystemMessage('No paused loop to resume.');
      }
      return true;
    }

    // --- Context directory commands ---
    if (text === '/context' || text === '/context help') {
      addSystemMessage(
        '## Context Directories\n' +
        '`/context add <path>` — Add a directory to context scope\n' +
        '`/context list` — Show current context directories\n' +
        '`/context remove <path>` — Remove a directory from context scope',
      );
      return true;
    }
    if (text.startsWith('/context add ')) {
      const dirPath = text.slice(13).trim();
      if (!dirPath) {
        addSystemMessage('Usage: `/context add <path>`');
        return true;
      }
      const err = actions.addContextDirectory(dirPath);
      if (err) {
        addSystemMessage(`**Error:** ${err}`);
      } else {
        addSystemMessage(`Added context directory: \`${dirPath}\``);
      }
      return true;
    }
    if (text === '/context list') {
      const dirs = actions.contextDirectories();
      if (dirs.length === 0) {
        addSystemMessage('No context directories configured. Use `/context add <path>` to add one.');
      } else {
        addSystemMessage(`## Context Directories\n${dirs.map(d => `- \`${d}\``).join('\n')}`);
      }
      return true;
    }
    if (text.startsWith('/context remove ')) {
      const dirPath = text.slice(16).trim();
      if (!dirPath) {
        addSystemMessage('Usage: `/context remove <path>`');
        return true;
      }
      const removed = actions.removeContextDirectory(dirPath);
      if (removed) {
        addSystemMessage(`Removed context directory: \`${dirPath}\``);
      } else {
        addSystemMessage(`Directory not found in context: \`${dirPath}\``);
      }
      return true;
    }

    // --- Skills & MCP commands ---
    if (text === '/skills') {
      const skills = actions.skillRegistry().list();
      if (skills.length === 0) {
        addSystemMessage('No skills found. Add .md files to `.iron-rain/skills/` or `.claude/skills/`.');
      } else {
        const grouped = actions.skillRegistry().grouped();
        const parts: string[] = ['## Available Skills'];
        for (const [source, sourceSkills] of grouped) {
          parts.push(`\n### ${source}`);
          for (const s of sourceSkills) {
            parts.push(`- **${s.command}** — ${s.description}`);
          }
        }
        addSystemMessage(parts.join('\n'));
      }
      return true;
    }

    if (text === '/mcp') {
      const mgr = actions.mcpManager();
      const status = mgr.getStatus();
      if (status.length === 0) {
        addSystemMessage('No MCP servers configured. Add `mcpServers` to your iron-rain.json config.');
      } else {
        const lines = status.map(s => {
          const icon = s.connected ? '\u2713' : '\u2717';
          return `${icon} **${s.name}**: ${s.connected ? `Connected (${s.toolCount} tools)` : 'Disconnected'}`;
        });
        addSystemMessage(`## MCP Servers\n${lines.join('\n')}\n\nTotal tools: ${mgr.totalToolCount}`);
      }
      return true;
    }

    // --- Skill command execution ---
    const skill = actions.skillRegistry().getByCommand(text.split(' ')[0]);
    if (skill) {
      const args = text.slice(skill.command!.length).trim();
      addSystemMessage(`Running skill: **${skill.name}**...`);
      const kernel = actions.getDispatcher().ensureKernel(state.slots);
      const executor = new SkillExecutor(kernel);
      try {
        const episode = await executor.execute(skill, args || undefined);
        addSystemMessage(episode.result);
      } catch (err) {
        addSystemMessage(`Skill error: ${err instanceof Error ? err.message : String(err)}`);
      }
      return true;
    }

    return false;
  }

  async function handlePlanGenerate(want: string) {
    setMode('plan-generating');
    addSystemMessage(`Generating plan for: *${want}*`);

    const kernel = actions.getDispatcher().ensureKernel(state.slots);
    const generator = new PlanGenerator(kernel);

    try {
      const plan = await generator.generatePlan(want);
      actions.setActivePlan(plan);

      const storage = new PlanStorage();
      storage.save(plan);

      setMode('plan-review');
      addSystemMessage(`Plan generated with ${plan.tasks.length} tasks. Review below.\n\nType **approve**, **reject**, or provide feedback.`);
    } catch (err) {
      addSystemMessage(`Plan generation failed: ${err instanceof Error ? err.message : String(err)}`);
      setMode('chat');
    }
  }

  async function handlePlanExecute(plan: Plan) {
    setMode('plan-executing');
    plan.status = 'approved';
    actions.setActivePlan(plan);

    const kernel = actions.getDispatcher().ensureKernel(state.slots);
    const executor = new PlanExecutor(kernel);

    try {
      const result = await executor.executePlan(plan, {
        onTaskStart: (task) => {
          addSystemMessage(`Starting task ${task.index + 1}: **${task.title}**`);
        },
        onTaskComplete: (task) => {
          addSystemMessage(`Completed task ${task.index + 1}: **${task.title}**${task.result?.commitHash ? ` (${task.result.commitHash})` : ''}`);
          actions.setActivePlan({ ...plan });
        },
        onTaskFail: (task, error) => {
          addSystemMessage(`Failed task ${task.index + 1}: **${task.title}** — ${error}`);
          actions.setActivePlan({ ...plan });
        },
        onPlanComplete: (completedPlan) => {
          const status = completedPlan.status === 'completed' ? 'completed successfully' : `finished with status: ${completedPlan.status}`;
          addSystemMessage(`Plan ${status}. ${completedPlan.stats.tasksCompleted}/${completedPlan.tasks.length} tasks completed.`);
        },
      });

      actions.setActivePlan(result);
    } catch (err) {
      addSystemMessage(`Plan execution error: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setMode('chat');
    }
  }

  async function handleLoopStart(want: string, until: string) {
    setMode('loop-running');

    const config: LoopConfig = {
      want,
      completionPromise: until,
      maxIterations: 10,
      autoCommit: true,
    };

    addSystemMessage(`Starting loop: *${want}*\nUntil: "${until}"\nMax iterations: ${config.maxIterations}`);

    const kernel = actions.getDispatcher().ensureKernel(state.slots);
    const loop = new RalphLoop(kernel, {
      onIterationStart: (i) => {
        addSystemMessage(`**Iteration ${i + 1}** starting...`);
      },
      onIterationComplete: (iter) => {
        const status = iter.completionMet ? 'CONDITION MET' : 'continuing';
        addSystemMessage(`**Iteration ${iter.index + 1}** — ${status}${iter.commitHash ? ` (${iter.commitHash})` : ''}`);
      },
      onComplete: (loopState) => {
        const msg = loopState.status === 'completed'
          ? `Loop completed after ${loopState.iterations.length} iterations.`
          : `Loop ${loopState.status} after ${loopState.iterations.length} iterations.`;
        addSystemMessage(msg);
      },
    });

    try {
      const result = await loop.run(config);
      actions.setActiveLoop(result);
    } catch (err) {
      addSystemMessage(`Loop error: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setMode('chat');
    }
  }

  async function handleLoopResume(loopState: LoopState) {
    setMode('loop-running');
    addSystemMessage('Resuming loop...');

    const kernel = actions.getDispatcher().ensureKernel(state.slots);
    const loop = new RalphLoop(kernel, {
      onIterationComplete: (iter) => {
        addSystemMessage(`**Iteration ${iter.index + 1}** — ${iter.completionMet ? 'CONDITION MET' : 'continuing'}`);
      },
      onComplete: (state) => {
        addSystemMessage(`Loop ${state.status} after ${state.iterations.length} iterations.`);
      },
    });

    try {
      const result = await loop.resume(loopState);
      actions.setActiveLoop(result);
    } catch (err) {
      addSystemMessage(`Loop resume error: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setMode('chat');
    }
  }

  function clearInput() {
    setInputValue('');
    setMenuIndex(0);
    if (inputRef) {
      try { inputRef.selectAll(); inputRef.deleteCharBackward(); } catch {}
    }
  }

  async function handleSubmit(value: string) {
    const text = value.trim();
    if (!text) return;

    // Mid-stream context injection: if loading and not a slash command, inject context
    if (actions.isLoading() && !text.startsWith('/')) {
      actions.addMessage({
        id: crypto.randomUUID?.() ?? `${Date.now()}`,
        role: 'user',
        content: text,
        slot: 'main',
        timestamp: Date.now(),
      });
      clearInput();
      actions.injectContext(text);
      return;
    }

    // Plan review mode: handle approve/reject/feedback
    if (mode() === 'plan-review') {
      const plan = actions.activePlan();
      if (plan) {
        if (text.toLowerCase() === 'approve' || text.toLowerCase() === 'a') {
          clearInput();
          await handlePlanExecute(plan);
          return;
        }
        if (text.toLowerCase() === 'reject' || text.toLowerCase() === 'r') {
          clearInput();
          setMode('chat');
          actions.setActivePlan(null);
          addSystemMessage('Plan rejected.');
          return;
        }
        // Treat as edit feedback — regenerate with feedback
        clearInput();
        addSystemMessage(`Regenerating plan with feedback: *${text}*`);
        await handlePlanGenerate(`${plan.description}\n\nFeedback: ${text}`);
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

    if (text.startsWith('/')) {
      clearInput();
      if (await handleSlashCommand(text)) {
        return;
      }
    }

    const { targetSlot, prompt, references } = parseReferences(
      text,
      process.cwd(),
      state.contextDirectories,
    );

    // Extract image metadata for display
    const imageRefs = references.filter(r => r.type === 'image');
    const images = imageRefs.length > 0
      ? imageRefs.map(r => {
          const name = r.path.split('/').pop() ?? r.path;
          const sizeMatch = r.content.match(/size="(\d+)KB"/);
          const sizeKB = sizeMatch ? parseInt(sizeMatch[1], 10) : 0;
          return { path: r.path, name, sizeKB };
        })
      : undefined;

    actions.addMessage({
      id: crypto.randomUUID?.() ?? `${Date.now()}`,
      role: 'user',
      content: text,
      slot: targetSlot ?? 'main',
      timestamp: Date.now(),
      images,
    });

    clearInput();
    actions.dispatch(prompt, targetSlot, references.length > 0 ? references : undefined);
  }

  useKeyboard((e) => {
    if (mode() === 'settings') {
      if (e.name === 'escape') {
        setMode('chat');
        e.preventDefault();
      }
      return;
    }

    // Plan review keyboard shortcuts
    if (mode() === 'plan-review') {
      if (e.name === 'a' && !e.ctrl && !e.meta) {
        const plan = actions.activePlan();
        if (plan) {
          e.preventDefault();
          handlePlanExecute(plan);
          return;
        }
      }
      if (e.name === 'r' && !e.ctrl && !e.meta) {
        e.preventDefault();
        setMode('chat');
        actions.setActivePlan(null);
        addSystemMessage('Plan rejected.');
        return;
      }
    }

    if (e.name === 'return' && !e.meta && !e.ctrl) {
      const val = inputValue();
      if (val.trim()) {
        handleSubmit(val);
      }
      e.preventDefault();
      return;
    }

    if (e.name === 'escape' && actions.isLoading()) {
      actions.cancelDispatch();
      return;
    }

    if (e.name === 'escape' && mode() !== 'chat') {
      setMode('chat');
      return;
    }

    if (!showSlashMenu()) return;
    const cmds = filteredCommands();
    if (cmds.length === 0) return;

    if (e.name === 'up') {
      setMenuIndex(i => Math.max(0, i - 1));
    } else if (e.name === 'down') {
      setMenuIndex(i => Math.min(cmds.length - 1, i + 1));
    } else if (e.name === 'tab') {
      const selected = cmds[menuIndex()];
      if (selected) {
        setInputValue(selected.name);
      }
    }
  });

  const hasMessages = createMemo(() => state.messages.length > 0);

  return (
    <Switch>
      <Match when={mode() === 'settings'}>
        <ErrorBoundary fallback={(err: any) => (
          <box flexDirection="column" paddingX={2} paddingY={1}>
            <text fg="red">{`Settings Error: ${String(err)}`}</text>
            <text fg={ironRainTheme.chrome.dimFg}>Press Esc to go back</text>
          </box>
        )}>
          <Settings
            config={loadConfig()}
            initialSection={Object.keys(loadConfig().providers ?? {}).length === 0 ? 'providers' : undefined}
            onSave={(cfg) => {
              if (cfg.slots) actions.updateSlots(cfg.slots);
              actions.clearMessages();
              setMode('chat');
            }}
            onClose={() => setMode('chat')}
          />
        </ErrorBoundary>
      </Match>
      <Match when={mode() === 'plan-review'}>
        <box flexDirection="column" flexGrow={1}>
          <Show when={actions.activePlan()}>
            {(plan: () => Plan) => (
              <scrollbox flexGrow={1} stickyScroll stickyStart="bottom" paddingX={1}>
                <PlanReview
                  plan={plan()}
                  onApprove={() => handlePlanExecute(plan())}
                  onReject={() => { setMode('chat'); actions.setActivePlan(null); }}
                  onEdit={(feedback) => handlePlanGenerate(`${plan().description}\n\nFeedback: ${feedback}`)}
                />
              </scrollbox>
            )}
          </Show>
          <box paddingX={1}>
            <input
              ref={(el: any) => { inputRef = el; }}
              width="100%"
              focused={inputFocused()}
              value={inputValue()}
              placeholder="Type approve, reject, or feedback..."
              placeholderColor={ironRainTheme.chrome.muted}
              textColor={ironRainTheme.chrome.fg}
              backgroundColor={ironRainTheme.chrome.bg}
              focusedBackgroundColor={ironRainTheme.chrome.bg}
              focusedTextColor={ironRainTheme.chrome.fg}
              keyBindings={[{ name: 'return', action: 'submit' }]}
              // @ts-expect-error OpenTUI types
              onSubmit={handleSubmit}
              onInput={(val: string) => { setInputValue(val); }}
            />
          </box>
        </box>
      </Match>
      <Match when={mode() === 'chat' || mode() === 'plan-generating' || mode() === 'plan-executing' || mode() === 'loop-running'}>
        <box flexDirection="column" flexGrow={1}>
          <Show when={hasMessages()} fallback={
            <WelcomeScreen model={state.slots.main.model} />
          }>
            <scrollbox
              flexGrow={1}
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
                  loadingStartTime={actions.loadingStartTime()}
                />

                <Show when={mode() === 'plan-executing' && actions.activePlan()}>
                  {(plan: () => Plan) => <PlanView plan={plan()} streamingContent={actions.streamingContent()} />}
                </Show>
              </box>
            </scrollbox>
          </Show>

          <Show when={showSlashMenu() && filteredCommands().length > 0}>
            <SlashMenu
              filter={inputValue()}
              selectedIndex={menuIndex()}
              onSelect={(cmd) => {
                setInputValue('');
                setMenuIndex(0);
                handleSlashCommand(cmd.name);
              }}
              extraCommands={skillCommands()}
            />
          </Show>

          <box paddingX={1}>
            <input
              ref={(el: any) => { inputRef = el; }}
              width="100%"
              focused={inputFocused()}
              value={inputValue()}
              placeholder={actions.isLoading() ? 'Add context or press Esc to cancel...' : 'Type a message...'}
              placeholderColor={ironRainTheme.chrome.muted}
              textColor={ironRainTheme.chrome.fg}
              backgroundColor={ironRainTheme.chrome.bg}
              focusedBackgroundColor={ironRainTheme.chrome.bg}
              focusedTextColor={ironRainTheme.chrome.fg}
              keyBindings={[{ name: 'return', action: 'submit' }]}
              // @ts-expect-error OpenTUI types onSubmit as SubmitEvent but calls with string at runtime
              onSubmit={handleSubmit}
              onInput={(val: string) => {
                setInputValue(val);
                setMenuIndex(0);
              }}
            />
          </box>

          <box flexDirection="row" paddingX={1} gap={2}>
            <text fg={ironRainTheme.chrome.dimFg}>
              {state.slots.main.model}
            </text>
            {actions.isLoading() && (
              <text fg={ironRainTheme.brand.primary}>
                {`${slotLabel(actions.activeSlot())} working...`}
              </text>
            )}
            <Show when={mode() === 'loop-running'}>
              <text fg={ironRainTheme.status.warning}>LOOP RUNNING</text>
            </Show>
            <Show when={mode() === 'plan-executing'}>
              <text fg={ironRainTheme.status.warning}>PLAN EXECUTING</text>
            </Show>
          </box>
        </box>
      </Match>
    </Switch>
  );
}
