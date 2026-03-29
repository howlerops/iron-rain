/**
 * Dispatch orchestration — extracted from slate-context to keep it focused.
 *
 * Handles: context compaction (RLM), system prompt building, streaming,
 * episode readback, tool routing heuristics, MCP injection, and abort handling.
 */
import type {
  CliPermissionMode,
  EpisodeSummary,
  HookEmitter,
  MCPManager,
  OrchestratorTask,
  ResolvedReference,
  SlotAssignment,
  SlotName,
} from "@howlerops/iron-rain";
import {
  buildContextWindow,
  buildEpisodeContext,
  buildSystemPrompt,
  createEpisodeSummary,
  detectToolType,
  formatEpisodeInputs,
  hasDispatchTags,
  ModelSlotManager,
  OrchestratorKernel,
  parseDispatchTags,
  stripDispatchTags,
} from "@howlerops/iron-rain";
import type {
  Message,
  SlotActivity,
  ToolCallEntry,
} from "../components/session-view.js";

export interface DispatchCallbacks {
  setIsLoading: (v: boolean) => void;
  setActiveSlot: (slot: SlotName) => void;
  setStreamingContent: (v: string) => void;
  setStreamingThinking: (v: string) => void;
  setStreamingSystemPrompt: (v: string) => void;
  setStreamingToolCalls: (v: ToolCallEntry[]) => void;
  setStreamingTask: (v: string) => void;
  setLoadingStartTime: (v: number) => void;
  getStreamingContent: () => string;
  getActiveSlot: () => SlotName;
  addMessage: (msg: Message) => void;
  updateStats: (duration: number, tokens: number) => void;
}

export interface DispatchState {
  messages: Message[];
  slots: SlotAssignment;
  contextDirectories?: string[];
}

export interface DispatchContext {
  rules?: string[];
  repoMap?: string;
  lessons?: string[];
  qualityGates?: string[];
}

export class DispatchController {
  private kernel: OrchestratorKernel | null = null;
  private currentAbort: AbortController | null = null;
  private mcpManager: MCPManager | null;
  private context: DispatchContext = {};
  private cliPermissions?: Record<string, CliPermissionMode>;
  private hookEmitter: HookEmitter | null = null;

  constructor(
    slots?: SlotAssignment,
    mcpManager?: MCPManager,
    cliPermissions?: Record<string, CliPermissionMode>,
  ) {
    this.cliPermissions = cliPermissions;
    if (slots) {
      this.kernel = new OrchestratorKernel(
        new ModelSlotManager(slots),
        this.cliPermissions,
      );
    }
    this.mcpManager = mcpManager ?? null;
  }

  setHookEmitter(emitter: HookEmitter): void {
    this.hookEmitter = emitter;
  }

  setCliPermissions(perms: Record<string, CliPermissionMode>): void {
    this.cliPermissions = perms;
    // Rebuild kernel so new permission flags take effect
    if (this.kernel) {
      this.kernel = null;
    }
  }

  setContext(ctx: DispatchContext): void {
    this.context = ctx;
  }

  cancel(): void {
    if (this.currentAbort) {
      this.currentAbort.abort();
      this.currentAbort = null;
    }
  }

  getKernel(): OrchestratorKernel | null {
    return this.kernel;
  }

  ensureKernel(slots: SlotAssignment): OrchestratorKernel {
    if (!this.kernel) {
      this.kernel = new OrchestratorKernel(
        new ModelSlotManager(slots),
        this.cliPermissions,
      );
    }
    return this.kernel;
  }

