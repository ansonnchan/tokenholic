# Contributing

Thanks for looking. Bug reports, ideas, and pull requests are all welcome.

## Found a bug, or want something added?

Open an [issue](https://github.com/ansonnchan/tokenholic/issues). That covers:

- a bug or a wrong number;
- a coding agent you would like counted;
- a feature, or something confusing in the terminal interface or the website.

For a bug, these help:

- what you ran, what you expected, and what happened;
- `tokenholic --version` and your operating system;
- the output of `tokenholic doctor`. It reports the health of each source
  and of local storage and never prints a path or any record.

Please do not paste prompts, transcripts, source code, file paths, project or
repository names, or credentials into an issue. tokenholic is built never to
collect them, and an issue is public.

For a wrong total, the numbers alone are enough: the agent, the day, and what
you expected against what tokenholic shows.

## Sending a pull request

1. Read [AGENTS.md](AGENTS.md). It is the contributor guide for people and for
   coding agents: product boundaries, architecture rules, style, and the Git
   workflow.
2. For anything larger than a fix, open an issue first so the approach can be
   agreed before you write it.
3. Branch as `<type>/<short-description>`, such as `fix/codex-fork-totals`,
   and use conventional commit subjects, such as
   `fix(collect): count a forked rollout once`.
4. Run the checks before pushing:

   ```sh
   make check             # formatting, vet, race tests, build
   pnpm check && pnpm test  # npm packaging and website tests
   ```

   The [development guide](docs/development.md) covers prerequisites, the
   PostgreSQL tests, and running the API locally.

### Rules that are easy to miss

- **Fixtures are synthetic.** Never copy a real agent record into a fixture, a
  test, a commit, or a pull request, even redacted. Build the smallest record
  by hand. See [fixtures/README.md](fixtures/README.md).
- **Unknown is not zero.** A value a source does not report stays unknown.
- **No guessed prices.** A price comes from the pinned snapshot or an override
  that cites the provider's own pricing page and the date it was read.
- **Interface changes come with snapshots.** A change to the terminal
  interface updates the golden snapshots at the documented widths, with and
  without color. See [docs/design.md](docs/design.md).

### Adding an agent

A new agent needs evidence before code: where it keeps usage records, which
token categories those records carry, and how a model is named. Open an issue
with that, described in words rather than pasted records. The
[adapter guide](internal/adapters/README.md) explains what an adapter owns.
