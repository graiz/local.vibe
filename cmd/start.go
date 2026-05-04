package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/graiz/local.vibe/internal/client"
	"github.com/spf13/cobra"
)

type vibeConfig struct {
	Name              string         `json:"name"`
	Port              int            `json:"port"`
	Cmd               string         `json:"cmd"`
	OAuthCallbackPort int            `json:"oauth_callback_port,omitempty"`
	ReservePorts        map[string]int `json:"reserve_ports,omitempty"`
}

var startCmd = &cobra.Command{
	Use:   "start [name] [port] [-- command...]",
	Short: "Start a managed app",
	Long: `Start a managed app. Works three ways:

  vibe start                          Read vibe.json and start the app
  vibe start myapp                    Start an already-registered route
  vibe start myapp 3000 -- npm dev    Register and start a new app

If port is omitted or set to 0 in vibe.json, a free port is auto-assigned
and injected as the PORT environment variable.

vibe.json fields (used by 'vibe start' with no args):
  name                  required — subdomain: name.vibe
  cmd                   required — shell command to start the app
  port                  optional — primary port; omit for auto-assign
  reserve_ports         optional — {"name": port} map of auxiliary ports
                        the cmd also binds; each is reserved across the
                        route table and injected as $PORT_<UPPER_NAME>.
                        Example: {"server": 3001} → $PORT_SERVER=3001
  oauth_callback_port   optional — fixed localhost port that 307-forwards
                        OAuth callbacks to name.vibe
  icon                  optional — emoji for the dashboard
  idle_timeout          optional — auto-stop after N minutes idle (0 = never)

Full guide: curl http://localhost:7999/setup.md`,
	Example: `  vibe start
  vibe start myapp
  vibe start myapp 3000 -- npm run dev`,
	DisableFlagParsing: false,
	SilenceUsage:       true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Split args at "--" to separate vibe args from the app command
		vibeArgs, appCmd := splitAtDash(args)

		switch {
		case len(vibeArgs) == 0 && len(appCmd) == 0:
			// vibe start — read vibe.json
			return startFromConfig()

		case len(vibeArgs) == 1 && len(appCmd) == 0:
			// vibe start myapp — start existing route
			return startExisting(vibeArgs[0])

		case len(vibeArgs) >= 2 && len(appCmd) > 0:
			// vibe start myapp 3000 -- npm run dev
			name := vibeArgs[0]
			var port int
			if _, err := fmt.Sscan(vibeArgs[1], &port); err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid port: %s", vibeArgs[1])
			}
			return startNew(name, port, strings.Join(appCmd, " "), 0, nil)

		default:
			return fmt.Errorf("usage: vibe start [name] [port] [-- command...]")
		}
	},
}

// startFromConfig reads vibe.json from the current directory and registers+starts the app.
func startFromConfig() error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	cfgPath := filepath.Join(dir, "vibe.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("no vibe.json found — create one or specify: vibe start <name> <port> -- <command>")
	}

	var cfg vibeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("invalid vibe.json: %w", err)
	}
	if cfg.Name == "" || cfg.Cmd == "" {
		return fmt.Errorf("vibe.json must have name and cmd fields")
	}

	return startNew(cfg.Name, cfg.Port, cfg.Cmd, cfg.OAuthCallbackPort, cfg.ReservePorts)
}

// startExisting starts an already-registered managed route.
func startExisting(name string) error {
	c := client.New()
	if err := c.Start(name); err != nil {
		return fmt.Errorf("could not start %s: %w", name, err)
	}
	fmt.Printf("started: %s\n", name)
	return nil
}

// startNew registers a managed route and starts it.
func startNew(name string, port int, command string, oauthCallbackPort int, reservePorts map[string]int) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	c := client.New()
	req := client.RegisterRequest{
		Name:       name,
		Port:       port,
		Cmd:        command,
		Dir:        dir,
		ReservePorts: reservePorts,
	}
	if oauthCallbackPort > 0 {
		req.OAuthCallbackPort = &oauthCallbackPort
	}
	resp, err := c.Register(req)
	if err != nil {
		return fmt.Errorf("could not register %s: %w", name, err)
	}
	if port == 0 && resp.Port > 0 {
		fmt.Printf("started: %s → %s (port %d)\n", name, resp.URL, resp.Port)
	} else {
		fmt.Printf("started: %s → %s\n", name, resp.URL)
	}
	fmt.Printf("  stop with: vibe stop %s\n", name)
	return nil
}

// splitAtDash splits args into the portion before "--" and after "--".
func splitAtDash(args []string) (before, after []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func init() {
	rootCmd.AddCommand(startCmd)
}
