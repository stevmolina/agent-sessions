#!/bin/sh
# Install the 15 minute push timer for this machine.
# Uses $(brew --prefix) and $HOME. Does not embed a database URL.
set -eu
prefix="$(brew --prefix)"
home="${HOME}"
bin="${home}/.local/bin/sessions"
path="${home}/.local/bin:${prefix}/bin:/usr/bin:/bin"
case "$(uname -s)" in
Darwin)
  dir="${home}/Library/LaunchAgents"
  mkdir -p "${dir}"
  plist="${dir}/com.stevmolina.sessions-push.plist"
  cat > "${plist}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.stevmolina.sessions-push</string>
  <key>ProgramArguments</key>
  <array>
    <string>${bin}</string>
    <string>push</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>${path}</string>
  </dict>
  <key>StartInterval</key>
  <integer>900</integer>
  <key>RunAtLoad</key>
  <true/>
</dict>
</plist>
EOF
  launchctl bootout "gui/$(id -u)/com.stevmolina.sessions-push" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "${plist}"
  ;;
Linux)
  dir="${XDG_CONFIG_HOME:-${home}/.config}/systemd/user"
  mkdir -p "${dir}"
  cat > "${dir}/sessions-push.service" <<EOF
[Unit]
Description=Push cleaned session transcripts

[Service]
Type=oneshot
ExecStart=${bin} push
Environment=PATH=${path}
EOF
  cat > "${dir}/sessions-push.timer" <<EOF
[Unit]
Description=Push cleaned session transcripts every 15 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=15min
AccuracySec=1min
Persistent=true

[Install]
WantedBy=timers.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable --now sessions-push.timer
  ;;
*)
  echo "unsupported OS: $(uname -s)" >&2
  exit 1
  ;;
esac
