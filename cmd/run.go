package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/localvibe/vibe/internal/client"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run <name> <port> -- <command...>",
	Short: "Register a route, run a command, deregister on exit",
	Long: `Wraps any command with automatic route registration and deregistration.
The route is removed when the command exits, crashes, or is killed.`,
	Example: `  vibe run myapp 3000 -- npm run dev
  vibe run api 5001 -- python3 app.py
  vibe run backend 8080 -- go run .`,
	Args:               cobra.MinimumNArgs(3),
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		var port int
		if _, err := fmt.Sscan(args[1], &port); err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid port: %s", args[1])
		}
		cmdArgs := args[2:]

		child := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		child.Stdin = os.Stdin
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr

		if err := child.Start(); err != nil {
			return fmt.Errorf("failed to start %q: %w", cmdArgs[0], err)
		}

		pid := child.Process.Pid
		c := client.New()
		resp, err := c.Register(client.RegisterRequest{
			Name: name,
			Port: port,
			PID:  &pid,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "vibe: warning: could not register route: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "vibe: %s → %s (pid %d)\n", name, resp.URL, pid)
		}

		// Forward signals to child process
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			for sig := range sigs {
				_ = child.Process.Signal(sig)
			}
		}()

		_ = child.Wait()
		signal.Stop(sigs)
		close(sigs)

		if err := c.Deregister(name); err != nil {
			fmt.Fprintf(os.Stderr, "vibe: warning: could not deregister route: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "vibe: deregistered %s\n", name)
		}

		return nil
	},
}
