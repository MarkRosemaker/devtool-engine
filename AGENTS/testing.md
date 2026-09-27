# Proving a test

A test written alongside a change passes by construction. Before trusting
it, break the change on purpose and watch the test go red — then put the
change back and watch it go green.

## A build failure is not a red test

`go test` prints `FAIL` for both. A mutation that leaves an import unused, or
a variable unread, fails to compile, and the line reads
`FAIL … [build failed]` — which looks like the test catching the mutation and
proves nothing about it. Read for the assertion's own message, and make the
mutation one that still compiles: narrow a slice rather than delete the
expression that used the import.

It happened adding `Files` to `TaskDone`: dropping the paths outright left
`slices` unused, and the first "red" was the compiler.

## Put the change back from a copy, not from git

`git checkout -- file` restores the committed version. On a file the change
created, there is none: the command fails quietly and the mutation stays in
place, ready to be committed. Copy the file aside before mutating it, and
restore from the copy. This happened on `maintain/vendored.go` minutes after
the section above was written.

## Check the fixture says what the test thinks it says

A fixture that is itself invalid makes a test pass or fail for the wrong
reason. Writing `moduleChanges`, a `go.mod` requiring `v2.0.0` without the
`/v2` path suffix did not parse, and a case meant to be "unreadable" parsed
fine, because `modfile.ParseLax` skips unknown directives by design — so the
"unreadable after" case passed only because the *before* side was broken too.
Parse a fixture on its own first, and for a lax parser, break a directive it
knows (a `require` with no version) rather than inventing one it ignores.
