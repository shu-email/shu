//go:build e2e

package main

// The automation plugin lets an agent read and drive the dev build even
// while its window is covered, which the system accessibility tree cannot
// follow because MyGo holds the frames of a window out of sight. It is
// linked only with -tags e2e, listens only when MYGO_AUTOMATION names a
// socket, and release builds leave it out. See
// .agents/skills/shu-desktop-check.
import _ "github.com/egoist/mygo/plugins/automation"
