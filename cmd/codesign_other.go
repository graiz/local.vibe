//go:build !darwin

package cmd

// signLocalBuild: only macOS keys privacy grants to code signatures.
func signLocalBuild(string) {}
