//go:build unix

package harness

import "syscall"

// openNonBlock keeps an open of a FIFO the app swapped in from blocking.
const openNonBlock = syscall.O_NONBLOCK
