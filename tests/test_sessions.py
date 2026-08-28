from __future__ import annotations

import os
import shutil
import tempfile
import unittest
from pathlib import Path

from sessions.index import refresh
from sessions.parse import format_turns, parse_file, parse_text
from sessions import store
from sessions.paths import decode_cursor_slug


FIXTURES = Path(__file__).resolve().parent / "fixtures"


class ParseTests(unittest.TestCase):
    def test_cursor_strips_tools_and_uses_user_query(self):
        parsed = parse_text(
            (FIXTURES / "cursor.jsonl").read_text(),
            "cursor",
            Path("/tmp/Users-tmp-proj/agent-transcripts/aaaa/aaaa.jsonl"),
        )
        self.assertEqual(parsed.title, "Cursor distinctive phrase zebra-pluto-419")
        self.assertEqual([t.role for t in parsed.turns], ["user", "assistant"])
        self.assertIn("zebra-pluto-419", parsed.body)
        self.assertNotIn("secret-tool-dump", parsed.body)
        self.assertNotIn("tool_use", format_turns(parsed))

    def test_cursor_slug_keeps_dashed_segments(self):
        self.assertEqual(
            decode_cursor_slug("Users-usuario-dev-tooling-sessions"),
            "/Users/usuario/dev/tooling/sessions",
        )
        self.assertEqual(
            decode_cursor_slug(
                "Users-usuario-dev-resorsi-engagements-miami-wellness"
            ),
            "/Users/usuario/dev/resorsi/engagements/miami-wellness",
        )

    def test_claude_skips_meta_tools_and_snapshots(self):
        parsed = parse_text(
            (FIXTURES / "claude.jsonl").read_text(),
            "claude",
            Path("/tmp/claude-fixture-1.jsonl"),
        )
        self.assertEqual(parsed.id, "claude-fixture-1")
        self.assertEqual(parsed.cwd, "/tmp/claude-proj")
        self.assertEqual(parsed.title, "Claude distinctive phrase mango-helix-882")
        self.assertNotIn("secret-tool-dump", parsed.body)
        self.assertNotIn("local-command-caveat", parsed.body)
        shown = format_turns(parsed)
        self.assertNotIn("tool_use", shown)
        self.assertNotIn("function_call", shown)

    def test_codex_skips_session_meta_and_tools(self):
        parsed = parse_text(
            (FIXTURES / "codex.jsonl").read_text(),
            "codex",
            Path("/tmp/rollout-codex-fixture-1.jsonl"),
        )
        self.assertEqual(parsed.id, "codex-fixture-1")
        self.assertEqual(parsed.cwd, "/tmp/codex-proj")
        self.assertEqual(parsed.parent_id, None)
        self.assertIn("quartz-ember-553", parsed.body)
        self.assertNotIn("secret-tool-dump", parsed.body)
        self.assertNotIn("session_meta", format_turns(parsed))

    def test_codex_title_uses_my_request(self):
        raw = (
            '{"type":"session_meta","payload":{"id":"c1","cwd":"/tmp","timestamp":"t"}}\n'
            '{"type":"response_item","payload":{"type":"message","role":"user","content":'
            '[{"type":"input_text","text":"<in-app-browser-context source=\\"x\\">tabs</in-app-browser-context>\\n'
            '## My request:\\nReal question about widgets"}]}}\n'
        )
        parsed = parse_text(raw, "codex", Path("/tmp/c1.jsonl"))
        self.assertEqual(parsed.title, "Real question about widgets")
        self.assertNotIn("in-app-browser-context", parsed.body)


class IndexSearchTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="sessions-test-"))
        self.cursor = self.tmp / "cursor" / "Users-tmp-proj" / "agent-transcripts" / "aaaa"
        self.claude = self.tmp / "claude" / "-tmp-proj"
        self.codex = self.tmp / "codex" / "2026" / "08" / "26"
        self.archive = self.tmp / "archive"
        for d in (self.cursor, self.claude, self.codex, self.archive):
            d.mkdir(parents=True)
        shutil.copy(FIXTURES / "cursor.jsonl", self.cursor / "aaaa.jsonl")
        shutil.copy(FIXTURES / "claude.jsonl", self.claude / "claude-fixture-1.jsonl")
        shutil.copy(FIXTURES / "codex.jsonl", self.codex / "rollout-codex-fixture-1.jsonl")
        self.index = self.tmp / "index.sqlite"
        os.environ["SESSIONS_INDEX"] = str(self.index)
        os.environ["SESSIONS_CURSOR_ROOT"] = str(self.tmp / "cursor")
        os.environ["SESSIONS_CLAUDE_ROOT"] = str(self.tmp / "claude")
        os.environ["SESSIONS_CODEX_ROOT"] = str(self.tmp / "codex")
        os.environ["SESSIONS_CODEX_ARCHIVE_ROOT"] = str(self.archive)
        self.conn = store.connect(self.index)

    def tearDown(self):
        self.conn.close()
        shutil.rmtree(self.tmp, ignore_errors=True)
        for key in (
            "SESSIONS_INDEX",
            "SESSIONS_CURSOR_ROOT",
            "SESSIONS_CLAUDE_ROOT",
            "SESSIONS_CODEX_ROOT",
            "SESSIONS_CODEX_ARCHIVE_ROOT",
        ):
            os.environ.pop(key, None)

    def test_search_finds_each_harness(self):
        stats = refresh(self.conn)
        self.assertEqual(stats.upserted, 3)
        self.assertEqual(stats.unchanged, 0)
        hits = {
            "zebra-pluto-419": "cursor",
            "mango-helix-882": "claude",
            "quartz-ember-553": "codex",
        }
        for phrase, source in hits.items():
            rows = store.search(self.conn, phrase, source=source)
            self.assertEqual(len(rows), 1, phrase)
            self.assertEqual(rows[0]["source"], source)
            self.assertIn(phrase.split()[-1], rows[0]["snippet"] + rows[0]["title"])

    def test_unchanged_files_are_not_reparsed(self):
        first = refresh(self.conn)
        self.assertEqual(first.upserted, 3)
        second = refresh(self.conn)
        self.assertEqual(second.upserted, 0)
        self.assertEqual(second.unchanged, 3)

    def test_touch_reindexes(self):
        refresh(self.conn)
        target = self.claude / "claude-fixture-1.jsonl"
        Path(target).write_text(target.read_text() + "\n")
        stats = refresh(self.conn)
        self.assertEqual(stats.upserted, 1)
        self.assertEqual(stats.unchanged, 2)

    def test_deleted_file_drops_out(self):
        refresh(self.conn)
        (self.codex / "rollout-codex-fixture-1.jsonl").unlink()
        stats = refresh(self.conn)
        self.assertEqual(stats.deleted, 1)
        rows = store.search(self.conn, "quartz-ember-553")
        self.assertEqual(rows, [])

    def test_show_has_no_tool_json(self):
        refresh(self.conn)
        for source, rel in (
            ("cursor", self.cursor / "aaaa.jsonl"),
            ("claude", self.claude / "claude-fixture-1.jsonl"),
            ("codex", self.codex / "rollout-codex-fixture-1.jsonl"),
        ):
            parsed = parse_file(rel, source)
            shown = format_turns(parsed)
            self.assertNotIn("secret-tool-dump", shown)
            self.assertNotIn('"type": "tool_use"', shown)
            self.assertNotIn("function_call", shown)
            self.assertNotIn("tool_use", shown)


if __name__ == "__main__":
    unittest.main()
