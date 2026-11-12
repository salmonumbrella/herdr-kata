import pathlib
import re
import shlex
import subprocess
import sys
import time


def status(value):
    print('\x1b]7501;state=' + value + ':app=fixture\x1b\\', end='', flush=True)

status('done')
for line in sys.stdin:
    status('working')
    while pathlib.Path('wait').exists():
        time.sleep(0.2)
    line = line.strip()
    assert line.startswith('Read ') and line.endswith(' and do exactly what it says.')
    path = pathlib.Path(line[5:-len(' and do exactly what it says.')])
    text = path.read_text()
    command = re.search(r'\n(.+ report --result .+)\n', text).group(1)
    argv = shlex.split(command.split(' --status ')[0])
    subprocess.run(argv + ['--status', 'succeeded', '--summary', 'Fixture completed'], check=True)
    with pathlib.Path('deliveries').open('a') as f:
        f.write('delivered\n')
    status('done')
