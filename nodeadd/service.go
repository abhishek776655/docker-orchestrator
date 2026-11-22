package nodeadd

// SystemdUnit is the user unit installed on Linux targets. %h is the home
// directory, so one text works for any user.
func SystemdUnit() string {
	return `[Unit]
Description=haco agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=%h/.haco/bin/agent run
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`
}

// LaunchdPlist is the LaunchAgent installed on macOS targets. It is written
// through an unquoted heredoc, so $HOME expands to the target's home
// directory (launchd does not expand it itself). PATH covers Docker
// Desktop's CLI, which launchd's minimal environment lacks.
func LaunchdPlist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>co.haco.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>$HOME/.haco/bin/agent</string>
    <string>run</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:/Applications/Docker.app/Contents/Resources/bin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/.haco/logs/agent.log</string>
  <key>StandardErrorPath</key><string>$HOME/.haco/logs/agent.log</string>
</dict>
</plist>
`
}
