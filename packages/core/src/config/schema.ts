import { z } from "zod";

const SlotConfigSchemaBase = z.object({
  provider: z.string(),
  model: z.string(),
  apiKey: z.string().optional(),
  apiBase: z.string().url().optional(),
  thinkingLevel: z.enum(["off", "low", "medium", "high"]).optional(),
  systemPrompt: z.string().optional(),
});

const SlotConfigSchema: z.ZodType<
  z.infer<typeof SlotConfigSchemaBase> & {
    fallback?: z.infer<typeof SlotConfigSchemaBase>;
  }
> = SlotConfigSchemaBase.extend({
  fallback: z.lazy(() => SlotConfigSchemaBase).optional(),
});

const ProviderConfigSchema = z.object({
  apiKey: z.string().optional(),
  apiBase: z.string().url().optional(),
});

const PermissionValue = z.enum(["allow", "deny", "ask"]);

const LcmConfigSchema = z.object({
  enabled: z.boolean().default(true),
  episodes: z
    .object({
      maxEpisodeTokens: z.number().default(4000),
    })
    .default({}),
});

const UpdatesConfigSchema = z.object({
  autoCheck: z.boolean().default(true),
  channel: z.enum(["stable", "beta", "canary"]).default("stable"),
});

const MCPServerConfigSchema = z.object({
  command: z.string(),
  args: z.array(z.string()).optional(),
  env: z.record(z.string(), z.string()).optional(),
});

const SkillsConfigSchema = z.object({
  paths: z.array(z.string()).optional(),
  autoDiscover: z.boolean().default(true),
});

const ContextConfigSchema = z.object({
  hotWindowSize: z.number().default(6),
  maxContextTokens: z.number().default(8000),
  maxFileSize: z.number().default(102400),
  maxImageSize: z.number().default(20971520),
  toolOutputMaxTokens: z.number().default(2000),
});

const MCPConfigSchema = z.object({
  requestTimeoutMs: z.number().default(10000),
});

const ResilienceConfigSchema = z.object({
  circuitBreakerThreshold: z.number().default(5),
  circuitBreakerResetMs: z.number().default(60000),
  maxRetries: z.number().default(3),
});

const AutoCommitConfigSchema = z.object({
  enabled: z.boolean().default(false),
  messagePrefix: z.string().default("iron-rain:"),
});

const SandboxConfigSchema = z.object({
  backend: z.enum(["none", "seatbelt", "docker", "gvisor"]).default("none"),
  allowNetwork: z.boolean().default(false),
  allowedWritePaths: z.array(z.string()).optional(),
  docker: z
    .object({
      image: z.string().default("node:20-slim"),
      memoryLimit: z.string().default("2g"),
      cpuLimit: z.string().default("2"),
    })
    .optional(),
});

const RulesConfigSchema = z.object({
  paths: z.array(z.string()).optional(),
  disabled: z.boolean().default(false),
});

const SessionConfigSchema = z.object({
  autoResume: z.boolean().default(true),
  maxHistory: z.number().default(50),
});

const MemoryConfigSchema = z.object({
  autoLearn: z.boolean().default(true),
  maxLessons: z.number().default(50),
});

const RepoMapConfigSchema = z.object({
  enabled: z.boolean().default(true),
  maxTokens: z.number().default(2000),
});

const PluginsConfigSchema = z.object({
  paths: z.array(z.string()).optional(),
  hooks: z.record(z.string(), z.string()).optional(),
});

const CostsConfigSchema = z.record(
  z.string(),
  z.object({
    input: z.number(),
    output: z.number(),
  }),
);

const LSPConfigSchema = z.object({
  enabled: z.boolean().default(false),
  servers: z
    .record(
      z.string(),
      z.object({
        command: z.string(),
        args: z.array(z.string()),
      }),
    )
    .optional(),
});

const VoiceConfigSchema = z.object({
  enabled: z.boolean().default(false),
  engine: z.enum(["whisper", "system"]).default("system"),
  whisperModel: z.string().optional(),
});

export const IronRainConfigSchema = z.object({
  slots: z
    .object({
      main: SlotConfigSchema,
      explore: SlotConfigSchema,
      execute: SlotConfigSchema,
    })
    .optional(),
  providers: z.record(z.string(), ProviderConfigSchema).optional(),
  permission: z.record(z.string(), PermissionValue).optional(),
  agent: z.string().default("build"),
  lcm: LcmConfigSchema.optional(),
  theme: z.string().default("default"),
  updates: UpdatesConfigSchema.optional(),
  mcpServers: z.record(z.string(), MCPServerConfigSchema).optional(),
  skills: SkillsConfigSchema.optional(),
  context: ContextConfigSchema.optional(),
  mcp: MCPConfigSchema.optional(),
  resilience: ResilienceConfigSchema.optional(),
  autoCommit: AutoCommitConfigSchema.optional(),
  sandbox: SandboxConfigSchema.optional(),
  rules: RulesConfigSchema.optional(),
  session: SessionConfigSchema.optional(),
  memory: MemoryConfigSchema.optional(),
  repoMap: RepoMapConfigSchema.optional(),
  plugins: PluginsConfigSchema.optional(),
  costs: CostsConfigSchema.optional(),
  lsp: LSPConfigSchema.optional(),
  voice: VoiceConfigSchema.optional(),
  configUrl: z.string().url().optional(),
});

export type IronRainConfig = z.infer<typeof IronRainConfigSchema>;

export function parseConfig(raw: unknown): IronRainConfig {
  return IronRainConfigSchema.parse(raw);
}

export function resolveEnvValue(value: string): string {
  if (value.startsWith("env:")) {
    const envVar = value.slice(4);
    return process.env[envVar] ?? "";
  }
  return value;
}
