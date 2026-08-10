# Third-Party Notices

Maestro includes third-party software and data. Maestro's own [MIT license](LICENSE) does not replace the terms that apply to those materials. The exact upstream texts distributed with the release are under [`LICENSES/`](LICENSES/); [`LICENSES/manifest.json`](LICENSES/manifest.json) pins every file by SHA-256.

## Historical Crush provenance (not shipped in v1)

Maestro's Git history contains four TUI components adapted from [Charmbracelet Crush](https://github.com/charmbracelet/crush) under FSL-1.1-MIT. Those components are historical material, not ancestors of the active Maestro v1 implementation and not part of the release artifacts.

The repository-lineage audit found:

- Commit `13b4d4f592f2758a41930e866ca9ab95041becd2` added the four ports only under `Archive/internal/tui/`: `glamour.go`, `diff.go`, `list.go`, and `pills.go`.
- The active root implementations existed side-by-side in that same initial commit. Their histories do not follow or rename from the `Archive/` paths. The active proposal diff view was added later and contains none of the archived `diffdetect`, `looksLikeDiff`, `parseUnifiedDiff`, or `parsedDiffFile` implementation.
- All four archived port files are deleted from the release tree. The GoReleaser and npm manifests package the built Maestro binary, its documentation, and this notice tree; they do not package `Archive/`.

