#!/bin/sh
# Install the 15 minute push timer for this machine, then prove it is loaded.
#
# Safe to run again: it rewrites the unit files, reloads them, and checks that the
# service manager really has the job. It exits non-zero, loudly, if it does not.
# On 2026-11-02 the Mac had the plist on disk but launchd had no such job, and the
# shared database went 19 days without a push. Nothing noticed. This check is the fix
# for the "installed but not running" half; `sessions health` covers the rest.
#
# Uses $HOME and, on macOS, $(brew --prefix) for PATH. Embeds no secret.
set -eu
label="com.stevmolina.sessions-push"
bin="${HOME}/.local/bin/sessions"
die() { echo "install-timer: $*" >&2; exit 1; }
[ -x "${bin}" ] || die "no binary at ${bin}; run deploy/install.sh first"

case "$(uname -s)" in
Darwin)
  prefix="$(brew --prefix 2>/dev/null || echo /opt/homebrew)"
  # /usr/local/bin covers Doppler installed by its own script or Intel Homebrew.
  path="${HOME}/.local/bin:${prefix}/bin:/usr/local/bin:/usr/bin:/bin"
  dir="${HOME}/Library/LaunchAgents"
  logdir="${HOME}/.cache/sessions"
  mkdir -p "${dir}" "${logdir}"
  plist="${dir}/${label}.plist"
  cat > "${plist}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${label}</string>
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
  <key>StandardOutPath</key>
  <string>${logdir}/timer.log</string>
  <key>StandardErrorPath</key>
  <string>${logdir}/timer.log</string>
</dict>
</plist>
EOF
  plutil -lint "${plist}" >/dev/null || die "invalid plist ${plist}"
  domain="gui/$(id -u)"
  # A job someone disabled stays disabled across bootstrap; clear that first.
  launchctl enable "${domain}/${label}" 2>/dev/null || true
  launchctl bootout "${domain}/${label}" 2>/dev/null || true
  # bootout returns before the job is fully gone; bootstrap can then fail with
  # "5: Input/output error". Retry a few times.
  n=0
  until err="$(launchctl bootstrap "${domain}" "${plist}" 2>&1)"; do
    n=$((n + 1))
    # Error 125 over SSH means no GUI login session; retrying will not help.
    [ "${n}" -lt 5 ] || die "launchctl bootstrap failed for ${plist}: ${err}"
    sleep 1
  done
  launchctl print "${domain}/${label}" >/dev/null 2>&1 || die "launchd does not have ${label} loaded"
  echo "ok: launchd has ${label} loaded (every 15 min; log ${logdir}/timer.log)"
  ;;
Linux)
  path="${HOME}/.local/bin:/usr/local/bin:/usr/bin:/bin"
  dir="${XDG_CONFIG_HOME:-${HOME}/.config}/systemd/user"
  mkdir -p "${dir}"
  cat > "${dir}/sessions-push.service" <<EOF
[Unit]
Description=Push cleaned session transcripts

[Service]
Type=oneshot
ExecStart=${bin} push
Environment=PATH=${path}
# One run can loop up to five pushes (pushlock.MaxRuns), each with a 10 minute cap.
TimeoutStartSec=60min
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
  systemctl --user enable --now sessions-push.timer >/dev/null 2>&1 || die "systemctl could not enable sessions-push.timer"
  systemctl --user is-enabled --quiet sessions-push.timer || die "sessions-push.timer is not enabled"
  systemctl --user is-active --quiet sessions-push.timer || die "sessions-push.timer is not active"
  if [ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || echo unknown)" != "yes" ]; then
    echo "warning: linger is off, so the timer stops when you log out (sudo loginctl enable-linger $(id -un))" >&2
  fi
  echo "ok: sessions-push.timer is enabled and active (every 15 min; journalctl --user -u sessions-push)"
  ;;
*)
  die "unsupported OS: $(uname -s)"
  ;;
esac
