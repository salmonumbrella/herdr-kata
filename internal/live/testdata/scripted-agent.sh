#!/bin/sh
# Deliberately no terminal status reporting: exercise the never-reported row.
set -eu
exec python3 - "$@" <<'PY'
import os, pathlib, re, shlex, subprocess, sys, time

line = sys.argv[-1]
assert line.startswith('Read ') and line.endswith(' and do exactly what it says.')
path = pathlib.Path(line[5:-len(' and do exactly what it says.')])
text = path.read_text()
work = pathlib.Path.cwd()
with (work / 'launches').open('a') as f:
    f.write(os.environ['KATA_CRON_RUN'] + '\n')
while (work / 'wait').exists():
    time.sleep(0.2)
(work / 'completed').write_text('scratch task complete\n')
report = re.search(r'\n(.+ report --result .+)\n', text).group(1)
argv = shlex.split(report.split(' --status ')[0])
subprocess.run(argv + ['--status', 'succeeded', '--summary', 'Scratch task completed', '--note', 'Fixture completed'], check=True)
close = re.search(r'`(.+ close .+)` when finished', text).group(1)
argv = shlex.split(close.split(' --done ')[0])
subprocess.run(argv + ['--done', '--message', 'Completed the isolated scratch task and checked its output file.', '--evidence', 'reviewed-paths:' + str(work / 'completed')], check=True)
PY
