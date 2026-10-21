import pathlib
import tempfile
import unittest

import check_source_links


class CheckSourceLinksTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.repo = pathlib.Path(temp.name)
        self.site = self.repo / "docs" / "site"
        self.write("docs/operations/runtime.md", "# Run the runtime\n")
        self.write("docs/site/operations/runtime/index.html",
                   '<h2 id="move-state">Move</h2><a id="4-old-anchor"></a>')
        self.write("docs/index.md", "# Overview\n")
        self.write("docs/site/index.html", '<h1 id="overview">Overview</h1>')

    def write(self, path, text):
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")

    def source(self, text, path="internal/example/example.go"):
        self.write(path, f'package example\n\nconst help = "{text}"\n')

    def test_accepts_heading_and_element_ids(self):
        self.source("see docs/operations/runtime.md#move-state")
        self.write("cmd/tool/main.go", 'package main\n\n// docs/operations/runtime.md#4-old-anchor\n')
        self.write("internal/example/root.go", 'package example\n\nconst root = "docs/index.md"\n')
        self.assertEqual(check_source_links.check(self.repo, self.site), ([], 3))

    def test_refuses_missing_page(self):
        self.source("see docs/operations/recovery.md")
        errors, _ = check_source_links.check(self.repo, self.site)
        self.assertEqual(errors, ["internal/example/example.go:3: docs/operations/recovery.md: page not found"])

    def test_refuses_missing_anchor(self):
        self.source("see docs/operations/runtime.md#gone")
        errors, _ = check_source_links.check(self.repo, self.site)
        self.assertEqual(errors, ["internal/example/example.go:3: docs/operations/runtime.md#gone: no element with this id"])

    def test_ignores_tests(self):
        self.source("see docs/operations/recovery.md", path="internal/example/example_test.go")
        self.assertEqual(check_source_links.check(self.repo, self.site), ([], 0))


if __name__ == "__main__":
    unittest.main()
