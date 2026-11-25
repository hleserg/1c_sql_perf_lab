//go:build !windows

package main

func pathLauncherExitCode() (int, bool) { return 0, false }
func ensureUserPath(exe string) error   { return nil }
