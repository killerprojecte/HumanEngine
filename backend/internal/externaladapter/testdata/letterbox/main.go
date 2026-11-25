//go:build wasip1

package main

import (
	"encoding/json"

	"github.com/killerprojecte/HumanEngine/backend/plugins/abi"
)

//go:wasmexport alloc
func alloc(size uint32) uint32 { return abi.Alloc(size) }

//go:wasmexport free
func free(ptr uint32) { abi.Free(ptr) }

//go:wasmexport plugin_call
func call(ptr, size uint32) uint64 { return abi.Invoke(ptr, size, invoke) }
func main()                        {}

func invoke(raw []byte) any {
	var envelope struct {
		Operation string          `json:"operation"`
		Input     json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	switch envelope.Operation {
	case "adapterProtocol":
		return map[string]string{"wire": "signed-json-http-1"}
	case "adapterIngress":
		var value struct {
			Letter  string
			Box     string
			Writer  string
			Payload string
			Stamp   string
			Recall  bool
		}
		if json.Unmarshal(envelope.Input, &value) != nil {
			return nil
		}
		if value.Recall {
			return map[string]any{"type": "recall", "peer": map[string]string{"kind": "group", "id": value.Box}, "message": value.Letter}
		}
		return map[string]any{
			"type": "message", "peer": map[string]string{"kind": "group", "id": value.Box},
			"message": value.Letter, "sender": value.Writer, "text": value.Payload, "sentAt": value.Stamp,
			"origin": "simulation", "sceneId": "ob:s:spoof", "receivedAt": "1900-01-01T00:00:00Z",
		}
	case "adapterSend":
		var value struct {
			Peer struct {
				ID string `json:"id"`
			} `json:"peer"`
			Parts   []json.RawMessage `json:"parts"`
			ReplyTo string            `json:"replyTo"`
		}
		if json.Unmarshal(envelope.Input, &value) != nil {
			return nil
		}
		return map[string]any{"box": value.Peer.ID, "inReplyTo": value.ReplyTo, "contents": value.Parts}
	case "adapterReceipt":
		var ack struct{ Ticket string }
		if json.Unmarshal(envelope.Input, &ack) != nil {
			return nil
		}
		return map[string]string{"message": ack.Ticket}
	}
	return nil
}
