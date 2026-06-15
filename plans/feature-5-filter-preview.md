# Feature 5: Filter Preview Panel

Browser-based live simulation of Logstash filter plugins applied to a sample event.

---

## 1. Why we cannot run real Logstash in the browser

Logstash is written in **Java + JRuby** (Java implementation of Ruby) running on the JVM. Filter plugins are JRuby gems.

Compiling the JVM+JRuby stack to WebAssembly is theoretically possible (via CheerpJ, TeaVM, GraalVM native) but practically infeasible for Logstash specifically:

| Blocker | Detail |
|---------|--------|
| Binary size | JVM + JRuby runtime: 100–300 MB before any Logstash code |
| JRuby runtime | Plugins are Ruby code loaded dynamically from gems; no WASM target handles dynamic class loading at a practical size |
| Startup time | Logstash takes 10–30 s natively; CheerpJ-on-browser is worse |
| Plugin ecosystem | grok patterns: ~10 MB file; geoip: 50–500 MB MaxMind DB; many plugins make OS-level syscalls |

**Conclusion**: Filter simulation must be implemented from scratch, one plugin at a time.

---

## 2. Chosen approach: Go WASM + JS hybrid

Reuse the project's existing Go WASM architecture. Add a new exported WASM function:

```
applyLogstashFilters(configSrc: string, eventJSON: string) → JSON string
```

Return shape:
```json
{
  "ok": true,
  "event": { "field": "value", ... },
  "notes": ["skipped: grok (not simulated)", ...],
  "error": ""
}
```

For plugins whose implementation is better served by JS libraries (primarily `grok`), a thin bridge passes data from Go into a JS function and back. For everything else, Go handles the simulation directly.

---

## 3. AST API — breml/logstash-config v0.5.3

Source: `/home/nouknouk/go/pkg/mod/github.com/breml/logstash-config@v0.5.3/ast/ast.go`

All filter section plugins are reachable via:

```go
cfg.Filter              // []ast.PluginSection
  .BranchOrPlugins      // []ast.BranchOrPlugin
    ast.Plugin          // concrete plugin node
      .Name()           // string — plugin name
      .Attributes       // []ast.Attribute
    ast.Branch          // if/else-if/else block
      .IfBlock.Block    // []ast.BranchOrPlugin
      .ElseIfBlock[n].Block
      .ElseBlock.Block
```

### Attribute types and value extraction

| Type | How to get the value |
|------|---------------------|
| `ast.StringAttribute` | `.Value() string` — returns raw string **without** surrounding quotes |
| `ast.NumberAttribute` | `.Value() float64` |
| `ast.ArrayAttribute` | `.Value() []ast.Attribute` — each element is itself an Attribute |
| `ast.HashAttribute` | `.Value() []ast.HashEntry` |
| `ast.PluginAttribute` | nested plugin (used for `codec =>`) |

`HashEntry` structure:
```go
type HashEntry struct {
    Key   HashEntryKey  // interface: StringAttribute or NumberAttribute
    Value Attribute
}
// Key access: type-assert to ast.StringAttribute and call .Value() to get unquoted string
// Key.ValueString() returns quoted form ("foo") — do NOT use this for key names
```

### Critical pitfall: HashEntry key extraction

`HashEntry.Key.ValueString()` returns the **quoted** representation for string keys (e.g. `"host"` with surrounding quotes). Always type-assert:

```go
func hashKeyStr(key ast.HashEntryKey) string {
    switch k := key.(type) {
    case ast.StringAttribute:
        return k.Value() // unquoted raw string
    case ast.NumberAttribute:
        return k.ValueString()
    default:
        v := key.ValueString()
        if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
            return v[1 : len(v)-1]
        }
        return v
    }
}
```

Same applies when extracting string values from any Attribute:

```go
func attrStr(attr ast.Attribute) string {
    switch a := attr.(type) {
    case ast.StringAttribute:
        return a.Value()
    case ast.NumberAttribute:
        return a.ValueString()
    default:
        v := attr.ValueString()
        if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
            return v[1 : len(v)-1]
        }
        return v
    }
}
```

