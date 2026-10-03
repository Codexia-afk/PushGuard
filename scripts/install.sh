#!/bin/sh
set -eu

# PushGuard binary installer. The release base is configurable so the same
# script can be served from GitHub Releases, a company mirror, or a future
# product domain without changing its download or verification logic.
DEFAULT_RELEASE_BASE="https://github.com/Codexia-afk/Hackspire_project/releases/latest/download"
RELEASE_BASE=${PUSHGUARD_RELEASE_BASE_URL:-"$DEFAULT_RELEASE_BASE"}
INSTALL_DIR=${PUSHGUARD_INSTALL_DIR:-"$HOME/.local/bin"}

fail() {
    printf 'PushGuard installer: %s\n' "$1" >&2
    exit 2
}

source_install() {
    project_dir=${PUSHGUARD_SOURCE_DIR:-"$(pwd)"}
    [ -f "$project_dir/go.mod" ] && [ -d "$project_dir/cmd/pushguard" ] || fail "source installation requires a PushGuard source directory; unset binary mode only when installing from the source checkout"
    command -v go >/dev/null 2>&1 || fail "Go 1.22+ is required for a source installation"
    go_version=$(go env GOVERSION 2>/dev/null || true)
    case "$go_version" in
        go1.[2-9][2-9]*|go1.[3-9][0-9]*|go[2-9].*) ;;
        *) fail "Go 1.22+ is required (found ${go_version:-an unknown version})" ;;
    esac
    if [ "$(uname -s)" = "Darwin" ]; then
        macos_major=$(sw_vers -productVersion | cut -d. -f1)
        case "$go_version" in
            go1.22.*|go1.23.*)
                [ "$macos_major" -ge 26 ] && fail "macOS 26+ requires Go 1.24+; install current stable Go and retry"
                ;;
        esac
    fi
    commit=$(git -C "$project_dir" rev-parse --short HEAD 2>/dev/null || printf 'dev')
    if [ -n "$(git -C "$project_dir" status --porcelain 2>/dev/null || true)" ]; then
        commit="$commit-dirty"
    fi
    built=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    ldflags="-X github.com/pushguard/pushguard/internal/model.BuildCommit=$commit -X github.com/pushguard/pushguard/internal/model.BuildDate=$built"

    # Build to a fresh inode on the destination filesystem. Rewriting an
    # installed Mach-O in place can leave macOS with stale code-signing pages.
    # A failed build/signature/startup check must leave the old executable intact.
    mkdir -p "$INSTALL_DIR"
    INSTALL_DIR=$(cd "$INSTALL_DIR" && pwd -P)
    source_stage=$(mktemp -d "$INSTALL_DIR/.pushguard-build.XXXXXX") || fail "could not create source installation staging directory"
    trap 'rm -rf "$source_stage"' EXIT
    trap 'exit 130' INT
    trap 'exit 143' HUP TERM
    staged="$source_stage/pushguard"
    (cd "$project_dir" && GOFLAGS="${GOFLAGS:-} -buildvcs=false" go build -trimpath -ldflags "$ldflags" -o "$staged" ./cmd/pushguard)
    if [ "$(uname -s)" = "Darwin" ]; then
        codesign --force --sign - "$staged"
        codesign --verify --strict "$staged"
    fi
    "$staged" version >/dev/null
    mv -f "$staged" "$INSTALL_DIR/pushguard"
    print_success
}

download() {
    url=$1
    destination=$2
    case "$url" in
        https://*) ;;
        *) fail "release URL must use HTTPS: $url" ;;
    esac
    if command -v curl >/dev/null 2>&1; then
        curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$url" --output "$destination"
    elif command -v wget >/dev/null 2>&1; then
        wget --https-only --quiet --output-document="$destination" "$url"
    else
        fail "curl or wget is required for a binary installation"
    fi
}

checksum() {
    file=$1
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$file" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$file" | awk '{print $1}'
    else
        fail "sha256sum or shasum is required to verify the release"
    fi
}

print_success() {
    printf '\nPushGuard Installer\n-------------------\n'
    "$INSTALL_DIR/pushguard" version
    printf '\nInstallation complete.\n\nOpen any Git repository and run:\n\n    pushguard push\n\nVerification only:\n\n    pushguard check\n'
    case ":${PATH:-}:" in
        *":$INSTALL_DIR:"*) ;;
        *) printf '\nAdd PushGuard to this terminal:\n    export PATH="%s:$PATH"\n' "$INSTALL_DIR" ;;
    esac
}

if [ "${PUSHGUARD_INSTALL_FROM_SOURCE:-0}" = "1" ]; then
    source_install
    exit 0
fi

os_name=$(uname -s)
arch=$(uname -m)
case "$os_name" in
    Darwin) target_os=darwin ;;
    Linux) target_os=linux ;;
    *) fail "unsupported operating system: $os_name" ;;
esac
case "$arch" in
    arm64|aarch64) target_arch=arm64 ;;
    x86_64|amd64) target_arch=amd64 ;;
    *) fail "unsupported CPU architecture: $arch" ;;
esac

asset="pushguard-$target_os-$target_arch.tar.gz"
if [ -n "${PUSHGUARD_RELEASE_VERSION:-}" ] && [ "$RELEASE_BASE" = "$DEFAULT_RELEASE_BASE" ]; then
    RELEASE_BASE="https://github.com/Codexia-afk/Hackspire_project/releases/download/v${PUSHGUARD_RELEASE_VERSION}"
fi
case "$RELEASE_BASE" in
    https://*) ;;
    *) fail "PUSHGUARD_RELEASE_BASE_URL must use HTTPS" ;;
esac

temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/pushguard-install.XXXXXX") || fail "could not create a temporary directory"
cleanup() { rm -rf "$temp_dir"; }
trap cleanup EXIT INT TERM
archive="$temp_dir/$asset"
checksums="$temp_dir/checksums.txt"
printf 'PushGuard Installer\n-------------------\nPlatform: %s %s\nDownloading PushGuard...\n' "$target_os" "$target_arch"
download "${RELEASE_BASE%/}/$asset" "$archive"
download "${RELEASE_BASE%/}/checksums.txt" "$checksums"
expected=$(awk -v name="$asset" '$2 == name {print $1; exit}' "$checksums")
[ -n "$expected" ] || fail "checksums.txt does not contain $asset"
actual=$(checksum "$archive")
[ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ] || fail "SHA-256 verification failed"
printf '✓ Downloaded\n✓ SHA-256 verified\n'

mkdir -p "$INSTALL_DIR"
tar -xzf "$archive" -C "$temp_dir"
[ -f "$temp_dir/pushguard" ] || fail "release archive did not contain pushguard"
chmod 0755 "$temp_dir/pushguard"
staged="$INSTALL_DIR/.pushguard-install.$$"
cp "$temp_dir/pushguard" "$staged"
chmod 0755 "$staged"
mv "$staged" "$INSTALL_DIR/pushguard"
printf '✓ Installed in %s\n' "$INSTALL_DIR"
print_success
