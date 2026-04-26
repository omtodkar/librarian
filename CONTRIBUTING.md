# Contributing to Librarian

Thanks for your interest in improving Librarian! Contributions in any shape are welcome — bug reports, new file-format handlers, assistant-platform integrations, grammar polish, docs, tests, ideas.

This guide covers the essentials. For architecture + code layout, read [docs/development.md](docs/development.md).

## Quick paths to contribute

| What you want to do | Start here |
|---|---|
| Report a bug | Open a GitHub issue with a minimal repro (file content + command + expected/actual) |
| Fix a typo or docs gap | Small PRs against `main` are welcome without a pre-discussion |
| Add a new file-format handler | [docs/handlers.md → Where to add a new format](docs/handlers.md#where-to-add-a-new-format) |
| Add a new AI-assistant platform | Follow the Aider / OpenCode precedent under `internal/install/` — open a GitHub issue first to discuss scope |
| Work on a larger feature | Open a GitHub issue first so we can sync on approach before you write the code |

## Development setup

```sh
git clone <fork-url> && cd librarian
make test        # full suite, no network needed
make build       # produces ./librarian
```

**Prerequisites:** Go 1.25+, CGo enabled (the default). No Docker, no system libs.

End-to-end exercises need an embedding provider:
- `LIBRARIAN_EMBEDDING_API_KEY` for Gemini, or
- A local OpenAI-compatible server (LM Studio, Ollama) and matching `.librarian/config.yaml`.

See [docs/development.md](docs/development.md) for the full project layout + test-file map.

## Testing

- `make test` must pass before you open a PR. CI runs the same target.
- New behaviour needs a regression test. Look at the closest sibling test for style — e.g. `internal/indexer/handlers/code/python_test.go` for grammar changes, `internal/install/install_test.go` for platform integrations.
- Avoid tests that depend on network, a local server, or an API key. Fixtures go in-package (ZIPs built at runtime, PDFs committed under `testdata/` when unavoidable — see the pattern in `internal/indexer/handlers/pdf/testdata/`).

## Code style

Standard Go conventions apply:

- `gofmt` (or `goimports`) on save — CI will reject unformatted code.
- `go vet ./...` must pass.
- `go mod tidy` before committing if you touched dependencies.
- Follow [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) — naming, error handling, package boundaries, doc comments on exported identifiers.

Plus a few project-specific preferences:

- **Default to no comments.** Comments explain *why*, not *what*. If removing a comment wouldn't confuse a future reader, remove it.
- **No speculative code.** Don't add features, abstractions, or error handling for scenarios that can't happen. Three similar lines beats a premature abstraction.
- **Named identifiers over magic numbers / strings.** If a constant is being tuned, give it a name.

## Pull-request flow

1. Fork the repo and branch off `main` (use a descriptive branch name, e.g. `feat/kiro-integration` or `fix/pdf-bookmark-depth`).
2. Make your changes; run `make test` and `go vet ./...`.
3. Commit messages: imperative one-line subject (max ~72 chars), body explaining *why* if non-obvious. Keep commits focused — squash trivial fixups before pushing.
4. Push to your fork and open a PR against `main`. Keep the PR focused — one concern per PR.
5. Link any issue the PR closes (e.g. `Closes #42`) in the description.
6. Address review feedback by pushing follow-up commits; maintainers will squash on merge if appropriate.

## Issue tracker

Use [GitHub Issues](../../issues) for bug reports, feature requests, and discussion. Before filing a bug, search existing issues; before opening a PR for anything larger than a small fix, open an issue so we can align on approach.

## Responsible Reporting

If you discover a security vulnerability while working on the codebase — even a theoretical one — **do not open a public issue or PR**. Follow the coordinated disclosure process described in [SECURITY.md](SECURITY.md). In short: report privately via GitHub Private Vulnerability Reporting (or email if PVR isn't enabled), give maintainers the 90-day window, and we will credit you in the release notes.

If the issue is a regular bug (not a security concern), open a GitHub issue as normal.

## License

By contributing, you agree that your contributions are licensed under the MIT License (see [LICENSE](LICENSE)).

## Community + conduct

We follow the [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/). Be kind, be specific, assume good faith.

Questions or want to chat about a direction before writing code? Open a GitHub discussion or issue describing what you're thinking about.
