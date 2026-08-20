// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	user_model "github.com/hanzoai/git/models/user"

	"github.com/urfave/cli/v3"
)

func newUserListAccessTokensCommand() *cli.Command {
	return &cli.Command{
		Name:  "list-access-tokens",
		Usage: "List the access tokens of a specific user",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "username",
				Aliases: []string{"u"},
				Usage:   "Username",
			},
		},
		Action: runListAccessTokens,
	}
}

func runListAccessTokens(ctx context.Context, c *cli.Command) error {
	if !c.IsSet("username") {
		return errors.New("you must provide a username to list the tokens of")
	}

	if err := initDB(ctx); err != nil {
		return err
	}

	user, err := user_model.GetUserByName(ctx, c.String("username"))
	if err != nil {
		return err
	}

	// UserID is required by ListAccessTokensOptions.ToConds, so one user's
	// tokens are the widest set this can ever return.
	tokens, err := db.Find[auth_model.AccessToken](ctx, auth_model.ListAccessTokensOptions{UserID: user.ID})
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 5, 0, 1, ' ', 0)
	fmt.Fprint(w, "ID\tName\tScope\tCreated\tLastUsed\n")
	for _, t := range tokens {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Scope,
			t.CreatedUnix.FormatDate(), t.UpdatedUnix.FormatDate())
	}
	return w.Flush()
}
