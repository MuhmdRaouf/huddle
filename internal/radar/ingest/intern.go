package ingest

import "unique"

// interned is the one copy of a word the records repeat: a session id, a model, an upstream, a
// header name. The store holds thousands of records that name a handful of such words, and
// each line's parse hands every record a copy of its own.
func interned(word string) string { return unique.Make(word).Value() }
