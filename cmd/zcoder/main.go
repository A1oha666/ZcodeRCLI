package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/itwanger/zcoder-go/internal/agent"
	"github.com/itwanger/zcoder-go/internal/cluster"
	"github.com/itwanger/zcoder-go/internal/config"
	"github.com/itwanger/zcoder-go/internal/llm"
	"github.com/itwanger/zcoder-go/internal/rag"
	"github.com/itwanger/zcoder-go/internal/runtime"
	"github.com/itwanger/zcoder-go/internal/skill"
	"github.com/itwanger/zcoder-go/internal/snapshot"
	"github.com/itwanger/zcoder-go/internal/tools"
	"github.com/itwanger/zcoder-go/internal/tui"
	"github.com/itwanger/zcoder-go/internal/version"
	"github.com/itwanger/zcoder-go/internal/wechat"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rootCommand() *cobra.Command {
	var once string
	var plain bool
	var cwd string
	var mode string

	cmd := &cobra.Command{
		Use:   "zcoder",
		Short: "Zcoder Go Agent CLI",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cwd != "" {
				if err := os.Chdir(cwd); err != nil {
					return err
				}
			}
			env, err := bootstrap(cmd.Context())
			if err != nil {
				return err
			}
			defer env.Close()
			if once != "" {
				answer, err := runAgentInput(cmd.Context(), env.Agent, mode, once)
				if err != nil {
					return err
				}
				fmt.Println(answer)
				return nil
			}
			if plain {
				return runPlain(cmd.Context(), env, mode)
			}
			return tui.Run(cmd.Context(), env.Agent, tui.Startup{
				Version:       version.Version,
				Provider:      env.Client.Provider(),
				Model:         env.Client.Model(),
				CWD:           mustCwd(),
				SkillsEnabled: env.Skills.EnabledCount(),
				SkillsTotal:   env.Skills.Count(),
				MCPReady:      env.MCPReady,
				MCPTools:      env.MCPTools,
				MaxContext:    env.Client.MaxContext(),
			})
		},
	}
	cmd.Flags().StringVar(&once, "once", "", "run one prompt and exit")
	cmd.Flags().BoolVar(&plain, "plain", false, "use plain stdin/stdout REPL")
	cmd.Flags().StringVar(&cwd, "cwd", "", "workspace directory")
	cmd.Flags().StringVar(&mode, "mode", "", "run mode for --once or --plain: react, plan, team")
	cmd.AddCommand(versionCommand(), doctorCommand(), indexCommand(), searchCommand(), graphCommand(),
		serveCommand(), wechatCommand(), snapshotCommand(), clusterCommand())
	return cmd
}

type environment struct {
	Config   config.Config
	Client   llm.Client
	Agent    *agent.Agent
	Tools    *tools.Registry
	Skills   *skill.Registry
	RAG      *rag.Index
	Snapshot *snapshot.Service
	MCPReady int
	MCPTools int
}

func (e *environment) Close() {
	if e.Tools != nil {
		e.Tools.Close()
	}
}

func bootstrap(ctx context.Context) (*environment, error) {
	cfg := config.Load()
	client, err := llm.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	project := mustCwd()
	skillRegistry := skill.NewRegistry(project)
	if err := skillRegistry.Reload(); err != nil {
		return nil, err
	}
	codeIndex := rag.NewIndex(filepath.Join(config.HomeDir(), ".zcoder", "rag", "go-code-index.json"))
	_ = codeIndex.Load()
	memoryStore := agent.NewMemoryStore(filepath.Join(config.HomeDir(), ".zcoder", "memory", "long_term_memory.json"), project)
	snapshots := snapshot.NewService(project, filepath.Join(config.HomeDir(), ".zcoder", "snapshots"))
	reg := tools.NewRegistry(project, tools.Options{
		Config:        cfg,
		RAG:           codeIndex,
		Skills:        skillRegistry,
		MemorySaver:   memoryStore.Save,
		Snapshot:      snapshots,
		PostWriteHook: tools.GoDiagnosticsHook(project),
	})
	mcpReady, mcpTools := reg.LoadMCP(ctx)
	ag := agent.New(client, reg, memoryStore, skillRegistry)
	ag.SetSnapshotService(snapshots)
	return &environment{
		Config:   cfg,
		Client:   client,
		Agent:    ag,
		Tools:    reg,
		Skills:   skillRegistry,
		RAG:      codeIndex,
		Snapshot: snapshots,
		MCPReady: mcpReady,
		MCPTools: mcpTools,
	}, nil
}