  async dispatch(
    prompt: string,
    state: DispatchState,
    callbacks: DispatchCallbacks,
    targetSlot?: SlotName,
    references?: ResolvedReference[],
    systemPromptOverride?: string,
  ): Promise<void> {
    if (!this.kernel) {
      this.kernel = new OrchestratorKernel(
        new ModelSlotManager(state.slots),
        this.cliPermissions,
      );
    }

    // Cancel any previous in-flight request
    this.cancel();
    this.currentAbort = new AbortController();
    const signal = this.currentAbort.signal;

    callbacks.setIsLoading(true);
    callbacks.setActiveSlot(targetSlot ?? "main");
    callbacks.setStreamingContent("");
    callbacks.setStreamingThinking("");
    callbacks.setStreamingToolCalls([]);
    callbacks.setStreamingTask(
      prompt.length > 60 ? `${prompt.slice(0, 57)}...` : prompt,
    );
    callbacks.setLoadingStartTime(Date.now());

    const start = Date.now();

    try {
      let currentTask = this.buildTask(
        prompt,
        state,
        targetSlot,
        references,
        systemPromptOverride,
      );
      const MAX_DISPATCH_DEPTH = 10; // Safety circuit breaker, not an architectural limit

      // Fire beforeDispatch hook
      this.hookEmitter
        ?.emit("beforeDispatch", { prompt, slot: targetSlot ?? "main" })
        .catch(() => {});
      let totalAccumulated = "";
      let totalTokens = 0;
      let round = 0;
      const allActivities: SlotActivity[] = [];

      // Expose system prompt for streaming sub-items display
      callbacks.setStreamingSystemPrompt(currentTask.systemPrompt ?? "");

      // Live tool call tracking (persists across dispatch rounds)
      const liveToolCalls: ToolCallEntry[] = [
        { name: "System prompt loaded", status: "done" },
      ];
      callbacks.setStreamingToolCalls([...liveToolCalls]);

      while (!signal.aborted) {
        let accumulated = "";
        let thinkingAccumulated = "";
        let detectedSlot: SlotName = "main";
        let tokenUsage: { input: number; output: number } | undefined;
        let thinkingStarted = false;
        let thinkingIdx = -1;

        for await (const chunk of this.kernel.dispatchStreaming(
          currentTask,
          signal,
        )) {
          detectedSlot = chunk.slot;
          callbacks.setActiveSlot(chunk.slot);

          if (chunk.type === "thinking") {
            if (!thinkingStarted) {
              thinkingStarted = true;
              thinkingIdx = liveToolCalls.length;
              liveToolCalls.push({ name: "Thinking...", status: "running" });
              callbacks.setStreamingToolCalls([...liveToolCalls]);
            }
            thinkingAccumulated += chunk.content;
            callbacks.setStreamingThinking(thinkingAccumulated);
          } else if (chunk.type === "text") {
            // Mark thinking complete on first text chunk
            if (
              thinkingStarted &&
              thinkingIdx >= 0 &&
              liveToolCalls[thinkingIdx]?.status === "running"
            ) {
              liveToolCalls[thinkingIdx] = {
                name: "Thinking (complete)",
                status: "done",
              };
              callbacks.setStreamingToolCalls([...liveToolCalls]);
            }
            accumulated += chunk.content;
            callbacks.setStreamingContent(
              stripDispatchTags(totalAccumulated + accumulated),
            );
          } else if (chunk.type === "tool_use" && chunk.toolCall) {
            if (chunk.toolCall.status === "start") {
              liveToolCalls.push({
                name: chunk.toolCall.name,
                status: "running",
              });
              this.hookEmitter
                ?.emit("onToolCall", { tool: chunk.toolCall.name })
                .catch(() => {});
            } else if (chunk.toolCall.status === "end") {
              const idx = liveToolCalls.findIndex(
                (tc) =>
                  tc.name === chunk.toolCall?.name && tc.status === "running",
              );
              if (idx >= 0) {
                liveToolCalls[idx] = {
                  name: chunk.toolCall.name,
                  status: "done",
                };
              }
              this.hookEmitter
                ?.emit("onToolResult", { tool: chunk.toolCall.name })
                .catch(() => {});
            }
            callbacks.setStreamingToolCalls([...liveToolCalls]);
          } else if (chunk.type === "error") {
            accumulated += `\n**Error:** ${chunk.content}`;
            callbacks.setStreamingContent(
              stripDispatchTags(totalAccumulated + accumulated),
            );
          } else if (chunk.type === "done" && chunk.tokens) {
            tokenUsage = chunk.tokens;
          }
        }

        const roundTokens = tokenUsage
          ? tokenUsage.input + tokenUsage.output
          : 0;
        totalTokens += roundTokens;

        const roundDuration = Date.now() - start;
        allActivities.push({
          slot: detectedSlot,
          task: prompt.length > 60 ? `${prompt.slice(0, 57)}...` : prompt,
          status: "done",
          duration: roundDuration,
          ...(roundTokens > 0 ? { tokens: roundTokens } : {}),
          ...(liveToolCalls.length > 1
            ? { toolCalls: [...liveToolCalls] }
            : {}),
        });

        // Natural exit: no dispatch tags means Cortex is done
        if (!hasDispatchTags(accumulated)) {
          totalAccumulated += accumulated;
          break;
        }

        // Safety circuit breaker — not an architectural limit
        round++;
        if (round >= MAX_DISPATCH_DEPTH) {
          console.warn(
            `[dispatch] Hit safety circuit breaker at ${MAX_DISPATCH_DEPTH} rounds — breaking`,
          );
          totalAccumulated += stripDispatchTags(accumulated);
          break;
        }

        // Parse and execute dispatch tags
        const tags = parseDispatchTags(accumulated);
        totalAccumulated += stripDispatchTags(accumulated);

        // Show dispatch activity in tool calls
        for (const tag of tags) {
          const label =
            tag.slot === "explore"
              ? `Scout: ${tag.content.slice(0, 40)}...`
              : `Forge: ${tag.content.slice(0, 40)}...`;
          liveToolCalls.push({ name: label, status: "running" });
        }
        callbacks.setStreamingToolCalls([...liveToolCalls]);
        callbacks.setStreamingTask("Dispatching to threads...");

        // Execute all dispatch tags in parallel via kernel
        const dispatchEpisodes = await this.executeDispatchTags(
          tags,
          currentTask,
          signal,
        );

        // Mark dispatch tool calls as done
        for (let i = 0; i < tags.length; i++) {
          const tcIdx = liveToolCalls.length - tags.length + i;
          if (tcIdx >= 0 && liveToolCalls[tcIdx]) {
            liveToolCalls[tcIdx] = {
              ...liveToolCalls[tcIdx],
              status: "done",
            };
          }
        }
        callbacks.setStreamingToolCalls([...liveToolCalls]);

        // Build continuation prompt with dispatch results
        const resultsContext = formatEpisodeInputs(dispatchEpisodes);
        const continuationPrompt = `${resultsContext}\n\nThe dispatched threads above have completed. Review their results, then either synthesize a final response or dispatch follow-up work if needed.`;

        // Build next task for the continuation round
        currentTask = {
          ...currentTask,
          id: crypto.randomUUID?.() ?? `${Date.now()}`,
          prompt: continuationPrompt,
          history: [
            ...(currentTask.history ?? []),
            { role: "user" as const, content: currentTask.prompt },
            { role: "assistant" as const, content: accumulated },
          ],
        };

        callbacks.setStreamingTask("Synthesizing results...");
      }

      const duration = Date.now() - start;
      const toolCallCount = liveToolCalls.filter(
        (tc) =>
          tc.name !== "System prompt loaded" && !tc.name.startsWith("Thinking"),
      ).length;
      const finalContent =
        stripDispatchTags(totalAccumulated) ||
        (toolCallCount > 0
          ? `*Completed ${toolCallCount} tool call${toolCallCount !== 1 ? "s" : ""} with no text response.*`
          : "(empty response)");

      callbacks.addMessage({
        id: currentTask.id,
        role: "assistant",
        content: finalContent,
        slot: allActivities[0]?.slot ?? "main",
        timestamp: Date.now(),
        activities: allActivities.length > 0 ? allActivities : undefined,
        duration,
        ...(totalTokens > 0 ? { tokens: totalTokens } : {}),
      });

      callbacks.updateStats(duration, totalTokens);

      // Fire afterDispatch hook
      this.hookEmitter
        ?.emit("afterDispatch", {
          prompt,
          slot: targetSlot ?? "main",
          duration,
          tokens: totalTokens,
        })
        .catch(() => {});
    } catch (err) {
      if (signal.aborted) {
        const partial = callbacks.getStreamingContent();
        if (partial) {
          const duration = Date.now() - start;
          callbacks.addMessage({
            id: `cancelled-${Date.now()}`,
            role: "assistant",
            content: `${partial}\n\n*— interrupted —*`,
            slot: callbacks.getActiveSlot(),
            timestamp: Date.now(),
            duration,
            activities: [
              {
                slot: callbacks.getActiveSlot(),
                task: prompt.length > 60 ? `${prompt.slice(0, 57)}...` : prompt,
                status: "interrupted",
                duration,
              },
            ],
          });
        }
      } else {
        const errorMsg = err instanceof Error ? err.message : String(err);
        callbacks.addMessage({
          id: `err-${Date.now()}`,
          role: "assistant",
          content: `**Error:** ${errorMsg}`,
          slot: "main" as SlotName,
          timestamp: Date.now(),
        });
        this.hookEmitter
          ?.emit("onError", { error: errorMsg, prompt })
          .catch(() => {});
      }
    } finally {
      callbacks.setIsLoading(false);
      callbacks.setStreamingContent("");
      callbacks.setStreamingThinking("");
      callbacks.setStreamingSystemPrompt("");
      callbacks.setStreamingToolCalls([]);
      callbacks.setStreamingTask("");
      this.currentAbort = null;
    }
  }

