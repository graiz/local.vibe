//go:build !darwin && !windows

package cmd

// signLocalBuild: only macOS keys privacy grants to code signatures.
func signLocalBuild(string) {}
