import { createMemo, For } from "solid-js";
import { ironRainTheme } from "../theme/theme.js";

export interface SlashCommand {
  name: string;
  description: string;
  action?: () => void;
}

export const SLASH_COMMANDS: SlashCommand[] = [
  { name: "/help", description: "Show available commands" },
  { name: "/new", description: "Start a new session" },
  { name: "/clear", description: "Clear message history" },
  { name: "/settings", description: "Open settings panel" },
  { name: "/plan", description: "Create a plan from a description" },
  { name: "/plans", description: "List saved plans" },
  { name: "/resume", description: "Resume a paused plan" },
  { name: "/loop", description: "Run a loop until a condition is met" },
  { name: "/loop-status", description: "Show current loop progress" },
  { name: "/loop-pause", description: "Pause the active loop" },
  { name: "/loop-resume", description: "Resume a paused loop" },
  { name: "/skills", description: "Browse available skills" },
  { name: "/mcp", description: "Show MCP server status" },
  { name: "/context", description: "Manage context directories" },
  { name: "/lessons", description: "Show learned lessons" },
  { name: "/init", description: "Analyze project and extract best practices" },
  { name: "/undo", description: "Undo last checkpoint" },
  { name: "/review", description: "Review code changes" },
  { name: "/model", description: "Show model assignments" },
  { name: "/permissions", description: "Toggle CLI auto-permissions" },
  { name: "/slot", description: "Show or set active slot" },
  { name: "/stats", description: "Show session statistics" },
  { name: "/version", description: "Show version and system info" },
  { name: "/update", description: "Check for and install updates" },
  { name: "/doctor", description: "Run system diagnostics" },
  { name: "/commit", description: "Stage, commit with AI message, and push" },
  { name: "/diff", description: "Show current git diff" },
  { name: "/branch", description: "Show or switch git branches" },
  { name: "/test", description: "Run project tests" },
  { name: "/copy", description: "Copy last response to clipboard" },
  { name: "/quit", description: "Exit iron-rain" },
];

export interface SlashMenuProps {
  filter: string;
  selectedIndex: number;
  onSelect: (command: SlashCommand) => void;
  extraCommands?: SlashCommand[];
}

export function SlashMenu(props: SlashMenuProps) {
  const allCommands = createMemo(() => {
    const extra = props.extraCommands ?? [];
    return [...SLASH_COMMANDS, ...extra];
  });

  const filtered = createMemo(() => {
    const q = props.filter.toLowerCase();
    return allCommands().filter((cmd) => cmd.name.startsWith(q));
  });

  // Pad command names to align descriptions
  const COL_WIDTH = 16;

  return (
    <box
      flexDirection="column"
      paddingX={2}
      paddingY={1}
      marginBottom={0}
      backgroundColor={ironRainTheme.chrome.bg}
      width="100%"
    >
      <For each={filtered()}>
        {(cmd, i) => {
          const isSelected = () => i() === props.selectedIndex;
          const padded = () => cmd.name.padEnd(COL_WIDTH);
          return (
            <box backgroundColor={ironRainTheme.chrome.bg} width="100%">
              <text
                fg={
                  isSelected()
                    ? ironRainTheme.brand.primary
                    : ironRainTheme.chrome.fg
                }
              >
                {`${isSelected() ? "\u25B8" : " "} ${padded()} ${cmd.description}`}
              </text>
            </box>
          );
        }}
      </For>
      {filtered().length === 0 && (
        <box backgroundColor={ironRainTheme.chrome.bg} width="100%">
          <text fg={ironRainTheme.chrome.muted}>No matching commands</text>
        </box>
      )}
    </box>
  );
}

export function getFilteredCommands(
  filter: string,
  extraCommands?: SlashCommand[],
): SlashCommand[] {
  const all = extraCommands
    ? [...SLASH_COMMANDS, ...extraCommands]
    : SLASH_COMMANDS;
  return all.filter((cmd) => cmd.name.startsWith(filter.toLowerCase()));
}
