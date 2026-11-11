package remotesvc

// GatewayRepairScript installs aiman-gateway at release tag and restarts it
// only when it was running. It never touches serve. The leading set -e stops
// the script at a failed install, before any restart.
func GatewayRepairScript(tag string) string {
	return "set -e\n" + gatewayLockstep(tag)
}

// GatewayProcPattern is the pgrep -f pattern that matches a running
// aiman-gateway and nothing else.
func GatewayProcPattern() string {
	return KindGateway.procPattern()
}
