#!/usr/bin/env python3
"""Check the docs pages and anchors that Go code prints against the built site.

Run it after the strict build. A link such as docs/<page>.md#<anchor> in a
non-test Go file under cmd/ or internal/ needs site/<page>/index.html and,
with an anchor, an element carrying that id.
"""
from __future__ import annotations

import argparse
import html.parser
import pathlib
import re
import sys

REPO = pathlib.Path(__file__).resolve().parents[2]
SOURCE_DIRS = ("cmd", "internal")
LINK = re.compile(r"docs/([A-Za-z0-9_./-]+?)\.md(?:#([A-Za-z0-9_.:-]+))?")


class _IDs(html.parser.HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.ids: set[str] = set()

    def handle_starttag(self, tag, attrs) -> None:
        self.ids.update(value for name, value in attrs if name == "id" and value)


def element_ids(path: pathlib.Path) -> set[str]:
    parser = _IDs()
    parser.feed(path.read_text(encoding="utf-8"))
    return parser.ids


def source_links(repo: pathlib.Path):
    for directory in SOURCE_DIRS:
        for path in sorted((repo / directory).rglob("*.go")):
            if path.name.endswith("_test.go"):
                continue
            lines = path.read_text(encoding="utf-8").splitlines()
            for number, line in enumerate(lines, 1):
                for match in LINK.finditer(line):
                    yield path.relative_to(repo).as_posix(), number, match.group(1), match.group(2)


def built_page(site: pathlib.Path, page: str) -> pathlib.Path:
    if page == "index" or page.endswith("/index"):
        return site / page[: -len("index")] / "index.html"
    return site / page / "index.html"


def check(repo: pathlib.Path, site: pathlib.Path) -> tuple[list[str], int]:
    errors: list[str] = []
    ids: dict[pathlib.Path, set[str]] = {}
    count = 0
    for source, number, page, anchor in source_links(repo):
        count += 1
        location = f"{source}:{number}: docs/{page}.md"
        built = built_page(site, page)
        if not (repo / "docs" / f"{page}.md").is_file() or not built.is_file():
            errors.append(f"{location}: page not found")
            continue
        if anchor is None:
            continue
        if built not in ids:
            ids[built] = element_ids(built)
        if anchor not in ids[built]:
            errors.append(f"{location}#{anchor}: no element with this id")
    return errors, count


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", type=pathlib.Path, default=REPO, help="repository root")
    parser.add_argument("--site", type=pathlib.Path, help="built site (default: docs/site)")
    args = parser.parse_args(argv)
    site = args.site or args.repo / "docs" / "site"
    if not site.is_dir():
        print(f"FAIL: {site} is missing; run make docs-build first", file=sys.stderr)
        return 1
    errors, count = check(args.repo, site)
    if errors:
        for error in errors:
            print(f"FAIL: {error}", file=sys.stderr)
        return 1
    print(f"source link checks passed ({count} links)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
