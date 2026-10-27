#!/bin/sh
# Install reclaim from a GitHub release, for Linux machines and CI runners
# without Homebrew. It needs curl or wget, tar, and sha256sum or shasum, and
# never uses sudo.
#
#   curl -fsSL https://raw.githubusercontent.com/mandloideep/reclaim/main/scripts/install.sh | sh
#   sh install.sh --version v0.2.0 --bin-dir /usr/local/bin
#
# Options:
#   --version TAG   install this release, such as v0.2.0, instead of the latest
#   --bin-dir DIR   install into DIR instead of ~/.local/bin
#   --base-url URL  download the archives and checksums.txt from URL, such as
#                   file:///path/to/dist for a local snapshot build
set -eu

repo="mandloideep/reclaim"
version=""
bin_dir="${HOME}/.local/bin"
base_url=""

fail() {
	printf 'reclaim install: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<'USAGE'
Install reclaim from a GitHub release, without sudo.

Usage: install.sh [--version TAG] [--bin-dir DIR] [--base-url URL]

  --version TAG   install this release, such as v0.2.0, instead of the latest
  --bin-dir DIR   install into DIR instead of ~/.local/bin
  --base-url URL  download the archives and checksums.txt from URL, such as
                  file:///path/to/dist for a local snapshot build
USAGE
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || fail "--version needs a tag, such as v0.2.0"
		version="$2"
		shift 2
		;;
	--version=*)
		version="${1#--version=}"
		shift
		;;
	--bin-dir)
		[ $# -ge 2 ] || fail "--bin-dir needs a folder"
		bin_dir="$2"
		shift 2
		;;
	--bin-dir=*)
		bin_dir="${1#--bin-dir=}"
		shift
		;;
	--base-url)
		[ $# -ge 2 ] || fail "--base-url needs a URL"
		base_url="$2"
		shift 2
		;;
	--base-url=*)
		base_url="${1#--base-url=}"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		fail "unknown option $1; see --help"
		;;
	esac
done

case "$(uname -s)" in
Linux) os="linux" ;;
Darwin) os="darwin" ;;
*) fail "unsupported operating system $(uname -s); reclaim runs on Linux and macOS" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch="amd64" ;;
arm64 | aarch64) arch="arm64" ;;
*) fail "unsupported architecture $(uname -m); reclaim is built for amd64 and arm64" ;;
esac

if [ -z "$base_url" ]; then
	if [ -n "$version" ]; then
		case "$version" in
		v*) ;;
		*) version="v${version}" ;;
		esac
		base_url="https://github.com/${repo}/releases/download/${version}"
	else
		base_url="https://github.com/${repo}/releases/latest/download"
	fi
fi
base_url="${base_url%/}"

# fetch URL FILE downloads URL to FILE with curl or wget, or copies a file://
# URL, which wget cannot read.
fetch() {
	case "$1" in
	file://*)
		cp "${1#file://}" "$2" || fail "cannot read ${1#file://}"
		;;
	*)
		if command -v curl >/dev/null 2>&1; then
			curl -fsSL -o "$2" "$1" || fail "cannot download $1"
		elif command -v wget >/dev/null 2>&1; then
			wget -q -O "$2" "$1" || fail "cannot download $1"
		else
			fail "curl or wget is needed to download $1"
		fi
		;;
	esac
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		fail "sha256sum or shasum is needed to check the download"
	fi
}

command -v tar >/dev/null 2>&1 || fail "tar is needed to unpack the download"

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t reclaim-install)"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM

fetch "${base_url}/checksums.txt" "${tmp}/checksums.txt"
# The archive is named reclaim_<version>_<os>_<arch>.tar.gz; checksums.txt
# lists every archive of the release, so it names the one to download.
# The name may hold only letters, digits, dots, dashes and underscores, so it
# can never name a path outside the temporary folder.
archive="$(awk -v suffix="_${os}_${arch}.tar.gz" '
	length($1) == 64 && $1 ~ /^[0-9a-f]+$/ && $2 ~ /^reclaim_[A-Za-z0-9._-]+$/ &&
	substr($2, length($2) - length(suffix) + 1) == suffix { print $2; exit }
' "${tmp}/checksums.txt")"
[ -n "$archive" ] || fail "${base_url}/checksums.txt lists no archive for ${os}/${arch}"
want="$(awk -v name="$archive" '$2 == name { print $1; exit }' "${tmp}/checksums.txt")"

printf 'Downloading %s\n' "${base_url}/${archive}"
fetch "${base_url}/${archive}" "${tmp}/${archive}"
got="$(sha256 "${tmp}/${archive}")"
if [ "$got" != "$want" ]; then
	fail "the checksum of ${archive} does not match checksums.txt (got ${got}, want ${want}); nothing was installed"
fi

mkdir -p "${tmp}/unpacked"
tar -xzf "${tmp}/${archive}" -C "${tmp}/unpacked" reclaim || fail "${archive} holds no reclaim binary"
mkdir -p "$bin_dir" || fail "cannot create ${bin_dir}"
cp "${tmp}/unpacked/reclaim" "${bin_dir}/reclaim.tmp.$$" || fail "cannot write to ${bin_dir}"
chmod 0755 "${bin_dir}/reclaim.tmp.$$"
mv -f "${bin_dir}/reclaim.tmp.$$" "${bin_dir}/reclaim"

printf 'Installed %s\n' "$("${bin_dir}/reclaim" version)"
printf 'into %s\n' "${bin_dir}/reclaim"

case ":${PATH}:" in
*":${bin_dir}:"*) ;;
# shellcheck disable=SC2016 # $PATH is printed for the user's profile, not expanded
*) printf '\n%s is not on your PATH; add it in your shell profile:\n  export PATH="%s:$PATH"\n' "$bin_dir" "$bin_dir" ;;
esac

case "$(basename "${SHELL:-sh}")" in
bash) printf '\nShell completion: add this line to ~/.bashrc\n  source <(reclaim completion bash)\n' ;;
zsh) printf '\nShell completion: add this line to ~/.zshrc\n  source <(reclaim completion zsh)\n' ;;
fish) printf '\nShell completion:\n  reclaim completion fish > ~/.config/fish/completions/reclaim.fish\n' ;;
*) printf '\nShell completion: see reclaim completion --help\n' ;;
esac
