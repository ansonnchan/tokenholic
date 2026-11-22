# Pinned ccusage runner

tokenholic has its own readers for Claude and Codex. For other coding agents
it runs [ccusage](https://github.com/ccusage/ccusage), a third-party program
that already understands their local records, and treats its output as an
external contract. This package is that boundary: what is pinned, how it is
run, what is accepted back, and how a report becomes stored usage.

Claude and Codex are never read this way. Their native readers give
per-minute usage and cache-write lifetimes, and reading them twice would count
the same usage twice.

## What is pinned

[`ccusage.lock.json`](../../../ccusage.lock.json) records the one release
tokenholic runs:

- the version, `20.0.51`, never a range;
- the argument array every report uses;
- the agents covered: GitHub Copilot CLI, Gemini CLI, Grok Build CLI,
  OpenCode, and Qwen Code;
- for each platform, the npm package, its npm integrity, and the SHA-256 of
  the executable inside it.

ccusage 20 is a native program. Its npm package is a small launcher over one
executable per platform, and tokenholic runs that executable directly, so no
Node process and no shell is involved at run time.

The same pin is compiled into tokenholic. `ValidateLock` fails a release
whose lock file differs from the compiled contract, and
`scripts/platforms.test.mjs` holds the npm manifest, the lockfile, and the
launcher to the lock. To move to another release, change all of them
together and rebuild the fixtures from that release.

## How a report is run

`Runner.Daily` runs `<agent> daily --json --no-cost --offline --timezone UTC`:

| Guard | How |
| --- | --- |
| Only the pinned build | The file's SHA-256 is compared with the compiled digest before anything starts. Any other file is refused and never executed |
| No shell | The executable is started directly with an argument array. The agent name comes from a fixed list |
| Time limit | 30 seconds per agent; the process is killed when it passes |
| No network | `--offline` and `CCUSAGE_OFFLINE=1` forbid fetching, and every proxy variable points at a closed local port, so a request that slipped through would fail on this machine |
| Allowlisted environment | Only the home variables and the locations the agents themselves document (`XDG_DATA_HOME`, `COPILOT_HOME`, `COPILOT_OTEL_FILE_EXPORTER_PATH`, `GROK_HOME`) are passed. No credential, proxy, or tokenholic setting reaches it |
| The user's ccusage settings do not apply | ccusage reads its configuration from the working directory first and stops at the first file. Each run starts in a private temporary directory holding an empty one, so a date filter or timezone in the user's own ccusage configuration cannot change what is reported |
| Nothing of its output is kept but counts | Standard error is discarded unread. Errors are fixed sentinel values that never repeat ccusage's text, which can name local paths |

One agent's failure is that agent's alone: each report is a separate run with
its own result.

## What it reads

ccusage reads each agent's local session records and returns token counts per
UTC day and model. tokenholic receives those counts and nothing else: no
prompt, response, path, project, or session name.

| Agent | Records ccusage reads |
| --- | --- |
| GitHub Copilot CLI | `~/.copilot/session-state/*/events.jsonl` and `~/.copilot/otel/**/*.jsonl`, or under `COPILOT_HOME`; the file named by `COPILOT_OTEL_FILE_EXPORTER_PATH` |
| Gemini CLI | `~/.gemini/tmp/*/chats/*.json` and `*.jsonl` |
| Grok Build CLI | `~/.grok/sessions/**/updates.jsonl`, or under `GROK_HOME` |
| OpenCode | `~/.local/share/opencode`, or under `XDG_DATA_HOME`. It also reads Claude Code's session records, to leave out turns OpenCode ran through Claude Code and Claude Code already recorded |
| Qwen Code | `~/.qwen/projects/*/chats/*.jsonl` |

Source: the ccusage guide for each agent at the pinned tag,
<https://github.com/ccusage/ccusage/tree/v20.0.51/docs/guide>, retrieved
2026-11-21.

## On by default, and how to turn it off

Running ccusage is on by default (decided November 21, 2026). It is
third-party code that reads local agent records, so the choice is the
user's:

```sh
tokenholic ccusage status   # whether it is on, and whether this install can run it
tokenholic ccusage off      # tokenholic stops running it
tokenholic ccusage on
```

The choice is saved in `preferences.json` beside the usage database and
applies to every later run. When that file cannot be read, ccusage does not
run.

## Where the executable comes from

| Install | ccusage |
| --- | --- |
| `npm install -g @tokenholic/cli` or `npx @tokenholic/cli` | Installed with it: the launcher package pins the six platform packages at the exact version, and npm installs the one for this system |
| A release archive or a build from source | Absent. `tokenholic doctor` says so and names the agents that are not counted. They are never shown as zero |

The npm launcher tells the native program where the executable is through
`TOKENHOLIC_CCUSAGE_PATH`. Nobody has to set that variable: the launcher sets
it. Someone running a release archive may point it at a ccusage 20.0.51
executable they installed themselves; the digest check applies all the same.

tokenholic never downloads ccusage itself.

A scheduled run does not start through the launcher. `tokenholic autosync
enable` therefore writes the location into the schedule, beside the other
variables a scheduled run needs. The location is the npm install's own, so
after an upgrade, or once an `npx` cache is cleared, the scheduled run
reports the agents as unavailable until autosync is enabled again. Their
stored usage is kept meanwhile.

## The output contract

`ParseDailyReport` accepts exactly the daily report of the pinned release and
rejects the whole report otherwise: unknown or duplicate fields, a missing,
negative, fractional, or textual count, an invalid or repeated date, a model
breakdown that exceeds its day, a total smaller than its categories, and
report totals that differ from the rows. ccusage's cost and pricing fields are
declared so a report carrying them still decodes, and are never read:
tokenholic prices usage itself.

Three properties of the report shape what a caller may conclude:

- **A zero is not a known zero.** ccusage writes `0` for a category an
  agent's records do not carry. Whether a zero means "none" or "not reported"
  depends on the agent.
- **A day's total can exceed its four categories.** Gemini CLI, OpenCode, and
  Qwen Code report reasoning tokens apart from output; ccusage adds them to
  `totalTokens` only. `Day.Extra` is that difference. It exists per day, not
  per model.
- **`unknown` is a placeholder.** ccusage writes the model name `unknown`
  when a record names no model.

## From a report to stored usage

`Profile.Observations` turns a validated report into day totals: one record
per agent, UTC day, and model, filed at 00:00 UTC of that day and holding the
day's whole value so far. The identity is the agent, the day, and the model,
so a later report for the same day replaces the record instead of adding to
it. Every record is marked `derived`: a day total is a sum ccusage computed,
never a number the agent wrote.

Usage that has no model to belong to is kept as one record per day with an
unknown model, so every token ccusage counted is counted and none is credited
on a guess: usage under the placeholder name `unknown`, any part of a day no
model breakdown accounts for, and the day's extra tokens when more than one
model shared the day. When exactly one model used the day, its extra tokens
are that model's and are added to its output, where reasoning belongs.

### What each agent's totals cover

These are the capability declarations in `Profiles`, read from how the pinned
release maps each agent's records (its guide and parser for each agent at
<https://github.com/ccusage/ccusage/tree/v20.0.51>, retrieved 2026-11-21).

| Agent | Uncached input | Cache read | Cache write | Output | Limits |
| --- | --- | --- | --- | --- | --- |
| GitHub Copilot CLI | Derived: input less both cache buckets | Reported | Reported | Reported, reasoning inside | Usage is known when a session shuts down or checkpoints. ccusage drops a `-1m` suffix from model names, so the name is ccusage's, not the record's. Copilot's dotted model names are not on a provider price list and stay unpriced |
| Gemini CLI | Derived: input less cached tokens, plus tool tokens | Reported | Not recorded: unknown, never zero | Reported; thought tokens join it only on a single-model day | A record that names no model is dropped by ccusage and is not counted |
| Grok Build CLI | Derived: input less cache reads and writes | Reported | Reported | Reported, reasoning inside | Only completed turns are recorded. A session killed mid-turn is not counted. Its model names, such as `grok-4.5-build`, are not on xAI's price list and stay unpriced |
| OpenCode | Reported | Reported | Reported | Reported; reasoning joins it only on a single-model day | Turns OpenCode ran through Claude Code are left out, because Claude Code recorded them and the native reader counts them. Model names carry no provider |
| Qwen Code | Reported | Reported | Not recorded: unknown, never zero | Reported; thought tokens join it only on a single-model day | ccusage reports the prompt count as input and the cached count as cache read without subtracting. If Qwen's prompt count includes cached tokens, they are counted in both. This was not verified against real records |

Shared by all five:

- **Day-level.** One total per UTC day. Where usage fell against a local
  midnight is approximate, and in a timezone behind UTC it shows under the
  previous local date until the UTC date changes.
- **No cache-write lifetimes, service tier, or prompt size.** A cache write is
  priced at the base rate and makes the estimate a lower bound. A model whose
  price depends on prompt length stays unpriced.
- **Reasoning is never reported per model.** It is unsupported as a category
  everywhere: inside output where the agent counts it there, and otherwise
  handled as above.
- **A day that shrinks is still the day's value.** If an agent prunes old
  sessions, a day that is still reported reads lower and the stored value
  follows it. A day that is no longer reported at all keeps its stored value.
- **Coding agents only.** Each entry is the terminal coding agent of that
  name. Nothing here reads Gemini, Copilot, Grok, or Qwen chat products.

None of the five was installed on the machine this was built on. The
fixtures prove what the pinned release emits for records in the shapes its own
parsers and tests describe; they do not prove that a current release of each
agent still writes those shapes. A first run against real records should end
in a newly built sanitized fixture.

## Reading an agent

`collect.DayLevel` reads the agents during a scan, and `tokenholic doctor`
uses the same reader:

1. It looks for each agent's record folder (`adapters.DayLevelRoots`). An
   agent with no folder is `not found`, and ccusage is not run for it.
2. For an agent that is present, it runs the pinned report. A valid report is
   `detected`, with the number of days reported.
3. An agent that is present but cannot be read is `unavailable` with its
   reason (ccusage is turned off, this install has none, or the executable
   is not the pinned build) or `error` (malformed output, time limit,
   failure to run). Either way its stored usage is left as it was. It is
   never replaced by zero.

One agent's outcome never affects another's, or the native sources.

## Fixtures

`fixtures/ccusage/<agent>-daily.json` is the pinned release's real output for
hand-built synthetic records: a temporary home directory was filled with
invented sessions, and the pinned executable was run against it. No real
usage was read. The expected numbers in `report_test.go` and the expected day
totals in `observe_test.go` are worked out from those invented records, not
copied from the report. `empty-daily.json` and
`qwen-empty-daily.json` are the two shapes of a report with no usage.

`claude-daily.json` and `codex-daily.json` predate the runner. They record the
shape of reports tokenholic never requests, and are kept only as a reference
for comparing the native readers by hand.

Tests never execute ccusage and never read a real home directory. The runner
tests start the test binary itself as a stand-in.

## Diagnostics

`tokenholic doctor` reports the runner and each agent:

| Line | Meaning |
| --- | --- |
| `ccusage: ready (pinned 20.0.51, executable verified)` | The digest matched |
| `ccusage: not available in this install` | No executable came with this install |
| `ccusage: refused` | A file exists but is not the pinned build. It was not run |
| `ccusage: turned off` | The user's choice. Nothing was read or run |

Each agent then has a line under `Sources:`: `detected (N days of usage)`,
`not found`, `unavailable: <reason>; not counted`, or `error: <reason>; not
counted`. The SOURCES view shows the same states, names the agents `by day`,
and explains below the table why an agent is not counted.
