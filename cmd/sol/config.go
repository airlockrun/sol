package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/airlockrun/sol/agent"
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
	if len(args) > 0 && args[0] == "agent" {
		return runAgentConfig(ctx, args[1:], output, store)
	}
	if len(args) < 1 || args[0] != "model" || len(args) > 2 {
		return errors.New("usage: sol config model [slug/provider/model | --clear]")
	}
	if len(args) == 1 {
		config, err := store.Load(ctx)
		if err != nil {
			return err
		}
		if config.DefaultModel == "" {
			_, err = fmt.Fprintln(output, "No default model configured.")
		} else {
			_, err = fmt.Fprintln(output, "Default model:", config.DefaultModel)
		}
		return err
	}
	value := args[1]
	if value == "--clear" {
		value = ""
	} else {
		ref, err := localconfig.ParseModelRef(value)
		if err != nil {
			return err
		}
		if err := provider.ValidateLocalEntry(ref.Entry); err != nil {
			return err
		}
		if _, ok := provider.GetModelInfo(ref.Provider, ref.Model); !ok {
			return errors.New("default model is not in the provider catalog")
		}
	}
	_, err := store.Update(ctx, func(config *localconfig.Config) error {
		if value != "" {
			if _, _, err := config.Resolve(value); err != nil {
				return err
			}
		}
		config.DefaultModel = value
		return nil
	})
	if err != nil {
		return err
	}
	if value == "" {
		_, err = fmt.Fprintln(output, "Default model cleared.")
	} else {
		_, err = fmt.Fprintln(output, "Default model:", value)
	}
	return err
}

func runAgentConfig(ctx context.Context, args []string, output io.Writer, store localconfig.Store) error {
	if len(args) > 1 {
		return errors.New("usage: sol config agent [name | --clear]")
	}
	if len(args) == 0 {
		c, err := store.Load(ctx)
		if err != nil {
			return err
		}
		name := c.DefaultAgent
		if name == "" {
			name = "build"
		}
		_, err = fmt.Fprintln(output, "Default agent:", name)
		return err
	}
	name := args[0]
	if name == "--clear" {
		name = ""
	} else if _, ok := agent.Get(name, ""); !ok {
		return fmt.Errorf("unknown agent %q; available: %v", name, agent.List())
	}
	_, err := store.Update(ctx, func(c *localconfig.Config) error { c.DefaultAgent = name; return nil })
	if err != nil {
		return err
	}
	if name == "" {
		name = "build"
	}
	_, err = fmt.Fprintln(output, "Default agent:", name)
	return err
}
func selectModel(config localconfig.Config, model string) (localconfig.ModelRef, localconfig.ProviderConfig, error) {
	if model == "" {
		model = config.DefaultModel
	}
	if model == "" {
		return localconfig.ModelRef{}, localconfig.ProviderConfig{}, errors.New("no model selected; use -model slug/provider/model or sol config model slug/provider/model")
	}
	return config.Resolve(model)
}
