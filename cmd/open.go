package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/spf13/cobra"
)

var openCmd = &cobra.Command{
	Use:   "open <name>",
	Short: "Open a registered service in the browser",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := client.New()
		routes, err := c.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		var url string
		for _, r := range routes {
			if r.Name == args[0] {
				url = r.URL
				break
			}
		}
		if url == "" {
			fmt.Fprintf(os.Stderr, "no route named %q\n", args[0])
			os.Exit(1)
		}

		fmt.Println(url)

		var openExec *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			openExec = exec.Command("open", url)
		case "linux":
			openExec = exec.Command("xdg-open", url)
		case "windows":
			openExec = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		default:
			return nil
		}
		return openExec.Run()
	},
}
