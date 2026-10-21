#!/usr/bin/env python3
"""Generate docs/llms.txt from the nav titles and page frontmatter.

The default checks that docs/llms.txt is current; --write rewrites it.
"""
from __future__ import annotations

import argparse
import pathlib
import sys
import tomllib

sys.dont_write_bytecode = True  # keep docs/scripts free of __pycache__
from check_frontmatter import parse_frontmatter  # noqa: E402

ROOT = pathlib.Path(__file__).resolve().parents[1]
# Nav sections and pages an agent can skip; llms.txt calls them optional.
OPTIONAL_SECTIONS = ("Design", "Development")
OPTIONAL_PAGES = ("changelog.md",)


def nav_entries(node, section=None):
    """Yield (section, title, page) for every page in nav order."""
    if isinstance(node, list):
        for item in node:
            yield from nav_entries(item, section)
    elif isinstance(node, dict):
        for title, value in node.items():
            if isinstance(value, str):
                yield section, title, value
            else:
                yield from nav_entries(value, section or title)


def render(docs_root: pathlib.Path) -> str:
    project = tomllib.loads((docs_root / "zensical.toml").read_text(encoding="utf-8"))["project"]
    docs: list[str] = []
    optional: list[str] = []
    for section, title, page in nav_entries(project["nav"]):
        metadata, error = parse_frontmatter(docs_root / page)
        description = (metadata or {}).get("description")
        if error is not None or not isinstance(description, str) or not description.strip():
            raise ValueError(f"{page}: frontmatter needs a description")
        entry = f"- [{title}]({page}): {description.strip()}"
        if section in OPTIONAL_SECTIONS or page in OPTIONAL_PAGES:
            optional.append(entry)
        else:
            docs.append(entry)
    lines = [f"# {project['site_name']}", "", f"> {project['site_description']}", "",
             "## Docs", "", *docs, "", "## Optional", "", *optional]
    return "\n".join(lines) + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--docs-root", type=pathlib.Path, default=ROOT,
                        help="docs root containing zensical.toml")
    parser.add_argument("--write", action="store_true", help="rewrite llms.txt instead of checking it")
    args = parser.parse_args(argv)
    target = args.docs_root / "llms.txt"
    try:
        content = render(args.docs_root)
    except ValueError as err:
        print(f"FAIL: {err}", file=sys.stderr)
        return 1
    if args.write:
        target.write_text(content, encoding="utf-8")
        print(f"wrote {target.name}")
        return 0
    if not target.is_file() or target.read_text(encoding="utf-8") != content:
        print("FAIL: llms.txt is stale; run: cd docs && uv run python scripts/llms_txt.py --write",
              file=sys.stderr)
        return 1
    print("llms.txt is current")
    return 0


if __name__ == "__main__":
    sys.exit(main())
