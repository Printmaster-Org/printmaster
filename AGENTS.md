# Agent Instructions

Full guidance for AI agents is in [.github/copilot-instructions.md](.github/copilot-instructions.md). Read it before making changes.

## Engineering expectations (required)

- Follow SOLID and DRY pragmatically: keep responsibilities clear, reuse existing helpers, and introduce interfaces at real boundaries rather than adding speculative abstractions.
- Follow the existing structured logging conventions, levels, and field names. Record actionable failures and recovery outcomes with operation context; never log credentials or silently swallow errors.
- Write concise comments explaining non-obvious invariants, tradeoffs, platform behavior, and recovery logic so contributors can safely extend the code. Do not narrate self-explanatory code.
- Implement and commit coherent, tested slices. Include related documentation in the same slice when the required impact check finds a reader-facing update is needed. Preserve supported upgrade paths and mixed-version behavior; test failures and recovery, not just happy paths.
- Flag discovered out-of-scope bugs and architectural risks in the completion summary and relevant existing issue/design documentation. Distinguish implemented fixes, deferred work, and unresolved decisions; do not hide them or expand scope silently.

## Documentation impact (required check)

The canonical public documentation lives in **Printmaster-Org/docs.printmaster.work**, normally checked out next to this repo at `../docs.printmaster.work`. Assess documentation impact for every change; **not every code change requires a documentation edit**.

- Update the canonical docs when a change alters user- or operator-visible behavior, a task's steps or outcome, supported platforms, configuration/defaults, permissions or security expectations, API/protocol contracts, deployment/upgrade/recovery guidance, or another fact readers rely on. Keep the edit focused on what readers need to know or do.
- An internal refactor, test-only change, or build/CI maintenance change normally needs no site edit when it does not change reader-facing behavior or instructions. Keep code-owned examples, test fixtures, contributor/security entry points, and concise source-local READMEs accurate when those artifacts themselves change.
- The docs site is for user/operator guidance and deliberately maintained technical references. Do not add implementation diaries, commit-by-commit audit notes, engineering backlogs, or internal progress reports there. Do not duplicate full guides in this repo or recreate the removed legacy documentation trees.
- For API changes, update the appropriate public contract only when the documented contract or its guarantees change. Verify affected behavior against implementation. Preserve imported `source`/`sourceCommit` provenance; update source-code links when they are part of the change.
- State the documentation disposition in the completion summary: pages/contracts updated, or a concise reason no public-doc change was needed. If a required docs checkout is unavailable, identify the exact pages that need updates and report the blocker.
- When an implementation change does require a paired docs change, link the commits one-way: the docs commit references the implementation SHA; the program commit does not reference the docs SHA. Mention both in the summary.

## Commit messages (required)

Use [Conventional Commits](https://www.conventionalcommits.org/) in this repo and in docs.printmaster.work. The `.githooks/commit-msg` hook rejects other subjects. Enable it once per clone with `git config core.hooksPath .githooks`, and never bypass it with `--no-verify`.

```
type(scope): imperative summary, lowercase, no trailing period

Why the change was needed and what it changes (wrap ~72 cols).

Co-authored-by: ...                                  (trailers last)
```

When documentation changes are required, the program commit must not contain a reciprocal docs SHA. After the program commit is created, reference it from the paired docs commit using the `Implementation:` trailer below. This one-way link avoids circular commit references.

- Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`, `style`, `revert`, `ux`. Add `!` after the type or scope for breaking changes.
- Scope is optional, lowercase, and names the area touched: `agent`, `server`, `web`, `scanner`, `storage`, `api`, `ci`, `deps`, or a vendor such as `epson`.
- Keep the subject at 100 characters or fewer, with a blank line before the body.
- In the paired docs commit, reference the program commit: `Implementation: Printmaster-Org/printmaster@<sha>`.