### Field reference interpolation (`%{field}`)

Logstash expands `%{fieldname}` in string values at runtime:

```go
var interpolateRe = regexp.MustCompile(`%\{(\w+)\}`)

func interpolate(s string, event map[string]interface{}) string {
    return interpolateRe.ReplaceAllStringFunc(s, func(m string) string {
        key := m[2 : len(m)-1]
        if v, ok := event[key]; ok {
            return fmt.Sprintf("%v", v)
        }
        return m // leave %{missing} as-is
    })
}
```

---

## 4. Filter plugin simulation — feasibility matrix

### Tier A — Implemented in Go WASM (straightforward)

| Plugin | Options to simulate | Implementation notes |
|--------|---------------------|----------------------|
| `mutate` | `add_field`, `remove_field`, `rename`, `update`, `replace`, `copy`, `uppercase`, `lowercase`, `strip`, `convert`, `gsub`, `split`, `merge` | Most complex filter but entirely self-contained string/map ops. `gsub` items come as a **flat array of triples** `[field, pattern, replacement, ...]` — process in groups of 3. `convert` target types: `integer`, `float`, `string`, `boolean`. |
| `json` | `source`, `target` | Parse `event[source]` as JSON. If `target` given, nest result under that key; otherwise merge fields into event root. |
| `kv` | `source`, `field_split`, `value_split`, `target`, `include_keys`, `exclude_keys`, `trim_key`, `trim_value`, `prefix` | Pure string splitting. Go stdlib sufficient. |
| `csv` | `source`, `target`, `separator`, `columns`, `skip_header`, `autogenerate_column_names` | Go `encoding/csv` handles parsing. Map columns to field names. |
| `dissect` | `mapping`, `tag_on_failure` | Dissect patterns like `%{clientip} %{ident}` are a simplified non-regex format. Implement as sequential substring extraction. |
| `drop` | (none) | Set a `dropped: true` flag in the result; UI shows "event dropped" instead of the event. |
| `urldecode` | `field`, `charset`, `all_fields` | `url.QueryUnescape` in Go. |
| `truncate` | `fields`, `length_bytes` | Simple slice on string field values. |
| `de_dot` | `fields`, `separator`, `recursive` | Rename event keys containing `.` to use the separator (default `_`). |
| `prune` | `whitelist_names`, `blacklist_names`, `whitelist_values`, `blacklist_values` | Iterate event keys, apply include/exclude patterns. |
| `fingerprint` | `source`, `target`, `method`, `key`, `concatenate_sources` | Go stdlib: `crypto/md5`, `crypto/sha1`, `crypto/sha256`. `MURMUR3` needs a small dependency. |

**Common options** available on every filter plugin (handled separately after plugin-specific ops):

| Option | Type | Behaviour |
|--------|------|-----------|
| `add_field` | HashAttribute | Add/interpolate fields into event |
| `remove_field` | ArrayAttribute | Delete fields from event |
| `add_tag` | ArrayAttribute | Append to `event["tags"]` (must be `[]interface{}`, not `[]string`) |
| `remove_tag` | ArrayAttribute | Remove from `event["tags"]` |

### Tier B — Medium effort (Go WASM, 1–2 days each)

| Plugin | Notes |
|--------|-------|
| `grok` | **Best done as a JS hybrid**: Elastic publishes `@elastic/grok` (npm), a maintained JS library covering the full Logstash pattern library. The Go WASM function can call a JS shim `window.simulateGrok(pattern, source, eventJSON)` to run the pattern match and return captured fields. Avoids reimplementing the 200-pattern library in Go. The pattern syntax is `%{PATTERN:field:type}` where `:type` coerces to `int` or `float`. |
| `date` | Parse a field value as a date and set `@timestamp`. Hard part: Logstash uses **Joda-Time format strings** (`YYYY-MM-dd HH:mm:ss`, `ISO8601`, `UNIX`, `UNIX_MS`, `TAI64N`). Go uses its own reference-time format. Need a Joda→Go format converter covering the most common tokens: `YYYY→2006`, `MM→01`, `dd→02`, `HH→15`, `mm→04`, `ss→05`. Alternatively delegate to JS `luxon` library which understands Joda format. |
| `xml` | Parse an XML string field. Logstash uses an xpath-like `store => [xpath, field]` syntax. Go has `encoding/xml` but XPath support requires `github.com/antchfx/xmlquery` (~small dependency). |
| `extractnumbers` | Regex `\d+(?:\.\d+)?` scan over a field, store as array. Trivial in Go. ~2h. |
| `range` | Apply tags/fields based on value ranges (`from`, `to`, `field`, `ranges`). Go map/compare ops. ~4h. |

