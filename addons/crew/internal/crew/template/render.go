// Package template implements a deliberately minimal placeholder engine used
// by crew tool definitions (command, url, headers, body) in agent.yaml.
//
// Supported syntax is literal substitution only — no loops, conditionals or
// nested placeholders. The grammar is {{<namespace>.<path>}} where
// <namespace> is one of input, env, agent. For input, <path> may be a
// dotted chain of identifiers that walks nested objects
// ({{input.message.chat.id}}); for env and agent it is a single flat key.
// Whitespace is tolerated between the braces and the expression.
package template

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrMissingValue is the sentinel returned (wrapped) when a placeholder is
// syntactically valid but the value is absent from the Context — an unknown
// input field, env var or agent key. Callers that want to degrade gracefully
// on absent data (e.g. conversation.key_fallback) match it with errors.Is;
// malformed templates produce a different error and stay fatal.
var ErrMissingValue = errors.New("template: missing")

// Context carries the substitution sources for a single Render call. Values
// are read-only from the engine's point of view; callers should not mutate
// maps while rendering is in progress.
type Context struct {
	Input map[string]any
	Env   map[string]string
	Agent map[string]string
}

var placeholderRe = regexp.MustCompile(`\{\{\s*(input|env|agent)\.([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)\s*\}\}`)

// Render substitutes every {{ns.path}} placeholder in tmpl with the value
// from ctx. A missing lookup produces an error wrapping ErrMissingValue; a
// leftover malformed placeholder produces a plain error. Scalars from Input
// are stringified without exponent notation (JSON numbers decode as float64,
// and a Telegram chat id must render as "987654321", not "9.87654321e+08");
// maps and slices fall back to fmt.Sprint.
func Render(tmpl string, ctx Context) (string, error) {
	var firstErr error
	out := placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		if firstErr != nil {
			return match
		}
		sub := placeholderRe.FindStringSubmatch(match)
		ns, path := sub[1], sub[2]
		val, err := lookup(ctx, ns, path)
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

func lookup(ctx Context, ns, path string) (string, error) {
	switch ns {
	case "input":
		v, err := walk(ctx.Input, strings.Split(path, "."))
		if err != nil {
			return "", err
		}
		return stringify(v), nil
	case "env":
		v, ok := ctx.Env[path]
		if !ok {
			return "", fmt.Errorf("%w env.%s", ErrMissingValue, path)
		}
		return v, nil
	case "agent":
		v, ok := ctx.Agent[path]
		if !ok {
			return "", fmt.Errorf("%w agent.%s", ErrMissingValue, path)
		}
		return v, nil
	default:
		return "", fmt.Errorf("template: unknown namespace %q", ns)
	}
}

// walk resolves a dotted path against a decoded JSON object. Every segment
// but the last must address a nested object; anything else (missing key,
// scalar mid-path) is reported as a missing value so callers can fall back
// rather than crash on payload shapes they do not control.
func walk(root map[string]any, segments []string) (any, error) {
	cur := any(root)
	for i, seg := range segments {
		obj, ok := asObject(cur)
		if !ok {
			return nil, fmt.Errorf("%w input.%s (input.%s is not an object)",
				ErrMissingValue, strings.Join(segments, "."), strings.Join(segments[:i], "."))
		}
		v, found := obj[seg]
		if !found {
			return nil, fmt.Errorf("%w input.%s", ErrMissingValue, strings.Join(segments[:i+1], "."))
		}
		cur = v
	}
	return cur, nil
}

// asObject normalises the two decoded shapes a JSON object can take here:
// map[string]any (encoding/json) and map[any]any (yaml.v3 pre-1.2 style).
func asObject(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			ks, ok := k.(string)
			if !ok {
				return nil, false
			}
			out[ks] = val
		}
		return out, true
	default:
		return nil, false
	}
}

// stringify renders a scalar the way an operator would write it by hand.
// The float64 case matters most: every JSON number decodes as float64, and
// fmt.Sprint switches to exponent notation past 21 digits of magnitude —
// which would silently mangle large integer ids into conversation keys.
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}
