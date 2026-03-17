/**
 * SQLite-backed session persistence using bun:sqlite.
 *
 * Stores sessions, messages, and stats so they survive app restarts.
 * The DB file lives at ~/.iron-rain/sessions.db by default.
 */
import { Database } from 'bun:sqlite';
import { mkdirSync, existsSync } from 'fs';
import { join } from 'path';
import { homedir } from 'os';
import type { Message, SessionStats, SlotActivity } from '../components/session-view.js';

export interface SessionRecord {
  id: string;
  createdAt: number;
  updatedAt: number;
  model: string;
  messageCount: number;
  totalTokens: number;
  totalDuration: number;
}

const DATA_DIR = join(homedir(), '.iron-rain');
const DB_PATH = join(DATA_DIR, 'sessions.db');

function ensureDataDir(): void {
  if (!existsSync(DATA_DIR)) {
    mkdirSync(DATA_DIR, { recursive: true });
  }
}

export interface Lesson {
  id: string;
  content: string;
  source: string;
  createdAt: number;
  expiresAt: number | null;
  tags: string[];
}

function initDb(db: Database): void {
  db.run(`
    CREATE TABLE IF NOT EXISTS sessions (
      id TEXT PRIMARY KEY,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL,
      model TEXT NOT NULL DEFAULT ''
    )
  `);

  db.run(`
    CREATE TABLE IF NOT EXISTS messages (
      id TEXT PRIMARY KEY,
      session_id TEXT NOT NULL,
      role TEXT NOT NULL,
      content TEXT NOT NULL,
      slot TEXT,
      timestamp INTEGER NOT NULL,
      tokens INTEGER,
      duration INTEGER,
      sort_order INTEGER NOT NULL,
      FOREIGN KEY (session_id) REFERENCES sessions(id)
    )
  `);

  db.run(`
    CREATE TABLE IF NOT EXISTS activities (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      message_id TEXT NOT NULL,
      slot TEXT NOT NULL,
      task TEXT NOT NULL,
      status TEXT NOT NULL,
      duration INTEGER,
      tokens INTEGER,
      FOREIGN KEY (message_id) REFERENCES messages(id)
    )
  `);

  db.run(`
    CREATE TABLE IF NOT EXISTS lessons (
      id TEXT PRIMARY KEY,
      content TEXT NOT NULL,
      source TEXT NOT NULL DEFAULT '',
      created_at INTEGER NOT NULL,
      expires_at INTEGER,
      tags TEXT NOT NULL DEFAULT '[]'
    )
  `);

  db.run(`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, sort_order)`);
  db.run(`CREATE INDEX IF NOT EXISTS idx_lessons_created ON lessons(created_at DESC)`);
}

/** Row shape returned from SQLite message queries */
interface MessageRow {
  id: string;
  session_id: string;
  role: 'user' | 'assistant';
  content: string;
  slot: string | null;
  timestamp: number;
  tokens: number | null;
  duration: number | null;
  sort_order: number;
}

/** Row shape returned from SQLite stats queries */
interface StatsRow {
  totalTokens: number;
  totalDuration: number;
  requestCount: number;
}

/** Row shape returned from SQLite lesson queries */
interface LessonRow {
  id: string;
  content: string;
  source: string;
  created_at: number;
  expires_at: number | null;
  tags: string;
}

export class SessionDB {
  private db: Database;

  constructor(dbPath?: string) {
    ensureDataDir();
    this.db = new Database(dbPath ?? DB_PATH);
    this.db.run('PRAGMA journal_mode = WAL');
    this.db.run('PRAGMA synchronous = NORMAL');
    initDb(this.db);
  }

  // --- Sessions ---

  createSession(id: string, model: string): void {
    const now = Date.now();
    this.db.run(
      'INSERT INTO sessions (id, created_at, updated_at, model) VALUES (?, ?, ?, ?)',
      [id, now, now, model],
    );
  }

  listSessions(limit = 20): SessionRecord[] {
    const rows = this.db.query(`
      SELECT
        s.id,
        s.created_at as createdAt,
        s.updated_at as updatedAt,
        s.model,
        COUNT(m.id) as messageCount,
        COALESCE(SUM(m.tokens), 0) as totalTokens,
        COALESCE(SUM(m.duration), 0) as totalDuration
      FROM sessions s
      LEFT JOIN messages m ON m.session_id = s.id
      GROUP BY s.id
      ORDER BY s.updated_at DESC
      LIMIT ?
    `).all(limit) as SessionRecord[];
    return rows;
  }

  getLatestSessionId(): string | null {
    const row = this.db.query(
      'SELECT id FROM sessions ORDER BY updated_at DESC LIMIT 1',
    ).get() as { id: string } | null;
    return row?.id ?? null;
  }

  // --- Messages ---

