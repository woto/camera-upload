// Package web embeds the static client assets so they can be served directly
// from the binary without a separate build step.
package web

import _ "embed"

// ClientHTML is the Uppy-based uploader UI served at /client.
//
//go:embed client/index.html
var ClientHTML []byte
