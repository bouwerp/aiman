#!/bin/bash
set -e

# Aiman Installation Script
# Detects architecture and installs the app

REPO_URL="https://github.com/bouwerp/aiman"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
BINARY_NAME="${BINARY_NAME:-aiman}"
GITHUB_API="https://api.github.com/repos/bouwerp/aiman/releases/latest"
RELEASES_URL="https://github.com/bouwerp/aiman/releases/download"
# Pin a release tag with --version or VERSION= (e.g. VERSION=v0.30.0 ./install.sh).
PINNED_VERSION="${VERSION:-}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Detect OS and Architecture
detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)
    
    case "$ARCH" in
        x86_64)
            ARCH="amd64"
            ;;
        arm64|aarch64)
            ARCH="arm64"
            ;;
        *)
            echo -e "${RED}Unsupported architecture: $ARCH${NC}"
            exit 1
            ;;
    esac
    
    case "$OS" in
        linux|darwin)
            PLATFORM="${OS}_${ARCH}"
            ;;
        *)
            echo -e "${RED}Unsupported operating system: $OS${NC}"
            exit 1
            ;;
    esac
    
    echo -e "${GREEN}Detected platform: $PLATFORM${NC}"
}

# Get the binary name for the current platform
get_binary_name() {
    case "$OS" in
        darwin)
            RELEASE_NAME="${BINARY_NAME}-darwin-${ARCH}"
            ARCHIVE_EXT="tar.gz"
            ;;
        linux)
            RELEASE_NAME="${BINARY_NAME}-linux-${ARCH}"
            ARCHIVE_EXT="tar.gz"
            ;;
        windows|mingw*|msys*)
            RELEASE_NAME="${BINARY_NAME}-windows-${ARCH}.exe"
            ARCHIVE_EXT="zip"
            ;;
    esac
}

normalize_pin() {
    local v="$1"
    v="${v#"${v%%[![:space:]]*}"}"
    v="${v%"${v##*[![:space:]]}"}"
    v="${v#v}"
    v="${v#V}"
    if [ -z "$v" ]; then
        echo -e "${RED}Error: empty release tag${NC}" >&2
        exit 1
    fi
    case "$v" in
        *[!A-Za-z0-9._-]*)
            echo -e "${RED}Error: invalid release tag: $1${NC}" >&2
            exit 1
            ;;
    esac
    printf 'v%s\n' "$v"
}

# refuse stops the whole install. It exits rather than returns because
# download_binary runs as an if condition, where set -e does not apply, and a
# return there would fall through to the source build.
refuse() {
    echo -e "${RED}Refusing to install: $*${NC}" >&2
    exit 1
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | cut -d' ' -f1
    else
        return 1
    fi
}

# verify_checksum checks a file in the current directory against the
# checksums.txt beside it (`<sha256>  <asset>` per line, exact asset name).
verify_checksum() {
    want=$(awk -v f="$1" '$2 == f { print $1; exit }' checksums.txt)
    [ -n "$want" ] || refuse "$1 is not listed in checksums.txt of $RELEASE_TAG"
    got=$(sha256_of "$1") || refuse "verifying the download needs sha256sum or shasum"
    [ "$got" = "$want" ] || refuse "checksum mismatch for $1 (expected $want, got $got)"
}

# resolve_latest_release sets RELEASE_TAG for an unpinned install. It returns 1
# only when no prebuilt asset can be found, which is the source build's cue.
resolve_latest_release() {
    if ! RELEASE_DATA=$(curl -sf "$GITHUB_API" 2>/dev/null); then
        echo -e "${YELLOW}No release found, will build from source${NC}"
        return 1
    fi
    if ! echo "$RELEASE_DATA" | grep -q "browser_download_url.*${RELEASE_NAME}.${ARCHIVE_EXT}\""; then
        echo -e "${YELLOW}Pre-built binary not available for $PLATFORM, will build from source${NC}"
        return 1
    fi
    RELEASE_TAG=$(echo "$RELEASE_DATA" | grep '"tag_name"' | cut -d '"' -f 4)
    [ -n "$RELEASE_TAG" ] || return 1
}

