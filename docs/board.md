# The board

## In herdr's sidebar

Registration also puts Herdr Kata in Herdr's sidebar, in the agents list above
Spaces. Herdr has no plugin surface for a sidebar entry, but `pane report-agent`
takes a free-form label, so the board reports its own pane as the agent
`Herdr Kata`: the row is there for as long as a board is open, and clicking it goes
to the board. It carries what the store holds as words — `4 parked · 2 running`
— and stays idle, a green dot, whatever those counts are. Parked runs used to
report `blocked`, which Herdr draws in red: right on the day a run parks and
useless a week later, when parked runs nobody has got to yet leave the row
permanently red. A colour that is always on is not a signal.

The three names in that row are deliberately not the same word. The agent is
`Herdr Kata`, its tab is `Herdr Kata TUI`, and the space is `Herdr Kata` — printed
together, the name three times said nothing, so the line with room for it says
which of the three it is. A startup hook opens one board unfocused in
Herdr Kata's own workspace (`herdr-kata board --pin`), so the row exists before
anybody has asked for it, and reopening it is a click rather than a command you
have to remember.

## Keys

A workflow run is one row until `space` opens it, and then each step is a row of
its own with what it did and how long it took:

| key | action |
|-----|--------|
| `1` … `4` | jobs / runs / workflows / leases — the tabs, left to right |
| `tab` `shift+tab` | cycle lists; `h` `l` move between detail levels |
| `j` `k` | move |
| `enter` | open job detail (or focus the agent, from a run) |
| `R` | run selected job now |
| `p` | pause / resume |
| `f` | pin / unpin a favorite (favorites sort to the top) |
| `F` | show / hide finished one-shots (hidden by default) |
| `P` | prune finished one-shots — names them and waits for `y` (jobs list) |
| `a` | focus the run's agent |
| `space` | open a workflow run's steps (runs list) |
| `/` | search — filters both lists as you type |
| `esc` | clear the search, or go back a level |
| `[` `]` | previous / next page |
| `M` | release the mouse to the terminal, and take it back |
| `q` | quit |

## Mouse

The board takes the mouse, so the wheel and the pointer work the way they do in
anything else on screen:

- **Click a row** to select it. Click the row that is already selected to open
  it, which is what `l` does — and only what `l` does. `enter` on a workflow
  launches it, and a slipped double click that started an agent would spend
  money there is no undo for.
- **Click a tab** to switch lists.
- **The wheel** moves the selection in a list, since a list is paged from its
  cursor rather than scrolled, and moves the window on the Leases tab and
  detail pages, which have no selection.
- **The inspector is not clickable.** It describes the selected row, so clicking
  it selects nothing rather than something else.

`M` hands the mouse back. A program that asks for mouse reporting takes the
terminal's own drag-select with it, and the board is a thing left open in a
split all day — the run id, or the argv line on a job's detail page, is there to
be copied. Shift-drag overrides the grab in most terminals; `M` is for the ones
where it does not, and takes the mouse back when pressed again.

Search filters jobs by id, name, description, tags, and schedule; runs by job,
outcome, park reason, note, and trigger; leases by scope, resource, holder, run,
and reason. Jobs and runs page to fit the pane. The Leases tab shows each live
hold with its explicit scope, holder, run, expiry, and reason. It refreshes on
the same tick as execution state. Expired and released holds disappear.

Jobs retain editing, favorites, finished-work filtering, and run history. Runs
retain step expansion, context, usage, parked reasons, and agent navigation.
The brand, tabs, footer, and help remain visible while content scrolls.

To open the board as a full-width horizontal split:

```bash
herdr-kata board-wide
```

Bind a key to the `salmonumbrella.herdr-kata.board-wide` action for that in one keystroke;
Herdr's manifest can name a placement but not a split direction, which is why
the wide layout is an action rather than a pane setting.

A split divides a pane rather than filling a workspace, so it is addressed by
`$HERDR_PANE_ID`. Herdr refuses a split given only a workspace, and ignores the
target pane when it is given both — a shell with no pane of its own therefore
gets the board in a tab instead of an error.

### The board without a terminal

An agent's shell has no TTY: its stdin is `/dev/null` and `/dev/tty` does not
open. `herdr-kata board` there used to fail with Bubble Tea's `could not open a new
TTY`, which names a device the caller never asked about — so an agent told to
open the board would try it again, differently, and never get one.

It now opens the board as a Herdr pane and exits:

```
$ herdr-kata board
herdr-kata: no TTY here — opened the board as a herdr split instead
```

"Open the board" is the instruction in both cases; only the terminal it is drawn
in differs. Outside a Herdr session there is no pane to draw it in, and that is
the one case that is still an error.

---

[← back to the README](../README.md)
