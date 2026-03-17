/**
 * Bridge error with retry semantics.
 */
export class BridgeError extends Error {
  readonly statusCode: number;
  readonly provider: string;

  constructor(message: string, statusCode: number, provider: string) {
    super(message);
    this.name = 'BridgeError';
    this.statusCode = statusCode;
    this.provider = provider;
  }

  /** Server errors (5xx) and rate limits (429) are retryable */
  isRetryable(): boolean {
    return this.statusCode === 429 || (this.statusCode >= 500 && this.statusCode < 600);
  }
}

export interface RetryConfig {
  maxRetries: number;
  baseDelayMs: number;
  maxDelayMs: number;
}

export const DEFAULT_RETRY_CONFIG: RetryConfig = {
  maxRetries: 3,
  baseDelayMs: 1000,
  maxDelayMs: 30000,
};

/**
 * Exponential backoff with jitter.
 */
export function backoffDelay(attempt: number, config: RetryConfig): number {
  const delay = Math.min(
    config.baseDelayMs * Math.pow(2, attempt),
    config.maxDelayMs,
  );
  // Add jitter: 50-100% of computed delay
  return delay * (0.5 + Math.random() * 0.5);
}

/**
 * Circuit breaker state tracker.
 * Opens after `threshold` consecutive failures, auto-resets after `resetMs`.
 */
export class CircuitBreaker {
  private consecutiveFailures = 0;
  private lastFailureTime = 0;
  private readonly threshold: number;
  private readonly resetMs: number;

  constructor(threshold = 5, resetMs = 60_000) {
    this.threshold = threshold;
    this.resetMs = resetMs;
  }

  isOpen(): boolean {
    if (this.consecutiveFailures < this.threshold) return false;
    // Auto-reset after resetMs
    if (Date.now() - this.lastFailureTime > this.resetMs) {
      this.reset();
      return false;
    }
    return true;
  }

  recordSuccess(): void {
    this.consecutiveFailures = 0;
  }

  recordFailure(): void {
    this.consecutiveFailures++;
    this.lastFailureTime = Date.now();
  }

  reset(): void {
    this.consecutiveFailures = 0;
    this.lastFailureTime = 0;
  }

  get failures(): number {
    return this.consecutiveFailures;
  }
}
