import { z } from 'zod';

const SlotConfigSchema = z.object({
  provider: z.string(),
  model: z.string(),
  apiKey: z.string().optional(),
  apiBase: z.string().url().optional(),
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
