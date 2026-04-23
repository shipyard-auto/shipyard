package crew

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// toolNameRe mirrors addons/crew/internal/crew.ToolNameRe. Duplicated here
// on purpose — the core CLI must not import the addon.
var toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// allowedHTTPMethods mirrors the addon's set of accepted verbs.
var allowedHTTPMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// allowedSchemaTypes mirrors addons/crew/internal/crew.isValidSchemaType.
// Duplicated locally so the core CLI stays independent of the addon —
// the addon revalidates at load time, which catches drift early.
var allowedSchemaTypes = map[string]bool{
	"string": true, "number": true, "boolean": true, "object": true, "array": true,
}

// toolDoc is the shape we serialise to ~/.shipyard/crew/tools/<name>.yaml.
// It intentionally mirrors the addon's crew.Tool struct — we keep it local
// to avoid importing across the boundary, and the addon's LoadTool
// validates everything again at runtime.
type toolDoc struct {
	Name         string            `yaml:"name"`
	Protocol     string            `yaml:"protocol"`
	Description  string            `yaml:"description,omitempty"`
	Command      []string          `yaml:"command,omitempty"`
	Method       string            `yaml:"method,omitempty"`
	URL          string            `yaml:"url,omitempty"`
	Headers      map[string]string `yaml:"headers,omitempty"`
	Body         string            `yaml:"body,omitempty"`
	InputSchema  map[string]string `yaml:"input_schema,omitempty"`
	OutputSchema map[string]string `yaml:"output_schema,omitempty"`
}

// newToolCmd is the shipyard crew tool parent command.
func newToolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Manage reusable tools for crew agents",
		Long: "Tools live in ~/.shipyard/crew/tools/<name>.yaml and can be referenced from\n" +
			"any agent via `tools: [{ref: <name>}]`. This command group CRUDs that library.",
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newToolAddCmd())
	cmd.AddCommand(newToolListCmd())
	cmd.AddCommand(newToolShowCmd())
	cmd.AddCommand(newToolRmCmd())
	return cmd
}

// --- add ---------------------------------------------------------------

type toolAddFlags struct {
	protocol     string
	description  string
	command      []string
	method       string
	url          string
	headers      []string
	body         string
	inputSchema  []string
	outputSchema []string
	force        bool
}

func newToolAddCmd() *cobra.Command {
	var f toolAddFlags
	cmd := &cobra.Command{
		Use:     "add <name>",
		Short:   "Create a new tool in the library",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runToolAdd(cmd.OutOrStdout(), args[0], f)
		},
	}
	cmd.Flags().StringVar(&f.protocol, "protocol", "", "protocol: exec | http (required)")
	cmd.Flags().StringVar(&f.description, "description", "", "human description shown to the LLM")
	cmd.Flags().StringArrayVar(&f.command, "command", nil, "exec: argv entry (repeat for each word)")
	cmd.Flags().StringVar(&f.method, "method", "", "http: verb (GET, POST, PUT, PATCH, DELETE)")
	cmd.Flags().StringVar(&f.url, "url", "", "http: target URL")
	cmd.Flags().StringArrayVar(&f.headers, "header", nil, "http: header as Key: Value (repeat)")
	cmd.Flags().StringVar(&f.body, "body", "", "http: request body template")
	cmd.Flags().StringArrayVar(&f.inputSchema, "input-schema", nil,
		"declare an input field as KEY=TYPE (repeat for each field; TYPE in {string,number,boolean,object,array})")
	cmd.Flags().StringArrayVar(&f.outputSchema, "output-schema", nil,
		"declare an output field inside data as KEY=TYPE (repeat; same TYPE set)")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite existing tool file")
	return cmd
}

