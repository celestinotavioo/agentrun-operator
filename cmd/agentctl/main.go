// Command agentctl is the friendly face of the operator: a live dashboard and a few helpers.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/scenarios"
	"github.com/celestinotavioo/agentrun-operator/internal/tui"
)

const usage = `agentctl — the AgentRun operator, in your terminal

Usage:
  agentctl [flags] <command> [args]

Commands:
  watch                 live dashboard (default)
  show                  presenter mode: the incident story, a live scoreboard and a report card
  launch <scenario>     create an AgentRun from a scenario ("all" launches every scenario)
  describe <name>       show the attempt ledger of a run
  scenarios             list the demo scenarios
  clean                 delete every AgentRun in the namespace

Flags:
`

func main() {
	fs := flag.NewFlagSet("agentctl", flag.ExitOnError)
	kubeContext := fs.String("context", envOr("AGENTRUN_CONTEXT", "docker-desktop"), "kubeconfig context")
	namespace := fs.String("n", envOr("AGENTRUN_NAMESPACE", "agents-demo"), "namespace")
	name := fs.String("name", "", "name for `launch` (default: generated)")
	allowRemote := fs.Bool("allow-remote", false, "allow contexts that do not look like a local cluster")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		fs.PrintDefaults()
	}
	// Accept flags anywhere (`agentctl launch flaky --name x`), not only before the command.
	var positional []string
	rest := os.Args[1:]
	for {
		_ = fs.Parse(rest)
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	cmd, args := "watch", positional
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if cmd == "scenarios" {
		listScenarios()
		return
	}
	if cmd == "help" || cmd == "-h" {
		fs.Usage()
		return
	}

	remote := !isLocalContext(*kubeContext)
	if remote && !*allowRemote {
		fail("context %q does not look like a local cluster; refusing to touch it.\n  Use --context docker-desktop (or pass --allow-remote if you really mean it).", *kubeContext)
	}
	c, err := newClient(*kubeContext)
	if err != nil {
		fail("%v", err)
	}

	switch cmd {
	case "watch":
		err = tui.Run(tui.Options{Client: c, Namespace: *namespace, KubeContext: *kubeContext, Remote: remote})
	case "show":
		err = tui.RunShow(tui.Options{Client: c, Namespace: *namespace, KubeContext: *kubeContext, Remote: remote})
	case "launch":
		err = launch(c, *namespace, *name, args)
	case "describe":
		err = describe(c, *namespace, args)
	case "clean":
		err = c.DeleteAllOf(context.Background(), &agentsv1.AgentRun{}, client.InNamespace(*namespace))
		if err == nil {
			ok("deleted all AgentRuns in %s", *namespace)
		}
	default:
		fs.Usage()
		os.Exit(2)
	}
	if err != nil {
		fail("%v", err)
	}
}

func newClient(kubeContext string) (client.Client, error) {
	cfg, err := config.GetConfigWithContext(kubeContext)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig context %q: %w", kubeContext, err)
	}
	cfg.Timeout = 10 * time.Second
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(eventsv1.AddToScheme(scheme))
	utilruntime.Must(agentsv1.AddToScheme(scheme))
	return client.New(cfg, client.Options{Scheme: scheme})
}

func launch(c client.Client, ns, name string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: agentctl launch <scenario|all>  (see: agentctl scenarios)")
	}
	names := []string{args[0]}
	if args[0] == "all" {
		names = nil
		for _, s := range scenarios.All {
			names = append(names, s.Name)
		}
		name = ""
	}
	for _, sc := range names {
		info, found := scenarios.Lookup(sc)
		if !found {
			return fmt.Errorf("unknown scenario %q (see: agentctl scenarios)", sc)
		}
		run := scenarios.Build(sc, name, ns)
		if err := c.Create(context.Background(), run); err != nil {
			return err
		}
		ok("%-22s %s", run.Name, dim("→ "+info.Expect))
	}
	return nil
}

func describe(c client.Client, ns string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: agentctl describe <name>")
	}
	var run agentsv1.AgentRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: args[0]}, &run); err != nil {
		return err
	}
	fmt.Println(tui.Describe(&run))
	return nil
}

func listScenarios() {
	title := lipgloss.NewStyle().Bold(true)
	for i, s := range scenarios.All {
		fmt.Printf("  %d  %s  %s\n     %s\n", i+1, title.Render(fmt.Sprintf("%-14s", s.Name)), s.Story, dim("expect: "+s.Expect))
	}
}

// isLocalContext guards against pointing a demo at a real cluster by accident.
// The only remote cluster allowed is the dedicated one from deploy/gcp/terraform.
func isLocalContext(name string) bool {
	if strings.HasPrefix(name, "gke_") && strings.HasSuffix(name, "_agentrun-demo") {
		return true
	}
	for _, p := range []string{"docker-desktop", "kind-", "minikube", "orbstack", "rancher-desktop", "colima", "k3d-"} {
		if name == p || strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func ok(f string, a ...any) {
	fmt.Println(lipgloss.NewStyle().Foreground(lipgloss.Color("#34A853")).Render("✔ ") + fmt.Sprintf(f, a...))
}

func dim(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#9AA0A6")).Render(s) }

func fail(f string, a ...any) {
	fmt.Fprintln(os.Stderr, lipgloss.NewStyle().Foreground(lipgloss.Color("#EA4335")).Render("✘ ")+fmt.Sprintf(f, a...))
	os.Exit(1)
}
