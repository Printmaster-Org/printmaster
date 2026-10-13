# Agent Instructions

Full guidance for AI agents is in [.github/copilot-instructions.md](.github/copilot-instructions.md). Read it before making changes.

## Keep the docs site in sync (required)

The canonical documentation lives in the **Printmaster-Org/docs.printmaster.work** repo. It is normally checked out next to this repo at `../docs.printmaster.work`.

- Any change to behavior, configuration, APIs, vendor/SNMP mappings, security, or UI must also update the relevant pages there:
  - guides and developer pages: `content/`
  - API contracts: `static/openapi/`
- Updating only this repo's `docs/` folder is **not** enough. Keep both in step. On imported pages, leave the `source`/`sourceCommit` front matter as it is (it records import provenance, and the docs tests check it), but point any source-code links at the current commit.
- Commit the program change and the docs change as a linked pair, and mention both in your summary.
- If the docs checkout is unavailable, say so and list the exact pages that need updating. Do not report the work as complete.
