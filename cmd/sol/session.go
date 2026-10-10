package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/airlockrun/sol/localconfig"
)

func runSessionCommand(ctx context.Context, args []string, selector string) error {
	if len(args) == 0 || len(args) > 2 {
		return errors.New("usage: sol --session new|list|show ID|compact [ID]")
	}
	root, err := localconfig.SessionPath()
	if err != nil {
		return err
	}
	id := ""
	if len(args) == 2 {
		id = args[1]
		if id == "" || selector != "" {
			return errors.New("provide one nonempty explicit session ID, either --sid or the session action ID")
		}
	}
	if selector != "" {
		if args[0] != "compact" {
			return errors.New("--sid is only supported with --session compact")
		}
		id = selector
	}
	switch args[0] {
	case "new":
		if id != "" {
			return errors.New("usage: sol --session new")
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
			return errors.New("usage: sol --session list")
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
			return errors.New("usage: sol --session show ID")
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
		return errors.New("usage: sol --session new|list|show ID|compact [ID]")
	}
}

func clearSession(ctx context.Context, explicit string) error {
	id, err := selectedSession(explicit)
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("clear requires --sid ID or SOL_SESSION")
	}
	root, err := localconfig.SessionPath()
	if err != nil {
		return err
	}
	s, err := localconfig.OpenSession(ctx, root, id, false)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Clear(ctx); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Cleared context for session %s; history retained.\n", id)
	return nil
}
