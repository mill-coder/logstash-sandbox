# logstash-sandbox

## Project identity

Browser-based Logstash configuration editor with **live error highlighting**, powered by a Go parser compiled to WebAssembly. No server needed — everything runs client-side.

- **License**: MIT
- **Status**: Pre-alpha (scaffolding phase)
- **Detailed implementation plans**: see [`plans/`](plans/) — features are independent and can be implemented in any order

## Architecture

```
CodeMirror 6 editor (browser)
  → onChange (debounced 300ms via CM linter)
  → JS calls Go WASM: parseLogstashConfig(source) → JSON string
  → Go calls github.com/breml/logstash-config Parse()
  → Extracts error positions via regex on pigeon parser error strings
  → Returns {ok, diagnostics: [{from, to, severity, message}]}
  → JS feeds diagnostics to CodeMirror's linter/lintGutter
  → Red underlines + gutter icons on errors
```

### Components

| Component | Tech | Location |
|-----------|------|----------|
| Parser WASM module | Go + `syscall/js` | `go/` |
| Web frontend | Vite + CodeMirror 6 | `web/` |
| Build system | Makefile | root |

### Key dependency

- **[breml/logstash-config](https://github.com/breml/logstash-config)** (Apache 2.0) — Pure Go PEG parser for the Logstash config format. Provides `Parse()` function and `GetFarthestFailure()`. All parser error types are unexported (pigeon-generated), so we extract positions by regex-parsing error strings.

## Tech stack

- **Go 1.22+** — compiled to WASM via `GOOS=js GOARCH=wasm`
- **Node.js 18+** — for Vite dev server and npm deps
- **Vite** — zero-config bundler for the frontend
- **CodeMirror 6** — modular editor with built-in `linter()` extension
- **No backend** — fully static, deployable to any HTTP server or GitHub Pages

## Project structure

```
logstash-sandbox/
├── CLAUDE.md              # This file
├── plans/                 # Detailed implementation plans
│   ├── feature-1-syntax-errors.md
│   ├── feature-2-semantic-validation.md
│   ├── feature-3-code-completion.md
│   └── feature-4-registry-scraper.md
├── Makefile               # Build targets: wasm, dev, build, clean
├── .gitignore
├── LICENSE
├── go/
│   ├── go.mod
│   ├── go.sum
│   ├── main.go            # WASM entry: parser bridge + error extraction
│   ├── registry.go        # Known plugins, codecs, and option schemas
│   ├── validate.go        # AST walker for semantic validation
│   └── complete.go        # Autocompletion context detection + generation
└── web/
    ├── package.json
    ├── vite.config.js
    ├── index.html
    ├── src/
    │   ├── main.js           # App init: load WASM, create editor
    │   ├── wasm-bridge.js   # WASM loading + parseLogstash() wrapper
    │   ├── editor.js        # CodeMirror 6 setup + lint integration
    │   └── style.css
    └── public/             # Build artifacts (gitignored)
        ├── parser.wasm
        └── wasm_exec.js
```

## Conventions

- **Scope**: parse errors, semantic validation (unknown plugins/options/codecs), and code completion
- Build artifacts (`parser.wasm`, `wasm_exec.js`, `node_modules/`, `dist/`) are gitignored
- Go→JS data exchange uses JSON strings (most reliable with `syscall/js`)
- Error positions: pigeon byte offsets treated as char offsets (correct for ASCII, covers ~all real Logstash configs)
- Debouncing handled by CodeMirror's built-in `linter({delay: 300})`

## Build & run

```bash
make dev      # Build WASM + start Vite dev server
make build    # Production build into dist/
make clean    # Remove all build artifacts
```
