package sol

import (
	"reflect"
	"testing"

	"github.com/airlockrun/goai/message"
)

func TestCoalesceConsecutiveUser_PreservesMetadata(t *testing.T) {
	for _, index := range []int{0, 1} {
		t.Run([]string{"first", "second"}[index], func(t *testing.T) {
			msgs := []message.Message{message.NewUserMessage("one"), message.NewUserMessage("two")}
			msgs[index].ProviderOptions = map[string]any{"cacheControl": "ephemeral"}
			if got := coalesceConsecutiveUser(msgs); !reflect.DeepEqual(got, msgs) {
				t.Fatalf("metadata-bearing messages merged: %+v", got)
			}
		})
	}
}
