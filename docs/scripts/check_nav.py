#!/usr/bin/env python3
"""Check that every public documentation page has a navigation entry."""
import pathlib
import sys
import tomllib

sys.dont_write_bytecode = True
from serve import PAGE_DIRS, ROOT_PAGES

ROOT = pathlib.Path(__file__).resolve().parents[1]


def nav_pages(node):
    if isinstance(node, str):
        yield node
    elif isinstance(node, list):
        for item in node:
            yield from nav_pages(item)
    elif isinstance(node, dict):
        for item in node.values():
            yield from nav_pages(item)


def check(root):
    config = tomllib.loads((root / "zensical.toml").read_text(encoding="utf-8"))
    nav = set(nav_pages(config["project"]["nav"]))
    pages = {page for page in ROOT_PAGES if (root / page).is_file()}
    pages.update(path.relative_to(root).as_posix()
                 for directory in PAGE_DIRS if directory != "stylesheets"
                 for path in (root / directory).rglob("*.md"))
    errors = [f"{page}: missing from nav" for page in sorted(pages - nav)]
    errors += [f"{page}: nav page does not exist" for page in sorted(nav)
               if not (root / page).is_file()]
    return errors


if __name__ == "__main__":
    errors = check(ROOT)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        sys.exit(1)
    print("nav checks passed")
