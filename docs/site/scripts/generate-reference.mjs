import { mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import YAML from 'yaml';

const here = dirname(fileURLToPath(import.meta.url));
const catalogPath = resolve(here, '../../reference/catalog.yaml');
const outputDirectory = resolve(here, '../src/content/docs/reference');
const catalog = YAML.parse(await readFile(catalogPath, 'utf8'));

if (catalog.schema !== 'hamster.reference/v1alpha1') {
  throw new Error(`Unsupported reference schema: ${catalog.schema}`);
}

await rm(outputDirectory, { recursive: true, force: true });
await mkdir(outputDirectory, { recursive: true });

for (const entry of catalog.entries) {
  const slug = entry.id.replaceAll('.', '-');
  await writeFile(resolve(outputDirectory, `${slug}.md`), render(entry));
}

await writeFile(
  resolve(outputDirectory, 'index.md'),
  `---\ntitle: Reference index\ndescription: Every language, process, command, and primitive contract in Hamster.\nsidebar:\n  order: 0\n---\n\n# Reference index\n\nThe catalog below is generated from the same structured source embedded in \`hamster man\`.\n\n| Symbol | Kind | Contract |\n| --- | --- | --- |\n${catalog.entries.map((entry) => `| [\`${entry.id}\`](./${entry.id.replaceAll('.', '-')}/) | ${entry.kind} | ${escapeTable(entry.summary)} |`).join('\n')}\n`,
);

function render(entry) {
  const syntax = entry.syntax.map((line) => `\`${line}\``).join('  \n');
  const aliases = (entry.aliases ?? []).map((alias) => `\`${alias}\``).join(', ') || '—';
  const preconditions = conditionTable(entry.preconditions ?? [], false);
  const postconditions = conditionTable(entry.postconditions ?? [], true);
  const semantics = entry.semantics.map((line) => `- ${line}`).join('\n');
  const failures = (entry.failures ?? []).length
    ? `| Code | Condition | Remediation |\n| --- | --- | --- |\n${entry.failures.map((failure) => `| \`${failure.code}\` | ${escapeTable(failure.when)} | ${escapeTable(failure.remediation)} |`).join('\n')}`
    : 'No operation-specific failures are currently declared.';
  const examples = (entry.examples ?? []).map((example) => `### ${example.title}\n\n\`\`\`${example.language === 'hamster' ? 'text' : example.language}\n${example.source.trim()}\n\`\`\``).join('\n\n');
  const related = (entry.related ?? []).map((id) => `[\`${id}\`](./${id.replaceAll('.', '-')}/)`).join(' · ') || '—';

  return `---
title: ${yamlString(entry.id)}
description: ${yamlString(entry.summary)}
sidebar:
  label: ${yamlString(entry.id)}
  badge:
    text: ${yamlString(entry.kind)}
    variant: note
---

<div class="reference-meta">
  <span>${entry.kind}</span><span>${entry.stability}</span><span>since ${entry.since}</span>
</div>

# ${entry.title}

${entry.summary}

**Aliases:** ${aliases}

## Syntax

${syntax}

## Preconditions

${preconditions}

## Postconditions and guarantees

${postconditions}

## Semantics

${semantics}

## Failure modes

${failures}

## Examples

${examples || 'No example is declared yet.'}

## See also

${related}
`;
}

function conditionTable(conditions, includesGuarantee) {
  if (!conditions.length) return 'No additional preconditions are declared.';
  const headers = includesGuarantee
    ? '| Predicate | Meaning | Guarantee | Evidence |\n| --- | --- | --- | --- |'
    : '| Predicate | Meaning | Checked by |\n| --- | --- | --- |';
  const rows = conditions.map((condition) => includesGuarantee
    ? `| \`${condition.predicate}\` | ${escapeTable(condition.description)} | <span class="guarantee guarantee-${condition.guarantee}">${condition.guarantee}</span> | ${escapeTable(condition.evidence ?? '—')} |`
    : `| \`${condition.predicate}\` | ${escapeTable(condition.description)} | ${condition.checked ?? '—'} |`);
  return [headers, ...rows].join('\n');
}

function escapeTable(value) {
  return String(value).replaceAll('|', '\\|').replaceAll('\n', ' ');
}

function yamlString(value) {
  return JSON.stringify(value);
}