# Download the pre-built binary of a GitHub release and verify it against that
# release's checksums.txt. Once a release is chosen every failure refuses: a
# pinned install or an unverifiable release never falls back to a source build.
download_binary() {
    echo "Checking for pre-built binary..."

    get_binary_name

    if [ -n "${PINNED_VERSION:-}" ]; then
        RELEASE_TAG="$PINNED_VERSION"
        echo "Pinning release: ${RELEASE_TAG}"
    elif ! resolve_latest_release; then
        return 1
    fi

    local archive="${RELEASE_NAME}.${ARCHIVE_EXT}"
    local base="${RELEASES_URL}/${RELEASE_TAG}"
    echo -e "${GREEN}Found release: $RELEASE_TAG${NC}"
    echo "Downloading $RELEASE_NAME..."

    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"

    curl -sfL "$base/checksums.txt" -o checksums.txt ||
        refuse "could not download checksums.txt of $RELEASE_TAG (releases published without one cannot be installed)"
    curl -sfL "$base/$archive" -o "$archive" ||
        refuse "could not download $archive from $RELEASE_TAG"

    verify_checksum "$archive"
    echo -e "${GREEN}Checksum verified${NC}"

    # Extract archive
    echo "Extracting binary..."
    case "$ARCHIVE_EXT" in
        tar.gz)
            tar -xzf "$archive"
            ;;
        zip)
            unzip -q "$archive"
            ;;
    esac

    [ -f "$RELEASE_NAME" ] || refuse "binary not found in $archive"

    # Rename to standard binary name
    mv "$RELEASE_NAME" "$BINARY_NAME"
    chmod +x "$BINARY_NAME"

    echo -e "${GREEN}Download successful!${NC}"
    return 0
}

# Check if required tools are available
check_prerequisites() {
    echo "Checking prerequisites..."
    
    if ! command -v git >/dev/null 2>&1; then
        echo -e "${RED}Error: git is required but not installed.${NC}"
        echo "Please install git first."
        exit 1
    fi
    
    if ! command -v go >/dev/null 2>&1; then
        echo -e "${YELLOW}Warning: Go is not installed. Attempting to install...${NC}"
        install_go
    else
        GO_VERSION=$(go version | grep -oE '[0-9]+\.[0-9]+' | head -1)
        echo -e "${GREEN}Go version: $GO_VERSION${NC}"
    fi
}

# Install Go if not present
install_go() {
    echo "Installing Go..."
    
    case "$OS" in
        linux)
            if command -v apt-get >/dev/null 2>&1; then
                sudo apt-get update && sudo apt-get install -y golang-go
            elif command -v yum >/dev/null 2>&1; then
                sudo yum install -y golang
            elif command -v pacman >/dev/null 2>&1; then
                sudo pacman -S go
            else
                echo -e "${RED}Could not install Go automatically. Please install Go 1.26+ manually.${NC}"
                exit 1
            fi
            ;;
        darwin)
            if command -v brew >/dev/null 2>&1; then
                brew install go
            else
                echo -e "${YELLOW}Homebrew not found. Installing Homebrew first...${NC}"
                /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
                brew install go
            fi
            ;;
    esac
}

# Clone repository
clone_repo() {
    echo "Cloning repository..."
    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"
    git clone --depth 1 "$REPO_URL" aiman-src
    cd aiman-src
}

# Build from source
build_binary() {
    echo "Building aiman for $PLATFORM..."
    
    # Set build flags for the target platform
    export GOOS="$OS"
    export GOARCH="$ARCH"
    
    # Build with version info
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    
    # Build the command matching BINARY_NAME. Without this, installing the
    # daemon (BINARY_NAME=aiman-trigger) from source would silently produce the
    # TUI binary under the daemon's name.
    if [ ! -d "./cmd/${BINARY_NAME}" ]; then
        echo -e "${RED}No such command: ./cmd/${BINARY_NAME}${NC}"
        exit 1
    fi

    if ! go build -ldflags "-X main.version=$VERSION -X main.buildTime=$BUILD_TIME" \
        -o "${BINARY_NAME}" \
        "./cmd/${BINARY_NAME}"; then
        echo -e "${RED}Build failed!${NC}"
        exit 1
    fi
    
    echo -e "${GREEN}Build successful!${NC}"
}

