package tools

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestProcessEnvironmentPropagatesAcrossPrograms(t *testing.T) {
	if os.Getenv("SOL_PROCESS_HELPER") == "1" {
		if os.Getenv("SOL_DEPTH") != "2" || os.Getenv("SOL_SESSION") != "" {
			os.Exit(2)
		}
		os.Exit(0)
	}
	ctx := WithProcessEnvironment(context.Background(), []string{"SOL_PROCESS_HELPER=1", "SOL_DEPTH=2"})
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessEnvironmentPropagatesAcrossPrograms$")
	cmd.Env = processEnvironment(ctx)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("environment propagation: %v %s", err, output)
	}
}
