#!/usr/bin/env node

import { randomBytes } from 'node:crypto';
import { loadConfig, ModelSlotManager, OrchestratorKernel, ProviderRegistry } from '@howlerops/iron-rain';
import { SPLASH_ART, TAGLINE } from '@howlerops/iron-rain-tui';

const VERSION = '0.1.0';

function printSplash(): void {
  console.log(SPLASH_ART);
  console.log(TAGLINE);
  console.log(`v${VERSION}\n`);
}

function printHelp(): void {
  printSplash();
  console.log('Usage:');
  console.log('  iron-rain                    Launch TUI');
  console.log('  iron-rain --headless "task"   Run without TUI');
  console.log('  iron-rain config              Show current config');
  console.log('  iron-rain models              List available models');
  console.log('  iron-rain --version           Show version');
  console.log('  iron-rain --help              Show this help');
}

function printConfig(): void {
  const config = loadConfig();
  console.log(JSON.stringify(config, null, 2));
}

function printModels(): void {
  const registry = new ProviderRegistry();
  for (const provider of registry.list()) {
    console.log(`\n${provider.name}:`);
    for (const model of provider.models) {
      console.log(`  - ${model}`);
    }
  }
}

async function runHeadless(prompt: string): Promise<void> {
  const config = loadConfig();
  const slotAssignment = config.slots ?? undefined;
  const slots = new ModelSlotManager(slotAssignment);
  const kernel = new OrchestratorKernel(slots);

  console.log(`Dispatching to main slot: ${slots.getSlot('main').model}\n`);

  const episode = await kernel.dispatch({
    id: randomBytes(16).toString('hex'),
    prompt,
    targetSlot: 'main',
  });

  if (episode.status === 'failure') {
    console.error(`Error: ${episode.result}`);
    console.error(`\n[${episode.status}] ${episode.duration}ms`);
    process.exit(1);
  }

  console.log(episode.result);
  console.log(
    `\n[${episode.status}] ${episode.tokens} tokens, ${episode.duration}ms`,
  );
}

async function main(): Promise<void> {
  const args = process.argv.slice(2);

  if (args.includes('--help') || args.includes('-h')) {
    printHelp();
    return;
  }

  if (args.includes('--version') || args.includes('-v')) {
    console.log(VERSION);
    return;
  }

  if (args[0] === 'config') {
    printConfig();
    return;
  }

  if (args[0] === 'models') {
    printModels();
    return;
  }

  const headlessIdx = args.indexOf('--headless');
  if (headlessIdx !== -1) {
    const prompt = args[headlessIdx + 1];
    if (!prompt) {
      console.error('Error: --headless requires a prompt argument');
      process.exit(1);
    }
    await runHeadless(prompt);
    return;
  }

  // Default: launch TUI
  printSplash();
  console.log('TUI mode launching...');
  console.log('(Full TUI rendering requires OpenTUI runtime — coming soon)');
  console.log('\nUse --headless "prompt" for immediate execution.');
}

main().catch((err) => {
  console.error('Fatal error:', err);
  process.exit(1);
});
