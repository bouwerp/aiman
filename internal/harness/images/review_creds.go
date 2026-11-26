package images

import "github.com/bouwerp/aiman/internal/agentjob"

// Credential modes of the review container.
const (
	CredentialAPIKey    = agentjob.CredentialAPIKey
	CredentialOAuthCopy = agentjob.CredentialOAuthCopy
)

// Credential is what the review container gets to reach its provider.
type Credential = agentjob.Credential

// HostCredentials reads the host's agent credentials without changing them.
type HostCredentials = agentjob.CredentialReader
