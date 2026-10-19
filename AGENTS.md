# Agent Instructions

Full guidance for AI agents is in [.github/copilot-instructions.md](.github/copilot-instructions.md). Read it before making changes.

## Engineering expectations (required)

- Follow SOLID and DRY pragmatically: keep responsibilities clear, reuse existing helpers, and introduce interfaces at real boundaries rather than adding speculative abstractions.
- Follow the existing structured logging conventions, levels, and field names. Record actionable failures and recovery outcomes with operation context; never log credentials or silently swallow errors.
- Write concise comments explaining non-obvious invariants, tradeoffs, platform behavior, and recovery logic so contributors can safely extend the code. Do not narrate self-explanatory code.
- Implement and commit coherent, tested slices with their related documentation. Preserve supported upgrade paths and mixed-version behavior; test failures and recovery, not just happy paths.
- Flag discovered out-of-scope bugs and architectural risks in the completion summary and relevant existing issue/design documentation. Distinguish implemented fixes, deferred work, and unresolved decisions; do not hide them or expand scope silently.

## Keep the docs site in sync (required)

The canonical documentation lives in the **Printmaster-Org/docs.printmaster.work** repo. It is normally checked out next to this repo at `../docs.printmaster.work`.

- Any change to behavior, configuration, APIs, vendor/SNMP mappings, security, or UI must also update the relevant pages there:
  - guides and developer pages: `content/`
  - API contracts: `static/openapi/`
- Review documentation impact for every feature, fix, removal, and deprecation as part of the same task. Keep source-local READMEs and examples accurate too. If no documentation change is needed, explicitly explain why in the PR or completion summary.
- Maintain documentation structure as work progresses: add new canonical pages where useful, reorganize or consolidate existing material, and remove obsolete legacy plans/docs when appropriate rather than only appending to them. Keep current plans aligned with agreed scope and implemented reality; clearly distinguish pending, completed, deferred, and historical work. Preserve still-relevant decisions and recovery guidance, and update affected links, navigation, redirects and documentation tests when moving or removing pages.
- The legacy documentation trees have been removed from this repo. Do not recreate them or duplicate guides here; retain only concise source-local READMEs, contribution/security entry points, agent instructions, and licenses. On imported docs-site pages, leave the `source`/`sourceCommit` front matter as it is (it records import provenance, and the docs tests check it), but point any source-code links at the current commit.
- Commit the program change and the docs change as a linked pair, and mention both in your summary.
- If the docs checkout is unavailable, say so and list the exact pages that need updating. Do not report the work as complete.

## Commit messages (required)

Use [Conventional Commits](https://www.conventionalcommits.org/) in this repo and in docs.printmaster.work. The `.githooks/commit-msg` hook rejects other subjects. Enable it once per clone with `git config core.hooksPath .githooks`, and never bypass it with `--no-verify`.

```
type(scope): imperative summary, lowercase, no trailing period

Why the change was needed and what it changes (wrap ~72 cols).

Docs: Printmaster-Org/docs.printmaster.work@<sha>   (or "Docs: none — <reason>")
Co-authored-by: ...                                  (trailers last)
```

- Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`, `style`, `revert`, `ux`. Add `!` after the type or scope for breaking changes.
- Scope is optional, lowercase, and names the area touched: `agent`, `server`, `web`, `scanner`, `storage`, `api`, `ci`, `deps`, or a vendor such as `epson`.
- Keep the subject at 100 characters or fewer, with a blank line before the body.
- In the paired docs commit, reference the program commit the same way: `Implementation: Printmaster-Org/printmaster@<sha>`.
