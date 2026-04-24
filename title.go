package sol

import (
	"context"
	"fmt"
	"strings"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/provider"
	"github.com/airlockrun/goai/provider/openai"
	"github.com/airlockrun/goai/stream"
)

// TitleResult holds the generated title and any error
type TitleResult struct {
	Title string
	Error error
}

// GenerateTitleAsync generates a title for the conversation asynchronously.
// It returns a channel that will receive the result.
func GenerateTitleAsync(ctx context.Context, userPrompt string, titleModel, apiKey, baseURL string) <-chan TitleResult {
	resultChan := make(chan TitleResult, 1)

	go func() {
		defer close(resultChan)

		title, err := generateTitle(ctx, userPrompt, titleModel, apiKey, baseURL)
		resultChan <- TitleResult{Title: title, Error: err}
	}()

	return resultChan
}

// generateTitle generates a title synchronously
func generateTitle(ctx context.Context, userPrompt string, titleModel, apiKey, baseURL string) (string, error) {
	p := openai.New(provider.Options{
		APIKey:  apiKey,
		BaseURL: baseURL,
	})
	model := p.Model(titleModel)

	messages := []goai.Message{
		goai.NewSystemMessage(TitlePrompt()),
		goai.NewUserMessage("Generate a title for this conversation:\n"),
		goai.NewUserMessage(fmt.Sprintf("%q\n", userPrompt)),
	}

	maxTokens := 32000
	result, err := goai.StreamText(ctx, stream.Input{
		Model:           model,
		Messages:        messages,
		MaxOutputTokens: &maxTokens,
		ProviderOptions: map[string]any{
			"systemMessageMode": "developer",
			"reasoningEffort":   "minimal",
		},
	})
	if err != nil {
		return "", fmt.Errorf("title generation error: %w", err)
	}

	var textBuilder strings.Builder
	for event := range result.FullStream {
		switch e := event.Data.(type) {
		case stream.TextDeltaEvent:
			textBuilder.WriteString(e.Text)
		case stream.ErrorEvent:
			return "", fmt.Errorf("title generation stream error: %w", e.Error)
		}
	}

	title := strings.TrimSpace(textBuilder.String())

	if len(title) > 50 {
		title = title[:50]
	}

	return title, nil
}
