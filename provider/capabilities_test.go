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
			want: CapabilitySet{Text: true, Vision: true, STT: true, TTS: true},
		},
		{
			name: "pure STT — audio in, text out",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"audio"},
				Output: []string{"text"},
			}},
			want: CapabilitySet{STT: true},
		},
		{
			name: "pure TTS — text in, audio out",
			in: ModelInfo{Modalities: &ModelModalities{
				Input:  []string{"text"},
				Output: []string{"audio"},
			}},
			want: CapabilitySet{TTS: true},
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
			CapabilitySet{Text: true, Vision: true, STT: true, TTS: true, ImageGen: true, Search: true},
			[]string{CapText, CapVision, CapSTT, CapTTS, CapImageGen, CapSearch},
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
