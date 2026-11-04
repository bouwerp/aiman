package gateway

// Device tokens reach serve through this allowlist; anything not listed is
// refused. The host token (the TUI on the laptop) is not filtered. A device
// acts as the operator: the gateway calls serve with no caller.
var (
	// deviceReadMethods only read state.
	deviceReadMethods = []string{
		"ping",
		"session.list", "session.get", "session.read", "session.wait", "session.log",
		"pty.capture",
		"todo.get", "todo.summary",
		"notify.since",
		"usage.get",
		"ci.get",
		"agents.status",
		"repo.list", "host.stats",
	}
	// deviceWriteMethods change state but cannot publish outside the host.
	// todo.mirror is left out: it stands for the agent's own task list, which
	// a phone does not have. session.restart and session.terminate stop an
	// agent, but any paired phone may call them: the app confirms, and
	// session.terminate refuses work it would lose without an explicit discard.
	deviceWriteMethods = []string{
		"session.prompt", "session.create", "session.rename", "session.move",
		"session.restart", "session.terminate",
		"steer.send",
		"todo.add", "todo.update", "todo.move", "todo.remove",
		"todo.ensure_done", "todo.mark_met", "todo.gate",
		"todo.approve_design", "todo.approve_review",
		"ci.watch",
		"pty.input",
		"push.register", "push.unregister",
	}
	// deviceDestructiveMethods kill terminals, drop sessions, post to
	// GitHub or replace agent installs. A device needs allow_destructive.
	deviceDestructiveMethods = []string{
		"pty.kill", "pty.forget",
		"session.forget",
		"review.post",
		"agents.update", "agents.move_prefix",
	}
	// gatewayMethods are answered by the gateway itself, not serve.
	gatewayMethods = []string{"push.register", "push.unregister"}
)

var (
	deviceAllowed     = toSet(deviceReadMethods, deviceWriteMethods)
	deviceDestructive = toSet(deviceDestructiveMethods)
)

// deviceMayCall reports whether a device token may call method.
func deviceMayCall(method string, allowDestructive bool) bool {
	if deviceAllowed[method] {
		return true
	}
	return allowDestructive && deviceDestructive[method]
}

func toSet(lists ...[]string) map[string]bool {
	out := map[string]bool{}
	for _, l := range lists {
		for _, m := range l {
			out[m] = true
		}
	}
	return out
}
