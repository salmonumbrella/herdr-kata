import contextlib
import io
import pathlib
import tempfile
import unittest

import check_frontmatter

PAGE = "---\ntitle: Page\ndescription: A page.\nlast_edited: 2026-10-17\n---\n\n# Page\n"


def run(root):
    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
        return check_frontmatter.run(root)


class CheckFrontmatterTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = pathlib.Path(temp.name)
        (self.root / "guide").mkdir()
        (self.root / "guide" / "jobs.md").write_text(PAGE, encoding="utf-8")
        (self.root / "index.md").write_text(PAGE, encoding="utf-8")
        (self.root / "changelog.md").write_text(PAGE, encoding="utf-8")

    def test_accepts_complete_pages(self):
        self.assertEqual(run(self.root), 0)

    def test_checks_root_pages(self):
        for name in ("index.md", "changelog.md"):
            with self.subTest(name=name):
                (self.root / name).write_text("# Page\n", encoding="utf-8")
                self.assertEqual(run(self.root), 1)
                (self.root / name).write_text(PAGE, encoding="utf-8")

    def test_requires_root_pages(self):
        (self.root / "changelog.md").unlink()
        self.assertEqual(run(self.root), 1)


if __name__ == "__main__":
    unittest.main()
