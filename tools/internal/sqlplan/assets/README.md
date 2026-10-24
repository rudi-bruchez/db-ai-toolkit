# Embedded drawing library

`qp.min.js`, `qp.css` and `qp_icons.png` are the build of
[rudi-bruchez/query-plan](https://github.com/rudi-bruchez/query-plan) at commit
`a6c23e3`, a fork of html-query-plan (MIT). To refresh them, build that repository and
copy `dist/qp.min.js`, `css/qp.css` and `css/qp_icons.png` here.

The operator icons in `qp_icons.png` are those of the Microsoft documentation
([MicrosoftDocs/sql-docs](https://github.com/MicrosoftDocs/sql-docs)), © Microsoft,
licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/), with their
light background made transparent. The warning, parallel and batch overlays come from
the Fat Cow "Farm Fresh" icon pack.

## PostgreSQL plans

`pev2.umd.js` and `pev2.css` are [pev2](https://github.com/dalibo/pev2) 1.24.0 by
Dalibo, from its npm package, under the PostgreSQL License (notice in `pev2-LICENSE`).
pev2 runs on `vue.global.prod.js` (Vue 3.5.43, MIT) and the stylesheet
`bootstrap.min.css` (Bootstrap 5.3.8, MIT); the d3, highlight.js and Font Awesome code
it uses is bundled in `pev2.umd.js`. To refresh them:
`npm pack pev2@<version> vue@<version> bootstrap@<version>` and copy the same files
from each package's `dist/`.
