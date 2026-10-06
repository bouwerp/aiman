package slack

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Credentials is the host Slack OAuth file. The access token is a user
// token (xoxp-) for the member who approved the app. A bot token is refused
// so a leftover install cannot post as the Aiman app.
type Credentials struct {
	ClientID     string `json:"client_id,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	Team         string `json:"team,omitempty"`
}

// ReadToken returns AIMAN_SLACK_USER_TOKEN when it is set, otherwise the
// access token in the credentials file. The environment value is for one
// process; the file is what every agent on the host shares.
func ReadToken(path string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("AIMAN_SLACK_USER_TOKEN")); v != "" {
		if err := requireUserToken(v); err != nil {
			return "", err
		}
		return v, nil
	}
	creds, err := Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("slack user token is not set")
		}
		return "", err
	}
	if strings.TrimSpace(creds.AccessToken) == "" {
		return "", fmt.Errorf("slack user token is not set")
	}
	if err := requireUserToken(creds.AccessToken); err != nil {
		return "", err
	}
	return creds.AccessToken, nil
}

// Load reads the credentials file. A missing file returns os.ErrNotExist.
func Load(path string) (Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var creds Credentials
	if err := json.Unmarshal(b, &creds); err != nil {
		return Credentials{}, fmt.Errorf("read slack credentials: %w", err)
	}
	return creds, nil
}

// Store writes credentials mode 0600. WriteFile keeps the mode of a file
// that already exists, so the mode is set again after the write.
func Store(path string, creds Credentials) error {
	if creds.AccessToken != "" {
		if err := requireUserToken(creds.AccessToken); err != nil {
			return err
		}
	}
	if err := requireClientID(creds.ClientID); err != nil && creds.ClientID != "" {
		return err
	}
	raw, err := json.MarshalIndent(creds, "", "  ") //nolint:gosec // G117: this file is the credential store
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write slack credentials: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("write slack credentials: %w", err)
	}
	return nil
}

// SaveClientID records the app's public client id without dropping a token
// that is already stored.
func SaveClientID(path, clientID string) error {
	clientID = strings.TrimSpace(clientID)
	if err := requireClientID(clientID); err != nil {
		return err
	}
	creds, err := Load(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	creds.ClientID = clientID
	return Store(path, creds)
}

func requireUserToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n \t") {
		return fmt.Errorf("slack user token is empty")
	}
	if strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxb-") {
		return fmt.Errorf("slack bot token cannot post as a user")
	}
	if !strings.HasPrefix(token, "xoxp-") && !strings.HasPrefix(token, "xoxe.xoxp-") {
		return fmt.Errorf("slack user token is empty")
	}
	return nil
}

func requireClientID(id string) error {
	if id == "" || strings.ContainsAny(id, "\r\n \t") {
		return fmt.Errorf("slack client id is empty")
	}
	return nil
}
