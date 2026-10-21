import pathlib
import tempfile
import unittest

import check_nav


class CheckNavTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = pathlib.Path(temp.name)
        (self.root / "guide").mkdir()
        for page in ("index.md", "changelog.md", "guide/jobs.md"):
            (self.root / page).write_text("# Page\n", encoding="utf-8")

    def write_nav(self, *pages):
        entries = ", ".join(f'{{"Page" = "{page}"}}' for page in pages)
        (self.root / "zensical.toml").write_text(
            f'[project]\nnav = [{{"Start" = [{entries}]}}]\n', encoding="utf-8")

    def test_accepts_root_pages_in_nav(self):
        self.write_nav("index.md", "changelog.md", "guide/jobs.md")
        self.assertEqual(check_nav.check(self.root), [])

    def test_requires_changelog_in_nav(self):
        self.write_nav("index.md", "guide/jobs.md")
        self.assertEqual(check_nav.check(self.root), ["changelog.md: missing from nav"])

    def test_refuses_missing_nav_page(self):
        (self.root / "changelog.md").unlink()
        self.write_nav("index.md", "changelog.md", "guide/jobs.md")
        self.assertEqual(check_nav.check(self.root), ["changelog.md: nav page does not exist"])


if __name__ == "__main__":
    unittest.main()
