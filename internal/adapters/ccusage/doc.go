// Package ccusage runs one pinned release of the third-party ccusage program
// and validates what it reports, for coding agents tokenholic has no reader
// of its own for. It holds the pin, the guarded execution, and the output
// contract; turning reports into usage records belongs to the collector.
//
// Claude and Codex are deliberately outside it. They have native adapters.
package ccusage