func runPlain(ctx context.Context, env *environment, mode string) error {
	fmt.Printf("Zcoder Go %s (%s/%s). Type /exit to quit.\n", version.Version, env.Client.Provider(), env.Client.Model())
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("* ")
		if !scanner.Scan() {
			return nil
		}
		line := scanner.Text()
		if strings.TrimSpace(line) == "/exit" {
			return nil
		}
		answer, err := runAgentInput(ctx, env.Agent, mode, line)
		if err != nil {
			fmt.Println("error:", err)
			continue
		}
		fmt.Println(answer)
	}
}

func runAgentInput(ctx context.Context, ag *agent.Agent, mode string, input string) (string, error) {
	if strings.TrimSpace(mode) != "" {
		return ag.RunMode(ctx, agent.RunMode(mode), input)
	}
	return ag.RunCommand(ctx, input)
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Version)
		},
	}
}

func doctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "check local runtime and configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			providerCfg := cfg.Provider(cfg.DefaultProvider)
			fmt.Printf("Zcoder Go: %s\n", version.Version)
			fmt.Printf("CWD: %s\n", mustCwd())
			fmt.Printf("Provider: %s\n", cfg.DefaultProvider)
			fmt.Printf("Model: %s\n", providerCfg.Model)
			fmt.Printf("Base URL: %s\n", present(providerCfg.BaseURL != ""))
			fmt.Printf("API key: %s\n", present(providerCfg.APIKey != ""))
			fmt.Printf("SERPAPI_API_KEY: %s\n", present(os.Getenv("SERPAPI_API_KEY") != ""))
			fmt.Printf("SEARXNG_BASE_URL: %s\n", present(os.Getenv("SEARXNG_BASE_URL") != ""))
			if _, err := os.Stat(filepath.Join(config.HomeDir(), ".zcoder", "mcp.json")); err == nil {
				fmt.Println("MCP user config: present")
			} else {
				fmt.Println("MCP user config: missing")
			}
			if _, err := os.Stat(".zcoder/mcp.json"); err == nil {
				fmt.Println("MCP project config: present")
			} else {
				fmt.Println("MCP project config: missing")
			}
			return nil
		},
	}
}

func indexCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "index [path]",
		Short: "build code RAG index",
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) > 0 {
				root = args[0]
			}
			idx := rag.NewIndex(filepath.Join(config.HomeDir(), ".zcoder", "rag", "go-code-index.json"))
			if err := idx.Build(root); err != nil {
				return err
			}
			if err := idx.Save(); err != nil {
				return err
			}
			fmt.Printf("indexed %d chunks from %s\n", len(idx.Chunks), root)
			return nil
		},
	}
}

func searchCommand() *cobra.Command {
	var topK int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "search code index",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx := rag.NewIndex(filepath.Join(config.HomeDir(), ".zcoder", "rag", "go-code-index.json"))
			if err := idx.Load(); err != nil {
				return err
			}
			for _, r := range idx.Search(strings.Join(args, " "), topK) {
				fmt.Printf("%s:%d score=%.3f\n%s\n\n", r.Chunk.Path, r.Chunk.StartLine, r.Score, r.Chunk.Preview())
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&topK, "top", 8, "number of results")
	return cmd
}

func graphCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "graph",
		Short: "print indexed code relation graph",
		RunE: func(cmd *cobra.Command, args []string) error {
			idx := rag.NewIndex(filepath.Join(config.HomeDir(), ".zcoder", "rag", "go-code-index.json"))
			if err := idx.Load(); err != nil {
				return err
			}
			for _, rel := range idx.Relations {
				fmt.Printf("%s -> %s (%s)\n", rel.From, rel.To, rel.Kind)
			}
			return nil
		},
	}
}

