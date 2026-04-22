// Package template implements a deliberately minimal placeholder engine used
// by crew tool definitions (command, url, headers, body) in agent.yaml.
//
// Supported syntax is literal substitution only — no loops, conditionals or
// nested placeholders. The grammar is {{<namespace>.<key>}} where <namespace>
// is one of input, env, agent and <key> is an identifier. Whitespace is
// tolerated between the braces and the expression.
package template

import (
	"fmt"
	"regexp"
	"strings"
)

// Context carries the substitution sources for a single Render call. Values
// are read-only from the engine's point of view; callers should not mutate
// maps while rendering is in progress.
type Context struct {
	Input map[string]any
	Env   map[string]string
	Agent map[string]string
}

var placeholderRe = regexp.MustCompile(`\{\{\s*(input|env|agent)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// Render substitutes every {{ns.key}} placeholder in tmpl with the value from
// ctx. Any missing lookup or leftover malformed placeholder produces an
// error. Values from Input are stringified via fmt.Sprint, which yields
// Go-native representations for maps/slices.
func Render(tmpl string, ctx Context) (string, error) {
	var firstErr error
	out := placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		if firstErr != nil {
			return match
		}
		sub := placeholderRe.FindStringSubmatch(match)
		ns, key := sub[1], sub[2]
		val, err := lookup(ctx, ns, key)
		if err != nil {
			firstErr = err
			return match
		}
		return val
	})
	if firstErr != nil {
		return "", firstErr
	}
	if strings.Contains(out, "{{") {
		return "", fmt.Errorf("template: unresolved placeholder syntax in output")
	}
	return out, nil
}

// RenderMap renders every value of m, preserving keys. A nil map returns
// nil, nil. Errors are wrapped with the offending key.
func RenderMap(m map[string]string, ctx Context) (map[string]string, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		rv, err := Render(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("template: key %q: %w", k, err)
		}
		out[k] = rv
	}
	return out, nil
}

// RenderSlice renders every element of items. A nil slice returns nil, nil.
// Errors are wrapped with the offending index.
func RenderSlice(items []string, ctx Context) ([]string, error) {
	if items == nil {
		return nil, nil
	}
	out := make([]string, len(items))
	for i, v := range items {
		rv, err := Render(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("template: index %d: %w", i, err)
		}
		out[i] = rv
	}
	return out, nil
}

func lookup(ctx Context, ns, key string) (string, error) {
	switch ns {
	case "input":
		v, ok := ctx.Input[key]
		if !ok {
			return "", fmt.Errorf("template: missing input.%s", key)
		}
		return fmt.Sprint(v), nil
	case "env":
		v, ok := ctx.Env[key]
		if !ok {
			return "", fmt.Errorf("template: missing env.%s", key)
		}
		return v, nil
	case "agent":
		v, ok := ctx.Agent[key]
		if !ok {
			return "", fmt.Errorf("template: missing agent.%s", key)
		}
		return v, nil
	default:
		return "", fmt.Errorf("template: unknown namespace %q", ns)
	}
}
