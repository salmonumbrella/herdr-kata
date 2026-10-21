import contextlib
import io
import pathlib
import tempfile
import unittest

import llms_txt

CONFIG = """[project]
site_name = "example"
site_description = "Run example work."
nav = [
  {"Start" = [{"Overview" = "index.md"}, {"Changelog" = "changelog.md"}]},
  {"Guide" = [{"Jobs" = "guide/jobs.md"}]},
  {"Design" = [{"Architecture" = "design/architecture.md"}]},
]
"""

EXPECTED = """# example

> Run example work.

## Docs

- [Overview](index.md): Start here.
- [Jobs](guide/jobs.md): Schedule jobs.

## Optional

- [Changelog](changelog.md): Release history.
- [Architecture](design/architecture.md): Why it works.
"""


def page(description):
    return f"---\ntitle: Page\ndescription: {description}\nlast_edited: 2026-10-17\n---\n\n# Page\n"


class LlmsTxtTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = pathlib.Path(temp.name)
        (self.root / "zensical.toml").write_text(CONFIG, encoding="utf-8")
        for path, description in {
            "index.md": "Start here.",
            "changelog.md": "Release history.",
            "guide/jobs.md": "Schedule jobs.",
            "design/architecture.md": "Why it works.",
        }.items():
            (self.root / path).parent.mkdir(parents=True, exist_ok=True)
            (self.root / path).write_text(page(description), encoding="utf-8")

    def run_main(self, *args):
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            return llms_txt.main(["--docs-root", str(self.root), *args])

    def test_render_follows_nav_and_descriptions(self):
        self.assertEqual(llms_txt.render(self.root), EXPECTED)

    def test_write_then_check(self):
        self.assertEqual(self.run_main(), 1)
        self.assertEqual(self.run_main("--write"), 0)
        self.assertEqual((self.root / "llms.txt").read_text(encoding="utf-8"), EXPECTED)
        self.assertEqual(self.run_main(), 0)

    def test_check_refuses_stale_index(self):
        (self.root / "llms.txt").write_text(EXPECTED, encoding="utf-8")
        (self.root / "guide/jobs.md").write_text(page("Run jobs."), encoding="utf-8")
        self.assertEqual(self.run_main(), 1)

    def test_page_without_description_fails(self):
        (self.root / "guide/jobs.md").write_text("# Jobs\n", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "guide/jobs.md"):
            llms_txt.render(self.root)


if __name__ == "__main__":
    unittest.main()
