/// <reference path="../glitched.d.ts" />

// Tools for agents. The engine runs this module with no window: each call
// is tool({ name, args, call }), and call.thread is the agent's thread.
// Notes live in the plugin's database, a SQLite file of its own.

type NoteRow = { text: string };

async function notes(thread: string): Promise<NoteRow[]> {
  await glitched.db.exec("CREATE TABLE IF NOT EXISTS notes (thread TEXT, text TEXT, at INTEGER)");
  return glitched.db.query<NoteRow>("SELECT text FROM notes WHERE thread = ? ORDER BY at", [thread]);
}

export async function tool({ name, args, call }: ToolArgs): Promise<Record<string, unknown>> {
  switch (name) {
    case "count_words": {
      const text = String(args.text ?? "");
      return { words: text.split(/\s+/).filter(Boolean).length, lines: text.split("\n").length, characters: [...text].length };
    }
    case "note": {
      const kept = await notes(call.thread);
      if (!args.text) return { notes: kept.map((n) => n.text) };
      await glitched.db.exec("INSERT INTO notes VALUES (?, ?, ?)", [call.thread, String(args.text), Date.now()]);
      return { kept: kept.length + 1 };
    }
  }
  throw new Error("no tool " + name);
}
