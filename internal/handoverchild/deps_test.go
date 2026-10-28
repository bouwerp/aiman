package handoverchild

import (
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/memoryjob"
	"github.com/bouwerp/aiman/internal/verify"
)

// serve's verify adapters and stores satisfy the manager's deps unchanged.
var (
	_ Sessions      = verify.Sessions(nil)
	_ Terminal      = verify.Terminal(nil)
	_ Jobs          = (*memoryjob.Queue)(nil)
	_ ContextWriter = domain.ContextStore(nil)
)
