package provider

import (
	"reflect"
	"testing"
)

func TestCapabilitiesFromModel(t *testing.T) {
	tests := []struct {
		name string
		in   ModelInfo
		want CapabilitySet
	}{
		{
			name: "nil modalities",
			in:   ModelInfo{ID: "x"},
			want: CapabilitySet{},
		},
		{
			name: "text only",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"text"},
				Output: []string{"text"},
			}},
			want: CapabilitySet{Text: true},
		},
		{
			name: "vision model — image + text in, text out",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"text", "image"},
				Output: []string{"text"},
			}},
			want: CapabilitySet{Text: true, Vision: true},
		},
		{
			name: "multimodal — image + audio + text in, text + audio out",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"text", "image", "audio"},
				Output: []string{"text", "audio"},
			}},
			want: CapabilitySet{Text: true, Vision: true, Speech: true, Transcription: true},
		},
		{
			name: "pure STT — audio in, text out",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"audio"},
				Output: []string{"text"},
			}},
			want: CapabilitySet{Transcription: true},
		},
		{
			name: "pure TTS — text in, audio out",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"text"},
				Output: []string{"audio"},
			}},
			want: CapabilitySet{Speech: true},
		},
		{
			name: "image gen — text in, image out",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"text"},
				Output: []string{"image"},
			}},
			want: CapabilitySet{ImageGen: true},
		},
		{
			name: "model level never sets Search",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"text"},
				Output: []string{"text"},
			}},
			want: CapabilitySet{Text: true},
		},

		// Kind-derived cases: a goai-supplied Kind sets the canonical flag
		// regardless of modalities. Empty modalities (typical for
		// embedding/reranking models on models.dev) used to leave the set
		// blank; now Kind alone classifies.
		{
			name: "kind=embedding (empty modalities)",
			in:   ModelInfo{ID: "text-embedding-3-small", Kind: KindEmbedding},
			want: CapabilitySet{Embedding: true},
		},
		{
			name: "kind=reranking (empty modalities)",
			in:   ModelInfo{ID: "rerank-3", Kind: KindReranking},
			want: CapabilitySet{Reranking: true},
		},
		{
			name: "kind=transcription with audio→text modalities",
			in: ModelInfo{
				ID:   "whisper-1",
				Kind: KindTranscription,
				Modalities: &ModelModalities{
					Input: []string{"audio"}, Output: []string{"text"},
				},
			},
			want: CapabilitySet{Transcription: true},
		},
		{
			name: "kind=speech with text→audio modalities",
			in: ModelInfo{
				ID:   "tts-1",
				Kind: KindSpeech,
				Modalities: &ModelModalities{
					Input: []string{"text"}, Output: []string{"audio"},
				},
			},
			want: CapabilitySet{Speech: true},
		},
		{
			name: "kind=image with text→image modalities",
			in: ModelInfo{
				ID:   "dall-e-3",
				Kind: KindImage,
				Modalities: &ModelModalities{
					Input: []string{"text"}, Output: []string{"image"},
				},
			},
			want: CapabilitySet{ImageGen: true},
		},
		{
			name: "kind=language preserves vision modality flag",
			in: ModelInfo{
				ID:   "gpt-4o",
				Kind: KindLanguage,
				Modalities: &ModelModalities{
					Input: []string{"text", "image"}, Output: []string{"text"},
				},
			},
			want: CapabilitySet{Text: true, Vision: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CapabilitiesFromModel(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CapabilitiesFromModel() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestProviderCapabilities(t *testing.T) {
	textModel := ModelInfo{ID: "text-only", Modalities: &ModelModalities{
		Input: []string{"text"}, Output: []string{"text"},
	}}
	visionModel := ModelInfo{ID: "vision", Modalities: &ModelModalities{
		Input: []string{"text", "image"}, Output: []string{"text"},
	}}
	imgGenModel := ModelInfo{ID: "image-gen", Modalities: &ModelModalities{
		Input: []string{"text"}, Output: []string{"image"},
	}}

	tests := []struct {
		name   string
		p      *ModelsDevProvider
		extras []string
		want   CapabilitySet
	}{
		{
			name:   "nil provider + search extra",
			p:      nil,
			extras: []string{CapSearch},
			want:   CapabilitySet{Search: true},
		},
		{
			name: "single text model",
			p: &ModelsDevProvider{Models: map[string]ModelInfo{
				"a": textModel,
			}},
			want: CapabilitySet{Text: true},
		},
		{
			name: "multi-model union",
			p: &ModelsDevProvider{Models: map[string]ModelInfo{
				"a": textModel,
				"b": visionModel,
				"c": imgGenModel,
			}},
			want: CapabilitySet{Text: true, Vision: true, ImageGen: true},
		},
		{
			name: "model union + search overlay",
			p: &ModelsDevProvider{Models: map[string]ModelInfo{
				"a": visionModel, // has text+image in, text out → text + vision
			}},
			extras: []string{CapSearch},
			want:   CapabilitySet{Text: true, Vision: true, Search: true},
		},
		{
			name:   "empty provider + no extras = empty set",
			p:      &ModelsDevProvider{Models: map[string]ModelInfo{}},
			extras: nil,
			want:   CapabilitySet{},
		},
		{
			name: "extras override missing modalities",
			p:    &ModelsDevProvider{Models: map[string]ModelInfo{}},
			// Overlay can, in theory, declare any capability. We don't use
			// this for text/vision/etc. today, but the OR semantics must
			// hold so the field is unambiguous.
			extras: []string{CapText, CapSearch},
			want:   CapabilitySet{Text: true, Search: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProviderCapabilities(tt.p, tt.extras)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ProviderCapabilities() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCapabilitySetList(t *testing.T) {
	tests := []struct {
		name string
		in   CapabilitySet
		want []string
	}{
		{"empty", CapabilitySet{}, []string{}},
		{"text only", CapabilitySet{Text: true}, []string{CapText}},
		{
			"everything",
			CapabilitySet{
				Text: true, Vision: true, ImageGen: true, Search: true,
				Embedding: true, Speech: true, Transcription: true, Reranking: true,
			},
			[]string{CapText, CapVision, CapTranscription, CapSpeech, CapImageGen, CapSearch, CapEmbedding, CapReranking},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.List()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("List() = %v, want %v", got, tt.want)
			}
		})
	}
}
