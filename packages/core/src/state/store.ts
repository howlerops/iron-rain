type Listener<T> = (value: T) => void;

export class Signal<T> {
  private value: T;
  private listeners: Set<Listener<T>> = new Set();

  constructor(initial: T) {
    this.value = initial;
  }

  get(): T {
    return this.value;
  }

  set(next: T): void {
    this.value = next;
    for (const fn of this.listeners) {
      fn(next);
    }
  }

  update(fn: (prev: T) => T): void {
    this.set(fn(this.value));
  }

  subscribe(fn: Listener<T>): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }
}

export function createSignal<T>(initial: T): Signal<T> {
  return new Signal(initial);
}
