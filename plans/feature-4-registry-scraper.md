# Feature 4: Auto-generate versioned plugin registry from Logstash source

## Context

The hand-curated `go/registry.go` is outdated — e.g. `tracking_field` for elasticsearch input is missing. Rather than fix entries manually, we build a scraper that auto-generates registry data from authoritative Ruby source files. Multiple Logstash versions must be supported, with live switching in the editor.

## How plugin versions map to Logstash releases

Each Logstash release branch (e.g. `8.15`) contains `Gemfile.jruby-3.1.lock.release` which pins exact plugin gem versions. For example:
- Logstash 8.10 bundles `logstash-input-elasticsearch 4.17.2` (no `tracking_field`)
- Logstash 8.15 bundles `logstash-input-elasticsearch 4.20.4` (no `tracking_field`)
- `tracking_field` only appears in `logstash-input-elasticsearch 5.1.0+` (not yet bundled)

The scraper fetches the lockfile for a given Logstash branch, then fetches each plugin's Ruby source at the pinned tag.

## Architecture

### Data flow

```
tools/scrape-registry/main.go -version 8.15
  │
  ├── GET elastic/logstash branch 8.15 → Gemfile.jruby-3.1.lock.release
  ├── Parse lockfile → {plugin-name: gem-version} map
  ├── For each plugin:
  │     GET logstash-plugins/{repo} tag v{gem-version} → Ruby source
  │     Regex-extract config declarations
  └── Write go/registrydata/8.15.json
```

### Versioned data storage

```
go/registrydata/
├── 8.10.json
├── 8.15.json
└── 8.17.json
```

Each JSON file contains the full registry for one Logstash version:
```json
{
  "version": "8.15",
  "plugins": {
    "input": ["beats", "elasticsearch", "file", ...],
    "filter": ["grok", "mutate", ...],
    "output": ["elasticsearch", "stdout", ...]
  },
  "codecs": ["json", "plain", "rubydebug", ...],
  "commonOptions": {
    "input": ["id", "enable_metric", "codec", "type", "tags", "add_field"],
    "filter": ["id", "enable_metric", "add_tag", "remove_tag", "add_field", "remove_field", "periodic_flush"],
    "output": ["id", "enable_metric", "codec", "workers"]
  },
  "pluginOptions": {
    "input/elasticsearch": ["hosts", "index", "query", "schedule", ...],
    "filter/grok": ["match", "overwrite", "patterns_dir", ...],
    ...
  }
}
```

Plugin options use `"{type}/{name}"` keys to handle plugins with the same name across types (e.g. `elasticsearch` exists as input, filter, and output with different options).

### Embedding in WASM via `//go:embed`

The JSON files are embedded into the WASM binary:

```go
//go:embed registrydata/*.json
var registryFS embed.FS
```

At WASM init, load the default version (highest available). On live switch, load a different JSON and rebuild the maps.

### Live version switching

New WASM function `window.setLogstashVersion(version)`:
- Loads the corresponding JSON from the embedded FS
- Rebuilds `knownPlugins`, `knownCodecs`, `commonOptions`, `pluginOptions` maps
- Returns `{"ok": true}` or error

New WASM function `window.getLogstashVersions()`:
- Returns JSON array of available version strings (e.g. `["8.10", "8.15", "8.17"]`)

The JS side adds a version dropdown in the header. On change, calls `setLogstashVersion()`, then re-triggers the linter to revalidate with the new registry.

## Files to create/modify

| File | Action | What |
|------|--------|------|
| `tools/scrape-registry/main.go` | **Create** | Scraper CLI: fetch lockfile + plugin sources, emit JSON |
| `go/registrydata/*.json` | **Create** (generated, committed) | One JSON file per Logstash version |
| `go/registry.go` | **Rewrite** | Load from embedded JSON, support version switching |
| `go/main.go` | **Modify** | Register `setLogstashVersion` + `getLogstashVersions`, add `//go:generate` |
| `Makefile` | **Modify** | Add `registry` target |
| `web/src/wasm-bridge.js` | **Modify** | Add `setVersion()` and `getVersions()` wrappers |
| `web/src/editor.js` | **Modify** | Re-lint on version change |
| `web/index.html` | **Modify** | Add version dropdown in header |
| `web/src/main.js` | **Modify** | Populate dropdown on init |
| `CLAUDE.md` | **Modify** | Document scraper + versioning |

## Step 1: Create `tools/scrape-registry/main.go`

### CLI interface

```bash
# Scrape a specific version
go run tools/scrape-registry/main.go -version 8.15 -out go/registrydata/8.15.json

# Or via Makefile
make registry VERSION=8.15
```

### Core logic

1. **Fetch lockfile**: GET `raw.githubusercontent.com/elastic/logstash/{version}/Gemfile.jruby-3.1.lock.release`
2. **Parse lockfile**: Extract `logstash-{type}-{name} (version)` and `logstash-integration-{name} (version)` entries
3. **Fetch common options**: GET logstash-core base classes (`plugins/inputs/base.rb`, etc.) from the same branch — extract `config :name` declarations
4. **For each plugin**:
   - Determine repo: `logstash-plugins/logstash-{type}-{name}` or `logstash-plugins/logstash-integration-{name}`
   - Fetch Ruby source at tag `v{gem-version}`: `lib/logstash/{type}s/{name}.rb`
   - For integrations: use GitHub API to list `lib/logstash/{inputs,filters,outputs}/*.rb`, fetch each
   - Regex-extract: `^\s*config\s+:(\w+)` → option names
