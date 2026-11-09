package providertransport

import "github.com/killerprojecte/HumanEngine/backend/internal/plugins"

// Protocols mirrors the source registry's builtin-first order and snapshots the
// live V1 registrations. Generation/handles and credentials are never exposed.
func (m *NativeModels) Protocols() []plugins.ModelProtocolMetadata {
	rows := []plugins.ModelProtocolMetadata{
		{ID: "openai-chat", Label: "OpenAI Chat Completions", Operations: []string{"complete", "embeddings", "listModels"}},
		{ID: "openai-responses", Label: "OpenAI Responses", Operations: []string{"complete", "embeddings", "listModels"}},
	}
	if m.Host != nil {
		rows = append(rows, m.Host.RegisteredModelProtocols()...)
	}
	return rows
}
