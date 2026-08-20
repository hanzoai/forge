// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	user_model "github.com/hanzoai/git/models/user"

	"github.com/urfave/cli/v3"
)

func newUserDeleteAccessTokenCommand() *cli.Command {
	return &cli.Command{
		Name:  "delete-access-token",
		Usage: "Delete an access token of a specific user",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "username",
				Aliases: []string{"u"},
				Usage:   "Username",
			},
			&cli.StringFlag{
				Name:    "token-name",
				Aliases: []string{"t"},
				Usage:   "Token name, as shown by list-access-tokens",
			},
			&cli.Int64Flag{
				Name:  "id",
				Usage: "Token id, as shown by list-access-tokens",
			},
		},
		Action: runDeleteAccessToken,
	}
}

func runDeleteAccessToken(ctx context.Context, c *cli.Command) error {
	if !c.IsSet("username") {
		return errors.New("you must provide a username to delete a token of")
	}
	if c.IsSet("token-name") == c.IsSet("id") {
		return errors.New("you must provide exactly one of --token-name or --id")
	}

	if err := initDB(ctx); err != nil {
		return err
	}

	user, err := user_model.GetUserByName(ctx, c.String("username"))
	if err != nil {
		return err
	}

	id := c.Int64("id")
	if c.IsSet("token-name") {
		// Name is unique per user, so this resolves to at most one token.
		tokens, err := db.Find[auth_model.AccessToken](ctx, auth_model.ListAccessTokensOptions{
			Name:   c.String("token-name"),
			UserID: user.ID,
		})
		if err != nil {
			return err
		}
		if len(tokens) == 0 {
			return fmt.Errorf("user %q has no access token named %q", user.Name, c.String("token-name"))
		}
		id = tokens[0].ID
	}

	// Scoped by user id as well as token id, so an id belonging to another
	// user cannot be deleted through this command.
	if err := auth_model.DeleteAccessTokenByID(ctx, id, user.ID); err != nil {
		return err
	}

	fmt.Printf("Access token %d of user %s was deleted\n", id, user.Name)
	return nil
}
