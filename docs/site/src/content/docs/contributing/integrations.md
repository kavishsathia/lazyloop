---
title: Document an integration
description: How integrations declare discoverable methods, prerequisites, guarantees, evidence, and failures.
---

# Document an integration

Every integration primitive is a catalog entry in `docs/reference/catalog.yaml`. The entry is the product contract: both `hamster man` and this website consume it.

## Required contract

An entry names its symbol, kind, summary, syntax, semantics, release, and stability. Integration primitives should additionally declare:

- **Preconditions** with the component that checks each one.
- **Postconditions** with a guarantee level and concrete evidence.
- **Failures** with stable codes and actionable remediation.
- **Examples** marked `parse` when Hamster source should be tested automatically.
- **Related symbols** for local discovery without a search round-trip.

```yaml
- id: git.push
  title: Push a ref
  kind: primitive
  syntax: ['git.push(remote, ref)']
  preconditions:
    - predicate: authenticated remote exists
      checked: integration
  postconditions:
    - predicate: remote ref equals local ref
      guarantee: enforced
      evidence: remote ref lookup after push
```

Do not label call occurrence as an enforced external effect. If Hamster only observes that a tool was invoked, use `observed`. An integration earns `enforced` by independently checking the postcondition.

## Validation

```sh
go test ./internal/reference
pnpm --dir docs/site build
```

The Go test validates the schema, aliases, guarantee vocabulary, language keyword coverage, and parse-tested examples. The site build regenerates every reference page from the catalog.
