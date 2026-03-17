/**
 * Dispatch orchestration — extracted from slate-context to keep it focused.
 *
 * Handles: context compaction (RLM), system prompt building, streaming,
 * episode readback, tool routing heuristics, MCP injection, and abort handling.
 */
import type {
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
  detectToolType,
  ModelSlotManager,
  OrchestratorKernel,
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
}

export class DispatchController {
  private kernel: OrchestratorKernel | null = null;
  private currentAbort: AbortController | null = null;
  private mcpManager: MCPManager | null;
  private context: DispatchContext = {};

  constructor(slots?: SlotAssignment, mcpManager?: MCPManager) {
    if (slots) {
      this.kernel = new OrchestratorKernel(new ModelSlotManager(slots));
    }
    this.mcpManager = mcpManager ?? null;
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
      this.kernel = new OrchestratorKernel(new ModelSlotManager(slots));
    }
    return this.kernel;
  }

  async dispatch(
    prompt: string,
    state: DispatchState,
    callbacks: DispatchCallbacks,
    targetSlot?: SlotName,
    references?: ResolvedReference[],
  ): Promise<void> {
    if (!this.kernel) {
      this.kernel = new OrchestratorKernel(new ModelSlotManager(state.slots));
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
      prompt.length > 60 ? prompt.slice(0, 57) + "..." : prompt,
    );
    callbacks.setLoadingStartTime(Date.now());

    const start = Date.now();

    try {
      const task = this.buildTask(prompt, state, targetSlot, references);

      // Expose system prompt for streaming sub-items display
      callbacks.setStreamingSystemPrompt(task.systemPrompt ?? "");

      let accumulated = "";
      let thinkingAccumulated = "";
      let detectedSlot: SlotName = "main";
      let tokenUsage: { input: number; output: number } | undefined;

      // Live tool call tracking
      const liveToolCalls: ToolCallEntry[] = [
        { name: "System prompt loaded", status: "done" },
      ];
      callbacks.setStreamingToolCalls([...liveToolCalls]);
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
          callbacks.setStreamingContent(accumulated);
        } else if (chunk.type === "tool_use" && chunk.toolCall) {
          if (chunk.toolCall.status === "start") {
            liveToolCalls.push({
              name: chunk.toolCall.name,
              status: "running",
            });
          } else if (chunk.toolCall.status === "end") {
            const idx = liveToolCalls.findIndex(
              (tc) =>
                tc.name === chunk.toolCall!.name && tc.status === "running",
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
          callbacks.setStreamingContent(accumulated);
        } else if (chunk.type === "done" && chunk.tokens) {
          tokenUsage = chunk.tokens;
        }
      }

      const duration = Date.now() - start;
      const toolCallCount = liveToolCalls.filter(
        (tc) =>
          tc.name !== "System prompt loaded" && !tc.name.startsWith("Thinking"),
      ).length;
      const finalContent =
        accumulated ||
        (toolCallCount > 0
          ? `*Completed ${toolCallCount} tool call${toolCallCount !== 1 ? "s" : ""} with no text response.*`
          : "(empty response)");
      const totalMsgTokens = tokenUsage
        ? tokenUsage.input + tokenUsage.output
        : 0;

      const activity: SlotActivity = {
        slot: detectedSlot,
        task: prompt.length > 60 ? prompt.slice(0, 57) + "..." : prompt,
        status: "done",
        duration,
        ...(totalMsgTokens > 0 ? { tokens: totalMsgTokens } : {}),
        ...(liveToolCalls.length > 1 ? { toolCalls: liveToolCalls } : {}),
      };

      callbacks.addMessage({
        id: task.id,
        role: "assistant",
        content: finalContent,
        slot: detectedSlot,
        timestamp: Date.now(),
        activities: [activity],
        duration,
        ...(totalMsgTokens > 0 ? { tokens: totalMsgTokens } : {}),
      });

      callbacks.updateStats(duration, totalMsgTokens);
    } catch (err) {
      if (signal.aborted) {
        const partial = callbacks.getStreamingContent();
        if (partial) {
          const duration = Date.now() - start;
          callbacks.addMessage({
            id: `cancelled-${Date.now()}`,
            role: "assistant",
            content: partial + "\n\n*— interrupted —*",
            slot: callbacks.getActiveSlot(),
            timestamp: Date.now(),
            duration,
            activities: [
              {
                slot: callbacks.getActiveSlot(),
                task: prompt.length > 60 ? prompt.slice(0, 57) + "..." : prompt,
                status: "interrupted",
                duration,
              },
            ],
          });
        }
      } else {
        callbacks.addMessage({
          id: `err-${Date.now()}`,
          role: "assistant",
          content: `**Error:** ${err instanceof Error ? err.message : String(err)}`,
          slot: "main" as SlotName,
          timestamp: Date.now(),
        });
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
        content: partial + "\n\n*— paused for context —*",
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

  private buildTask(
    prompt: string,
    state: DispatchState,
    targetSlot?: SlotName,
    references?: ResolvedReference[],
  ): OrchestratorTask {
    const effectiveSlot = targetSlot ?? "main";
    const slotConfig = state.slots[effectiveSlot] ?? state.slots.main;
    const episodeContext = this.kernel
      ? buildEpisodeContext([...this.kernel.getEpisodes()])
      : "";

    // RLM context compaction
    const allMessages = state.messages.map((m) => ({
      role: m.role as "user" | "assistant",
      content: m.content,
      originalId: m.id,
    }));
    const contextWindow = buildContextWindow(allMessages, prompt);

    // Build system prompt with compacted archive context + project context
    const systemParts = [
      buildSystemPrompt(effectiveSlot, slotConfig.systemPrompt, {
        rules: this.context.rules,
        repoMap: this.context.repoMap,
        lessons: this.context.lessons,
      }),
    ];
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
