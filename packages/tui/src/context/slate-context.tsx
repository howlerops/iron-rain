import { statSync } from "node:fs";
import { isAbsolute, resolve } from "node:path";
import type {
  Checkpoint,
  IronRainConfig,
  LoopState,
  Plan,
  ResolvedReference,
  Skill,
  SlotAssignment,
  SlotName,
} from "@howlerops/iron-rain";
import {
  CheckpointManager,
  CostRegistry,
  DEFAULT_SLOT_ASSIGNMENT,
  generateRepoMap,
  HookEmitter,
  loadIgnoreRules,
  loadProjectRules,
  MCPManager,
  SkillRegistry,
} from "@howlerops/iron-rain";
import { createContext, createSignal, type JSX, useContext } from "solid-js";
import { createStore } from "solid-js/store";
import type { Message, SessionStats, ToolCallEntry } from "../components/session-view.js";
import type { SlashCommand } from "../components/slash-menu.js";
import {
  NullSessionDB,
  SessionDB,
  type SessionRecord,
} from "../store/session-db.js";
import { DispatchController } from "./dispatch.js";

export interface SlateState {
  messages: Message[];
  slots: SlotAssignment;
  sessionStats: SessionStats;
  contextDirectories: string[];
}

export interface SlateActions {
  currentSessionId: () => string;
  listSessions: (limit?: number) => SessionRecord[];
  resumeSession: (sessionId: string) => void;
  newSession: () => void;
  addMessage: (msg: Message) => void;
  clearMessages: () => void;
  updateSlots: (slots: Partial<SlotAssignment>) => void;
  isLoading: () => boolean;
  setLoading: (loading: boolean) => void;
  activeSlot: () => SlotName;
  setActiveSlot: (slot: SlotName) => void;
  streamingContent: () => string;
  streamingThinking: () => string;
  streamingSystemPrompt: () => string;
  streamingToolCalls: () => ToolCallEntry[];
  streamingTask: () => string;
  loadingStartTime: () => number;
  dispatch: (
    prompt: string,
    targetSlot?: SlotName,
    references?: ResolvedReference[],
  ) => Promise<void>;
  cancelDispatch: () => void;
  injectContext: (text: string) => Promise<void>;

  // Context directories
  addContextDirectory: (path: string) => string | null;
  removeContextDirectory: (path: string) => boolean;
  contextDirectories: () => string[];

  // Plan state
  activePlan: () => Plan | null;
  setActivePlan: (plan: Plan | null) => void;

  // Loop state
  activeLoop: () => LoopState | null;
  setActiveLoop: (loop: LoopState | null) => void;

  // MCP
  mcpManager: () => MCPManager;

  // Skills
  skillRegistry: () => SkillRegistry;
  skillCommands: () => SlashCommand[];

  // Checkpoint/Undo
  createCheckpoint: (label: string) => string | undefined;
  undo: () => { success: boolean; label?: string };
  listCheckpoints: () => readonly Checkpoint[];

  // Cost tracking
  costRegistry: () => CostRegistry;

  // Hooks
  hookEmitter: () => HookEmitter;

  // Dispatch controller (for planner/loop)
  getDispatcher: () => DispatchController;
}

type SlateContextValue = [SlateState, SlateActions];

const SlateContext = createContext<SlateContextValue>();

// Direct access to the DB for slash commands (lessons, etc.)
let _globalDB: SessionDB | NullSessionDB | null = null;

export function getSessionDB(): SessionDB | NullSessionDB | null {
  return _globalDB;
}

