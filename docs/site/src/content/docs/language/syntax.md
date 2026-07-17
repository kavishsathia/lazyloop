---
title: Syntax
description: Current Hamster grammar, statements, expressions, and composition rules.
---

# Syntax

Hamster is brace-delimited and semicolon-terminated, so the same source works in files and ephemeral CLI strings. Whitespace and indentation are not semantically significant.

## Loop declarations

```text
loop    = goal | flow | foreach ;
goal    = "goal" [ identifier ] "{" { goal-statement } "}" ;
flow    = "flow" [ identifier ] "{" { flow-statement } "}" ;
foreach = "foreach" [ identifier ] identifier "in" expression "{" loop "}" ;
```

Only a `flow` may contain `step`. A step value may be an expression or an inline `goal`, `flow`, or `foreach`, which is how workflows nest without a separate call construct.

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

## Expressions

The current expression surface includes strings, numbers, booleans, lists, identifiers, member access, calls, named arguments, unary `!`, comparisons, equality, `&&`, and `||`.

For the normative embedded grammar, run:

```sh
hamster man grammar
```
