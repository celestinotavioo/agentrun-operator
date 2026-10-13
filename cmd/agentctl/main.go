// Command agentctl runs the on-stage show: the incident story, a live scoreboard and a report card.
package main

import (
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
	"github.com/celestinotavioo/agentrun-operator/internal/tui"
)

func main() {
	fs := flag.NewFlagSet("agentctl", flag.ExitOnError)
	kubeContext := fs.String("context", envOr("AGENTRUN_CONTEXT", "docker-desktop"), "kubeconfig context")
	namespace := fs.String("n", envOr("AGENTRUN_NAMESPACE", "agents-demo"), "namespace")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: agentctl [--context docker-desktop] [-n agents-demo] show\n\n")
		fs.PrintDefaults()
	}
	// Accept flags before or after the command.
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
	if len(positional) != 1 || positional[0] != "show" {
		fs.Usage()
		os.Exit(2)
	}

	if !isLocalContext(*kubeContext) {
		fail("context %q does not look like a local cluster; refusing to touch it.\n  Use --context docker-desktop.", *kubeContext)
	}
	c, err := newClient(*kubeContext)
	if err != nil {
		fail("%v", err)
	}
	if err := tui.RunShow(tui.Options{Client: c, Namespace: *namespace, KubeContext: *kubeContext}); err != nil {
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

// isLocalContext guards against pointing the demo at a real cluster by accident.
func isLocalContext(name string) bool {
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

func fail(f string, a ...any) {
	fmt.Fprintln(os.Stderr, lipgloss.NewStyle().Foreground(lipgloss.Color("#EA4335")).Render("✘ ")+fmt.Sprintf(f, a...))
	os.Exit(1)
}
