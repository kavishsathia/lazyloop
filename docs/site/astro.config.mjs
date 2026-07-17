import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: process.env.SITE_URL,
  integrations: [
    starlight({
      title: 'Hamster',
      description: 'Enforceable loops for Codex.',
      customCss: ['./src/styles/reference.css'],
      social: [],
      lastUpdated: true,
      sidebar: [
        {
          label: 'Start here',
          items: [
            { label: 'Overview', slug: 'index' },
            { label: 'Language model', slug: 'language/model' },
            { label: 'Syntax', slug: 'language/syntax' },
          ],
        },
        {
          label: 'Reference',
          items: [{ autogenerate: { directory: 'reference' } }],
        },
        {
          label: 'Contributing',
          items: [{ label: 'Document an integration', slug: 'contributing/integrations' }],
        },
      ],
    }),
  ],
});
