package main

import (
	"fmt"
	"strings"

	"github.com/breml/logstash-config/ast"
)

// validate walks a parsed AST and returns warning diagnostics for
// unknown plugin names, unknown codec names, and unknown plugin options.
func validate(cfg ast.Config, input string) []Diagnostic {
	var diags []Diagnostic

	for _, section := range cfg.Input {
		diags = walkSection(section, input, diags)
	}
	for _, section := range cfg.Filter {
		diags = walkSection(section, input, diags)
	}
	for _, section := range cfg.Output {
		diags = walkSection(section, input, diags)
	}

	return diags
}

func walkSection(section ast.PluginSection, input string, diags []Diagnostic) []Diagnostic {
	for _, bop := range section.BranchOrPlugins {
		diags = walkBranchOrPlugin(bop, section.PluginType, input, diags)
	}
	return diags
}

func walkBranchOrPlugin(bop ast.BranchOrPlugin, pluginType ast.PluginType, input string, diags []Diagnostic) []Diagnostic {
	switch node := bop.(type) {
	case ast.Plugin:
		diags = validatePlugin(node, pluginType, input, diags)
	case ast.Branch:
		diags = walkBranch(node, pluginType, input, diags)
	}
	return diags
}

func walkBranch(branch ast.Branch, pluginType ast.PluginType, input string, diags []Diagnostic) []Diagnostic {
	for _, bop := range branch.IfBlock.Block {
		diags = walkBranchOrPlugin(bop, pluginType, input, diags)
	}
	for _, elseIf := range branch.ElseIfBlock {
		for _, bop := range elseIf.Block {
			diags = walkBranchOrPlugin(bop, pluginType, input, diags)
		}
	}
	for _, bop := range branch.ElseBlock.Block {
		diags = walkBranchOrPlugin(bop, pluginType, input, diags)
	}
	return diags
}

func validatePlugin(plugin ast.Plugin, pluginType ast.PluginType, input string, diags []Diagnostic) []Diagnostic {
	name := plugin.Name()
	offset := plugin.Pos().Offset

	// Validate plugin name
	pluginKnown := true
	if plugins, ok := knownPlugins[pluginType]; ok {
		if !plugins[name] {
			pluginKnown = false
			from := clampFrom(offset, input)
			to := clampTo(from+len(name), input)
			diags = append(diags, Diagnostic{
				From:     from,
				To:       to,
				Severity: "warning",
				Message:  fmt.Sprintf("unknown %s plugin %q", pluginType, name),
			})
		}
	}

	// Validate attributes (options + codec)
	knownOpts := getPluginOptions(pluginType, name)
	for _, attr := range plugin.Attributes {
		diags = validateAttribute(attr, pluginType, pluginKnown, knownOpts, input, diags)
	}

	return diags
}

func validateAttribute(attr ast.Attribute, pluginType ast.PluginType, pluginKnown bool, knownOpts map[string]bool, input string, diags []Diagnostic) []Diagnostic {
	attrName := attr.Name()

	// Check for codec attribute (PluginAttribute with nested plugin)
	if attrName == "codec" {
		if pa, ok := attr.(ast.PluginAttribute); ok {
			diags = validateCodecPlugin(pa, input, diags)
			return diags
		}
		// codec as string: extract name from ValueString()
		codecName := extractCodecName(attr.ValueString())
		if codecName != "" && !knownCodecs[codecName] {
			from := clampFrom(attr.Pos().Offset, input)
			// Position at the codec value, not the "codec" key.
			// Approximate: offset + len("codec => ") but we just use the attr pos
			// and highlight the codec name length.
			to := clampTo(from+len("codec")+len(" => ")+len(codecName), input)
			diags = append(diags, Diagnostic{
				From:     from,
				To:       to,
				Severity: "warning",
				Message:  fmt.Sprintf("unknown codec %q", codecName),
			})
		}
		return diags
	}

	// Skip option validation if plugin is unknown or we have no schema
	if !pluginKnown || knownOpts == nil {
		return diags
	}

	// Validate option name against known options
	if !knownOpts[attrName] {
		from := clampFrom(attr.Pos().Offset, input)
		to := clampTo(from+len(attrName), input)
		diags = append(diags, Diagnostic{
			From:     from,
			To:       to,
			Severity: "warning",
			Message:  fmt.Sprintf("unknown option %q", attrName),
		})
	}

	return diags
}

// validateCodecPlugin checks a codec specified as a nested plugin (e.g. codec => json {}).
func validateCodecPlugin(pa ast.PluginAttribute, input string, diags []Diagnostic) []Diagnostic {
	codecStr := pa.ValueString()
	codecName := extractCodecName(codecStr)
	if codecName != "" && !knownCodecs[codecName] {
		// Position at the codec plugin name inside the value
		from := clampFrom(pa.Pos().Offset, input)
		to := clampTo(from+len("codec")+len(" => ")+len(codecName), input)
		diags = append(diags, Diagnostic{
			From:     from,
			To:       to,
			Severity: "warning",
			Message:  fmt.Sprintf("unknown codec %q", codecName),
		})
	}
	return diags
}

// extractCodecName extracts the codec plugin name from a ValueString().
// ValueString() for a PluginAttribute returns something like "json {\n}\n" or "plain {\n}\n".
// For a StringAttribute it might be "json" or "\"json\"".
func extractCodecName(s string) string {
	s = strings.TrimSpace(s)
	// Remove surrounding quotes if present
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') {
		s = s[1 : len(s)-1]
	}
	// Take first word (before space or brace)
	for i, c := range s {
		if c == ' ' || c == '\t' || c == '{' || c == '\n' {
			return s[:i]
		}
	}
	return s
}

func clampFrom(offset int, input string) int {
	if offset < 0 {
		return 0
	}
	if offset >= len(input) {
		if len(input) > 0 {
			return len(input) - 1
		}
		return 0
	}
	return offset
}

func clampTo(offset int, input string) int {
	if offset > len(input) {
		return len(input)
	}
	if offset < 0 {
		return 0
	}
	return offset
}