func runToolAdd(out io.Writer, name string, f toolAddFlags) error {
	if !toolNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, toolNameRe)
	}

	inputSchema, err := parseSchemaFlag("--input-schema", f.inputSchema)
	if err != nil {
		return err
	}
	outputSchema, err := parseSchemaFlag("--output-schema", f.outputSchema)
	if err != nil {
		return err
	}

	doc := toolDoc{
		Name:         name,
		Description:  f.description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
	}
	switch f.protocol {
	case "exec":
		if len(f.command) == 0 {
			return errors.New("protocol \"exec\" requires at least one --command")
		}
		if f.method != "" || f.url != "" || len(f.headers) > 0 || f.body != "" {
			return errors.New("protocol \"exec\" must not set --method/--url/--header/--body")
		}
		doc.Protocol = "exec"
		doc.Command = f.command
	case "http":
		if !allowedHTTPMethods[strings.ToUpper(f.method)] {
			return fmt.Errorf("protocol \"http\" requires --method in {GET,POST,PUT,PATCH,DELETE}, got %q", f.method)
		}
		if strings.TrimSpace(f.url) == "" {
			return errors.New("protocol \"http\" requires --url")
		}
		if len(f.command) > 0 {
			return errors.New("protocol \"http\" must not set --command")
		}
		hdrs, err := parseHeaders(f.headers)
		if err != nil {
			return err
		}
		doc.Protocol = "http"
		doc.Method = strings.ToUpper(f.method)
		doc.URL = f.url
		doc.Headers = hdrs
		doc.Body = f.body
	default:
		return fmt.Errorf("invalid --protocol %q: must be \"exec\" or \"http\"", f.protocol)
	}

	home, err := shipyardHome()
	if err != nil {
		return err
	}
	libDir := filepath.Join(home, "crew", "tools")
	if err := os.MkdirAll(libDir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", libDir, err)
	}
	path := filepath.Join(libDir, name+".yaml")
	if _, err := os.Stat(path); err == nil {
		if !f.force {
			return fmt.Errorf("tool %q already exists at %s (use --force to overwrite)", name, path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(out, "Tool %q created at %s\n", name, path)
	return nil
}

// parseSchemaFlag parses a repeated --input-schema / --output-schema flag
// of the form KEY=TYPE into a map suitable for toolDoc. The kind argument
// is the flag name we report on errors so both use sites get precise
// messages without duplicating logic.
//
// Rules, in order:
//   - each entry must contain exactly one '=' (empty raw slice → nil map).
//   - key cannot be empty or whitespace-only.
//   - type must be in allowedSchemaTypes.
//   - duplicate keys across entries are rejected.
func parseSchemaFlag(kind string, raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, entry := range raw {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("invalid %s %q: expected KEY=TYPE", kind, entry)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			return nil, fmt.Errorf("invalid %s %q: empty field name", kind, entry)
		}
		if v == "" {
			return nil, fmt.Errorf("invalid %s %q: empty type", kind, entry)
		}
		if !allowedSchemaTypes[v] {
			return nil, fmt.Errorf("invalid %s %q: type %q not in {string,number,boolean,object,array}", kind, entry, v)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("invalid %s: field %q declared more than once", kind, k)
		}
		out[k] = v
	}
	return out, nil
}

func parseHeaders(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, h := range raw {
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			return nil, fmt.Errorf("invalid --header %q: expected Key: Value", h)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			return nil, fmt.Errorf("invalid --header %q: empty key", h)
		}
		out[k] = v
	}
	return out, nil
}

// --- list --------------------------------------------------------------

type toolListFlags struct {
	JSON bool
}

func newToolListCmd() *cobra.Command {
	var f toolListFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tools available in the library",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runToolList(cmd.OutOrStdout(), f)
		},
	}
	cmd.Flags().BoolVar(&f.JSON, "json", false, "emit output as JSON")
	return cmd
}

type toolListEntry struct {
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	Description string `json:"description,omitempty"`
}