5. **Write JSON** to output path

### Lockfile parsing

The lockfile format (Bundler) looks like:
```
    logstash-input-elasticsearch (4.20.4)
      ...dependencies...
    logstash-filter-grok (4.4.3)
```

Regex: `^\s{4}(logstash-(?:input|filter|output|codec|integration)-[\w-]+)\s+\(([\d.]+(?:-java)?)\)`

### Rate limiting

- Use `GITHUB_TOKEN` env var if set (5000 req/hr vs 60/hr)
- Fetch raw content where possible (doesn't count against API rate limit)
- GitHub API only needed for integration plugin directory listings

## Step 2: Rewrite `go/registry.go`

Replace hardcoded maps with embedded JSON loading:

```go
package main

import (
    "embed"
    "encoding/json"
    "sync"
    "github.com/breml/logstash-config/ast"
)

//go:embed registrydata/*.json
var registryFS embed.FS

type RegistryData struct {
    Version       string                       `json:"version"`
    Plugins       map[string][]string          `json:"plugins"`
    Codecs        []string                     `json:"codecs"`
    CommonOptions map[string][]string          `json:"commonOptions"`
    PluginOptions map[string][]string          `json:"pluginOptions"`
}

var (
    mu            sync.Mutex
    knownPlugins  map[ast.PluginType]map[string]bool
    knownCodecs   map[string]bool
    commonOptions map[ast.PluginType]map[string]bool
    pluginOptions map[string]map[string]bool
    currentVersion string
)

func loadVersion(version string) error {
    data, err := registryFS.ReadFile("registrydata/" + version + ".json")
    if err != nil {
        return err
    }
    var reg RegistryData
    if err := json.Unmarshal(data, &reg); err != nil {
        return err
    }
    mu.Lock()
    defer mu.Unlock()
    // rebuild knownPlugins, knownCodecs, commonOptions, pluginOptions from reg
    currentVersion = version
    return nil
}

func getPluginOptions(pluginType ast.PluginType, pluginName string) map[string]bool {
    // same merge logic, reads from current maps
}
```

## Step 3: WASM functions in `go/main.go`

```go
//go:generate go run ../tools/scrape-registry/main.go -version 8.15 -out registrydata/8.15.json

func init() {
    // Load default version (highest available)
    entries, _ := registryFS.ReadDir("registrydata")
    // pick highest version, call loadVersion()
}

func main() {
    js.Global().Set("parseLogstashConfig", js.FuncOf(parseLogstash))
    js.Global().Set("setLogstashVersion", js.FuncOf(setLogstashVersion))
    js.Global().Set("getLogstashVersions", js.FuncOf(getLogstashVersions))
    select {}
}
```

`setLogstashVersion(version string)` → calls `loadVersion()`, returns `{"ok": true}` or error.

`getLogstashVersions()` → reads `registryFS` directory, returns `["8.10", "8.15", "8.17"]`.

## Step 4: Frontend version selector

### `web/index.html`

Add dropdown in header:
```html
<select id="version"></select>
```

### `web/src/main.js`

On init, populate dropdown from `getVersions()`, set default, add change handler that calls `setVersion()` then re-lints.

### `web/src/wasm-bridge.js`

```js
export async function getVersions() { ... }
export async function setVersion(v) { ... }
```

### `web/src/editor.js`

Export a `relint()` function that forces the linter to re-run (dispatch a `ViewPlugin` update or replace the linter extension).

## Step 5: Makefile

```makefile
.PHONY: registry
registry:
	go run tools/scrape-registry/main.go -version $(VERSION) -out go/registrydata/$(VERSION).json

$(WASM_OUT): go/main.go go/go.mod go/registry.go go/registrydata/*.json
```

## Edge cases

- **Plugins with same name across types** (e.g. `elasticsearch`): `pluginOptions` uses `"{type}/{name}"` keys so each type has its own option set
- **Deprecated options**: include them (users may still use them)
- **Options from mixins/modules**: some plugins `include LogStash::PluginMixins::...`. For MVP, only extract `config` from the main file — mixins can be added later
- **GitHub fetch failures**: skip plugin with warning, don't fail the whole generation
- **Integration plugins** (kafka, jdbc, rabbitmq, snmp, aws): use GitHub API to discover `lib/logstash/{inputs,filters,outputs}/*.rb` files, parse each

## Verification

1. `make registry VERSION=8.15` → produces `go/registrydata/8.15.json`
2. `make registry VERSION=8.10` → produces `go/registrydata/8.10.json`
3. `make wasm` compiles with embedded JSON files
4. `make dev` → editor loads, version dropdown shows available versions
5. Select 8.15 → `tracking_field` is not recognized (not in pinned 4.20.4)
6. Scrape with latest plugin versions → `tracking_field` recognized
7. Switch versions in dropdown → linter re-runs, warnings update accordingly
