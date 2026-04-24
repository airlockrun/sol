package provider

// Capability constants. These are the strings exposed over the API and used
// as keys in the UI capability matrix. Keep them lowercase snake_case.
const (
	CapText     = "text"
	CapVision   = "vision"
	CapSTT      = "stt"
	CapTTS      = "tts"
	CapImageGen = "image_gen"
	CapSearch   = "search"
)

// CapabilitySet is the set of high-level capabilities a model or provider
// offers. Derived from models.dev modalities plus the overlay.
type CapabilitySet struct {
	Text     bool
	Vision   bool
	STT      bool
	TTS      bool
	ImageGen bool
	Search   bool
}

// List returns the set as a sorted slice of capability strings in the
// canonical UI order.
func (c CapabilitySet) List() []string {
	out := make([]string, 0, 6)
	if c.Text {
		out = append(out, CapText)
	}
	if c.Vision {
		out = append(out, CapVision)
	}
	if c.STT {
		out = append(out, CapSTT)
	}
	if c.TTS {
		out = append(out, CapTTS)
	}
	if c.ImageGen {
		out = append(out, CapImageGen)
	}
	if c.Search {
		out = append(out, CapSearch)
	}
	return out
}

// CapabilitiesFromModel derives the capability set for a single model from
// its modality list. Search is never set at the model level — it's a
// provider capability (see ProviderCapabilities).
func CapabilitiesFromModel(m ModelInfo) CapabilitySet {
	var cs CapabilitySet
	if m.Modalities == nil {
		return cs
	}
	inText := containsStr(m.Modalities.Input, "text")
	outText := containsStr(m.Modalities.Output, "text")
	inImage := containsStr(m.Modalities.Input, "image")
	outImage := containsStr(m.Modalities.Output, "image")
	inAudio := containsStr(m.Modalities.Input, "audio")
	outAudio := containsStr(m.Modalities.Output, "audio")

	if inText && outText {
		cs.Text = true
	}
	if inImage && outText {
		cs.Vision = true
	}
	if inAudio && outText {
		cs.STT = true
	}
	if inText && outAudio {
		cs.TTS = true
	}
	if inText && outImage {
		cs.ImageGen = true
	}
	return cs
}

// ProviderCapabilities unions ModelCapabilities across every model in the
// provider and then ORs in extras (capabilities that aren't derivable from
// any single model's modalities, such as "search"). Callers typically pass
// Overlay[providerID].ExtraCapabilities as extras.
//
// It's valid for a provider to have no models — e.g. brave, which is only
// represented via Overlay. In that case the result is whatever the extras
// declare.
func ProviderCapabilities(p *ModelsDevProvider, extras []string) CapabilitySet {
	var cs CapabilitySet
	if p != nil {
		for _, m := range p.Models {
			mc := CapabilitiesFromModel(m)
			cs.Text = cs.Text || mc.Text
			cs.Vision = cs.Vision || mc.Vision
			cs.STT = cs.STT || mc.STT
			cs.TTS = cs.TTS || mc.TTS
			cs.ImageGen = cs.ImageGen || mc.ImageGen
		}
	}
	for _, cap := range extras {
		switch cap {
		case CapText:
			cs.Text = true
		case CapVision:
			cs.Vision = true
		case CapSTT:
			cs.STT = true
		case CapTTS:
			cs.TTS = true
		case CapImageGen:
			cs.ImageGen = true
		case CapSearch:
			cs.Search = true
		}
	}
	return cs
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
