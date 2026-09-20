# Maestro brand assets

The September 2026 palette follows the OpenTUI workspace. The conductor mark
and custom wordmark retain their original vector geometry.

| Color | Value | Use |
| --- | --- | --- |
| Ink | `#0B0C10` | Main background |
| Panel | `#101116` | Secondary background |
| Charcoal | `#191A22` | Raised surfaces |
| Ivory | `#EEECE5` | Mark, wordmark and primary text |
| Lavender | `#B7A5E6` | Chevron and active accents |
| Light lavender | `#CBBBF2` | Accent gradient highlight |
| Deep lavender | `#8973B8` | Chevron on light backgrounds |

## Exports

- `exports/maestro-logo-primary.svg` and `.png`: stacked logo on an ink background.
- `exports/maestro-logo-horizontal.svg`: horizontal lockup.
- `exports/maestro-logo-transparent.svg` and `.png`: stacked logo with transparency.
- `exports/maestro-app-icon-dark.svg` and `.png`: dark app icon.
- `exports/maestro-app-icon-light.svg` and `.png`: light app icon.

SVGs are the editable source of truth. The PNG exports are rendered directly
from those vectors. On macOS, regenerate an export without opening an app:

```sh
swift scripts/render-brand.swift \
  docs/brand/exports/maestro-logo-primary.svg \
  docs/brand/exports/maestro-logo-primary.png
```

## README images

`../assets/maestro-cover.png` is a promotional composite made with the built-in
ImageGen tool: the updated logo in front of the terminal capture with Gaussian
blur and a dark overlay. The complete prompt is in [cover-prompt.txt](cover-prompt.txt).
Its inputs were `../assets/tui-contract.png` and the primary logo PNG above.
The generated cover is not presented as an unaltered screenshot.

The three `../assets/tui-*.png` images are unchanged exports from the production
Go host and compiled OpenTUI running in a real pseudo-terminal. They show an
isolated example project at 160×48 (contract and connections) and 80×24
(monochrome contract). They were captured during the earlier TUI qualification;
no new TUI was launched to prepare this README.

Asset hashes and source references are recorded in [assets.json](assets.json).
