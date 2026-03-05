#!/bin/bash
set -e

# Aiman Update Script
# Updates aiman to the latest version

REPO_URL="https://github.com/bouwerp/aiman"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="aiman"
BACKUP_SUFFIX=".backup.$(date +%Y%m%d_%H%M%S)"
GITHUB_API="https://api.github.com/repos/bouwerp/aiman/releases/latest"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Current version (placeholder - will be set during build)
CURRENT_VERSION="${CURRENT_VERSION:-unknown}"

# Find where aiman is installed
find_installation() {
    if command -v aiman &> /dev/null; then
        AIMAN_PATH=$(which aiman)
        INSTALL_DIR=$(dirname "$AIMAN_PATH")
        echo -e "${BLUE}Found aiman at: $AIMAN_PATH${NC}"
        
        # Try to get current version
        if [ -f "$AIMAN_PATH" ]; then
            CURRENT_VERSION=$("$AIMAN_PATH" --version 2>/dev/null || echo "unknown")
            echo -e "${BLUE}Current version: $CURRENT_VERSION${NC}"
        fi
    else
        echo -e "${RED}Error: aiman not found in PATH${NC}"
        echo "Please ensure aiman is installed before updating."
        exit 1
    fi
}

# Check for latest version
get_latest_version() {
    echo "Checking for latest version..."
    
    # Try to get latest tag from GitHub
    LATEST_VERSION=$(curl -s "https://api.github.com/repos/bouwerp/aiman/releases/latest" | 
                     grep '"tag_name":' | 
                     sed -E 's/.*"([^"]+)".*/\1/' 2>/dev/null || echo "")
    
    if [ -z "$LATEST_VERSION" ]; then
        echo -e "${YELLOW}Could not fetch latest version info. Will update to latest commit.${NC}"
        LATEST_VERSION="latest"
    else
        echo -e "${GREEN}Latest version: $LATEST_VERSION${NC}"
    fi
    
    # Check if already up to date
    if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ]; then
        echo -e "${GREEN}Already up to date!${NC}"
        exit 0
    fi
}

# Backup current binary
backup_current() {
    echo "Creating backup of current binary..."
    BACKUP_PATH="${AIMAN_PATH}${BACKUP_SUFFIX}"
    cp "$AIMAN_PATH" "$BACKUP_PATH"
    echo -e "${GREEN}Backup created: $BACKUP_PATH${NC}"
}

# Detect platform
detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
    esac

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
    echo "Attempting to download pre-built binary..."

    detect_platform

    # Get latest release info
    if ! RELEASE_DATA=$(curl -sf "$GITHUB_API" 2>/dev/null); then
        echo -e "${YELLOW}No release found, will build from source${NC}"
        return 1
    fi

    # Extract version and download URL
    LATEST_VERSION=$(echo "$RELEASE_DATA" | grep '"tag_name"' | cut -d '"' -f 4)
    DOWNLOAD_URL=$(echo "$RELEASE_DATA" | grep "browser_download_url.*${RELEASE_NAME}.${ARCHIVE_EXT}\"" | cut -d '"' -f 4)

    if [ -z "$DOWNLOAD_URL" ]; then
        echo -e "${YELLOW}Pre-built binary not available for ${OS}-${ARCH}, will build from source${NC}"
        return 1
    fi

    echo -e "${GREEN}Downloading version: $LATEST_VERSION${NC}"

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

# Clone or update repository
fetch_source() {
    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"
    
    echo "Fetching latest source code..."
    git clone --depth 1 "$REPO_URL" aiman-src 2>/dev/null || {
        echo -e "${RED}Failed to clone repository${NC}"
        exit 1
    }
    
    cd aiman-src
}

# Build new version
build_new_version() {
    echo "Building new version..."
    
    # Detect platform
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)
    
    case "$ARCH" in
        x86_64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
    esac
    
    export GOOS="$OS"
    export GOARCH="$ARCH"
    
    # Build
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    
    go build -ldflags "-X main.version=$VERSION -X main.buildTime=$BUILD_TIME" \
        -o "${BINARY_NAME}" \
        ./cmd/aiman
    
    if [ $? -ne 0 ]; then
        echo -e "${RED}Build failed!${NC}"
        restore_backup
        exit 1
    fi
    
    echo -e "${GREEN}Build successful!${NC}"
}

# Install new version
install_new_version() {
    echo "Installing new version..."
    
    # Check if we need sudo
    if [ -w "$INSTALL_DIR" ]; then
        mv "${BINARY_NAME}" "$AIMAN_PATH"
    else
        echo -e "${YELLOW}Requesting sudo access to update $AIMAN_PATH${NC}"
        sudo mv "${BINARY_NAME}" "$AIMAN_PATH"
    fi
    
    chmod +x "$AIMAN_PATH"
    
    # Verify installation
    NEW_VERSION=$("$AIMAN_PATH" --version 2>/dev/null || echo "unknown")
    echo -e "${GREEN}Updated to version: $NEW_VERSION${NC}"
}

# Restore backup on failure
restore_backup() {
    if [ -f "$BACKUP_PATH" ]; then
        echo "Restoring previous version..."
        cp "$BACKUP_PATH" "$AIMAN_PATH"
        echo -e "${GREEN}Previous version restored${NC}"
    fi
}

# Cleanup old backups (keep last 5)
cleanup_old_backups() {
    echo "Cleaning up old backups..."
    find "$INSTALL_DIR" -name "${BINARY_NAME}.backup.*" -type f | 
        sort -r | 
        tail -n +6 | 
        xargs -r rm -f
}

# Cleanup temp directory
cleanup() {
    if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
        rm -rf "$TEMP_DIR"
    fi
}

trap cleanup EXIT

# Show update history
show_changelog() {
    echo ""
    echo -e "${BLUE}Recent changes:${NC}"
    git log --oneline -10 2>/dev/null || echo "Changelog not available"
    echo ""
}

# Main update flow
main() {
    echo "=== Aiman Update Script ==="
    echo ""

    find_installation
    get_latest_version
    backup_current

    # Try to download pre-built binary first
    if download_binary; then
        install_new_version
    else
        echo ""
        echo -e "${YELLOW}Falling back to building from source...${NC}"
        fetch_source
        show_changelog
        build_new_version
        install_new_version
    fi

    cleanup_old_backups

    echo ""
    echo -e "${GREEN}Update complete!${NC}"
    echo ""
    echo "Run 'aiman --version' to verify the update."
    echo "If you encounter any issues, you can restore the previous version from:"
    echo "  $BACKUP_PATH"
}

# Check for user-specified install directory
while [[ $# -gt 0 ]]; do
    case $1 in
        --prefix)
            INSTALL_DIR="$2"
            shift 2
            ;;
        --force)
            FORCE_UPDATE=1
            shift
            ;;
        --skip-backup)
            SKIP_BACKUP=1
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --prefix DIR    Specify installation directory"
            echo "  --force         Force update even if already on latest version"
            echo "  --skip-backup   Skip creating backup (not recommended)"
            echo "  -h, --help      Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Skip backup if requested
if [ "$SKIP_BACKUP" = "1" ]; then
    backup_current() {
        echo -e "${YELLOW}Skipping backup (--skip-backup specified)${NC}"
    }
    restore_backup() {
        echo -e "${RED}Cannot restore: backup was skipped${NC}"
    }
fi

main
