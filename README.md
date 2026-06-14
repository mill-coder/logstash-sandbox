# Logstash Playground

Browser-based Logstash configuration editor with live error highlighting and semantic validation. Powered by a Go parser compiled to WebAssembly — everything runs client-side, no server needed.

## Features

- **Syntax error highlighting** — red underlines and gutter icons on parse errors, powered by [breml/logstash-config](https://github.com/breml/logstash-config) PEG parser
- **Semantic validation** — yellow warnings for unknown plugin names, unknown options, and invalid codec names
- **Dark theme editor** — CodeMirror 6 with monospace font, fills the viewport

## Quick start

### Prerequisites

- Go 1.22+
- Node.js 18+
- Make

### Development

```bash
make dev
```

This builds the WASM parser, installs npm dependencies, and starts Vite at `http://localhost:5173`.

### Production build

```bash
make build
```

Outputs static files to `dist/` — deploy to any HTTP server or GitHub Pages.

### Docker

```bash
docker build -t logstash-sandbox .
docker run -p 8080:80 logstash-sandbox
```

## How it works

```
CodeMirror 6 editor (browser)
  -> onChange (debounced 300ms)
  -> JS calls Go WASM: parseLogstashConfig(source) -> JSON
  -> Go parses config, extracts error positions
  -> Returns {ok, diagnostics: [{from, to, severity, message}]}
  -> On success, walks AST to validate plugin/option/codec names
  -> JS feeds diagnostics to CodeMirror linter
  -> Red underlines for errors, yellow for warnings
```

The Go parser ([breml/logstash-config](https://github.com/breml/logstash-config)) is compiled to WebAssembly. All parsing and validation happens in the browser — no data leaves the client.

## Project structure

```
logstash-sandbox/
├── Makefile               # Build targets: wasm, dev, build, clean
├── Dockerfile             # Multi-stage build (Go -> Node -> nginx)
├── go/
│   ├── main.go            # WASM entry point + error extraction
│   ├── registry.go        # Known plugins, codecs, and option schemas
│   └── validate.go        # AST walker for semantic validation
└── web/
    ├── index.html
    ├── vite.config.js
    └── src/
        ├── main.js         # App init: load WASM, create editor
        ├── wasm-bridge.js  # WASM loading + JS wrapper
        ├── editor.js       # CodeMirror 6 setup + lint integration
        └── style.css
```

## License

[MIT](LICENSE)