  /**
   * Dispatch a task with full streaming UI, returning an EpisodeSummary.
   * Used by PlanExecutor/RalphLoop to route through streaming infrastructure.
   */
  async dispatchForTask(
    task: OrchestratorTask,
    state: DispatchState,
    callbacks: DispatchCallbacks,
  ): Promise<EpisodeSummary> {
    if (!this.kernel) {
      this.kernel = new OrchestratorKernel(
        new ModelSlotManager(state.slots),
        this.cliPermissions,
      );
    }

    this.cancel();
    this.currentAbort = new AbortController();
    const signal = this.currentAbort.signal;

    callbacks.setIsLoading(true);
    callbacks.setActiveSlot(task.targetSlot ?? "main");
    callbacks.setStreamingContent("");
    callbacks.setStreamingThinking("");
    callbacks.setStreamingToolCalls([]);
    const taskLabel =
      task.prompt.length > 60 ? `${task.prompt.slice(0, 57)}...` : task.prompt;
    callbacks.setStreamingTask(taskLabel);
    callbacks.setLoadingStartTime(Date.now());
    callbacks.setStreamingSystemPrompt(task.systemPrompt ?? "");

    const start = Date.now();
    let accumulated = "";
    let totalTokens = 0;
    let detectedSlot: SlotName = task.targetSlot ?? "main";
    let filesModified: string[] = [];

    const liveToolCalls: ToolCallEntry[] = [
      { name: "System prompt loaded", status: "done" },
    ];
    callbacks.setStreamingToolCalls([...liveToolCalls]);

    try {
      let thinkingAccumulated = "";
      let thinkingStarted = false;
      let thinkingIdx = -1;

      for await (const chunk of this.kernel.dispatchStreaming(task, signal)) {
        detectedSlot = chunk.slot;
        callbacks.setActiveSlot(chunk.slot);

        if (chunk.type === "thinking") {
          if (!thinkingStarted) {
            thinkingStarted = true;
            thinkingIdx = liveToolCalls.length;
            liveToolCalls.push({ name: "Thinking...", status: "running" });
            callbacks.setStreamingToolCalls([...liveToolCalls]);
          }
          thinkingAccumulated += chunk.content;
          callbacks.setStreamingThinking(thinkingAccumulated);
        } else if (chunk.type === "text") {
          if (
            thinkingStarted &&
            thinkingIdx >= 0 &&
            liveToolCalls[thinkingIdx]?.status === "running"
          ) {
            liveToolCalls[thinkingIdx] = {
              name: "Thinking (complete)",
              status: "done",
            };
            callbacks.setStreamingToolCalls([...liveToolCalls]);
          }
          accumulated += chunk.content;
          callbacks.setStreamingContent(stripDispatchTags(accumulated));
        } else if (chunk.type === "tool_use" && chunk.toolCall) {
          if (chunk.toolCall.status === "start") {
            liveToolCalls.push({
              name: chunk.toolCall.name,
              status: "running",
            });
          } else if (chunk.toolCall.status === "end") {
            const idx = liveToolCalls.findIndex(
              (tc) =>
                tc.name === chunk.toolCall?.name && tc.status === "running",
            );
            if (idx >= 0) {
              liveToolCalls[idx] = {
                name: chunk.toolCall.name,
                status: "done",
              };
            }
          }
          callbacks.setStreamingToolCalls([...liveToolCalls]);
        } else if (chunk.type === "error") {
          accumulated += `\n**Error:** ${chunk.content}`;
          callbacks.setStreamingContent(stripDispatchTags(accumulated));
        } else if (chunk.type === "done" && chunk.tokens) {
          totalTokens = chunk.tokens.input + chunk.tokens.output;
        }
      }

      const duration = Date.now() - start;
      const content = stripDispatchTags(accumulated);

      // Extract file tool calls as proxy for filesModified
      filesModified = liveToolCalls
        .filter(
          (tc) =>
            tc.status === "done" &&
            (tc.name.startsWith("write_") ||
              tc.name.startsWith("edit_") ||
              tc.name === "write_file" ||
              tc.name === "edit_file"),
        )
        .map((tc) => tc.name);

      callbacks.updateStats(duration, totalTokens);

      return createEpisodeSummary({
        slot: detectedSlot,
        task: task.prompt,
        result: content,
        tokens: totalTokens,
        duration,
        filesModified,
        status: "success",
      });
    } catch (err) {
      const duration = Date.now() - start;
      const errorMsg = err instanceof Error ? err.message : String(err);

      return createEpisodeSummary({
        slot: detectedSlot,
        task: task.prompt,
        result: errorMsg,
        tokens: totalTokens,
        duration,
        filesModified: [],
        status: "failure",
      });
    } finally {
      callbacks.setIsLoading(false);
      callbacks.setStreamingContent("");
      callbacks.setStreamingThinking("");
      callbacks.setStreamingSystemPrompt("");
      callbacks.setStreamingToolCalls([]);
      callbacks.setStreamingTask("");
      this.currentAbort = null;
    }
  }

