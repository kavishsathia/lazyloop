# Hamster documentation website

This is the standalone, statically hosted Hamster documentation product. It
does not run inside the Hamster daemon and shares no listener or lifecycle with
the runtime observability console.

Reference pages are generated from `../reference/catalog.yaml`, which is also
embedded into the Go CLI for `hamster man`. Hand-authored guides live under
`src/content/docs`.

## Development

```sh
pnpm install
pnpm dev
```

## Production build

The hosting pipeline must set the public origin so Astro emits correct
canonical and sitemap URLs:

```sh
SITE_URL=https://docs.your-domain.example pnpm build
```

Deploy the resulting `dist/` directory with any static hosting provider. The
generated site includes Pagefind search and requires no server-side runtime.
