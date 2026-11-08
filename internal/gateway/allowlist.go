package gateway

// Device tokens reach serve through this allowlist; anything not listed is
// refused. The host token (the TUI on the laptop) is not filtered. A device
// acts as the operator: the gateway calls serve with no caller.
var (
	// deviceReadMethods only read state.
	deviceReadMethods = []string{
		"ping",
		"session.list", "session.get", "session.read", "session.wait", "session.log",
		"session.transcript", "session.transcript_item", "session.process_output",
		"pty.capture",
		"todo.get", "todo.summary",
		"notify.since",
		"usage.get",
		"ci.get",
		"agents.status",
		"repo.list", "host.stats",
		"aws.status", "aws.login_status",
	}
	// deviceWriteMethods change state but cannot publish outside the host.
	// todo.mirror is left out: it stands for the agent's own task list, which
	// a phone does not have. session.terminate stops an agent, but any paired
	// phone may call it: the app confirms, and it refuses work it would lose
	// without an explicit discard. todo.update and todo.remove on a gate item
	// also need allow_destructive (gate_todo.go). todo.approve_design and
	// todo.approve_review stay here because serve accepts them only from the
	// list's own session, and a device has no caller.
	deviceWriteMethods = []string{
		"session.prompt", "session.rename", "session.move",
		"session.terminate",
		"steer.send",
		"todo.add", "todo.update", "todo.move", "todo.remove",
		"todo.ensure_done", "todo.mark_met", "todo.gate",
		"todo.approve_design", "todo.approve_review",
		"ci.watch",
		"push.register", "push.unregister",
	}
	// deviceDestructiveMethods kill terminals, drop sessions, post to GitHub,
	// replace agent installs, or amount to a shell on the host: raw terminal
	// input and starting an agent or a command. A device needs allow_destructive,
	// as it does for the terminal WebSocket. session.prompt and steer.send are
	// not here: they reach the agent's composer or its open dialog, not a shell.
	// aws.login_start is here because approval is often one tap in a browser
	// that already holds the portal session, so it can mint a host credential.
	deviceDestructiveMethods = []string{
		"pty.input", "pty.create", "pty.run", "pty.kill", "pty.forget",
		"session.create", "session.restart", "session.forget",
		"review.post",
		"agents.update", "agents.move_prefix",
		"aws.login_start",
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
