import type { Accessor, Setter } from "solid-js";
import type { SlashCommand } from "../components/slash-menu.js";
import type { SlateActions, SlateState } from "../context/slate-context.js";

export type SessionMode =
  | "chat"
  | "settings"
  | "plan-generating"
  | "plan-review"
  | "plan-executing"
  | "loop-running"
  | "skills-browse"
  | "mcp-status";

export type AddSystemMessage = (content: string) => void;

export interface SessionContext {
  state: SlateState;
  actions: SlateActions;
  setMode: Setter<SessionMode>;
  addSystemMessage: AddSystemMessage;
  version?: string;
  onQuit?: () => void;
  skillCommands: Accessor<SlashCommand[]>;
}

export type SessionControllerContext = SessionContext;
