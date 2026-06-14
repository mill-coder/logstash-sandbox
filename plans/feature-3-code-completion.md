# Feature 3: Code Completion

## Context

The playground has syntax error highlighting (iteration 1) and semantic validation (iteration 2). Users still need to know Logstash plugin/option names by heart. Autocompletion suggests section keywords, plugin names, option names, and codec names based on cursor context.

## Architecture: WASM-based completions

Add `window.getLogstashCompletions(source, cursorPos)` in Go. It detects context via text scanning and returns completions from the existing registry. JS passes the result to CodeMirror's `autocompletion` extension.

**Why WASM-side:** Single source of truth (registry in Go), all Logstash logic in Go (consistent pattern), no data duplication.

## Completion contexts

| Context | Trigger | Suggestions |
|---------|---------|-------------|
| Top-level | `\|` or `inp\|` | `input`, `filter`, `output` |
| Inside section | `input { \| }` | Plugin names for that section type |
| Inside plugin | `grok { \| }` | Option names (common + plugin-specific) |
| After `codec =>` | `codec => \|` | Codec names |
| No completion | Inside strings, comments, hash values, after `=>` for non-codec | Nothing |

## Files to create/modify

| File | Action | What |
|------|--------|------|
| `go/complete.go` | **Create** | Context detection (forward scan) + completion generation |
| `go/main.go` | **Modify** | Register `getLogstashCompletions` in `main()` (1 line) |
| `web/src/wasm-bridge.js` | **Modify** | Add `getCompletions(source, pos)` wrapper |
| `web/src/editor.js` | **Modify** | Add `autocompletion` extension with custom source |

## Step 1: Create `go/complete.go`

### Context detection algorithm

`detectContext(source string, pos int) CompletionContext` works in two passes:

**Pass A — Check for value position:** Scan left from cursor past the partial word, skip whitespace. If `=>` is found immediately before, extract the attribute name before `=>`. If it's `"codec"`, return `Kind="codec"`. Otherwise return `Kind="none"` (don't complete arbitrary values).

**Pass B — Forward scan with brace-nesting stack:** Scan from document start to cursor, tracking a stack of context frames. For each token (skipping `#` comments and quoted strings):

- `input`/`filter`/`output` followed by `{` → push **section** frame
- identifier followed by `{` inside a section/conditional → push **plugin** frame
- `if`/`else if`/`else` followed by `{` → push **conditional** frame (inherits section type)
- `=>` followed by `{` → push **hash** frame
- `}` → pop frame

Top of stack at cursor:
- Empty stack → `Kind="section"`
- Section frame → `Kind="plugin"` (with section type)
- Plugin frame → `Kind="option"` (with section type + plugin name)
- Conditional frame → `Kind="plugin"` (inherits section type)
- Hash frame → `Kind="none"`

### Completion generation

`buildCompletions(ctx CompletionContext)`:
- `"section"` → `[input, filter, output]`
- `"plugin"` → `knownPlugins[sectionType]` keys (from `go/registry.go`)
- `"option"` → `getPluginOptions(pluginType, pluginName)` keys (from `go/registry.go`)
- `"codec"` → `knownCodecs` keys (from `go/registry.go`)
- `"none"` → empty

### WASM function

`getCompletions(this js.Value, args []js.Value) interface{}` — takes `(source, cursorPos)`, returns JSON:
```json
{
  "from": 8,
  "options": [
    {"label": "grok", "type": "type", "detail": "filter plugin"},
    {"label": "mutate", "type": "type", "detail": "filter plugin"}
  ]
}
```

Completion `type` values: `"keyword"` for sections, `"type"` for plugins, `"property"` for options, `"enum"` for codecs.

## Step 2: Register in `go/main.go`

Add in `main()`:
```go
js.Global().Set("getLogstashCompletions", js.FuncOf(getCompletions))
```

## Step 3: Add bridge in `web/src/wasm-bridge.js`

```js
export async function getCompletions(source, pos) {
  if (!wasmReady) await readyPromise;
  const jsonStr = window.getLogstashCompletions(source, pos);
  return JSON.parse(jsonStr);
}
```

## Step 4: Wire into `web/src/editor.js`

```js
import { autocompletion } from '@codemirror/autocomplete';
import { parseLogstash, getCompletions } from './wasm-bridge.js';

async function logstashCompletionSource(context) {
  const word = context.matchBefore(/[a-zA-Z_][a-zA-Z0-9_]*/);
  if (!word && !context.explicit) return null;

  const source = context.state.doc.toString();
  const result = await getCompletions(source, context.pos);
  if (!result.options || result.options.length === 0) return null;

  return {
    from: result.from,
    options: result.options,
    validFor: /^[a-zA-Z_][a-zA-Z0-9_]*$/,
  };
}
```

Add after `basicSetup` in extensions (overrides default word-based completions):
```js
autocompletion({ override: [logstashCompletionSource] }),
```

## Verification

1. `make wasm` compiles
2. `make dev` — editor loads
3. Type at top level → suggests `input`, `filter`, `output`
4. Inside `input { }` → input plugins (beats, stdin, file, ...)
5. Inside `filter { grok { } }` → grok options (match, overwrite, ...)
6. `codec =>` inside a plugin → codecs (json, plain, rubydebug, ...)
7. Inside a string or after `#` → no completions
8. Inside `if [x] == "y" { }` within filter → filter plugins
9. Inside `match => { }` (hash value) → no completions