  async injectAndContinue(
    injection: string,
    state: DispatchState,
    callbacks: DispatchCallbacks,
  ): Promise<void> {
    // 1. Abort current stream
    this.cancel();

    // 2. Save partial response as assistant message with pause marker
    const partial = callbacks.getStreamingContent();
    if (partial) {
      callbacks.addMessage({
        id: `paused-${Date.now()}`,
        role: "assistant",
        content: `${partial}\n\n*— paused for context —*`,
        slot: callbacks.getActiveSlot(),
        timestamp: Date.now(),
      });
    }

    // 3. Reset streaming state
    callbacks.setStreamingContent("");
    callbacks.setIsLoading(false);

    // 4. Re-dispatch with continuation prompt
    const continuationPrompt = `Continue. Additional context from user: ${injection}`;
    await this.dispatch(continuationPrompt, state, callbacks);
  }

  /**
   * Execute parsed dispatch tags in parallel via the kernel.
   * Each tag becomes a dispatch to the specified slot (Scout or Forge).
   * Returns the resulting episodes for thread composition.
   */
  private async executeDispatchTags(
    tags: Array<{ slot: "explore" | "execute"; content: string }>,
    parentTask: OrchestratorTask,
    _signal?: AbortSignal,
  ): Promise<EpisodeSummary[]> {
    if (!this.kernel || tags.length === 0) return [];

    const tasks: OrchestratorTask[] = tags.map((tag) => ({
      id: crypto.randomUUID?.() ?? `${Date.now()}-${tag.slot}`,
      prompt: tag.content,
      targetSlot: tag.slot,
      systemPrompt: parentTask.systemPrompt,
    }));

    const results = await Promise.allSettled(
      tasks.map((t) => this.kernel?.dispatch(t)),
    );

    return results
      .filter(
        (r): r is PromiseFulfilledResult<EpisodeSummary> =>
          r.status === "fulfilled",
      )
      .map((r) => r.value);
  }

