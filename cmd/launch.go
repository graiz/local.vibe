package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/localvibe/vibe/internal/client"
	"github.com/spf13/cobra"
)

type vibeConfig struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	Cmd  string `json:"cmd"`
}

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launch an app defined in vibe.json (recommended)",
	Long: `Reads vibe.json from the current directory, registers the app with the
daemon, and starts it as a managed process. The daemon will keep the route
registered even if the process exits — visiting the URL offers to restart it.

Create a vibe.json in your project root:

  {"name": "myapp", "port": 3000, "cmd": "npm run dev"}`,
	Example: `  # In your project directory with a vibe.json:
  vibe launch

  # Stop the app later:
  vibe stop myapp`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("could not determine working directory: %w", err)
		}

		cfgPath := filepath.Join(dir, "vibe.json")
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			return fmt.Errorf("no vibe.json found in %s\n\nCreate one:\n  {\"name\": \"myapp\", \"port\": 3000, \"cmd\": \"npm run dev\"}", dir)
		}

		var cfg vibeConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("invalid vibe.json: %w", err)
		}
		if cfg.Name == "" || cfg.Port == 0 || cfg.Cmd == "" {
			return fmt.Errorf("vibe.json must have name, port, and cmd fields")
		}

		c := client.New()
		resp, err := c.Register(client.RegisterRequest{
			Name: cfg.Name,
			Port: cfg.Port,
			Cmd:  cfg.Cmd,
			Dir:  dir,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("launched: %s → %s\n", cfg.Name, resp.URL)
		fmt.Printf("  cmd: %s\n", cfg.Cmd)
		fmt.Printf("  dir: %s\n", dir)
		fmt.Printf("\nStop with: vibe stop %s\n", cfg.Name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(launchCmd)
}
