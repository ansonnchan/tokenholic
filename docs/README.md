# Documentation

This directory owns decisions that cross package boundaries. Package-specific implementation details belong beside the relevant code, using a package `doc.go`, a focused subsystem README, or short comments at the invariant they explain.

## Documents

- [Architecture](./architecture.md) defines system boundaries, data flow, persistence and synchronization direction, deployment constraints, and privacy invariants.
- [Design direction](./design.md) defines the retro-terminal visual language, TUI interaction and responsive layout, the read-only website, accessibility, and content style.
- [Command reference](./commands.md) lists every command, the terminal interface's keys, the optional display variables, and what the npm package requires.
- [Development guide](./development.md) explains prerequisites, commands, test boundaries, CI, and contribution workflow.
- [Free demo deployment](./deployment.md) explains the optional Render and Supabase environment, secret setup, validation, limitations, and removal.
- [Release artifacts](./releasing.md) defines reproducible native packaging, checksums, and tag publication.
- [Release validation](./release-validation.md) lists the checks a person runs by hand before a release, starting with the Windows run.
- [Roadmap](../ROADMAP.md) defines scope, delivery order, dependencies, and acceptance criteria.
- [Contributor guidance](../AGENTS.md) defines implementation, documentation, review, privacy, and Git workflow rules.
- [Contributing](../CONTRIBUTING.md) says how to report a bug, ask for a feature, and send a pull request.

`images/` holds the pictures the root README shows. They are renders of the
terminal interface's own frames with the synthetic sample data the snapshot
tests use, written by `TestWritePaintedPreviews` in `internal/tui`.

## Subsystem documentation

- [Internal package map](../internal/README.md)
- [Source adapters](../internal/adapters/README.md)
- [Autosync](../internal/autosync/README.md)
- [Pinned ccusage contract](../internal/adapters/ccusage/README.md)
- [Normalized usage contract](../internal/normalize/README.md)
- [Collection semantics](../internal/collect/README.md)
- [Model catalog and pricing](../internal/modelcatalog/README.md)
- [Source diagnostics](../internal/doctor/README.md)
- [Compatibility fixtures](../fixtures/README.md)
- [Server boundary](../server/README.md)
- [Website](../web/README.md)

## Where new documentation belongs

Use this directory for decisions shared by several packages or product areas. Use a subsystem README when a directory has multiple packages, important operational instructions, or a contract that developers need before reading implementation details. Use Go documentation comments for package APIs and comments next to non-obvious invariants.

Avoid creating a README in every leaf directory. A small package with a clear `doc.go` is easier to navigate than a second document that repeats its source. When a decision changes, update the closest authoritative document rather than adding a competing explanation elsewhere.
