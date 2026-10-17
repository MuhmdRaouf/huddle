package shared

// The radar version: the build stamps it into the binary (-ldflags "-X main.version=<v>",
// read from plugins/radar/VERSION), and every consumer — the CLI's status line, the API —
// receives it as a value. This package holds no copy, so the binary cannot disagree with
// itself.
