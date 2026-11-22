// Package privatefs creates files and directories that only the current user
// may open, and names the per-user directory tokenholic keeps them in.
//
// On Unix "private" is the owner-only permission bits. On Windows it is a
// protected access-control list holding a single entry for the current user,
// which is what those bits mean there: no inherited entries and nobody else.
package privatefs
