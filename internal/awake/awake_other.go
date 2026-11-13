//go:build !windows

// Package awake asks the system not to sleep while a run is going (SPEC
// section 9, DECISIONS.md D64).
package awake

// Hold does nothing outside Windows yet.
func Hold() (release func()) { return func() {} }
