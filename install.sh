#!/bin/sh
# devstack installer — downloads the right release binary for your OS/arch from
# GitHub Releases, verifies its checksum, and installs it.
#
# Quick start:
#   curl -fsSL https://raw.githubusercontent.com/open-source-cloud/devstack/main/install.sh | sh
#
# Options (environment variables):
#   DEVSTACK_VERSION=v0.1.0          pin a version (default: latest release)
#   DEVSTACK_INSTALL_DIR=/usr/bin    install dir (default: $XDG_BIN_HOME or ~/.local/bin)
#   DEVSTACK_ALIASES="rq uranus"     also install argv[0] alias symlinks
#   DEVSTACK_NO_VERIFY=1             skip the SHA-256 checksum verification
#
# The script is POSIX sh, needs only curl-or-wget + tar + sha256sum/shasum, and
# never requires root unless you point DEVSTACK_INSTALL_DIR at a system path.
set -eu

REPO="open-source-cloud/devstack"
BINARY="devstack"

# --- pretty output ---------------------------------------------------------
if [ -t 1 ]; then
	BOLD="$(printf '\033[1m')"; GREEN="$(printf '\033[32m')"; RED="$(printf '\033[31m')"; RESET="$(printf '\033[0m')"
else
	BOLD=""; GREEN=""; RED=""; RESET=""
fi
info() { printf '%s==>%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '%swarning:%s %s\n' "$RED" "$RESET" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

# --- prerequisites ---------------------------------------------------------
have() { command -v "$1" >/dev/null 2>&1; }

if have curl; then
	dl() { curl -fsSL "$1"; }
	dl_to() { curl -fsSL -o "$2" "$1"; }
elif have wget; then
	dl() { wget -qO- "$1"; }
	dl_to() { wget -qO "$2" "$1"; }
else
	die "need curl or wget to download devstack"
fi
have tar || die "need tar to unpack the release archive"

# --- detect platform -------------------------------------------------------
os="$(uname -s)"
case "$os" in
	Linux)  os="linux" ;;
	Darwin) os="darwin" ;;
	*) die "unsupported OS: $os (devstack ships linux and darwin builds; on Windows use WSL2)" ;;
esac

arch="$(uname -m)"
case "$arch" in
	x86_64 | amd64)  arch="amd64" ;;
	aarch64 | arm64) arch="arm64" ;;
	*) die "unsupported architecture: $arch (devstack ships amd64 and arm64)" ;;
esac

# --- resolve version -------------------------------------------------------
tag="${DEVSTACK_VERSION:-}"
if [ -z "$tag" ]; then
	info "resolving latest release"
	tag="$(dl "https://api.github.com/repos/${REPO}/releases/latest" \
		| grep '"tag_name"' | head -n1 | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/')"
	[ -n "$tag" ] || die "could not determine the latest release (set DEVSTACK_VERSION to pin one)"
fi
# goreleaser strips the leading 'v' from the archive filename's version field.
version="${tag#v}"

archive="${BINARY}_${version}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${tag}"

# --- install location ------------------------------------------------------
install_dir="${DEVSTACK_INSTALL_DIR:-${XDG_BIN_HOME:-$HOME/.local/bin}}"
mkdir -p "$install_dir" || die "cannot create install dir $install_dir"

# --- download + verify + unpack -------------------------------------------
tmp="$(mktemp -d "${TMPDIR:-/tmp}/devstack-install.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT INT TERM

info "downloading ${BOLD}${archive}${RESET} (${tag})"
dl_to "${base}/${archive}" "${tmp}/${archive}" || die "download failed: ${base}/${archive}"

if [ "${DEVSTACK_NO_VERIFY:-0}" != "1" ]; then
	if have sha256sum; then sha_cmd="sha256sum";
	elif have shasum; then sha_cmd="shasum -a 256";
	else warn "no sha256sum/shasum found — skipping checksum verification"; sha_cmd=""; fi
	if [ -n "$sha_cmd" ]; then
		info "verifying checksum"
		dl_to "${base}/checksums.txt" "${tmp}/checksums.txt" || die "could not fetch checksums.txt"
		want="$(grep " ${archive}\$" "${tmp}/checksums.txt" | awk '{print $1}')"
		[ -n "$want" ] || die "no checksum entry for ${archive}"
		got="$(cd "$tmp" && $sha_cmd "$archive" | awk '{print $1}')"
		[ "$want" = "$got" ] || die "checksum mismatch for ${archive} (expected $want, got $got)"
	fi
fi

info "unpacking"
tar -xzf "${tmp}/${archive}" -C "$tmp"
[ -f "${tmp}/${BINARY}" ] || die "archive did not contain a ${BINARY} binary"

install -m 0755 "${tmp}/${BINARY}" "${install_dir}/${BINARY}" 2>/dev/null \
	|| { cp "${tmp}/${BINARY}" "${install_dir}/${BINARY}" && chmod 0755 "${install_dir}/${BINARY}"; }
info "installed ${BOLD}${install_dir}/${BINARY}${RESET}"

# --- optional argv[0] aliases ---------------------------------------------
if [ -n "${DEVSTACK_ALIASES:-}" ]; then
	for a in $DEVSTACK_ALIASES; do
		ln -sf "${BINARY}" "${install_dir}/${a}" && info "aliased ${a} -> ${BINARY}"
	done
fi

# --- PATH hint -------------------------------------------------------------
case ":${PATH}:" in
	*":${install_dir}:"*) ;;
	*) warn "${install_dir} is not on your PATH — add it, e.g.:"
	   printf '       export PATH="%s:$PATH"\n' "$install_dir" >&2 ;;
esac

printf '\n%s%s installed.%s run %s%s doctor%s to verify your environment.\n' \
	"$GREEN" "$BINARY" "$RESET" "$BOLD" "$BINARY" "$RESET"
