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

// SessionProfileName returns the aiman-managed AWS profile name for a session.
// The first 12 characters of the session ID provide uniqueness across sessions
// on the same remote without exposing the full UUID.
func SessionProfileName(sessionID string) string {
	id := strings.TrimSpace(sessionID)
	if len(id) > 12 {
		id = id[:12]
	}
	return "aiman-" + id
}

// ApplyDelegatedProfile merges the delegated profile into $HOME/.aws/config on the remote.
// Secrets are never written by this function.
// region is optional; when non-empty it is written into the profile block.
func ApplyDelegatedProfile(ctx context.Context, r RemoteRunner, profileName, roleARN, sourceProfile, region string) error {
	home, trimHome, err := ensureRemoteAWSDir(ctx, r)
	if err != nil {
		return err
	}

	configPath := fmt.Sprintf("%s/.aws/config", trimHome)
	existing, err := r.Execute(ctx, fmt.Sprintf(`test -f %q/.aws/config && cat %q/.aws/config || true`, home, home))
	if err != nil {
		return fmt.Errorf("read remote ~/.aws/config: %w", err)
	}

	merged := MergeProfileIntoConfig(existing, profileName, roleARN, sourceProfile, region)
	if err := r.WriteFile(ctx, configPath, []byte(merged)); err != nil {
		return fmt.Errorf("write remote ~/.aws/config: %w", err)
	}
	if _, err := r.Execute(ctx, fmt.Sprintf(`chmod 600 %q`, configPath)); err != nil {
		return fmt.Errorf("chmod remote ~/.aws/config: %w", err)
	}
	return nil
}

// ApplyDelegatedCredentials merges temporary tokens into $HOME/.aws/credentials on the remote.
func ApplyDelegatedCredentials(ctx context.Context, r RemoteRunner, profileName string, creds *SessionCredentials) error {
	home, trimHome, err := ensureRemoteAWSDir(ctx, r)
	if err != nil {
		return err
	}

	credsPath := fmt.Sprintf("%s/.aws/credentials", trimHome)
	existing, err := r.Execute(ctx, fmt.Sprintf(`test -f %q/.aws/credentials && cat %q/.aws/credentials || true`, home, home))
	if err != nil {
		return fmt.Errorf("read remote ~/.aws/credentials: %w", err)
	}

	merged := MergeCredentialsIntoConfig(existing, profileName, creds)
	if err := r.WriteFile(ctx, credsPath, []byte(merged)); err != nil {
		return fmt.Errorf("write remote ~/.aws/credentials: %w", err)
	}
	if _, err := r.Execute(ctx, fmt.Sprintf(`chmod 600 %q`, credsPath)); err != nil {
		return fmt.Errorf("chmod remote ~/.aws/credentials: %w", err)
	}
	return nil
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

func ensureRemoteAWSDir(ctx context.Context, r RemoteRunner) (home string, trimHome string, err error) {
	home, err = getRemoteHome(ctx, r)
	if err != nil {
		return "", "", err
	}
	if home == "" || home == "/" {
		return "", "", fmt.Errorf("refusing to write to suspicious home directory: %q", home)
	}
	if _, err := r.Execute(ctx, fmt.Sprintf(`mkdir -p %q/.aws && chmod 700 %q/.aws`, home, home)); err != nil {
		return "", "", fmt.Errorf("remote mkdir .aws: %w", err)
	}
	return home, strings.TrimRight(home, "/"), nil
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

// RenameSessionProfile renames an AWS shared profile in both ~/.aws/credentials
// and ~/.aws/config on the remote host. It preserves the existing section bodies
// and refuses to overwrite an existing target profile.
func RenameSessionProfile(ctx context.Context, r RemoteRunner, oldProfileName, newProfileName string) error {
	oldProfileName = normalizeProfileName(oldProfileName)
	newProfileName = normalizeProfileName(newProfileName)
	if oldProfileName == newProfileName {
		return nil
	}
	if newProfileName == "" {
		return fmt.Errorf("new profile name is required")
	}

	home, err := getRemoteHome(ctx, r)
	if err != nil {
		return err
	}
	if home == "" || home == "/" {
		return fmt.Errorf("refusing to write to suspicious home directory: %q", home)
	}

	trimHome := strings.TrimRight(home, "/")
	credsPath := fmt.Sprintf("%s/.aws/credentials", trimHome)
	configPath := fmt.Sprintf("%s/.aws/config", trimHome)

	existingCreds, err := r.Execute(ctx, fmt.Sprintf(`test -f %q/.aws/credentials && cat %q/.aws/credentials || true`, home, home))
	if err != nil {
		return fmt.Errorf("read remote ~/.aws/credentials: %w", err)
	}
	renamedCreds, err := renameCredentialsProfile(existingCreds, oldProfileName, newProfileName)
	if err != nil {
		return err
	}

	existingConfig, err := r.Execute(ctx, fmt.Sprintf(`test -f %q/.aws/config && cat %q/.aws/config || true`, home, home))
	if err != nil {
		return fmt.Errorf("read remote ~/.aws/config: %w", err)
	}
	renamedConfig, err := renameConfigProfile(existingConfig, oldProfileName, newProfileName)
	if err != nil {
		return err
	}

	if err := r.WriteFile(ctx, credsPath, []byte(renamedCreds)); err != nil {
		return fmt.Errorf("write remote ~/.aws/credentials: %w", err)
	}
	if err := r.WriteFile(ctx, configPath, []byte(renamedConfig)); err != nil {
		return fmt.Errorf("write remote ~/.aws/config: %w", err)
	}
	return nil
}

func renameCredentialsProfile(existing, oldProfileName, newProfileName string) (string, error) {
	renamed, err := renameSection(existing, fmt.Sprintf("[%s]", oldProfileName), fmt.Sprintf("[%s]", newProfileName), newProfileName)
	if err != nil {
		return "", err
	}
	return finalizeConfig(renamed), nil
}

func renameConfigProfile(existing, oldProfileName, newProfileName string) (string, error) {
	renamed, err := renameSection(existing, ProfileSectionHeader(oldProfileName), ProfileSectionHeader(newProfileName), newProfileName)
	if err != nil {
		return "", err
	}
	return finalizeConfig(renamed), nil
}

func normalizeProfileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "default"
	}
	return name
}

// RemoveSessionCredentialFiles removes the legacy per-session ~/.aiman/aws/<sessionID>
// directory from older releases. Current sync flows use ~/.aws/{credentials,config}.
func RemoveSessionCredentialFiles(ctx context.Context, r RemoteRunner, sessionID string) error {
	home, err := getRemoteHome(ctx, r)
	if err != nil {
		return err
	}
	if home == "" || home == "/" {
		return fmt.Errorf("refusing to write to suspicious home directory: %q", home)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}

	dir := fmt.Sprintf("%s/.aiman/aws/%s", strings.TrimRight(home, "/"), sessionID)
	_, err = r.Execute(ctx, fmt.Sprintf(`rm -rf %q`, dir))
	return err
}

// RemoveAllSessionCredentialFiles removes the legacy ~/.aiman/aws tree that older
// releases used for isolated session credential files.
func RemoveAllSessionCredentialFiles(ctx context.Context, r RemoteRunner) error {
	home, err := getRemoteHome(ctx, r)
	if err != nil {
		return err
	}
	if home == "" || home == "/" {
		return fmt.Errorf("refusing to write to suspicious home directory: %q", home)
	}

	dir := fmt.Sprintf("%s/.aiman/aws", strings.TrimRight(home, "/"))
	_, err = r.Execute(ctx, fmt.Sprintf(`rm -rf %q`, dir))
	return err
}
