# Feature 1: MVP — Syntax Error Highlighting

## Overview

Build a browser-based Logstash config playground by compiling [breml/logstash-config](https://github.com/breml/logstash-config) (Go PEG parser) to WebAssembly and integrating it with CodeMirror 6 for live error highlighting.

**MVP scope**: parse + error markers only. No lint rules, no auto-format, no syntax coloring.

---

## Phase 1: Go WASM module

### File: `go/go.mod`

```
module github.com/mill-coder/logstash-sandbox
go 1.23
require github.com/breml/logstash-config v0.5.3
```

### File: `go/main.go`

**Purpose**: WASM entry point. Registers `window.parseLogstashConfig(source)` via `syscall/js`.

**Core logic**:
1. Receive source string from JS
2. Call `config.Parse("", []byte(source))`
3. If no error → return `{"ok": true, "diagnostics": []}`
4. If error → extract positions from the error string, return diagnostics

**Error position extraction** (the key challenge):

The pigeon parser types (`errList`, `parserError`, `position`) are all **unexported**. We cannot type-assert into them. Instead, we regex-parse the `.Error()` string.

Each pigeon `parserError.Error()` produces:
```
line:col (offset): rule ruleName: message text
```

Extraction regex:
```go
var errLineRegex = regexp.MustCompile(`^(?:\S+:)?(\d+):(\d+)\s+\((\d+)\)(?::\s*(?:rule\s+\S+:\s*)?)(.*)`)
```

Groups: (1) line, (2) col, (3) byte offset, (4) message.

The **byte offset** (group 3) maps directly to CodeMirror's `Diagnostic.from` (char offset) for ASCII content. For `Diagnostic.to`, we use `from + 1` since pigeon only reports a point, not a range.

**Supplementary signal**: `config.GetFarthestFailure()` returns the deepest parse point in format:
```
Parsing error at pos line:col [offset] and [pos] (after: 'text'):
-> expected_1
-> expected_2
```

Extraction regex:
```go
var farthestRegex = regexp.MustCompile(`at pos (\d+):(\d+) \[(\d+)\] and \[(\d+)\]`)
```

**JSON return schema**:
```json
{
  "ok": false,
  "diagnostics": [
    {"from": 42, "to": 43, "severity": "error", "message": "no match found, expected: ..."}
  ],
  "farthest": {
    "from": 42, "to": 43, "severity": "warning", "message": "expected: input, filter, output"
  }
}
```

**Go→JS interop**: Return a JSON **string** (not a JS object). `syscall/js` handles complex objects poorly; JSON roundtrip is the most reliable pattern.

**Keep alive**: `select {}` at end of `main()` — standard pattern for Go WASM.

**Full implementation sketch**:

```go
package main

import (
    "encoding/json"
    "regexp"
    "strconv"
    "strings"
    "syscall/js"
    config "github.com/breml/logstash-config"
)

type Diagnostic struct {
    From     int    `json:"from"`
    To       int    `json:"to"`
    Severity string `json:"severity"`
    Message  string `json:"message"`
}

type ParseResult struct {
    OK          bool        `json:"ok"`
    Diagnostics []Diagnostic `json:"diagnostics"`
    Farthest    *Diagnostic  `json:"farthest"`
}

var errLineRegex = regexp.MustCompile(`^(?:\S+:)?(\d+):(\d+)\s+\((\d+)\)(?::\s*(?:rule\s+\S+:\s*)?)(.*)`)
var farthestRegex = regexp.MustCompile(`at pos (\d+):(\d+) \[(\d+)\] and \[(\d+)\]`)

func parseLogstash(this js.Value, args []js.Value) interface{} {
    if len(args) < 1 {
        return marshal(ParseResult{OK: false, Diagnostics: []Diagnostic{
            {From: 0, To: 1, Severity: "error", Message: "no input provided"},
        }})
    }

    input := args[0].String()
    _, err := config.Parse("", []byte(input))
    if err == nil {
        return marshal(ParseResult{OK: true, Diagnostics: []Diagnostic{}})
    }

    result := ParseResult{OK: false, Diagnostics: []Diagnostic{}}
    seen := map[int]bool{}

    for _, line := range strings.Split(err.Error(), "\n") {
        line = strings.TrimSpace(line)
        if line == "" {
            continue
        }
        m := errLineRegex.FindStringSubmatch(line)
        if m == nil {
            if !seen[-1] {
                seen[-1] = true
                result.Diagnostics = append(result.Diagnostics, Diagnostic{
                    From: 0, To: min(1, len(input)), Severity: "error", Message: line,
                })
            }
            continue
        }
        offset, _ := strconv.Atoi(m[3])
        msg := m[4]
        if msg == "" {
            msg = line
        }
        if !seen[offset] {
            seen[offset] = true
            from := min(offset, max(0, len(input)-1))
            to := min(from+1, len(input))
            result.Diagnostics = append(result.Diagnostics, Diagnostic{
                From: from, To: to, Severity: "error", Message: msg,
            })
        }
    }

    // Supplementary: farthest failure
    if ff, ok := config.GetFarthestFailure(); ok {
        if fm := farthestRegex.FindStringSubmatch(ff); fm != nil {
            offset, _ := strconv.Atoi(fm[3])
            var msgs []string
            for _, fl := range strings.Split(ff, "\n") {
                fl = strings.TrimSpace(fl)
                if strings.HasPrefix(fl, "->") {
                    msgs = append(msgs, strings.TrimSpace(strings.TrimPrefix(fl, "->")))
                }
            }
            msg := strings.Join(msgs, "; ")
            if msg == "" {
                msg = "parse failed at this position"
            }
            from := min(offset, max(0, len(input)-1))
            to := min(from+1, len(input))
            result.Farthest = &Diagnostic{
                From: from, To: to, Severity: "warning", Message: msg,
            }
        }
    }

    if len(result.Diagnostics) == 0 {
        result.Diagnostics = append(result.Diagnostics, Diagnostic{
            From: 0, To: min(1, len(input)), Severity: "error", Message: err.Error(),
        })
    }

    return marshal(result)
}

func marshal(r ParseResult) string {
    b, _ := json.Marshal(r)
    return string(b)
}

func main() {
    js.Global().Set("parseLogstashConfig", js.FuncOf(parseLogstash))
    select {}
}
```

### Build command

```bash
cd go && GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o ../web/public/parser.wasm .
```

`-ldflags="-s -w"` strips debug info (~30% size reduction). Expected output: 2-5 MB.

### wasm_exec.js

Copy from Go SDK:
```bash
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/public/
```

Must match the Go version used for compilation.

---

## Phase 2: Web frontend

### File: `web/package.json`

```json
{
  "name": "logstash-sandbox",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "codemirror": "^6.0.0",
    "@codemirror/lint": "^6.0.0"
  },
  "devDependencies": {
    "vite": "^6.0.0"
  }
}
```

`codemirror` meta-package re-exports `basicSetup`, `EditorView`, `EditorState`.

### File: `web/vite.config.js`

```js
import { defineConfig } from 'vite';

export default defineConfig({
  root: '.',
  publicDir: 'public',
  build: {
    outDir: '../dist',
    emptyOutDir: true,
  },
});
```

### File: `web/index.html`

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Logstash Playground</title>
  <link rel="stylesheet" href="/src/style.css">
</head>
<body>
  <header>
    <h1>Logstash Playground</h1>
    <span id="status">Loading WASM parser...</span>
  </header>
  <div id="editor"></div>
  <script src="/wasm_exec.js"></script>
  <script type="module" src="/src/main.js"></script>
</body>
</html>
```

`wasm_exec.js` loaded as regular script (defines global `Go` class) before the ES module.

### File: `web/src/wasm-bridge.js`

```js
let wasmReady = false;
let readyResolve;
const readyPromise = new Promise((resolve) => { readyResolve = resolve; });

export async function initWasm() {
  const go = new Go();
  const result = await WebAssembly.instantiateStreaming(
    fetch('/parser.wasm'),
    go.importObject
  );
  go.run(result.instance); // non-blocking (Go blocks on select{})
  wasmReady = true;
  readyResolve();
}

export async function parseLogstash(source) {
  if (!wasmReady) await readyPromise;
  const jsonStr = window.parseLogstashConfig(source);
  return JSON.parse(jsonStr);
}
```

### File: `web/src/editor.js`

```js
import { EditorView, basicSetup } from 'codemirror';
import { EditorState } from '@codemirror/state';
import { linter, lintGutter } from '@codemirror/lint';
import { parseLogstash } from './wasm-bridge.js';

const SAMPLE = `input {
  beats {
    port => 5044
  }
}

filter {
  mutate {
    add_tag => ["processed"]
  }
}

output {
  elasticsearch {
    hosts => ["http://localhost:9200"]
    index => "logstash-%{+YYYY.MM.dd}"
  }
}
`;

const logstashLinter = linter(async (view) => {
  const doc = view.state.doc.toString();
  if (!doc.trim()) return [];

  try {
    const result = await parseLogstash(doc);
    if (result.ok) return [];

    const diagnostics = result.diagnostics.map(d => ({
      from: Math.max(0, d.from),
      to: Math.min(d.to, doc.length),
      severity: d.severity,
      message: d.message,
    }));

    if (result.farthest && !diagnostics.some(d => d.from === result.farthest.from)) {
      diagnostics.push({
        from: Math.max(0, result.farthest.from),
        to: Math.min(result.farthest.to, doc.length),
        severity: result.farthest.severity,
        message: result.farthest.message,
      });
    }

    return diagnostics;
  } catch (err) {
    console.error('Linter error:', err);
    return [];
  }
}, { delay: 300 });

export function createEditor(parent) {
  return new EditorView({
    state: EditorState.create({
      doc: SAMPLE,
      extensions: [
        basicSetup,
        lintGutter(),
        logstashLinter,
        EditorView.theme({
          '&': { height: 'calc(100vh - 60px)' },
          '.cm-scroller': { overflow: 'auto' },
        }),
      ],
    }),
    parent,
  });
}
```

### File: `web/src/main.js`

```js
import { initWasm } from './wasm-bridge.js';
import { createEditor } from './editor.js';

async function init() {
  const status = document.getElementById('status');
  try {
    await initWasm();
    status.textContent = 'Parser ready';
    status.classList.add('ready');
  } catch (err) {
    status.textContent = `Failed to load parser: ${err.message}`;
    status.classList.add('error');
    console.error('WASM init failed:', err);
  }
  createEditor(document.getElementById('editor'));
}

init();
```

### File: `web/src/style.css`

Dark theme, minimal layout. Header bar with title + status. Editor fills remaining viewport height.

---

## Phase 3: Build system

### File: `Makefile`

```makefile
GOROOT  ?= $(shell go env GOROOT)
WASM_OUT = web/public/parser.wasm
WASM_EXEC = web/public/wasm_exec.js

.PHONY: all clean wasm wasm-exec deps dev build

all: wasm wasm-exec deps build

wasm: $(WASM_OUT)
$(WASM_OUT): go/main.go go/go.mod
	cd go && GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o ../$(WASM_OUT) .

wasm-exec: $(WASM_EXEC)
$(WASM_EXEC):
	cp "$(GOROOT)/lib/wasm/wasm_exec.js" $(WASM_EXEC)

deps: web/node_modules
web/node_modules: web/package.json
	cd web && npm install
	touch web/node_modules

dev: wasm wasm-exec deps
	cd web && npx vite

build: wasm wasm-exec deps
	cd web && npx vite build

clean:
	rm -f $(WASM_OUT) $(WASM_EXEC)
	rm -rf dist web/node_modules
```

---

## Implementation order

| Step | What | Verify |
|------|------|--------|
| 1 | `go/go.mod` + `go get` dependency | `go mod tidy` succeeds |
| 2 | Minimal `go/main.go` (stub returning `{"ok":true}`) | `GOOS=js GOARCH=wasm go build` produces `.wasm` |
| 3 | Copy `wasm_exec.js`, create `index.html` + `wasm-bridge.js` | Browser console: WASM loads, `parseLogstashConfig("x")` returns JSON |
| 4 | Full `go/main.go` with real parsing + error extraction | Console: `parseLogstashConfig("invalid {")` returns diagnostics with positions |
| 5 | Vite + CodeMirror setup (`package.json`, `editor.js`, `main.js`, `style.css`) | `make dev` → editor renders, errors appear as red underlines |
| 6 | `Makefile` | `make dev` and `make build` both work |
| 7 | Edge case testing | See test cases below |

---

## Test cases

| Input | Expected |
|-------|----------|
| Valid config (sample) | No diagnostics, `ok: true` |
| Empty string | No diagnostics (skip linting) |
| `input { stdin {} } output { stdout {} }` | No diagnostics |
| `input {` | Error near end (missing closing brace) |
| `filter { mutate { add_tag => } }` | Error at empty value position |
| `invalid` | Error — not a valid section keyword |
| `# just a comment` | Depends on parser — likely error (no sections) or ok |

---

## Known risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Pigeon error string format changes | Regex stops extracting positions | Pin `breml/logstash-config` version; add Go tests for error format |
| WASM binary too large | Slow first load | gzip/brotli (Vite does this in prod); `-ldflags="-s -w"` already applied |
| Byte offset != char offset for UTF-8 | Wrong error position for non-ASCII | Rare in Logstash configs; fixable later with byte→char mapping in JS |
| `GetFarthestFailure()` uses package global | Race condition in concurrent calls | Single-threaded WASM — safe; each WASM instance has its own Go runtime |
| TinyGo incompatibility | Can't use TinyGo for smaller binary | Standard Go is fine for MVP; TinyGo optimization is a future nice-to-have |

---

## Future enhancements (post-MVP)

- Logstash-aware syntax highlighting (CodeMirror Lezer grammar)
- Lint rules (missing plugin IDs, comment placement) from mustache
- Auto-format button
- Share config via URL (base64 in hash)
- Load config from file (drag & drop)
- Dark/light theme toggle
- Deploy to GitHub Pages via GitHub Actions
