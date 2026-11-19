/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/
package mfctl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

func typesetCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{Use: "typeset", Aliases: []string{"theme"}, Short: "Work with themes"}
	var name string
	var cluster bool
	var params []string
	build := &cobra.Command{
		Use:   "build DIR",
		Short: "Pack a theme folder into a Typeset (or ClusterTypeset) and print it as YAML",
		Long: `Pack a theme folder into a Typeset and print it as YAML, ready for
kubectl apply. *.html files are templates, every other file is an asset;
the folder must be flat. The theme is parsed first, so template errors show
here rather than in the cluster.

  mfctl typeset build ./my-theme -n blog-alice | kubectl apply -f -`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			if name == "" {
				abs, err := filepath.Abs(dir)
				if err != nil {
					return err
				}
				name = filepath.Base(abs)
			}
			var namespace string
			if !cluster {
				ns, err := env.Namespace()
				if err != nil {
					return err
				}
				namespace = ns
			}
			obj, err := BuildTypeset(dir, name, namespace, cluster, params)
			if err != nil {
				return err
			}
			b, err := yaml.Marshal(obj)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(b)
			return err
		},
	}
	build.Flags().StringVar(&name, "name", "", "The theme's name (default: the folder's name).")
	build.Flags().BoolVar(&cluster, "cluster", false, "Build a ClusterTypeset, shared by every namespace.")
	build.Flags().StringArrayVar(&params, "param", nil, "A default parameter, key=value (repeatable).")
	cmd.AddCommand(build)
	return cmd
}

// BuildTypeset reads a flat theme folder into a Typeset, or a ClusterTypeset
// when cluster is set, after checking that it parses.
func BuildTypeset(dir, name, namespace string, cluster bool, params []string) (any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	src := &v1.InlineTypesetSource{Templates: map[string]string{}, Assets: map[string]string{}}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			return nil, fmt.Errorf("%s: a theme folder must be flat; move %s's files up or out", dir, e.Name())
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- the user's own theme folder
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(b) {
			return nil, fmt.Errorf("%s is not text; themes inline text assets only (put images in a Print)", e.Name())
		}
		if filepath.Ext(e.Name()) == ".html" {
			src.Templates[e.Name()] = string(b)
		} else {
			src.Assets[e.Name()] = string(b)
		}
	}
	if len(src.Assets) == 0 {
		src.Assets = nil
	}
	if _, err := render.ParseTheme(src.Templates, src.Assets); err != nil {
		return nil, fmt.Errorf("the theme does not parse: %w", err)
	}
	spec := v1.TypesetSpec{ContractVersion: v1.ContractV1, Source: v1.TypesetSource{Inline: src}}
	if len(params) > 0 {
		spec.Params = map[string]string{}
		for _, p := range params {
			k, v, ok := strings.Cut(p, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("--param %q: want key=value", p)
			}
			spec.Params[k] = v
		}
	}
	meta := metav1.ObjectMeta{Name: name}
	if cluster {
		return &v1.ClusterTypeset{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "ClusterTypeset"},
			ObjectMeta: meta, Spec: spec,
		}, nil
	}
	meta.Namespace = namespace
	return &v1.Typeset{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "Typeset"},
		ObjectMeta: meta, Spec: spec,
	}, nil
}
