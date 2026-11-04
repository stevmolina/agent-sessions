#!/bin/sh
# Install the hourly health check on one always-on Linux machine (today: ovh).
#
#   sh deploy/install-health-timer.sh CHANNEL_ID "host-a,host-b,host-c"
#
# Each hour it runs `sessions health --notify-slack CHANNEL_ID`, which posts only
# when a machine turns bad or recovers. Announced state lives in
# ~/.cache/sessions/health-alert.json. The Slack bot token is read from Doppler
# (claudito/prd) at run time; nothing secret is written here.
set -eu
channel="${1:?usage: install-health-timer.sh CHANNEL_ID EXPECTED_MACHINES}"
expect="$(printf '%s' "${2:?usage: install-health-timer.sh CHANNEL_ID EXPECTED_MACHINES}" | tr -d '[:space:]')"
bin="${HOME}/.local/bin/sessions"
die() { echo "install-health-timer: $*" >&2; exit 1; }
[ "$(uname -s)" = Linux ] || die "Linux only (systemd user timer)"
[ -x "${bin}" ] || die "no binary at ${bin}"
dir="${XDG_CONFIG_HOME:-${HOME}/.config}/systemd/user"
mkdir -p "${dir}"
cat > "${dir}/sessions-health.service" <<EOF
[Unit]
Description=Alert on machines that stopped pushing sessions

[Service]
Type=oneshot
ExecStart=${bin} health --notify-slack ${channel} --expect ${expect}
Environment=PATH=${HOME}/.local/bin:/usr/local/bin:/usr/bin:/bin
TimeoutStartSec=5min
EOF
cat > "${dir}/sessions-health.timer" <<EOF
[Unit]
Description=Check session push health every hour

[Timer]
OnBootSec=10min
OnUnitActiveSec=1h
AccuracySec=1min
Persistent=true

[Install]
WantedBy=timers.target
EOF
systemctl --user daemon-reload
systemctl --user enable --now sessions-health.timer >/dev/null 2>&1 || die "systemctl could not enable sessions-health.timer"
systemctl --user is-active --quiet sessions-health.timer || die "sessions-health.timer is not active"
if [ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || echo unknown)" != "yes" ]; then
  echo "warning: linger is off, so the timer stops when you log out (sudo loginctl enable-linger $(id -un))" >&2
fi
echo "ok: sessions-health.timer is enabled and active (hourly; journalctl --user -u sessions-health)"
