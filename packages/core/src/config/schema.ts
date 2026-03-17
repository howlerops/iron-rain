import { z } from 'zod';

const SlotConfigSchemaBase = z.object({
  provider: z.string(),
  model: z.string(),
  apiKey: z.string().optional(),
  apiBase: z.string().url().optional(),
  thinkingLevel: z.enum(['off', 'low', 'medium', 'high']).optional(),
  systemPrompt: z.string().optional(),
});

const SlotConfigSchema: z.ZodType<z.infer<typeof SlotConfigSchemaBase> & { fallback?: z.infer<typeof SlotConfigSchemaBase> }> = SlotConfigSchemaBase.extend({
  fallback: z.lazy(() => SlotConfigSchemaBase).optional(),
});

const ProviderConfigSchema = z.object({
  apiKey: z.string().optional(),
  apiBase: z.string().url().optional(),
});

const PermissionValue = z.enum(['allow', 'deny', 'ask']);

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
  channel: z.enum(['stable', 'beta', 'canary']).default('stable'),
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
  agent: z.string().default('build'),
  lcm: LcmConfigSchema.optional(),
  theme: z.string().default('default'),
  updates: UpdatesConfigSchema.optional(),
  mcpServers: z.record(z.string(), MCPServerConfigSchema).optional(),
  skills: SkillsConfigSchema.optional(),
});

export type IronRainConfig = z.infer<typeof IronRainConfigSchema>;

export function parseConfig(raw: unknown): IronRainConfig {
  return IronRainConfigSchema.parse(raw);
}

export function resolveEnvValue(value: string): string {
  if (value.startsWith('env:')) {
    const envVar = value.slice(4);
    return process.env[envVar] ?? '';
  }
  return value;
}
