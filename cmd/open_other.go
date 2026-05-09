//go:build !darwin && !linux && !windows

package cmd

import "fmt"

// openURL on unknown platforms just prints the URL — there's no portable
// "open this in a browser" interface across BSDs / Plan9 / etc.
func openURL(url string) error {
	fmt.Println(url)
	return nil
}
