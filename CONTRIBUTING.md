# Contributing to Hamster

Thanks for helping make agent loops more enforceable, observable, and useful.
Hamster is still experimental, so small, well-tested changes are especially
valuable.

By participating, you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
Contributions submitted to this repository are licensed under the
[Apache License 2.0](LICENSE).

## Before opening a change

- Search existing issues before creating a new one.
- Use an issue for bugs, language proposals, integrations, or substantial
  architectural changes.
- Keep security reports private by following [SECURITY.md](SECURITY.md).
- Keep a pull request focused on one coherent outcome.

## Development setup

The core requires Go 1.25. The documentation website requires Node 22.12 or
newer and pnpm 10; the checked-in `.nvmrc` selects the tested Node release.

```sh
git clone git@github.com:kavishsathia/hamster.git
cd hamster
go mod download

nvm install
nvm use
corepack enable
pnpm --dir docs/site install --frozen-lockfile
```

## Making a change

1. Create a descriptive branch from `main`.
2. Add or update tests before relying on the implementation.
3. Keep runtime-owned verification separate from agent-owned work.
4. Update the reference catalog when syntax, commands, primitives, contracts,
   preconditions, postconditions, or guarantees change.
5. Commit generated documentation only where the repository explicitly tracks
   it; reference pages and `dist/` remain build artifacts.

Integration guarantees use the following vocabulary:

- **enforced**: Hamster independently checks the condition.
- **observed**: Hamster sees the event but cannot prove its complete effect.
- **declared**: the contract records a condition that is not yet enforceable.

Do not describe a tool call as an enforced external effect unless an
independent postcondition check supports it.

## Required checks

Run the checks relevant to your change. Before requesting review, the complete
suite should pass:

```sh
gofmt -w $(git ls-files '*.go')
go test ./...
go vet ./...
SITE_URL=https://docs.example.invalid pnpm --dir docs/site run build
git diff --check
```

The documentation build validates Astro content and regenerates the reference
pages from `docs/reference/catalog.yaml`.

## Pull requests

Explain the problem, the chosen design, and the evidence used to validate the
result. Call out changes to language semantics, hook enforcement, persistent
state, compatibility, or security boundaries. Screenshots are useful for
visible dashboard or documentation changes.

Maintainers may ask for a smaller change, stronger verification, or a design
discussion before merging. That is part of keeping the trusted kernel small.
