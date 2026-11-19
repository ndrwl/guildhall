# Third-party notices

Guildhall's binary embeds the third-party files below, unmodified.
Each is vendored under `internal/web/vendor/<package>/` with its licence file and a `manifest.json` that pins its upstream version, source and every file's sha256; `TestVendorManifests` and the loader in `internal/web/vendored.go` refuse a file that does not match its pin ([decision record](docs/decisions/list-page-scripts.md#vendored-files)).
Work adapted into Guildhall's own source, rather than vendored, is credited in [NOTICE](NOTICE).

## uPlot 1.6.32

- Files: `internal/web/vendor/uplot/` holds `dist/uPlot.iife.min.js` and `dist/uPlot.min.css` from the package, with its `LICENSE`.
- Source: <https://registry.npmjs.org/uplot/-/uplot-1.6.32.tgz> (npm shasum `c800a63b432bad692d6d746f44f0882aa73a49ae`); project <https://github.com/leeoniya/uPlot>.
- Used for: the two 14-day charts in the stats strip of the Director's task list, `/`, inlined on that page only.
- Licence: MIT.

```text
The MIT License (MIT)

Copyright (c) 2022 Leon Sorokin

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```
