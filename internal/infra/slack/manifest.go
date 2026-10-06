package slack

// Manifest is the Slack app the operator installs once. It requests user
// scopes only, so chat.postMessage runs as the member who approves it.
// The loopback redirect is http on purpose: Slack's manifest editor accepts
// it, and the browser reaches this host through an SSH local forward of
// the same port when the browser is on another machine.
const Manifest = `{
  "display_information": {
    "name": "Aiman",
    "description": "Lets Aiman coding agents post, read, and wait on Slack as the member who approved the app.",
    "background_color": "#1a1a1a"
  },
  "oauth_config": {
    "redirect_urls": [
      "http://127.0.0.1:43127/slack/callback"
    ],
    "scopes": {
      "user": [
        "chat:write",
        "channels:history",
        "groups:history",
        "im:history",
        "mpim:history",
        "channels:read",
        "groups:read",
        "im:read",
        "mpim:read",
        "users:read",
        "reactions:write"
      ]
    },
    "pkce_enabled": true
  },
  "settings": {
    "org_deploy_enabled": false,
    "socket_mode_enabled": false,
    "token_rotation_enabled": false
  }
}
`
