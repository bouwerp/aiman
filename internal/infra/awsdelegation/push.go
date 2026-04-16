package awsdelegation

import (
	"context"
	"fmt"
	"strings"
)

// RemoteRunner runs commands and writes files on the remote host (e.g. *ssh.Manager).
type RemoteRunner interface {
	Execute(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
}

// ApplyDelegatedProfile merges the delegated profile into $HOME/.aws/config on the remote.
// Secrets are never written by this function.
// region is optional; when non-empty it is written into the profile block.
func ApplyDelegatedProfile(ctx context.Context, r RemoteRunner, profileName, roleARN, sourceProfile, region string) error {
	home, err := getRemoteHome(ctx, r)
	if err != nil {
		return err
	}
	if home == "" || home == "/" {
		return fmt.Errorf("refusing to write to suspicious home directory: %q", home)
	}

	if _, err := r.Execute(ctx, fmt.Sprintf(`mkdir -p %q/.aws && chmod 700 %q/.aws`, home, home)); err != nil {
		return fmt.Errorf("remote mkdir .aws: %w", err)
	}

	configPath := fmt.Sprintf("%s/.aws/config", strings.TrimRight(home, "/"))
	existing, err := r.Execute(ctx, fmt.Sprintf(`test -f %q/.aws/config && cat %q/.aws/config || true`, home, home))
	if err != nil {
		return fmt.Errorf("read remote ~/.aws/config: %w", err)
	}

	merged := MergeProfileIntoConfig(existing, profileName, roleARN, sourceProfile, region)
	return r.WriteFile(ctx, configPath, []byte(merged))
}

// ApplyDelegatedCredentials merges temporary tokens into $HOME/.aws/credentials on the remote.
func ApplyDelegatedCredentials(ctx context.Context, r RemoteRunner, profileName string, creds *SessionCredentials) error {
	home, err := getRemoteHome(ctx, r)
	if err != nil {
		return err
	}
	if home == "" || home == "/" {
		return fmt.Errorf("refusing to write to suspicious home directory: %q", home)
	}

	if _, err := r.Execute(ctx, fmt.Sprintf(`mkdir -p %q/.aws && chmod 700 %q/.aws`, home, home)); err != nil {
		return fmt.Errorf("remote mkdir .aws: %w", err)
	}

	credsPath := fmt.Sprintf("%s/.aws/credentials", strings.TrimRight(home, "/"))
	existing, err := r.Execute(ctx, fmt.Sprintf(`test -f %q/.aws/credentials && cat %q/.aws/credentials || true`, home, home))
	if err != nil {
		return fmt.Errorf("read remote ~/.aws/credentials: %w", err)
	}

	merged := MergeCredentialsIntoConfig(existing, profileName, creds)
	return r.WriteFile(ctx, credsPath, []byte(merged))
}

func getRemoteHome(ctx context.Context, r RemoteRunner) (string, error) {
	// Simple and direct. Wrapping in bash -c to ensure $HOME is expanded by the remote shell.
	homeOut, err := r.Execute(ctx, `bash -c 'printf %s "$HOME"'`)
	if err != nil {
		return "", fmt.Errorf("remote $HOME: %w", err)
	}
	home := strings.TrimSpace(homeOut)
	if home == "" {
		return "", fmt.Errorf("remote $HOME is empty")
	}
	return home, nil
}
