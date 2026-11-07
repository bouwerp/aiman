package remotesvc

// InstallPipeAt is the install command for k pinned to release tag: install.sh
// and the binary both come from that release.
//
// Stub: returns the unpinned pipe until pinning lands.
func (k Kind) InstallPipeAt(tag string) string {
	_ = tag
	return k.InstallPipe()
}
