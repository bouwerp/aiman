package server

import (
	"context"

	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// AWSLogin is the device-flow engine serve's aws.* handlers drive.
// *awslogin.Manager implements it.
type AWSLogin interface {
	Start(ctx context.Context, d awslogin.Delegation) (awslogin.Login, error)
	Status(id string) (awslogin.Login, bool)
}

// SetAWS wires the aws.* methods: cfg is serve's delegated list,
// credentialsPath the host credentials file, login the device-flow engine.
// Without it the methods answer unavailable.
func (s *Server) SetAWS(cfg config.ServeAWSConfig, credentialsPath string, login AWSLogin) {
	_, _, _, _ = s, cfg, credentialsPath, login
}
