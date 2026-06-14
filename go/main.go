package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"syscall/js"

	config "github.com/breml/logstash-config"
	"github.com/breml/logstash-config/ast"
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
	parsed, err := config.Parse("", []byte(input))
	if err == nil {
		result := ParseResult{OK: true, Diagnostics: []Diagnostic{}}
		if cfg, ok := parsed.(ast.Config); ok {
			result.Diagnostics = validate(cfg, input)
		}
		return marshal(result)
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
