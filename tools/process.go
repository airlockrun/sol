package tools

import "context"

type processEnvironmentKey struct{}

// WithProcessEnvironment supplies an explicit environment for launched programs.
// In-process child agents inherit this context along with the execution policy.
func WithProcessEnvironment(ctx context.Context, env []string) context.Context {
	return context.WithValue(ctx, processEnvironmentKey{}, append([]string(nil), env...))
}

func processEnvironment(ctx context.Context) []string {
	env, _ := ctx.Value(processEnvironmentKey{}).([]string)
	return env
}