func serveCommand() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "start local Runtime API",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := bootstrap(cmd.Context())
			if err != nil {
				return err
			}
			defer env.Close()
			key := os.Getenv("ZCODER_RUNTIME_API_KEY")
			if key == "" {
				return fmt.Errorf("ZCODER_RUNTIME_API_KEY is required")
			}
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			fmt.Printf("Runtime API listening on http://%s\n", addr)
			return runtime.NewServer(addr, key, env.Agent.Clone).ListenAndServe(cmd.Context())
		},
	}
	cmd.Flags().IntVar(&port, "port", 8080, "listen port")
	return cmd
}

func clusterCommand() *cobra.Command {
	var agents int
	var concurrency int
	var simulate bool
	var simDelayMs int
	var workspaceDir string

	cmd := &cobra.Command{
		Use:   "cluster [task]",
		Short: "run a concurrent agent cluster on one task",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task := strings.Join(args, " ")
			var client llm.Client
			if !simulate {
				env, err := bootstrap(cmd.Context())
				if err != nil {
					return err
				}
				defer env.Close()
				client = env.Client
			}
			cl := cluster.New(cluster.Config{
				Agents:       agents,
				Concurrency:  concurrency,
				Simulate:     simulate,
				SimDelay:     time.Duration(simDelayMs) * time.Millisecond,
				ProjectRoot:  mustCwd(),
				WorkspaceDir: workspaceDir,
			}, client, clusterPrinter())
			report, err := cl.Run(cmd.Context(), task)
			if err != nil {
				return err
			}
			stats := report.Stats
			fmt.Printf("\n=== Stats ===\nagents: %d | succeeded: %d | failed: %d | peak concurrency: %d | total: %s\n",
				stats.Total, stats.Succeeded, stats.Failed, stats.PeakConcurrency, stats.Duration.Round(time.Millisecond))
			fmt.Printf("sandboxes: %s\n", report.RunDir)
			fmt.Println("\n=== Final Report ===")
			fmt.Println(report.Summary)
			return nil
		},
	}
	cmd.Flags().IntVar(&agents, "agents", 8, "number of agents in the cluster")
	cmd.Flags().IntVar(&concurrency, "concurrency", 8, "max agents running at the same time")
	cmd.Flags().BoolVar(&simulate, "simulate", false, "run mock agents without real LLM calls")
	cmd.Flags().IntVar(&simDelayMs, "sim-delay-ms", 0, "base simulated latency per mock LLM call")
	cmd.Flags().StringVar(&workspaceDir, "workspace-dir", "", "base directory for agent sandboxes (default <cwd>/.zcoder/cluster)")
	return cmd
}

func clusterPrinter() cluster.Observer {
	var mu sync.Mutex
	return func(ev cluster.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case "plan":
			fmt.Printf("[worker-%04d] subtask: %s\n", ev.Worker+1, ev.Content)
		case "done":
			fmt.Printf("[worker-%04d] %s\n", ev.Worker+1, ev.Content)
		case "error":
			fmt.Printf("[worker-%04d] error: %s\n", ev.Worker+1, ev.Content)
		case "info":
			fmt.Printf("[cluster] %s\n", ev.Content)
		}
	}
}

func wechatCommand() *cobra.Command {
	return wechat.Command(config.HomeDir())
}

func snapshotCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "snapshot", Short: "manage workspace snapshots"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := snapshot.NewService(mustCwd(), filepath.Join(config.HomeDir(), ".zcoder", "snapshots"))
			items, err := svc.List()
			if err != nil {
				return err
			}
			for _, item := range items {
				fmt.Printf("%s %s %s\n", item.ID, item.Phase, item.Created.Format(time.RFC3339))
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "restore <id>",
		Short: "restore a snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := snapshot.NewService(mustCwd(), filepath.Join(config.HomeDir(), ".zcoder", "snapshots"))
			return svc.Restore(args[0])
		},
	})
	return cmd
}

func present(ok bool) string {
	if ok {
		return "present"
	}
	return "missing"
}

func mustCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
