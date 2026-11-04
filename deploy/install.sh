#!/bin/sh
# Install or update the sessions binary from the GitHub release, then the timer.
#
#   gh release download latest -R stevmolina/agent-sessions -p install.sh -O - | sh
#
# Needs gh signed in to an account that can read the repo. Downloads only when the
# release is newer than the installed binary. Safe to run again.
# SESSIONS_SKIP_TIMER=1 installs the binary only.
set -eu
repo="${SESSIONS_REPO:-stevmolina/agent-sessions}"
tag="${SESSIONS_RELEASE:-latest}"
bindir="${HOME}/.local/bin"
bin="${bindir}/sessions"
die() { echo "install: $*" >&2; exit 1; }
command -v gh >/dev/null 2>&1 || die "gh is not installed"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported CPU: $(uname -m)" ;;
esac
asset="sessions_${os}_${arch}"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
gh release download "${tag}" -R "${repo}" -p VERSION -p install-timer.sh -D "${tmp}" \
  || die "cannot read release ${tag} of ${repo} (is gh signed in?)"
want="$(tr -d '[:space:]' < "${tmp}/VERSION")"
have="$("${bin}" version 2>/dev/null || true)"
if [ "${have}" = "${want}" ]; then
  echo "ok: sessions ${have} is current"
else
  gh release download "${tag}" -R "${repo}" -p "${asset}" -D "${tmp}" || die "no ${asset} in release ${tag}"
  chmod +x "${tmp}/${asset}"
  got="$("${tmp}/${asset}" version)" || die "downloaded binary does not run"
  mkdir -p "${bindir}"
  # Rename over the old file so a running push keeps its inode.
  mv -f "${tmp}/${asset}" "${bin}.new"
  mv -f "${bin}.new" "${bin}"
  echo "installed: sessions ${got} (was ${have:-none})"
fi

if [ "${SESSIONS_SKIP_TIMER:-}" != "1" ]; then
  sh "${tmp}/install-timer.sh"
fi
