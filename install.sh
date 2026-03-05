#!/bin/bash
set -e

# Aiman Installation Script
# Detects architecture and installs the app

REPO_URL="https://github.com/bouwerp/aiman"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="aiman"
GITHUB_API="https://api.github.com/repos/bouwerp/aiman/releases/latest"

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
            RELEASE_NAME="aiman-darwin-${ARCH}"
            ARCHIVE_EXT="tar.gz"
            ;;
        linux)
            RELEASE_NAME="aiman-linux-${ARCH}"
            ARCHIVE_EXT="tar.gz"
            ;;
        windows|mingw*|msys*)
            RELEASE_NAME="aiman-windows-${ARCH}.exe"
            ARCHIVE_EXT="zip"
            ;;
    esac
}

# Download pre-built binary from GitHub releases
download_binary() {
    echo "Checking for pre-built binary..."

    get_binary_name

    # Get latest release info
    if ! RELEASE_DATA=$(curl -sf "$GITHUB_API" 2>/dev/null); then
        echo -e "${YELLOW}No release found, will build from source${NC}"
        return 1
    fi

    # Extract download URL for the correct architecture
    DOWNLOAD_URL=$(echo "$RELEASE_DATA" | grep "browser_download_url.*${RELEASE_NAME}.${ARCHIVE_EXT}\"" | cut -d '"' -f 4)

    if [ -z "$DOWNLOAD_URL" ]; then
        echo -e "${YELLOW}Pre-built binary not available for $PLATFORM, will build from source${NC}"
        return 1
    fi

    VERSION=$(echo "$RELEASE_DATA" | grep '"tag_name"' | cut -d '"' -f 4)
    echo -e "${GREEN}Found release: $VERSION${NC}"
    echo "Downloading $RELEASE_NAME..."

    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"

    if ! curl -sfL "$DOWNLOAD_URL" -o "${RELEASE_NAME}.${ARCHIVE_EXT}"; then
        echo -e "${RED}Download failed${NC}"
        return 1
    fi

    # Extract archive
    echo "Extracting binary..."
    case "$ARCHIVE_EXT" in
        tar.gz)
            tar -xzf "${RELEASE_NAME}.${ARCHIVE_EXT}"
            ;;
        zip)
            unzip -q "${RELEASE_NAME}.${ARCHIVE_EXT}"
            ;;
    esac

    if [ ! -f "$RELEASE_NAME" ]; then
        echo -e "${RED}Binary not found in archive${NC}"
        return 1
    fi

    # Rename to standard binary name
    mv "$RELEASE_NAME" "$BINARY_NAME"
    chmod +x "$BINARY_NAME"

    echo -e "${GREEN}Download successful!${NC}"
    return 0
}

# Check if required tools are available
check_prerequisites() {
    echo "Checking prerequisites..."
    
    if ! command -v git &> /dev/null; then
        echo -e "${RED}Error: git is required but not installed.${NC}"
        echo "Please install git first."
        exit 1
    fi
    
    if ! command -v go &> /dev/null; then
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
            if command -v apt-get &> /dev/null; then
                sudo apt-get update && sudo apt-get install -y golang-go
            elif command -v yum &> /dev/null; then
                sudo yum install -y golang
            elif command -v pacman &> /dev/null; then
                sudo pacman -S go
            else
                echo -e "${RED}Could not install Go automatically. Please install Go 1.26+ manually.${NC}"
                exit 1
            fi
            ;;
        darwin)
            if command -v brew &> /dev/null; then
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
    
    go build -ldflags "-X main.version=$VERSION -X main.buildTime=$BUILD_TIME" \
        -o "${BINARY_NAME}" \
        ./cmd/aiman
    
    if [ $? -ne 0 ]; then
        echo -e "${RED}Build failed!${NC}"
        exit 1
    fi
    
    echo -e "${GREEN}Build successful!${NC}"
}

# Install binary
install_binary() {
    echo "Installing aiman to $INSTALL_DIR..."
    
    # Check if we need sudo
    if [ -w "$INSTALL_DIR" ]; then
        mv "${BINARY_NAME}" "$INSTALL_DIR/"
    else
        echo -e "${YELLOW}Requesting sudo access to install to $INSTALL_DIR${NC}"
        sudo mv "${BINARY_NAME}" "$INSTALL_DIR/"
    fi
    
    chmod +x "$INSTALL_DIR/$BINARY_NAME"
    
    # Verify installation
    if command -v aiman &> /dev/null; then
        echo -e "${GREEN}Installation successful!${NC}"
        echo ""
        echo "aiman is now installed at: $(which aiman)"
        echo ""
        echo "Next steps:"
        echo "  1. Run 'aiman init' to configure JIRA and remote servers"
        echo "  2. Run 'aiman' to start the TUI"
    else
        echo -e "${YELLOW}Warning: aiman was installed but is not in your PATH${NC}"
        echo "You may need to add $INSTALL_DIR to your PATH"
    fi
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
while [[ $# -gt 0 ]]; do
    case $1 in
        --prefix)
            INSTALL_DIR="$2"
            shift 2
            ;;
        --user)
            INSTALL_DIR="$HOME/.local/bin"
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --prefix DIR    Install to custom directory (default: /usr/local/bin)"
            echo "  --user          Install to user's home directory (~/.local/bin)"
            echo "  -h, --help      Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

main