export function SlateProvider(props: {
  config?: IronRainConfig;
  children: JSX.Element;
}) {
  const slotAssignment =
    (props.config?.slots as SlotAssignment) ?? DEFAULT_SLOT_ASSIGNMENT;

  // Initialize persistent storage
  let db: SessionDB | NullSessionDB;
  try {
    db = new SessionDB();
  } catch {
    db = new NullSessionDB();
  }

  const sessionId = crypto.randomUUID?.() ?? `${Date.now()}`;
  const model = slotAssignment.main?.model ?? "unknown";
  db.createSession(sessionId, model);
  _globalDB = db;

  // UI state signals
  const [isLoading, setIsLoading] = createSignal(false);
  const [activeSlot, setActiveSlot] = createSignal<SlotName>("main");
  const [currentSession, setCurrentSession] = createSignal<string>(sessionId);
  const [streamingContent, setStreamingContent] = createSignal("");
  const [streamingThinking, setStreamingThinking] = createSignal("");
  const [streamingSystemPrompt, setStreamingSystemPrompt] = createSignal("");
  const [streamingToolCalls, setStreamingToolCalls] = createSignal<ToolCallEntry[]>([]);
  const [streamingTask, setStreamingTask] = createSignal("");
  const [loadingStartTime, setLoadingStartTime] = createSignal(0);

  // Plan & Loop state
  const [activePlan, setActivePlan] = createSignal<Plan | null>(null);
  const [activeLoop, setActiveLoop] = createSignal<LoopState | null>(null);

  // MCP Manager
  const mcpMgr = new MCPManager({ mcpServers: props.config?.mcpServers });

  // Skill Registry
  const skillReg = new SkillRegistry();
  const skillPaths = props.config?.skills?.paths;
  if (props.config?.skills?.autoDiscover !== false) {
    skillReg.discover(skillPaths);
  }

  // Skill-derived slash commands
  const [skillCmds, setSkillCmds] = createSignal<SlashCommand[]>(
    skillReg
      .getCommands()
      .map((c) => ({ name: c.name, description: c.description })),
  );

  // Initialize MCP connections (non-blocking)
  mcpMgr.connectAll().catch(() => {});

  // Load project rules and repo map (Phase 1-2 integration)
  const cwd = process.cwd();
  const rules = props.config?.rules?.disabled ? [] : loadProjectRules(cwd);
  const ignoreFilter = loadIgnoreRules(cwd);
  const repoMap = props.config?.repoMap?.enabled !== false
    ? generateRepoMap(cwd, ignoreFilter, props.config?.repoMap?.maxTokens)
    : "";

  // Initialize checkpoint manager (Phase 1 T-01)
  const checkpointMgr = new CheckpointManager();

  // Initialize cost registry (Phase 2 I-04)
  const costReg = new CostRegistry();
  costReg.loadFromConfig(props.config?.costs as Record<string, { input: number; output: number }> | undefined);

  // Initialize hook emitter (Phase 2 I-05)
  const hooks = new HookEmitter();
  hooks.emit("onSessionStart", { sessionId }).catch(() => {});

  // Store for complex nested state
  const [state, setState] = createStore<SlateState>({
    messages: [],
    slots: slotAssignment,
    sessionStats: {
      totalDuration: 0,
      totalTokens: 0,
      modelCount: 1,
      requestCount: 0,
    },
    contextDirectories: [],
  });

  // Dispatch controller handles all orchestration logic
  const dispatcher = new DispatchController(
    props.config?.slots ? slotAssignment : undefined,
    mcpMgr,
  );

  // Wire project context into dispatch system prompts
  dispatcher.setContext({ rules, repoMap });

  const actions: SlateActions = {
    currentSessionId: () => currentSession(),

    listSessions(limit = 20) {
      return db.listSessions(limit);
    },

    resumeSession(sid: string) {
      setCurrentSession(sid);
      setState("messages", db.getMessages(sid));
      setState("sessionStats", db.getSessionStats(sid));
    },

    newSession() {
      const newId = crypto.randomUUID?.() ?? `${Date.now()}`;
      db.createSession(newId, state.slots.main?.model ?? "unknown");
      setCurrentSession(newId);
      setState("messages", []);
      setState("sessionStats", {
        totalDuration: 0,
        totalTokens: 0,
        modelCount: 1,
        requestCount: 0,
      });
    },

    addMessage(msg) {
      const sortOrder = state.messages.length;
      setState("messages", (prev) => [...prev, msg]);
      try {
        db.addMessage(currentSession(), msg, sortOrder);
      } catch {
        // Non-fatal: UI still works without persistence
      }
    },

    clearMessages() {
      setState("messages", []);
      setState("sessionStats", {
        totalDuration: 0,
        totalTokens: 0,
        modelCount: 1,
        requestCount: 0,
      });
      try {
        db.clearMessages(currentSession());
      } catch {
        /* non-fatal */
      }
    },

    isLoading,
    setLoading: setIsLoading,
    activeSlot,
    setActiveSlot,
    streamingContent,
    streamingThinking,
    streamingSystemPrompt,
    streamingToolCalls,
    streamingTask,
    loadingStartTime,

    activePlan,
    setActivePlan,
    activeLoop,
    setActiveLoop,

    mcpManager: () => mcpMgr,
    skillRegistry: () => skillReg,
    skillCommands: () => skillCmds(),

    createCheckpoint(label: string) {
      return checkpointMgr.createCheckpoint(label);
    },

    undo() {
      return checkpointMgr.restoreLastCheckpoint();
    },

    listCheckpoints() {
      return checkpointMgr.listCheckpoints();
    },

    costRegistry: () => costReg,
    hookEmitter: () => hooks,

    getDispatcher: () => dispatcher,

    updateSlots(slots) {
      setState("slots", (prev) => ({ ...prev, ...slots }));
    },

    cancelDispatch() {
      dispatcher.cancel();
    },

    async dispatch(
      prompt: string,
      targetSlot?: SlotName,
      references?: ResolvedReference[],
    ) {
      await dispatcher.dispatch(
        prompt,
        state,
        {
          setIsLoading,
          setActiveSlot,
          setStreamingContent,
          setStreamingThinking,
          setStreamingSystemPrompt,
          setStreamingToolCalls,
          setStreamingTask,
          setLoadingStartTime,
          getStreamingContent: streamingContent,
          getActiveSlot: activeSlot,
          addMessage: actions.addMessage,
          updateStats: (duration, tokens) => {
            setState("sessionStats", (prev) => ({
              totalDuration: prev.totalDuration + duration,
              totalTokens: prev.totalTokens + tokens,
              modelCount: prev.modelCount,
              requestCount: prev.requestCount + 1,
            }));
          },
        },
        targetSlot,
        references,
      );
    },

    async injectContext(text: string) {
      await dispatcher.injectAndContinue(text, state, {
        setIsLoading,
        setActiveSlot,
        setStreamingContent,
        setStreamingThinking,
        setStreamingSystemPrompt,
        setStreamingToolCalls,
        setStreamingTask,
        setLoadingStartTime,
        getStreamingContent: streamingContent,
        getActiveSlot: activeSlot,
        addMessage: actions.addMessage,
        updateStats: (duration, tokens) => {
          setState("sessionStats", (prev) => ({
            totalDuration: prev.totalDuration + duration,
            totalTokens: prev.totalTokens + tokens,
            modelCount: prev.modelCount,
            requestCount: prev.requestCount + 1,
          }));
        },
      });
    },

    addContextDirectory(dirPath: string): string | null {
      const abs = isAbsolute(dirPath)
        ? dirPath
        : resolve(process.cwd(), dirPath);
      try {
        const s = statSync(abs);
        if (!s.isDirectory()) return `Not a directory: ${abs}`;
      } catch {
        return `Directory not found: ${abs}`;
      }
      if (state.contextDirectories.includes(abs))
        return `Already added: ${abs}`;
      setState("contextDirectories", (prev) => [...prev, abs]);
      return null;
    },

    removeContextDirectory(dirPath: string): boolean {
      const abs = isAbsolute(dirPath)
        ? dirPath
        : resolve(process.cwd(), dirPath);
      const idx = state.contextDirectories.indexOf(abs);
      if (idx === -1) return false;
      setState("contextDirectories", (prev) =>
        prev.filter((_, i) => i !== idx),
      );
      return true;
    },

    contextDirectories() {
      return state.contextDirectories;
    },
  };

  return (
    <SlateContext.Provider value={[state, actions]}>
      {props.children}
    </SlateContext.Provider>
  );
}

export function useSlate(): SlateContextValue {
  const ctx = useContext(SlateContext);
  if (!ctx) throw new Error("useSlate must be used within SlateProvider");
  return ctx;
}