### Tier C — Feasible but low priority

| Plugin | Notes |
|--------|-------|
| `split` (plugin) | Splits one event into N events (one per array element). UI challenge: the preview currently shows one output event. Would need to show a list/pagination of output events. ~0.5d Go + ~1d UI. |
| `clone` | Clones the event and adds tags. Same multi-event UI challenge as `split`. |
| `cipher` | AES/DES encrypt/decrypt. Go `crypto/aes` etc. Low real-world demand in a sandbox. |
| `useragent` | ua-parser pattern database is ~2 MB YAML. Could be embedded in WASM but big. Low priority. |
| `translate` | Dictionary lookup from a YAML/CSV file. In a browser context the user would need to supply the dictionary inline — requires UI work. |
| `sleep` | Irrelevant for simulation; always skip. |

### Tier D — Not feasible

| Plugin | Reason |
|--------|--------|
| `geoip` | Requires MaxMind GeoLite2 database (50–500 MB). Not embeddable. |
| `ruby` | Requires a Ruby interpreter runtime. |
| `dns` | Browser sandbox blocks DNS resolution. |
| `http` | External HTTP calls break the offline/static model. |
| `aggregate` | Stateful across multiple events — fundamentally incompatible with single-event simulation. |
| `elapsed` | Same: stateful across event pairs. |
| `throttle` | Stateful rate-limiting. |
| `metrics` | Stateful counters/gauges. |
| `environment` | Reads OS environment variables — not available in browser. |

### Conditional branches

`if [field] == "x" { ... } else { ... }` blocks: evaluating Logstash's full condition language (field selectors, comparators, `in`/`not in`, regex match `=~`, boolean operators `and`/`or`/`nand`/`xor`) is a significant sub-project in itself (~2–3d). Without it, any filter inside a branch is skipped and a note is added. This is the most impactful gap for real-world configs.

---

## 5. Effort summary

| Scope | Filters | Effort |
|-------|---------|--------|
| MVP (current plan) | mutate, json, common options | ~2–3d (full stack incl. UI) |
| + Tier A additions | + kv, csv, dissect, drop, fingerprint, urldecode, truncate, de_dot, prune | +2–3d |
| + grok (JS hybrid) | via `@elastic/grok` npm | +1d |
| + date | Joda format conversion | +1–2d |
| + xml | XPath subset | +1–2d |
| + conditional branches | Full expression evaluator | +2–3d |
| **Full realistic total** | ~15 plugins + branches | **~9–14d** |

Highest ROI additions after the MVP: **grok** (used in the majority of real configs), then **date**, then **kv**/**csv**/**dissect** as a batch.

---

## 6. Key implementation decisions

### Go file structure

- `go/simulate.go` — new file, `package main`: `SimulateResult` struct, `applyLogstashFilters` WASM entrypoint, `runFilterSimulation`, `simPlugin`, `simMutate`, `simJSONFilter`, `simCommonOptions`, helpers.
- `go/main.go` — add one line: `js.Global().Set("applyLogstashFilters", js.FuncOf(applyLogstashFilters))`
- Do **not** reuse `marshal(ParseResult)` from `main.go`; define `marshalSimulate(SimulateResult)` in `simulate.go`.

### Makefile

Change WASM dependency from `go/main.go` to `go/*.go` so adding `simulate.go` (or any future Go file) triggers a rebuild:

