package check

import (
	"path"
)

// globMatch wraps path.Match so the diff logic has one place to change if the
// glob semantics ever need to differ.
//
// path.Match's `*` does not cross `/`, which is what we want for tag names: a
// pattern like `v1.*` must not match `v1.2/extra`.
func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	if err != nil {
		return false // malformed pattern matches nothing
	}
	return ok
}