func runToolList(out io.Writer, f toolListFlags) error {
	home, err := shipyardHome()
	if err != nil {
		return err
	}
	libDir := filepath.Join(home, "crew", "tools")
	entries := readToolLibrary(libDir)

	if f.JSON {
		if entries == nil {
			entries = []toolListEntry{}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "no tools")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPROTOCOL\tDESCRIPTION")
	for _, e := range entries {
		desc := e.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, defaultStr(e.Protocol, "-"), desc)
	}
	return tw.Flush()
}

// readToolLibrary returns a sorted list of tools in libDir. Broken files
// (bad YAML, wrong filename) are silently skipped so the library cannot
// be paralysed by one misbehaving file — callers that need to surface
// them can use `tool show <name>`, which parses strictly.
func readToolLibrary(libDir string) []toolListEntry {
	dir, err := os.ReadDir(libDir)
	if err != nil {
		return nil
	}
	out := make([]toolListEntry, 0, len(dir))
	for _, e := range dir {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".yaml")
		raw, err := os.ReadFile(filepath.Join(libDir, e.Name()))
		if err != nil {
			continue
		}
		var doc toolDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			continue
		}
		if doc.Name != base {
			continue
		}
		out = append(out, toolListEntry{
			Name:        doc.Name,
			Protocol:    doc.Protocol,
			Description: doc.Description,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- show --------------------------------------------------------------

func newToolShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Print a tool's definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runToolShow(cmd.OutOrStdout(), args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit output as JSON")
	return cmd
}

func runToolShow(out io.Writer, name string, asJSON bool) error {
	if !toolNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, toolNameRe)
	}
	home, err := shipyardHome()
	if err != nil {
		return err
	}
	path := filepath.Join(home, "crew", "tools", name+".yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("tool %q not found at %s", name, path)
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if asJSON {
		var doc toolDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	_, err = out.Write(raw)
	return err
}

// --- rm ----------------------------------------------------------------

type toolRmFlags struct {
	yes bool
}

func newToolRmCmd() *cobra.Command {
	var f toolRmFlags
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Short:   "Remove a tool from the library",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireInstalled,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runToolRm(cmd.OutOrStdout(), args[0], f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "skip agent-usage scan and force removal")
	return cmd
}

func runToolRm(out io.Writer, name string, f toolRmFlags) error {
	if !toolNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match %s", name, toolNameRe)
	}
	home, err := shipyardHome()
	if err != nil {
		return err
	}
	path := filepath.Join(home, "crew", "tools", name+".yaml")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("tool %q not found at %s", name, path)
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if !f.yes {
		users, err := findAgentUsers(filepath.Join(home, "crew"), name)
		if err != nil {
			return fmt.Errorf("scan agents: %w", err)
		}
		if len(users) > 0 {
			return fmt.Errorf("tool %q is referenced by agent(s): %s (use --yes to force)", name, strings.Join(users, ", "))
		}
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	fmt.Fprintf(out, "Tool %q removed.\n", name)
	return nil
}

// agentUsageDoc is the tolerant subset of agent.yaml read during rm-scan.
// Only the `ref` field matters — inline tools cannot use a name from the
// library, so they do not block removal.
type agentUsageDoc struct {
	Tools []struct {
		Ref string `yaml:"ref"`
	} `yaml:"tools"`
}

// findAgentUsers scans every <crewRoot>/<agent>/agent.yaml and returns the
// agent names that have `- ref: <toolName>` in their tools list. Broken or
// unreadable yamls are ignored (they will fail loudly at `crew run` time,
// which is the right place).
func findAgentUsers(crewRoot, toolName string) ([]string, error) {
	entries, err := os.ReadDir(crewRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var users []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "tools" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(crewRoot, e.Name(), "agent.yaml"))
		if err != nil {
			continue
		}
		var doc agentUsageDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			continue
		}
		for _, t := range doc.Tools {
			if strings.TrimSpace(t.Ref) == toolName {
				users = append(users, e.Name())
				break
			}
		}
	}
	sort.Strings(users)
	return users, nil
}
