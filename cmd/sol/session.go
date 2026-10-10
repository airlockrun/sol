package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/airlockrun/sol/localconfig"
)

func runSessionCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return errors.New("usage: sol session new|list|show ID|compact [ID]")
	}
	root, err := localconfig.SessionPath()
	if err != nil {
		return err
	}
	id := ""
	if len(args) == 2 {
		id = args[1]
	}
	switch args[0] {
	case "new":
		if id != "" {
			return errors.New("usage: sol session new")
		}
		s, err := localconfig.OpenSession(ctx, root, "", true)
		if err != nil {
			return err
		}
		defer s.Close()
		fmt.Fprintln(os.Stdout, s.Record.ID)
		return nil
	case "list":
		if id != "" {
			return errors.New("usage: sol session list")
		}
		records, err := localconfig.ListSessions(root)
		if err != nil {
			return err
		}
		for _, r := range records {
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n", r.ID, r.Updated.Format("2006-01-02 15:04:05"), r.Agent, r.Title)
		}
		return nil
	case "show":
		if id == "" {
			return errors.New("usage: sol session show ID")
		}
		s, err := localconfig.OpenSession(ctx, root, id, false)
		if err != nil {
			return err
		}
		defer s.Close()
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(s.Record)
	case "compact":
		return executeBatch(ctx, "", id, "", "", "", "", false, "", false, true)
	default:
		return errors.New("usage: sol session new|list|show ID|compact [ID]")
	}
}
