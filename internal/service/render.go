package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func systemdUnitName(id string) string {
	return fmt.Sprintf("shipyard-%s.service", strings.ToUpper(strings.TrimSpace(id)))
}

func launchdLabel(id string) string {
	return fmt.Sprintf("com.shipyard.service.%s", strings.ToUpper(strings.TrimSpace(id)))
}

func launchdPlistName(id string) string {
	return launchdLabel(id) + ".plist"
}

func renderSystemdUnit(record ServiceRecord) string {
	record = withDefaultEnvironment(record, "")
	lines := []string{
		"# Managed by Shipyard - do not edit manually.",
		fmt.Sprintf("# id=%s", record.ID),
		"[Unit]",
		"Description=" + renderDescription(record),
		"",
		"[Service]",
		"Type=simple",
		fmt.Sprintf("ExecStart=/bin/sh -lc '%s'", escapeShellSingleQuoted(record.Command)),
		fmt.Sprintf("Restart=%s", map[bool]string{true: "on-failure", false: "no"}[record.AutoRestart]),
		fmt.Sprintf("StandardOutput=append:%s", serviceStdoutPath(record.ID)),
		fmt.Sprintf("StandardError=append:%s", serviceStderrPath(record.ID)),
	}
	if record.WorkingDir != "" {
		lines = append(lines, "WorkingDirectory="+record.WorkingDir)
	}
	for _, entry := range sortedEnvironmentEntries(record.Environment) {
		lines = append(lines, fmt.Sprintf("Environment=\"%s\"", entry))
	}
	lines = append(lines, "", "[Install]", "WantedBy=default.target", "")
	return strings.Join(lines, "\n")
}

func renderLaunchdPlist(record ServiceRecord) string {
	record = withDefaultEnvironment(record, "")
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	buf.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	writePlistKeyString(&buf, "Label", launchdLabel(record.ID))
	buf.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range []string{"/bin/sh", "-lc", record.Command} {
		buf.WriteString("    <string>" + escapeXML(arg) + "</string>\n")
	}
	buf.WriteString("  </array>\n")
	if record.Enabled {
		writePlistBool(&buf, "RunAtLoad", true)
	}
	if record.AutoRestart {
		writePlistBool(&buf, "KeepAlive", true)
	}
	if record.WorkingDir != "" {
		writePlistKeyString(&buf, "WorkingDirectory", record.WorkingDir)
	}
	if len(record.Environment) > 0 {
		buf.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		keys := make([]string, 0, len(record.Environment))
		for key := range record.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			writePlistKeyString(&buf, key, record.Environment[key])
		}
		buf.WriteString("  </dict>\n")
	}
	writePlistKeyString(&buf, "StandardOutPath", serviceStdoutPath(record.ID))
	writePlistKeyString(&buf, "StandardErrorPath", serviceStderrPath(record.ID))
	buf.WriteString("</dict>\n</plist>\n")
	return buf.String()
}

func renderDescription(record ServiceRecord) string {
	if strings.TrimSpace(record.Description) == "" {
		return record.Name
	}
	return record.Name + " - " + strings.TrimSpace(record.Description)
}

func escapeShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, `'`, `'\''`)
}

func withDefaultEnvironment(record ServiceRecord, homeDir string) ServiceRecord {
	record.Environment = cloneEnvironment(record.Environment)
	if record.Environment == nil {
		record.Environment = map[string]string{}
	}
	if strings.TrimSpace(homeDir) != "" {
		record.Environment["HOME"] = homeDir
	}
	return record
}

func serviceStdoutPath(id string) string {
	return filepath.Join("/tmp", fmt.Sprintf("shipyard-%s.out.log", strings.ToUpper(strings.TrimSpace(id))))
}

func serviceStderrPath(id string) string {
	return filepath.Join("/tmp", fmt.Sprintf("shipyard-%s.err.log", strings.ToUpper(strings.TrimSpace(id))))
}

func sortedEnvironmentEntries(environment map[string]string) []string {
	if len(environment) == 0 {
		return nil
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+environment[key])
	}
	return out
}

func writePlistKeyString(buf *bytes.Buffer, key, value string) {
	buf.WriteString("  <key>" + escapeXML(key) + "</key>\n")
	buf.WriteString("  <string>" + escapeXML(value) + "</string>\n")
}

func writePlistBool(buf *bytes.Buffer, key string, value bool) {
	buf.WriteString("  <key>" + escapeXML(key) + "</key>\n")
	if value {
		buf.WriteString("  <true/>\n")
		return
	}
	buf.WriteString("  <false/>\n")
}

func escapeXML(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}
