---
title: Syntax
description: Current Hamster grammar, statements, expressions, and composition rules.
---

# Syntax

Hamster is brace-delimited and semicolon-terminated, so the same source works in files and ephemeral CLI strings. Whitespace and indentation are not semantically significant.

## Loop declarations

```text
program = loop ;
loop    = goal | foreach ;
goal    = "goal" "{" { goal-statement } "}" [ "with" flow ] ;
flow    = "flow" "{" step { step } "}" ;
foreach = "foreach" identifier "in" expression "{" loop "}" ;
```

Loops are anonymous. A `flow` is not an independent loop: it attaches to the
preceding goal with `with flow` and contains one or more steps. A step value may
be an expression or an inline `goal` or `foreach`, which is how workflows nest
without a separate call construct.

## Statements

```text
step name = expression;
verify name = expression;
limit kind expression;
allow expression;
disallow expression;
return expression;
```

Limits are prospective: accounting starts only when execution reaches the clause. A `return` supplies the normal completion value; it does not bypass verification.

Verifiers run in source order and stop at the first failure. A declarative goal
probes after every hook-visible tool call. A goal with an attached flow probes
only after the complete flow cycle, then either completes or starts the next
cycle at its first step.

## Expressions

The current expression surface includes strings, numbers, booleans, lists, identifiers, member access, calls, named arguments, unary `!`, comparisons, equality, `&&`, and `||`.

For the normative embedded grammar, run:

```sh
hamster man grammar
```
