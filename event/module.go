package event

import (
	"strings"
	"time"

	"golang.org/x/mod/module"
)

// String reads the way "go get -u" reports a requirement it moved, with a
// pseudo-version given as the time it names, since its commit hash is no help
// to a reader, and a path on GitHub without its host.
func (m ModuleChange) String() string {
	path := strings.TrimPrefix(m.Path, defaultHost)

	switch {
	case m.From == "":
		return path + " " + readable(m.To) + " (added)"
	case m.To == "":
		return path + " " + readable(m.From) + " (removed)"
	default:
		return path + " " + readable(m.From) + " => " + sameDay(m.From, m.To)
	}
}

// Tagged reports whether v is a version somebody released, rather than a
// pseudo-version.
func Tagged(v string) bool { return v != "" && !module.IsPseudoVersion(v) }

// defaultHost is where nearly every module lives, so naming it says nothing.
// Any other host is kept, since there it is the one thing worth reading.
const defaultHost = "github.com/"

// pseudoLayout is the time a pseudo-version names, in UTC as it records it.
const pseudoLayout = "2006-01-02 15:04"

// readable is v as a reader wants it: a tag as is, a pseudo-version as its time.
func readable(v string) string {
	t, err := module.PseudoVersionTime(v)
	if err != nil {
		return v
	}

	return t.Format(pseudoLayout)
}

// sameDay is to's version, dropping the date where from's already said it.
func sameDay(from, to string) string {
	f, errF := module.PseudoVersionTime(from)
	t, errT := module.PseudoVersionTime(to)

	if errF != nil || errT != nil || f.Format(time.DateOnly) != t.Format(time.DateOnly) {
		return readable(to)
	}

	return t.Format(time.TimeOnly[:5])
}
