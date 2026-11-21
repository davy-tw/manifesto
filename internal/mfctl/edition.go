/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/
package mfctl

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func editionCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{Use: "edition", Aliases: []string{"revision"}, Short: "Work with the revision history of a Dispatch"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list DISPATCH",
		Short: "List a Dispatch's Editions, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := connect(env)
			if err != nil {
				return err
			}
			editions, err := Editions(cmd.Context(), c, ns, args[0])
			if err != nil {
				return err
			}
			if len(editions) == 0 {
				return fmt.Errorf("dispatch %q has no Editions", args[0])
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "EDITION\tAGE\tTITLE")
			for _, e := range slices.Backward(editions) {
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\n", e.Spec.Number,
					duration.HumanDuration(time.Since(e.CreationTimestamp.Time)), e.Spec.Snapshot.Title)
			}
			return w.Flush()
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show DISPATCH NUMBER",
		Short: "Print an Edition's body",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := getEdition(cmd.Context(), env, args[0], args[1])
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), e.Spec.Snapshot.Content.Body)
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "restore DISPATCH NUMBER",
		Short: "Make an Edition's content the Dispatch's content again",
		Long: `Copy an Edition's title, summary, cover, author and body back into its
Dispatch. press then records this as a new Edition; history is not
rewritten. Publishing (state, publishAt) is left as it is.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := getEdition(cmd.Context(), env, args[0], args[1])
			if err != nil {
				return err
			}
			c, ns, err := connect(env)
			if err != nil {
				return err
			}
			if err := Restore(cmd.Context(), c, ns, e); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "dispatch/%s restored to edition %d\n", e.Spec.DispatchRef, e.Spec.Number)
			return err
		},
	})
	return cmd
}

func connect(env *Env) (client.Client, string, error) {
	ns, err := env.Namespace()
	if err != nil {
		return nil, "", err
	}
	c, err := env.Client()
	return c, ns, err
}

// Editions lists a Dispatch's Editions, oldest first.
func Editions(ctx context.Context, c client.Client, ns, dispatch string) ([]v1.Edition, error) {
	var list v1.EditionList
	if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{v1.EditionDispatchLabel: dispatch}); err != nil {
		return nil, err
	}
	slices.SortFunc(list.Items, func(a, b v1.Edition) int { return int(a.Spec.Number - b.Spec.Number) })
	return list.Items, nil
}

func getEdition(ctx context.Context, env *Env, dispatch, number string) (*v1.Edition, error) {
	n, err := strconv.ParseInt(number, 10, 64)
	if err != nil || n < 1 {
		return nil, fmt.Errorf("edition number %q: want 1, 2, 3…", number)
	}
	c, ns, err := connect(env)
	if err != nil {
		return nil, err
	}
	editions, err := Editions(ctx, c, ns, dispatch)
	if err != nil {
		return nil, err
	}
	for i := range editions {
		if editions[i].Spec.Number == n {
			return &editions[i], nil
		}
	}
	return nil, fmt.Errorf("dispatch %q has no edition %d (see mfctl edition list %s)", dispatch, n, dispatch)
}

// Restore copies an Edition's snapshot into its Dispatch. It is a JSON merge
// patch rather than an apply, because restoring must also remove fields the
// Edition does not have (an apply only removes fields its own manager set),
// and it carries the resourceVersion read, so a concurrent edit is refused
// instead of overwritten.
func Restore(ctx context.Context, c client.Client, ns string, e *v1.Edition) error {
	var d v1.Dispatch
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: e.Spec.DispatchRef}, &d); err != nil {
		return err
	}
	s := e.Spec.Snapshot
	orNil := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": d.ResourceVersion},
		"spec": map[string]any{
			"title":     s.Title,
			"summary":   orNil(s.Summary),
			"cover":     orNil(string(s.Cover)),
			"scribeRef": orNil(s.ScribeRef),
			"content":   map[string]any{"format": orNil(string(s.Content.Format)), "body": orNil(s.Content.Body)},
		},
	})
	if err != nil {
		return err
	}
	return c.Patch(ctx, &d, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(FieldManager))
}
