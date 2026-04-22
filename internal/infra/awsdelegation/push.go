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

// RemoveSessionProfile strips a session-scoped AWS profile from both
// ~/.aws/credentials and ~/.aws/config on the remote host.
// It is a no-op when the profile is not present in either file.
func RemoveSessionProfile(ctx context.Context, r RemoteRunner, profileName string) error {
	home, err := getRemoteHome(ctx, r)
	if err != nil {
		return err
	}
	if home == "" || home == "/" {
		return fmt.Errorf("refusing to write to suspicious home directory: %q", home)
	}

	trimHome := strings.TrimRight(home, "/")

	// Remove from ~/.aws/credentials
	credsPath := fmt.Sprintf("%s/.aws/credentials", trimHome)
	existing, err := r.Execute(ctx, fmt.Sprintf(`test -f %q/.aws/credentials && cat %q/.aws/credentials || true`, home, home))
	if err == nil && strings.TrimSpace(existing) != "" {
		cleaned := MergeCredentialsIntoConfig(existing, profileName, nil)
		_ = r.WriteFile(ctx, credsPath, []byte(cleaned))
	}

	// Remove from ~/.aws/config
	configPath := fmt.Sprintf("%s/.aws/config", trimHome)
	existing, err = r.Execute(ctx, fmt.Sprintf(`test -f %q/.aws/config && cat %q/.aws/config || true`, home, home))
	if err == nil && strings.TrimSpace(existing) != "" {
		cleaned := MergeProfileIntoConfig(existing, profileName, "", "", "")
		_ = r.WriteFile(ctx, configPath, []byte(cleaned))
	}

	return nil
}
