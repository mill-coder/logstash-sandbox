# Feature 2: Semantic Validation

## Context

The playground currently catches only **syntax errors** (malformed config structure) via the `breml/logstash-config` PEG parser. It cannot detect semantic issues like misspelled plugin names (`grop` instead of `grok`), unknown plugin options (`matchh` instead of `match`), or invalid codec names. This plan adds AST-based semantic validation so the editor warns users about these content-level mistakes.

## Approach

After a successful `Parse()`, type-assert the result to `ast.Config`, walk the AST, and check plugin names and option names against an embedded registry of known Logstash plugins. Produce `"warning"`-severity diagnostics (yellow underlines, not red) since community/custom plugins are legitimate.

## Phase 1: Plugin Name Validation

### 1. Create `go/registry.go` — plugin name data

Hardcoded `map[string]bool` per plugin type. Data from Elastic's `plugins-metadata.json`. ~120 names total, negligible WASM size impact.

```go
var knownPlugins = map[ast.PluginType]map[string]bool{
    ast.Input:  {"beats": true, "file": true, "stdin": true, ...},  // ~35 names
    ast.Filter: {"grok": true, "mutate": true, "date": true, ...},  // ~37 names
    ast.Output: {"elasticsearch": true, "stdout": true, ...},        // ~24 names
}
var knownCodecs = map[string]bool{"json": true, "plain": true, ...}  // ~17 names
```

### 2. Create `go/validate.go` — AST walker + validation logic

- Walk `cfg.Input`, `cfg.Filter`, `cfg.Output` sections
- Recurse into `Branch` conditionals (if/else-if/else blocks)
- For each `Plugin`: check `Name()` against `knownPlugins[sectionType]`
- For `PluginAttribute` named `"codec"`: extract codec name from `ValueString()` (unexported `value` field — parse name as text before first `{` or space), validate against `knownCodecs`
- Produce `Diagnostic{From: plugin.Start.Offset, To: offset+len(name), Severity: "warning", Message: ...}`

### 3. Modify `go/main.go` — integrate validation after parse

```go
// Change: capture parse result, type-assert, call validate
parsed, err := config.Parse("", []byte(input))
if err == nil {
    result := ParseResult{OK: true, Diagnostics: []Diagnostic{}}
    if cfg, ok := parsed.(ast.Config); ok {
        result.Diagnostics = validate(cfg, input)
    }
    return marshal(result)
}
```

Add import: `"github.com/breml/logstash-config/ast"`

### 4. Modify `web/src/editor.js` — show warnings when parse succeeds

Current code returns `[]` when `result.ok === true`. Change to always map `result.diagnostics` to CodeMirror diagnostics. Move farthest-failure logic inside `if (!result.ok)`.

## Phase 2: Plugin Option Validation

### 5. Add common + per-plugin option schemas to `go/registry.go`

Common options shared by all plugins of each type:
- **Inputs**: `id`, `enable_metric`, `codec`, `type`, `tags`, `add_field`
- **Filters**: `id`, `enable_metric`, `add_tag`, `remove_tag`, `add_field`, `remove_field`, `periodic_flush`
- **Outputs**: `id`, `enable_metric`, `codec`, `workers`

Per-plugin options for the ~15-20 most common plugins (grok, mutate, date, geoip, elasticsearch, beats, stdin, stdout, file, csv, json, kv, dissect, ruby, http, tcp, udp, syslog). Data curated from Elastic docs.

### 6. Extend `go/validate.go` — option checking

For known plugins that have a schema, iterate `Plugin.Attributes`, check each `attr.Name()` against the union of common options and plugin-specific options. Unknown options → `"warning"` severity diagnostic positioned at `attr.Pos().Offset`.

Skip option checking for unknown plugins (already warned on name).

## Phase 3 (future): Scraper tool

Optional `tools/scrape-options/main.go` CLI that fetches Elastic doc pages and extracts option names from the settings tables. Outputs `go/plugin_options.json` which can be embedded via `//go:embed`. Not part of MVP.

## Files to create/modify

| File | Action | What |
|------|--------|------|
| `go/registry.go` | **Create** | Plugin names, codec names, common options, per-plugin option schemas |
| `go/validate.go` | **Create** | AST walker, name + option validation, codec validation |
| `go/main.go` | **Modify** | Type-assert parse result, call `validate()`, return warnings |
| `web/src/editor.js` | **Modify** | Display diagnostics when `result.ok` is true |

## Key design decisions

- **Warning not error**: Unknown plugins/options use `"warning"` severity (yellow) since custom/community plugins exist
- **Hardcoded maps**: ~120 plugin names + ~200 option names as Go map literals — simple, no runtime parsing, negligible size
- **PluginAttribute codec access**: `value` field is unexported; extract codec name from `ValueString()` string parsing
- **Position accuracy**: Use `Pos.Offset` (byte offset = char offset for ASCII Logstash configs, matching existing convention)

## Verification

1. `make dev` — build WASM + start dev server
2. Type valid config → no diagnostics
3. Type `filter { grop {} }` → yellow warning underline on "grop" saying "unknown filter plugin"
4. Type `filter { grok { matchh => {} } }` → warning on "matchh" saying "unknown option"
5. Type `input { stdin { codec => jsn {} } }` → warning on "jsn" saying "unknown codec"
6. Type `filter { grok { match => {} } }` → no warnings (valid plugin + valid option)
