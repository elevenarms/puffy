//go:build !openbsd

package main

// restrictProcess is a no-op outside OpenBSD (pledge(2) is OpenBSD-only).
func restrictProcess(s Settings) {}