The historical notes do not name one exact Crush snapshot. An upstream history comparison narrows the source but cannot distinguish one snapshot because the six named upstream files have identical Git blobs in Crush v0.86.0, v0.87.0, v0.88.0, and commit `b109e35f260cbb6a7903581728dbeb6d11895d57` (the latest `main` commit by committer timestamp before Maestro's port commit):

| Historical port | Earliest common tagged source | Git blob |
| --- | --- | --- |
| `Archive/internal/tui/glamour.go` | [`internal/ui/common/markdown.go` at v0.86.0](https://github.com/charmbracelet/crush/blob/v0.86.0/internal/ui/common/markdown.go) | `a2a2d3d22ad10ff6cce5f0483d43a33405266cde` |
| `Archive/internal/tui/diff.go` | [`internal/diffdetect/detect.go` at v0.86.0](https://github.com/charmbracelet/crush/blob/v0.86.0/internal/diffdetect/detect.go) | `213803e6b3f3754491dd3953ca82b100090def89` |
| `Archive/internal/tui/diff.go` | [`internal/ui/chat/unified_diff.go` at v0.86.0](https://github.com/charmbracelet/crush/blob/v0.86.0/internal/ui/chat/unified_diff.go) | `71cdf5b6d2b86f4ab93e3b26cf3e4669029adf26` |
| `Archive/internal/tui/list.go` | [`internal/ui/list/list.go` at v0.86.0](https://github.com/charmbracelet/crush/blob/v0.86.0/internal/ui/list/list.go) | `49c196c0567482dcb145e8c592acfabf6facf054` |
| `Archive/internal/tui/list.go` | [`internal/ui/list/item.go` at v0.86.0](https://github.com/charmbracelet/crush/blob/v0.86.0/internal/ui/list/item.go) | `68c34a896cffbf8fdd839e4dc2b7da43af6fe679` |
| `Archive/internal/tui/pills.go` | [`internal/ui/model/pills.go` at v0.86.0](https://github.com/charmbracelet/crush/blob/v0.86.0/internal/ui/model/pills.go) | `5fd9f493f2d29785465eb7e4115d6b30252c50bc` |

[Crush v0.86.0 was published](https://github.com/charmbracelet/crush/releases/tag/v0.86.0) at `2026-07-20T20:08:59Z`. Using that official publication timestamp as the availability date, its FSL future MIT license would take effect on `2028-07-20T20:08:59Z`; that future date does not change the terms governing redistribution of the historical ports in 2026.

Because the FSL-covered ports are deleted, unshipped, and have no active-code lineage, they are not mapped as a Maestro v1 release blocker. Their exact archived license remains bundled for transparent provenance at [`LICENSES/provenance/Crush-FSL-1.1-MIT`](LICENSES/provenance/Crush-FSL-1.1-MIT). Restoring or redistributing those historical files before the applicable future-license date would require compliance with FSL-1.1-MIT, including its **Competing Use** restriction. This repository audit is not legal advice.

## Embedded and generated data

- **Liberation Mono.** Chroma v2.27.0's linked SVG formatter embeds a WOFF font. Its copyright statements and SIL Open Font License 1.1 are in [`LICENSES/data/Liberation-Mono-OFL-1.1`](LICENSES/data/Liberation-Mono-OFL-1.1). The text was extracted from Chroma's `formatters/svg/font_liberation_mono.go` header without changing the legal text.
- **Unicode data.** Linked segmentation and display-width packages include generated Unicode data tables, including the tables documented by `github.com/rivo/uniseg` as generated from Unicode 15.0.0 data. The Unicode data/software notice is in [`LICENSES/data/Unicode-LICENSE-v3`](LICENSES/data/Unicode-LICENSE-v3).
- **GitHub gemoji data.** `github.com/yuin/goldmark-emoji` generates its embedded GitHub emoji definitions from the GitHub emoji API and `github/gemoji`. The gemoji license is in [`LICENSES/data/github-gemoji-LICENSE`](LICENSES/data/github-gemoji-LICENSE).
- **models.dev catalog data.** `internal/agentcore/catalog.go` contains Maestro's fallback model catalog in the models.dev schema and with models.dev-derived metadata. The models.dev license is in [`LICENSES/data/models.dev-LICENSE`](LICENSES/data/models.dev-LICENSE).

## Linked Go modules

The list below is the union of `CGO_ENABLED=0 go list -deps ./cmd/maestro` for macOS, Linux, and Windows on amd64 and arm64. Standard-library packages and Maestro's main module are excluded.

| Module | Version | Linked targets | License and notice text |
| --- | --- | --- | --- |
| `charm.land/lipgloss/v2` | `v2.0.5` | all six release targets | [`LICENSE`](LICENSES/modules/charm.land/lipgloss/v2/LICENSE) |
| `github.com/alecthomas/chroma/v2` | `v2.27.0` | all six release targets | [`COPYING`](LICENSES/modules/github.com/alecthomas/chroma/v2/COPYING) |
| `github.com/atotto/clipboard` | `v0.1.4` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/atotto/clipboard/LICENSE) |
| `github.com/aymanbagabas/go-osc52/v2` | `v2.0.1` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/aymanbagabas/go-osc52/v2/LICENSE) |
| `github.com/aymerick/douceur` | `v0.2.0` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/aymerick/douceur/LICENSE) |
| `github.com/charmbracelet/bubbles` | `v1.0.0` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/bubbles/LICENSE) |
| `github.com/charmbracelet/bubbletea` | `v1.3.10` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/bubbletea/LICENSE) |
| `github.com/charmbracelet/colorprofile` | `v0.4.3` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/colorprofile/LICENSE) |
| `github.com/charmbracelet/glamour` | `v1.0.0` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/glamour/LICENSE) |
| `github.com/charmbracelet/lipgloss` | `v1.1.1-0.20250404203927-76690c660834` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/lipgloss/LICENSE) |
| `github.com/charmbracelet/ultraviolet` | `v0.0.0-20251205161215-1948445e3318` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/ultraviolet/LICENSE) |
| `github.com/charmbracelet/x/ansi` | `v0.11.7` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/x/ansi/LICENSE) |
| `github.com/charmbracelet/x/cellbuf` | `v0.0.15` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/x/cellbuf/LICENSE) |
| `github.com/charmbracelet/x/exp/slice` | `v0.0.0-20250327172914-2fdc97757edf` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/x/exp/slice/LICENSE) |
| `github.com/charmbracelet/x/term` | `v0.2.2` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/x/term/LICENSE) |
| `github.com/charmbracelet/x/termios` | `v0.1.1` | macOS + Linux (amd64, arm64) | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/x/termios/LICENSE) |
| `github.com/charmbracelet/x/windows` | `v0.2.2` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/charmbracelet/x/windows/LICENSE) |
| `github.com/clipperhouse/displaywidth` | `v0.11.0` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/clipperhouse/displaywidth/LICENSE) |
| `github.com/clipperhouse/uax29/v2` | `v2.7.0` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/clipperhouse/uax29/v2/LICENSE) |
| `github.com/dlclark/regexp2/v2` | `v2.2.1` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/dlclark/regexp2/v2/LICENSE) |
| `github.com/erikgeiser/coninput` | `v0.0.0-20211004153227-1c3628e74d0f` | Windows (amd64, arm64) | [`LICENSE`](LICENSES/modules/github.com/erikgeiser/coninput/LICENSE) |
| `github.com/gorilla/css` | `v1.0.1` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/gorilla/css/LICENSE) |
| `github.com/lucasb-eyer/go-colorful` | `v1.4.1` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/lucasb-eyer/go-colorful/LICENSE) |
| `github.com/mattn/go-isatty` | `v0.0.20` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/mattn/go-isatty/LICENSE) |
| `github.com/mattn/go-localereader` | `v0.0.1` | Windows (amd64, arm64) | [`LICENSE`](LICENSES/modules/github.com/mattn/go-localereader/LICENSE) |
| `github.com/mattn/go-runewidth` | `v0.0.23` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/mattn/go-runewidth/LICENSE) |
| `github.com/microcosm-cc/bluemonday` | `v1.0.27` | all six release targets | [`LICENSE.md`](LICENSES/modules/github.com/microcosm-cc/bluemonday/LICENSE.md) |
| `github.com/muesli/ansi` | `v0.0.0-20230316100256-276c6243b2f6` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/muesli/ansi/LICENSE) |
| `github.com/muesli/cancelreader` | `v0.2.2` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/muesli/cancelreader/LICENSE) |
| `github.com/muesli/reflow` | `v0.3.0` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/muesli/reflow/LICENSE) |
| `github.com/muesli/termenv` | `v0.16.0` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/muesli/termenv/LICENSE) |
| `github.com/rivo/uniseg` | `v0.4.7` | all six release targets | [`LICENSE.txt`](LICENSES/modules/github.com/rivo/uniseg/LICENSE.txt) |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.2` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/santhosh-tekuri/jsonschema/v6/LICENSE) |
| `github.com/xo/terminfo` | `v0.0.0-20220910002029-abceb7e1c41e` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/xo/terminfo/LICENSE) |
| `github.com/yuin/goldmark` | `v1.7.17` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/yuin/goldmark/LICENSE) |
| `github.com/yuin/goldmark-emoji` | `v1.0.6` | all six release targets | [`LICENSE`](LICENSES/modules/github.com/yuin/goldmark-emoji/LICENSE) |
| `golang.org/x/net` | `v0.38.0` | all six release targets | [`LICENSE`](LICENSES/modules/golang.org/x/net/LICENSE); [`PATENTS`](LICENSES/modules/golang.org/x/PATENTS) |
| `golang.org/x/sync` | `v0.21.0` | all six release targets | [`LICENSE`](LICENSES/modules/golang.org/x/sync/LICENSE); [`PATENTS`](LICENSES/modules/golang.org/x/PATENTS) |
| `golang.org/x/sys` | `v0.46.0` | all six release targets | [`LICENSE`](LICENSES/modules/golang.org/x/sys/LICENSE); [`PATENTS`](LICENSES/modules/golang.org/x/PATENTS) |
| `golang.org/x/term` | `v0.36.0` | all six release targets | [`LICENSE`](LICENSES/modules/golang.org/x/term/LICENSE); [`PATENTS`](LICENSES/modules/golang.org/x/PATENTS) |
| `golang.org/x/text` | `v0.39.0` | all six release targets | [`LICENSE`](LICENSES/modules/golang.org/x/text/LICENSE); [`PATENTS`](LICENSES/modules/golang.org/x/PATENTS) |
| `gopkg.in/yaml.v3` | `v3.0.1` | all six release targets | [`LICENSE`](LICENSES/modules/gopkg.in/yaml.v3/LICENSE); [`NOTICE`](LICENSES/modules/gopkg.in/yaml.v3/NOTICE) |

### Version-specific source note

The `github.com/mattn/go-localereader@v0.0.1` module archive contains no standalone license file; its README identifies the project as MIT and names Yasuhiro Matsumoto as author. The bundled exact license text is pinned to the later upstream commit [`2491eb6c1c75720122ef321ed7acc3a8d9de95b1`](https://github.com/mattn/go-localereader/blob/2491eb6c1c75720122ef321ed7acc3a8d9de95b1/LICENSE), which added a standalone upstream license.

## Reproducible audit

Run:

```text
go run ./scripts/check_licenses.go
```

The gate recomputes all six linked graphs, rejects unmapped modules or version/target drift, validates every bundled SHA-256, checks that every mapped component appears in this notice, verifies the exact npm mirror, and confirms that npm and GoReleaser include the notice tree exactly once.