```make
$(WASM_OUT): go/*.go go/go.mod
```

### JS bridge for grok

```js
// In wasm-bridge.js or a dedicated grok-bridge.js
import { Grok } from '@elastic/grok';

window.simulateGrok = function(patternsJSON, pattern, input) {
  const customPatterns = JSON.parse(patternsJSON || '{}');
  const g = new Grok({ patterns: customPatterns });
  return JSON.stringify(g.parseSync(pattern, input) ?? {});
};
```

The Go WASM code would call this via `js.Global().Get("simulateGrok").Invoke(...)` and merge the returned fields into the event.

### `event["tags"]` typing

JSON unmarshal produces `[]interface{}` for arrays. Always use `[]interface{}` (never `[]string`) when initialising or appending to `event["tags"]` to avoid type assertion panics:

```go
func getTagsSlice(event map[string]interface{}) []interface{} {
    if v, ok := event["tags"]; ok {
        if arr, ok := v.([]interface{}); ok { return arr }
    }
    return []interface{}{}
}
```

---

## 7. UI design findings

### Layout

Transform `#page-editor` from a single-column layout to a horizontal split:

```
┌──────────────────────────────────────────────────────────────┐
│ header                               [Editor] [Docs]  status │
├────────────────────────────┬─────────────────────────────────┤
│                            │ EVENT PREVIEW             [×]   │
│   Config editor            ├─────────────────────────────────┤
│   (CodeMirror)             │ Input Event (JSON)              │
│                            │   CodeMirror JSON editor        │
│                            │   (top 45% of panel)            │
│                            ├─────────────────────────────────┤
│                            │ Output Event                    │
│                            │   <pre> display                 │
│                            │                                 │
│                            ├─────────────────────────────────┤
│                            │ ℹ skipped: grok (not simulated) │
└────────────────────────────┴─────────────────────────────────┘
```

- Config editor: `flex: 1`, takes remaining width
- Preview panel: `width: 380px; flex-shrink: 0`
- Panel toggle: `×` button in panel header hides it; "Show Preview" nav button re-shows it

### Input event editor

Use a second CodeMirror instance with `@codemirror/lang-json` for JSON syntax highlighting and editing experience. Requires adding `"@codemirror/lang-json": "^6.0.0"` to `web/package.json`.

### Output display

A styled `<pre>` element is sufficient. Color `#9cdcfe` (VS Code light-blue for JSON values) on dark background matches the editor theme.

### Simulation notes

A `<div id="preview-notes">` below the output pre shows simulation notes (skipped plugins, conversion errors) in small italic muted text.

### Debouncing

Both the config editor and the input event editor trigger simulation with a shared 300 ms debounce. The linter and simulator are independent — the linter runs via CodeMirror's `linter({delay: 300})` extension; the preview runs via its own `setTimeout`.

### Height propagation (CSS critical path)

CodeMirror requires explicit heights on all ancestor elements. The preview input editor needs:

```css
.preview-input-wrap { flex: 0 0 45%; display: flex; flex-direction: column; overflow: hidden; }
#preview-input-editor { flex: 1; overflow: hidden; min-height: 0; }
#preview-input-editor .cm-editor { height: 100%; }
```

Without `min-height: 0` on the flex child, CodeMirror collapses to zero height in some browsers.

---

## 8. Files affected

| File | Change |
|------|--------|
| `go/simulate.go` | **New** — simulation engine |
| `go/main.go` | +1 line: register WASM export |
| `Makefile` | Fix dependency glob |
| `web/package.json` | Add `@codemirror/lang-json` (+ optionally `@elastic/grok`) |
| `web/src/wasm-bridge.js` | Add `applyFilters()` export |
| `web/src/preview.js` | **New** — panel init, input editor, debounced sim, output render |
| `web/index.html` | Wrap `#editor` in `.editor-area`, add `.preview-panel` structure |
| `web/src/style.css` | Horizontal flex layout + panel styles |
| `web/src/editor.js` | Accept optional `{ onChange }` callback |
| `web/src/main.js` | Import preview, wire onChange, handle toggle |
