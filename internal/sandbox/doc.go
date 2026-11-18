// Package sandbox holds what aiman's two Docker Compose builders share: the
// review sandbox (internal/review) and the harness run project
// (internal/harness). It has the compose document types, the hardening
// profile every container that runs untrusted code gets, the cache volumes
// and their chown service, network definitions and YAML writing.
//
// Every compose file aiman runs is built from these types, never taken from
// a repository.
package sandbox
