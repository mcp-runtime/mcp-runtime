package adapterd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

const launchdLabel = "org.mcpruntime.adapter"

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Binary}}</string>
    <string>adapter</string>
    <string>service</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>ProcessType</key>
  <string>Interactive</string>
{{- if .Env}}
  <key>EnvironmentVariables</key>
  <dict>
{{- range $key, $value := .Env}}
    <key>{{$key}}</key>
    <string>{{$value}}</string>
{{- end}}
  </dict>
{{- end}}
</dict>
</plist>
`

// LaunchdPlistPath is the per-user LaunchAgent path.
func LaunchdPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

// WriteLaunchdPlist writes the user LaunchAgent. It does not start the daemon.
func WriteLaunchdPlist(path, binary string, env map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return err
	}
	if err := tmpl.Execute(f, map[string]any{"Label": launchdLabel, "Binary": binary, "Env": env}); err != nil {
		return err
	}
	return nil
}

// RemoveLaunchdPlist deletes the managed plist.
func RemoveLaunchdPlist(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove launch agent: %w", err)
	}
	return nil
}
