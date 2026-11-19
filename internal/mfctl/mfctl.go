/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/
// Package mfctl is Manifesto's command-line tool. It talks to the API server
// with the caller's kubeconfig, so RBAC decides what it may do, exactly as
// for desk.
package mfctl

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// FieldManager is mfctl's Server-Side Apply field manager.
const FieldManager = "manifesto-mfctl"

// Version is set at build time with -ldflags.
var Version = "dev"

// Env is how a command reaches the cluster and the terminal. Tests replace
// Client and Namespace.
type Env struct {
	Out, Err io.Writer
	// Client returns a client for the cluster; nil uses the kubeconfig.
	Client func() (client.Client, error)
	// Namespace returns the namespace to work in.
	Namespace func() (string, error)
	// Poll is how often to check on progress (default 500ms).
	Poll time.Duration
}

// NewCommand returns the mfctl root command.
func NewCommand(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use:           "mfctl",
		Short:         "Manifesto's command-line tool",
		Long:          "mfctl works with Manifesto resources using your kubeconfig: Kubernetes RBAC decides what you may do.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(env.Out)
	root.SetErr(env.Err)
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	flags := root.PersistentFlags()
	flags.StringVar(&loading.ExplicitPath, "kubeconfig", "", "Path to the kubeconfig file.")
	flags.StringVar(&overrides.CurrentContext, "context", "", "The kubeconfig context to use.")
	flags.StringVarP(&overrides.Context.Namespace, "namespace", "n", "", "The namespace to work in.")
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides)
	if env.Client == nil {
		env.Client = func() (client.Client, error) {
			cfg, err := config.ClientConfig()
			if err != nil {
				return nil, err
			}
			return newClient(cfg)
		}
	}
	if env.Namespace == nil {
		env.Namespace = func() (string, error) {
			ns, _, err := config.Namespace()
			return ns, err
		}
	}
	if env.Poll == 0 {
		env.Poll = 500 * time.Millisecond
	}
	root.AddCommand(typesetCommand(env), printCommand(env), editionCommand(env), &cobra.Command{
		Use:   "version",
		Short: "Print mfctl's version",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { _, _ = fmt.Fprintln(cmd.OutOrStdout(), Version) },
	})
	return root
}

// Scheme knows the Kubernetes and Manifesto types.
func Scheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

func newClient(cfg *rest.Config) (client.Client, error) {
	s, err := Scheme()
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: s})
}
