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
#   GITHUB_TOKEN / GH_TOKEN          auth for the GitHub API (raises rate limits;
#                                    required while the repo/releases are private)
#
# The script is POSIX sh, needs only curl-or-wget + tar + sha256sum/shasum, and
# never requires root unless you point DEVSTACK_INSTALL_DIR at a system path.
set -eu

REPO="open-source-cloud/devstack"
BINARY="devstack"
API="https://api.github.com/repos/${REPO}"
TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"

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
	# dl_to and api send the token when set so PRIVATE-repo asset/API access works.
	# curl drops the Authorization header on the cross-host redirect to the asset
	# CDN (default since 7.58), so the token never leaks to storage.
	dl_to() { if [ -n "$TOKEN" ]; then curl -fsSL -H "Authorization: Bearer $TOKEN" -o "$2" "$1"; else curl -fsSL -o "$2" "$1"; fi; }
	api() { if [ -n "$TOKEN" ]; then curl -fsSL -H "Authorization: Bearer $TOKEN" "$1"; else curl -fsSL "$1"; fi; }
elif have wget; then
	dl() { wget -qO- "$1"; }
	dl_to() { if [ -n "$TOKEN" ]; then wget -qO "$2" --header="Authorization: Bearer $TOKEN" "$1"; else wget -qO "$2" "$1"; fi; }
	api() { if [ -n "$TOKEN" ]; then wget -qO- --header="Authorization: Bearer $TOKEN" "$1"; else wget -qO- "$1"; fi; }
else
	die "need curl or wget to download devstack"
fi
have tar || die "need tar to unpack the release archive"

# extract_tag pulls the first tag_name out of a GitHub releases JSON payload.
extract_tag() { grep '"tag_name"' | head -n1 | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/'; }

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
	# Prefer /releases/latest; fall back to the first entry of /releases (covers
	# pre-release-only repos and the brief post-publish API propagation window).
	tag="$(api "${API}/releases/latest" 2>/dev/null | extract_tag || true)"
	[ -n "$tag" ] || tag="$(api "${API}/releases" 2>/dev/null | extract_tag || true)"
	[ -n "$tag" ] || die "could not determine the latest release. Pin one with DEVSTACK_VERSION=vX.Y.Z, and if the repo is private set GITHUB_TOKEN."
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
	   # \$PATH is an escaped literal here (printed for the user to copy), not a variable.
	   printf "       export PATH=\"%s:\$PATH\"\n" "$install_dir" >&2 ;;
esac

# --- shell-integration hint ------------------------------------------------
# The eval hook puts the install dir on PATH, loads completions, and (the point)
# lets `${BINARY} use` switch your shell's workspace/project. Opt-in — we only
# print the line for the detected shell; we never edit your rc. The \$(...) is an
# escaped literal for the user to copy.
_ds_shell="$(basename "${SHELL:-sh}")"
case "$_ds_shell" in
	zsh)  info "shell integration — add to ~/.zshrc:  eval \"\$(${BINARY} shell-init zsh)\"" ;;
	bash) info "shell integration — add to ~/.bashrc: eval \"\$(${BINARY} shell-init bash)\"" ;;
	fish) info "shell integration — add to ~/.config/fish/config.fish:  ${BINARY} shell-init fish | source" ;;
	*)    info "shell integration (zsh/bash/fish): eval \"\$(${BINARY} shell-init <shell>)\" — enables '${BINARY} use' to switch your shell" ;;
esac

printf '\n%s%s installed.%s run %s%s doctor%s to verify your environment.\n' \
	"$GREEN" "$BINARY" "$RESET" "$BOLD" "$BINARY" "$RESET"
