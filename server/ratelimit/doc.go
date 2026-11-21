// Package ratelimit is an in-memory token-bucket limiter for the hosted API.
//
// State lives in one process. That is correct for the single demo instance and
// wrong for several: each instance would grant the full limit on its own.
// Replace it with shared state before running more than one, and not before a
// measured need.
package ratelimit