  addMessage(sessionId: string, msg: Message, sortOrder: number): void {
    this.db.run(
      `INSERT OR REPLACE INTO messages (id, session_id, role, content, slot, timestamp, tokens, duration, sort_order)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [msg.id, sessionId, msg.role, msg.content, msg.slot ?? null, msg.timestamp, msg.tokens ?? null, msg.duration ?? null, sortOrder],
    );

    // Store activities
    if (msg.activities?.length) {
      const stmt = this.db.prepare(
        'INSERT INTO activities (message_id, slot, task, status, duration, tokens) VALUES (?, ?, ?, ?, ?, ?)',
      );
      for (const a of msg.activities) {
        stmt.run(msg.id, a.slot, a.task, a.status, a.duration ?? null, a.tokens ?? null);
      }
    }

    // Update session timestamp
    this.db.run('UPDATE sessions SET updated_at = ? WHERE id = ?', [Date.now(), sessionId]);
  }

  getMessages(sessionId: string): Message[] {
    const rows = this.db.query(
      'SELECT * FROM messages WHERE session_id = ? ORDER BY sort_order',
    ).all(sessionId) as MessageRow[];

    return rows.map((row) => {
      const activities = this.db.query(
        'SELECT slot, task, status, duration, tokens FROM activities WHERE message_id = ?',
      ).all(row.id) as SlotActivity[];

      return {
        id: row.id,
        role: row.role,
        content: row.content,
        slot: row.slot ?? undefined,
        timestamp: row.timestamp,
        tokens: row.tokens ?? undefined,
        duration: row.duration ?? undefined,
        activities: activities.length > 0 ? activities : undefined,
      } as Message;
    });
  }

  clearMessages(sessionId: string): void {
    // Delete activities first (foreign key)
    this.db.run(
      'DELETE FROM activities WHERE message_id IN (SELECT id FROM messages WHERE session_id = ?)',
      [sessionId],
    );
    this.db.run('DELETE FROM messages WHERE session_id = ?', [sessionId]);
  }

  getSessionStats(sessionId: string): SessionStats {
    const row = this.db.query(`
      SELECT
        COALESCE(SUM(tokens), 0) as totalTokens,
        COALESCE(SUM(duration), 0) as totalDuration,
        COUNT(*) as requestCount
      FROM messages
      WHERE session_id = ? AND role = 'assistant'
    `).get(sessionId) as StatsRow | null;

    return {
      totalTokens: row?.totalTokens ?? 0,
      totalDuration: row?.totalDuration ?? 0,
      modelCount: 1,
      requestCount: row?.requestCount ?? 0,
    };
  }

  // --- Lessons (persistent cross-session memory) ---

  addLesson(content: string, source: string, tags: string[] = [], ttlMs?: number): string {
    const id = crypto.randomUUID?.() ?? `lesson-${Date.now()}`;
    const now = Date.now();
    const expiresAt = ttlMs ? now + ttlMs : null;
    this.db.run(
      'INSERT INTO lessons (id, content, source, created_at, expires_at, tags) VALUES (?, ?, ?, ?, ?, ?)',
      [id, content, source, now, expiresAt, JSON.stringify(tags)],
    );
    return id;
  }

  getLessons(limit = 50): Lesson[] {
    const now = Date.now();
    // Clean expired lessons
    this.db.run('DELETE FROM lessons WHERE expires_at IS NOT NULL AND expires_at < ?', [now]);

    const rows = this.db.query(
      'SELECT * FROM lessons ORDER BY created_at DESC LIMIT ?',
    ).all(limit) as LessonRow[];

    return rows.map(r => ({
      id: r.id,
      content: r.content,
      source: r.source,
      createdAt: r.created_at,
      expiresAt: r.expires_at,
      tags: JSON.parse(r.tags || '[]'),
    }));
  }

  searchLessons(query: string, limit = 10): Lesson[] {
    const now = Date.now();
    this.db.run('DELETE FROM lessons WHERE expires_at IS NOT NULL AND expires_at < ?', [now]);

    const rows = this.db.query(
      'SELECT * FROM lessons WHERE content LIKE ? ORDER BY created_at DESC LIMIT ?',
    ).all(`%${query}%`, limit) as LessonRow[];

    return rows.map(r => ({
      id: r.id,
      content: r.content,
      source: r.source,
      createdAt: r.created_at,
      expiresAt: r.expires_at,
      tags: JSON.parse(r.tags || '[]'),
    }));
  }

  deleteLesson(id: string): void {
    this.db.run('DELETE FROM lessons WHERE id = ?', [id]);
  }

  /** Get lessons formatted as context for injection into prompts */
  getLessonContext(limit = 10): string {
    const lessons = this.getLessons(limit);
    if (lessons.length === 0) return '';
    const items = lessons.map(l => `- ${l.content}`).join('\n');
    return `## Lessons Learned\n${items}`;
  }

  close(): void {
    this.db.close();
  }
}

/**
 * No-op implementation for environments without bun:sqlite (e.g. Node.js).
 * Implements the same interface as SessionDB via duck typing.
 */
export class NullSessionDB {
  createSession(): void { /* no-op */ }
  listSessions(): SessionRecord[] { return []; }
  getLatestSessionId(): string | null { return null; }
  addMessage(): void { /* no-op */ }
  getMessages(): Message[] { return []; }
  clearMessages(): void { /* no-op */ }
  getSessionStats(): SessionStats {
    return { totalTokens: 0, totalDuration: 0, modelCount: 1, requestCount: 0 };
  }
  addLesson(): string { return ''; }
  getLessons(): Lesson[] { return []; }
  searchLessons(): Lesson[] { return []; }
  deleteLesson(): void { /* no-op */ }
  getLessonContext(): string { return ''; }
  close(): void { /* no-op */ }
}