# Install binary
install_binary() {
    echo "Installing aiman to $INSTALL_DIR..."

    # Ensure install directory exists
    mkdir -p "$INSTALL_DIR"

    # Copy beside the target, then rename: the download may be on another
    # filesystem, and a copy killed part way must not leave a broken binary
    # under the real name.
    tmp_binary="$INSTALL_DIR/.$BINARY_NAME.new.$$"
    run_install=""
    if [ ! -w "$INSTALL_DIR" ]; then
        echo -e "${YELLOW}Requesting sudo access to install to $INSTALL_DIR${NC}"
        run_install="sudo"
    fi
    if ! $run_install cp "$BINARY_NAME" "$tmp_binary"; then
        $run_install rm -f "$tmp_binary"
        echo "Failed to copy $BINARY_NAME to $INSTALL_DIR" >&2
        exit 1
    fi
    $run_install chmod +x "$tmp_binary"
    $run_install mv -f "$tmp_binary" "$INSTALL_DIR/$BINARY_NAME"
    rm -f "$BINARY_NAME"

    # Ensure INSTALL_DIR is on PATH, adding a line to the shell profile if needed
    ensure_path

    # Verify installation
    if command -v aiman >/dev/null 2>&1 || [ -x "$INSTALL_DIR/$BINARY_NAME" ]; then
        echo -e "${GREEN}Installation successful!${NC}"
        echo ""
        echo "aiman is now installed at: $INSTALL_DIR/$BINARY_NAME"
        echo ""
        INSTALLED_VERSION=$("$INSTALL_DIR/$BINARY_NAME" --version 2>/dev/null || echo "unknown")
        echo "Version: $INSTALLED_VERSION"
        echo ""
        echo "Next steps:"
        echo "  1. Run 'aiman init' to configure JIRA and remote servers"
        echo "  2. Run 'aiman' to start the TUI"
    else
        echo -e "${YELLOW}Warning: aiman was installed but could not be verified${NC}"
        echo "Binary location: $INSTALL_DIR/$BINARY_NAME"
    fi
}

# Ensure INSTALL_DIR is on the user's PATH, appending to the shell profile if not.
ensure_path() {
    case ":$PATH:" in
        *":$INSTALL_DIR:"*) return ;;  # already on PATH
    esac

    # Determine the shell profile to update
    SHELL_NAME=$(basename "${SHELL:-/bin/bash}")
    case "$SHELL_NAME" in
        zsh)  PROFILE="$HOME/.zshrc" ;;
        bash)
            # Prefer .bash_profile on macOS, .bashrc on Linux
            if [ "$OS" = "darwin" ] && [ -f "$HOME/.bash_profile" ]; then
                PROFILE="$HOME/.bash_profile"
            else
                PROFILE="$HOME/.bashrc"
            fi
            ;;
        *)    PROFILE="$HOME/.profile" ;;
    esac

    PATH_LINE="export PATH=\"\$PATH:$INSTALL_DIR\""

    # Don't duplicate if the line already exists in the file
    if [ -f "$PROFILE" ] && grep -qF "$INSTALL_DIR" "$PROFILE" 2>/dev/null; then
        return
    fi

    {
        echo ""
        echo "# Added by aiman installer"
        echo "$PATH_LINE"
    } >> "$PROFILE"
    echo -e "${GREEN}Added $INSTALL_DIR to PATH in $PROFILE${NC}"
    echo -e "${YELLOW}Restart your shell or run: source $PROFILE${NC}"

    # Make it available in the current session too
    export PATH="$PATH:$INSTALL_DIR"
}

# Setup config directory
setup_config() {
    echo "Setting up configuration directory..."
    CONFIG_DIR="$HOME/.aiman"
    
    if [ ! -d "$CONFIG_DIR" ]; then
        mkdir -p "$CONFIG_DIR"
        echo -e "${GREEN}Created $CONFIG_DIR${NC}"
    fi
    
    echo "Configuration will be stored in: $CONFIG_DIR"
}

# Cleanup
cleanup() {
    if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
        rm -rf "$TEMP_DIR"
    fi
}

trap cleanup EXIT

# Main installation flow
main() {
    echo "=== Aiman Installation Script ==="
    echo ""

    detect_platform
    setup_config

    # Try to download pre-built binary first
    if download_binary; then
        install_binary
    else
        echo ""
        echo -e "${YELLOW}Falling back to building from source...${NC}"
        check_prerequisites
        clone_repo
        build_binary
        install_binary
    fi

    echo ""
    echo -e "${GREEN}Installation complete!${NC}"
}

# Check for user-specified install directory
while [ $# -gt 0 ]; do
    case $1 in
        --prefix)
            INSTALL_DIR="$2"
            shift 2
            ;;
        --system)
            INSTALL_DIR="/usr/local/bin"
            shift
            ;;
        --version)
            if [ -z "${2:-}" ]; then
                echo "Error: --version requires a tag (e.g. v0.30.0)"
                exit 1
            fi
            PINNED_VERSION="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --prefix DIR    Install to custom directory (default: ~/.local/bin)"
            echo "  --system        Install to /usr/local/bin (requires sudo)"
            echo "  --version TAG   Install a specific release tag (also: VERSION=v0.30.0)"
            echo "  -h, --help      Show this help message"
            echo ""
            echo "Every install is verified against the release's checksums.txt, so a"
            echo "release published without one cannot be installed."
            echo ""
            echo "Recover a broken install:"
            echo "  curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash -s -- --version <tag>"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

if [ -n "${PINNED_VERSION:-}" ]; then
    PINNED_VERSION="$(normalize_pin "$PINNED_VERSION")"
fi

main