  private buildTask(
    prompt: string,
    state: DispatchState,
    targetSlot?: SlotName,
    references?: ResolvedReference[],
    systemPromptOverride?: string,
  ): OrchestratorTask {
    const effectiveSlot = targetSlot ?? "main";
    const slotConfig = state.slots[effectiveSlot] ?? state.slots.main;
    const episodeContext = this.kernel
      ? buildEpisodeContext([...this.kernel.getEpisodes()])
      : "";

    // RLM context compaction
    // Exclude the last message if it's the current user prompt — the bridge
    // always appends the prompt as the final user message, so including it
    // in history would send it twice and break role alternation.
    const msgs = state.messages;
    const historyEnd =
      msgs.length > 0 && msgs[msgs.length - 1].role === "user"
        ? msgs.length - 1
        : msgs.length;
    const allMessages = msgs.slice(0, historyEnd).map((m) => ({
      role: m.role as "user" | "assistant",
      content: m.content,
      originalId: m.id,
    }));
    const contextWindow = buildContextWindow(allMessages, prompt);

    // Build system prompt with compacted archive context + project context
    const systemParts: string[] = [];
    if (systemPromptOverride) {
      systemParts.push(systemPromptOverride, "---");
    }
    systemParts.push(
      buildSystemPrompt(effectiveSlot, slotConfig.systemPrompt, {
        rules: this.context.rules,
        repoMap: this.context.repoMap,
        lessons: this.context.lessons,
        qualityGates: this.context.qualityGates,
      }),
    );
    if (contextWindow.systemParts.length > 0) {
      systemParts.push(...contextWindow.systemParts);
    }
    if (episodeContext) {
      systemParts.push(episodeContext);
    }

    // Inject MCP tool descriptions if available
    if (this.mcpManager) {
      const toolDescriptions = this.mcpManager.getToolDescriptions();
      if (toolDescriptions) {
        systemParts.push(toolDescriptions);
      }
    }

    // Inject @ referenced context (non-image references go in system prompt)
    if (references && references.length > 0) {
      const textRefs = references.filter((r) => r.type !== "image");
      if (textRefs.length > 0) {
        const refContent = textRefs.map((r) => r.content).join("\n\n");
        systemParts.push(`## Referenced Context\n${refContent}`);
      }
      // Image references: note them in the system prompt (actual image data
      // is handled by the bridge layer when multimodal content is supported)
      const imageRefs = references.filter((r) => r.type === "image");
      if (imageRefs.length > 0) {
        const imageDescs = imageRefs.map((r) => r.content).join("\n");
        systemParts.push(`## Attached Images\n${imageDescs}`);
      }
    }

    // Inject context directories
    const dirs = state.contextDirectories;
    if (dirs && dirs.length > 0) {
      systemParts.push(
        `## Additional Context Directories\n${dirs.map((d) => `- ${d}`).join("\n")}`,
      );
    }

    const detectedToolType = targetSlot ? undefined : detectToolType(prompt);

    return {
      id: crypto.randomUUID?.() ?? `${Date.now()}`,
      prompt,
      targetSlot: effectiveSlot,
      toolType: detectedToolType ?? undefined,
      systemPrompt: systemParts.join("\n\n"),
      history: contextWindow.messages,
    };
  }
}
