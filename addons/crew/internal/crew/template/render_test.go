package template

import (
	"fmt"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	cases := []struct {
		name    string
		tmpl    string
		ctx     Context
		want    string
		wantErr string
	}{
		{
			name: "literal no placeholder",
			tmpl: "hello world",
			want: "hello world",
		},
		{
			name: "empty",
			tmpl: "",
			want: "",
		},
		{
			name: "input simple",
			tmpl: "hi {{input.name}}",
			ctx:  Context{Input: map[string]any{"name": "leo"}},
			want: "hi leo",
		},
		{
			name: "input int",
			tmpl: "n={{input.n}}",
			ctx:  Context{Input: map[string]any{"n": 42}},
			want: "n=42",
		},
		{
			name: "input bool",
			tmpl: "{{input.flag}}",
			ctx:  Context{Input: map[string]any{"flag": true}},
			want: "true",
		},
		{
			name: "input float",
			tmpl: "x={{input.x}}",
			ctx:  Context{Input: map[string]any{"x": 3.14}},
			want: "x=3.14",
		},
		{
			name: "env",
			tmpl: "tok={{env.TG_TOKEN}}",
			ctx:  Context{Env: map[string]string{"TG_TOKEN": "xyz"}},
			want: "tok=xyz",
		},
		{
			name: "agent",
			tmpl: "dir={{agent.dir}}",
			ctx:  Context{Agent: map[string]string{"dir": "/tmp/a"}},
			want: "dir=/tmp/a",
		},
		{
			name: "multiple namespaces",
			tmpl: "{{input.a}}/{{env.B}}/{{agent.c}}",
			ctx: Context{
				Input: map[string]any{"a": "1"},
				Env:   map[string]string{"B": "2"},
				Agent: map[string]string{"c": "3"},
			},
			want: "1/2/3",
		},
		{
			name: "whitespace tolerant",
			tmpl: "{{ input.a }}",
			ctx:  Context{Input: map[string]any{"a": "1"}},
			want: "1",
		},
		{
			name:    "missing input",
			tmpl:    "{{input.x}}",
			ctx:     Context{Input: map[string]any{}},
			wantErr: "template: missing input.x",
		},
		{
			name:    "missing env",
			tmpl:    "{{env.FOO}}",
			wantErr: "template: missing env.FOO",
		},
		{
			name:    "missing agent",
			tmpl:    "{{agent.z}}",
			wantErr: "template: missing agent.z",
		},
		{
			name:    "unknown namespace leftover",
			tmpl:    "{{secret.x}}",
			wantErr: "unresolved placeholder syntax",
		},
		{
			name: "same placeholder repeated",
			tmpl: "{{input.a}}-{{input.a}}",
			ctx:  Context{Input: map[string]any{"a": "1"}},
			want: "1-1",
		},
		{
			name: "unicode value",
			tmpl: "{{input.msg}}",
			ctx:  Context{Input: map[string]any{"msg": "olá 🌍"}},
			want: "olá 🌍",
		},
		{
			name: "empty string value",
			tmpl: "[{{input.a}}]",
			ctx:  Context{Input: map[string]any{"a": ""}},
			want: "[]",
		},
		{
			name: "trailing braces not consumed",
			tmpl: "{{input.a}}}}",
			ctx:  Context{Input: map[string]any{"a": "x"}},
			want: "x}}",
		},
		{
			name:    "malformed half-open",
			tmpl:    "{{input.foo}",
			ctx:     Context{Input: map[string]any{"foo": "bar"}},
			wantErr: "unresolved placeholder syntax",
		},
		{
			name: "map value via fmt.Sprint",
			tmpl: "{{input.m}}",
			ctx:  Context{Input: map[string]any{"m": map[string]int{"k": 1}}},
			want: "map[k:1]",
		},
		{
			name: "nil input value",
			tmpl: "{{input.n}}",
			ctx:  Context{Input: map[string]any{"n": nil}},
			want: "<nil>",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Render(c.tmpl, c.ctx)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (out=%q)", c.wantErr, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error = %q, want contains %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRenderMap(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		got, err := RenderMap(nil, Context{})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if got != nil {
			t.Fatalf("want nil, got %v", got)
		}
	})
	t.Run("empty returns empty", func(t *testing.T) {
		got, err := RenderMap(map[string]string{}, Context{})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("want empty, got %v", got)
		}
	})
	t.Run("success", func(t *testing.T) {
		in := map[string]string{
			"Authorization": "Bearer {{env.TOKEN}}",
			"X-Agent":       "{{agent.name}}",
		}
		ctx := Context{
			Env:   map[string]string{"TOKEN": "abc"},
			Agent: map[string]string{"name": "jarvis"},
		}
		got, err := RenderMap(in, ctx)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if got["Authorization"] != "Bearer abc" || got["X-Agent"] != "jarvis" {
			t.Fatalf("unexpected map: %v", got)
		}
	})
	t.Run("error wraps key", func(t *testing.T) {
		in := map[string]string{"X-Missing": "{{input.x}}"}
		_, err := RenderMap(in, Context{})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), `key "X-Missing"`) {
			t.Fatalf("error = %q, want key reference", err.Error())
		}
	})
}

func TestRenderSlice(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		got, err := RenderSlice(nil, Context{})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if got != nil {
			t.Fatalf("want nil, got %v", got)
		}
	})
	t.Run("success", func(t *testing.T) {
		in := []string{"/bin/echo", "{{input.msg}}"}
		ctx := Context{Input: map[string]any{"msg": "hi"}}
		got, err := RenderSlice(in, ctx)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if len(got) != 2 || got[1] != "hi" {
			t.Fatalf("unexpected: %v", got)
		}
	})
	t.Run("error wraps index", func(t *testing.T) {
		in := []string{"ok", "also-ok", "{{input.x}}"}
		_, err := RenderSlice(in, Context{})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "index 2") {
			t.Fatalf("error = %q, want index reference", err.Error())
		}
	})
}

func ExampleRender() {
	out, _ := Render("hello {{input.who}}", Context{Input: map[string]any{"who": "world"}})
	fmt.Println(out)
	// Output: hello world
}
