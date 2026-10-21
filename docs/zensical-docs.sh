#!/usr/bin/env bash
set -euo pipefail

command_name="${1:-}"
if [[ "$command_name" != "build" && "$command_name" != "serve" ]]; then
  printf 'usage: %s {build|serve} [zensical args...]\n' "$0" >&2
  exit 2
fi
shift
docs_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -n "${VIRTUAL_ENV:-}" && -x "$VIRTUAL_ENV/bin/zensical" ]]; then
  zensical_bin="$VIRTUAL_ENV/bin/zensical"
elif [[ -x "$docs_root/.venv/bin/zensical" ]]; then
  zensical_bin="$docs_root/.venv/bin/zensical"
elif command -v zensical >/dev/null 2>&1; then
  zensical_bin="$(command -v zensical)"
else
  printf 'zensical not found; run make docs-install\n' >&2
  exit 127
fi

tmp_root="$(mktemp -d "$docs_root/zensical-public-docs.XXXXXX")"
tmp_config="$tmp_root.toml"
trap 'rm -rf "$tmp_root"; rm -f "$tmp_config"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
python3 "$docs_root/scripts/serve.py" --once "$docs_root" "$tmp_root" "$tmp_config"

cd "$docs_root"
if [[ "$command_name" == "build" ]]; then
  "$zensical_bin" build --strict --config-file "$(basename "$tmp_config")" "$@"
else
  python3 "$docs_root/scripts/serve.py" "$docs_root" "$tmp_root" "$tmp_config" "$zensical_bin" "$@"
fi
