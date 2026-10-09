package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
)

func localStore() (*localconfig.FileStore, error) {
	path, err := localconfig.DefaultPath()
	if err != nil {
		return nil, err
	}
	return localconfig.NewFileStore(path)
}

func runConfig(ctx context.Context, args []string, output io.Writer, store localconfig.Store) error {
	if len(args) == 0 || args[0] != "model" {
		return errors.New("usage: sol config model [--auth api-key|codex] [PROVIDER/MODEL]; sol config model --clear")
	}
	flags := flag.NewFlagSet("config model", flag.ContinueOnError)
	// Do not include untrusted arguments in diagnostics; configuration has no secret output.
	flags.SetOutput(io.Discard)
	auth := flags.String("auth", localconfig.APIKeyMode, "authentication method")
	clear := flags.Bool("clear", false, "clear default")
	if err := flags.Parse(args[1:]); err != nil {
		return errors.New("invalid config model arguments")
	}
	authSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "auth" {
			authSet = true
		}
	})
	if flags.NArg() > 1 || *clear && (flags.NArg() != 0 || authSet) {
		return errors.New("invalid config model arguments")
	}
	if *clear {
		if _, err := store.Update(ctx, func(config *localconfig.Config) error { config.DefaultModel = nil; return nil }); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "Default model cleared.")
		return err
	}
	if flags.NArg() == 0 {
		if authSet {
			return errors.New("setting authentication requires a model")
		}
		config, err := store.Load(ctx)
		if err != nil {
			return err
		}
		if config.DefaultModel == nil {
			_, err = fmt.Fprintln(output, "No default model configured.")
			return err
		}
		_, err = fmt.Fprintf(output, "Default model: %s (auth: %s)\n", config.DefaultModel.Model, config.DefaultModel.Auth)
		return err
	}
	model := flags.Arg(0)
	selection := localconfig.ModelSelection{Model: model, Auth: *auth}
	if err := selection.Validate(); err != nil {
		return err
	}
	id, modelID := provider.ParseModel(model)
	if err := validateAuthMode(*auth, id, modelID, "", "", true); err != nil {
		return err
	}
	if _, err := store.Update(ctx, func(config *localconfig.Config) error { config.DefaultModel = &selection; return nil }); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "Default model: %s (auth: %s)\n", selection.Model, selection.Auth)
	return err
}

// An explicit model starts in API-key mode. Only an explicit auth flag or the
// complete stored default selection may choose Codex.
func selectModel(config localconfig.Config, model, auth string, explicitModel, explicitAuth bool) (localconfig.ModelSelection, error) {
	selection := localconfig.ModelSelection{Model: "openai/gpt-4o", Auth: localconfig.APIKeyMode}
	if !explicitModel && config.DefaultModel != nil {
		selection = *config.DefaultModel
	}
	if explicitModel {
		id, modelID := provider.ParseModel(model)
		selection = localconfig.ModelSelection{Model: id + "/" + modelID, Auth: localconfig.APIKeyMode}
	}
	if explicitAuth {
		selection.Auth = auth
	}
	if err := selection.Validate(); err != nil {
		return localconfig.ModelSelection{}, err
	}
	id, modelID := provider.ParseModel(selection.Model)
	if err := validateAuthMode(selection.Auth, id, modelID, "", "", explicitModel || config.DefaultModel != nil); err != nil {
		return localconfig.ModelSelection{}, err
	}
	return selection, nil
}
