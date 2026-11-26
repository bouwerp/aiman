// Package worker holds the Cloudflare Worker that serves artifacts.
package worker

import _ "embed"

// Script is worker.js, the ES module aiman artifact setup uploads.
//
//go:embed worker.js
var Script []byte
