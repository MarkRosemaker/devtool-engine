# Roadmap

Work not yet done in this module. Entries about the generators are on
`devtool`'s roadmap, entries about the chat on `patchpal`'s, and entries about
which repositories are maintained on `portfolio`'s.

## A human-readable renderer for the event stream

`event.Write` writes JSON Lines, which is what a parser on the other side of a
process boundary wants and not what a person watching a run at a terminal
wants. The same typed events could drive a second renderer — a sentence per
event, or the live table `Board` already draws — chosen by a flag, defaulting
to human-readable at a terminal and to JSON Lines when stdout is a pipe.

This used to be proposed as routing the events through `slog`, with a handler
per audience. **That is the thing not to do.** `slog`'s default logger is
process-wide, and pointing its handler at stdout is exactly what put log lines
into the event stream and made a run's report unreadable for months. Events and
logs are two streams for a reason; a second renderer of the typed events is a
second `Emitter`, not a second handler.

What the split does leave open is the timestamp: `event.Emit` fills one in and
`slog` fills in its own. If the two ever meet again, the event's own time is
the one the table orders by.

## Retry transient failures

A network blip currently fails a repository for the whole run. The dependency
graph makes this awkward — a retried repository's dependents are already
unblocked — so it needs thought rather than a loop.

## Board is write-only after the run

A run returns `board.Results()` and the board is discarded. If the coverage
gate on `devtool`'s roadmap needs a second pass over the results, that is where
it would live.

## Tag this module

Every consumer depends on a pseudo-version. It should be a version once the
surface has stopped moving. `selfupdate` works without tags — an untagged
module resolves to a pseudo-version naming the commit, and this module has
never been tagged and resolves — so nothing is blocked on it; it is a courtesy
to whoever reads a `go.mod`.

## Drop the deprecated aliases in maintain

`maintain` keeps an alias for every name that moved to `event`, so a consumer
that has not moved yet goes on compiling. Once nothing imports them, they go.

## Notes on selfupdate, checked rather than assumed

Not work to do, but the properties the design rests on, so a change that breaks
one is a change to reconsider:

- **No tags needed.** An untagged module resolves to a pseudo-version naming
  the commit.
- **Private modules work.** It shells out to `go list -m` rather than writing a
  proxy request by hand, so whatever `GOPROXY`, `GOPRIVATE` and git credentials
  the machine already has are the ones that apply.
- **The proxy caches.** Fifty minutes after a push, `proxy.golang.org` was
  still naming the commit before it, while a direct query named the right one.
  `Updater.Direct` skips the proxy, and wants setting wherever a change is
  meant to take effect promptly — which is the whole point of the split.

It also refuses to replace a local build, since the toolchain stamps one
`(devel)`, and it reports when an older copy earlier on `PATH` will shadow what
it just installed.
