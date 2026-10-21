#!/usr/bin/env python3
"""Stage public documentation once or keep it current while Zensical serves it."""
import pathlib
import re
import signal
import subprocess
import sys
import time

ROOT_PAGES = ("index.md", "changelog.md")
PAGE_DIRS = ("get-started", "guide", "reference", "operations", "design", "development", "stylesheets")


def sync(docs, staging, config):
    sources = {staging / page: docs / page for page in ROOT_PAGES}
    for directory in PAGE_DIRS:
        for source in (docs / directory).rglob("*"):
            if source.is_file():
                sources[staging / source.relative_to(docs)] = source
    for target, source in sources.items():
        try:
            content = source.read_bytes()
        except FileNotFoundError:
            continue  # An editor may replace a file between listing and reading it.
        target.parent.mkdir(parents=True, exist_ok=True)
        if not target.exists() or target.read_bytes() != content:
            target.write_bytes(content)
    for target in staging.rglob("*"):
        if target.is_file() and target not in sources:
            target.unlink()
    content = (docs / "zensical.toml").read_text(encoding="utf-8")
    content = re.sub(r'^docs_dir = .*$', f'docs_dir = "{staging.name}"', content, flags=re.M)
    content = re.sub(r'^site_dir = .*$', 'site_dir = "site"', content, flags=re.M)
    if not config.exists() or config.read_text(encoding="utf-8") != content:
        config.write_text(content, encoding="utf-8")


if __name__ == "__main__":
    if sys.argv[1:2] == ["--once"]:
        sync(*map(pathlib.Path, sys.argv[2:5]))
        sys.exit(0)
    docs, staging, config = map(pathlib.Path, sys.argv[1:4])
    def stop(signum, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, stop)
    process = subprocess.Popen([sys.argv[4], "serve", "--config-file", str(config), *sys.argv[5:]])
    try:
        while process.poll() is None:
            sync(docs, staging, config)
            time.sleep(0.5)
    except KeyboardInterrupt:
        pass
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
    sys.exit(process.returncode)
