package channel

import (
	"testing"
	"time"

	"github.com/riverfjs/aevitas/internal/bus"
	"github.com/riverfjs/aevitas/internal/interaction"
	"github.com/riverfjs/aevitas/internal/protocol"
	sdklogger "github.com/riverfjs/agentsdk-go/pkg/logger"
)

func TestInteractionChannel_OnInboundEvent_HostReadyWithoutChatID(t *testing.T) {
	msgBus := bus.NewMessageBus(4)
	ch := &InteractionChannel{
		BaseChannel: NewBaseChannel(interactionChannelName, msgBus, nil, sdklogger.NewDefault()),
		routes:      map[string]interaction.OutboundAction{},
	}

	ch.onInboundEvent(interaction.InboundEvent{
		PluginID:  "feishuOfficial",
		Platform:  "feishu",
		ChatID:    "",
		Timestamp: time.Now().Format(time.RFC3339),
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventHostReady,
			"plugin_id":          "feishuOfficial",
			"platform":           "feishu",
		},
	})

	select {
	case in := <-msgBus.Inbound:
		if in.Channel != interactionChannelName {
			t.Fatalf("unexpected channel: %s", in.Channel)
		}
		if in.ChatID != "" {
			t.Fatalf("expected empty chat id for host_ready control event, got %q", in.ChatID)
		}
		if protocol.EventType(in.Metadata) != protocol.EventHostReady {
			t.Fatalf("expected host_ready event type, got %q", protocol.EventType(in.Metadata))
		}
	default:
		t.Fatal("expected host_ready control event forwarded to inbound bus")
	}
}

func TestInteractionChannel_OnInboundEvent_OutboundResultRequiresSessionChatID(t *testing.T) {
	msgBus := bus.NewMessageBus(4)
	ch := &InteractionChannel{
		BaseChannel: NewBaseChannel(interactionChannelName, msgBus, nil, sdklogger.NewDefault()),
		routes:      map[string]interaction.OutboundAction{},
	}

	ch.onInboundEvent(interaction.InboundEvent{
		PluginID:  "feishuOfficial",
		Platform:  "generic",
		ChatID:    "ou_123",
		Timestamp: time.Now().Format(time.RFC3339),
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventOutboundResult,
			"message_id":          "mid-1",
		},
	})

	select {
	case <-msgBus.Inbound:
		t.Fatal("control event without session_chat_id should be dropped")
	default:
	}

	ch.onInboundEvent(interaction.InboundEvent{
		PluginID:  "feishuOfficial",
		Platform:  "generic",
		ChatID:    "ou_123",
		Timestamp: time.Now().Format(time.RFC3339),
		Metadata: map[string]any{
			protocol.EventTypeKey:    protocol.EventOutboundResult,
			protocol.SessionChatIDKey: "feishuOfficial:feishu:ou_123",
			"message_id":             "mid-2",
		},
	})

	select {
	case in := <-msgBus.Inbound:
		if in.ChatID != "feishuOfficial:feishu:ou_123" {
			t.Fatalf("unexpected chat id: %q", in.ChatID)
		}
		if protocol.EventType(in.Metadata) != protocol.EventOutboundResult {
			t.Fatalf("unexpected event type: %q", protocol.EventType(in.Metadata))
		}
	default:
		t.Fatal("expected outbound_result with session_chat_id to be forwarded")
	}
}

func TestInteractionChannel_OnInboundEvent_ToolCatalogWithoutSessionChatID(t *testing.T) {
	msgBus := bus.NewMessageBus(4)
	ch := &InteractionChannel{
		BaseChannel: NewBaseChannel(interactionChannelName, msgBus, nil, sdklogger.NewDefault()),
		routes:      map[string]interaction.OutboundAction{},
	}

	ch.onInboundEvent(interaction.InboundEvent{
		PluginID:  "feishuOfficial",
		Platform:  "feishu",
		ChatID:    "",
		Timestamp: time.Now().Format(time.RFC3339),
		Metadata: map[string]any{
			protocol.EventTypeKey: protocol.EventToolCatalog,
			"plugin_id":          "feishuOfficial",
			"platform":           "feishu",
			"tools":              []any{},
		},
	})

	select {
	case in := <-msgBus.Inbound:
		if in.ChatID != "" {
			t.Fatalf("tool_catalog should not require session chat id, got %q", in.ChatID)
		}
		if protocol.EventType(in.Metadata) != protocol.EventToolCatalog {
			t.Fatalf("unexpected event type: %q", protocol.EventType(in.Metadata))
		}
	default:
		t.Fatal("expected tool_catalog control event forwarded to inbound bus")
	}
}

func TestInteractionChannel_BuildOutboundAction_UsesMetadataRequestID(t *testing.T) {
	msgBus := bus.NewMessageBus(1)
	ch := &InteractionChannel{
		BaseChannel: NewBaseChannel(interactionChannelName, msgBus, nil, sdklogger.NewDefault()),
		routes:      map[string]interaction.OutboundAction{},
	}
	msg := bus.OutboundMessage{
		Channel: interactionChannelName,
		ChatID:  "feishuOfficial:feishu:ou_1",
		Content: "hello",
		Metadata: map[string]any{
			"request_id": "gw-123",
		},
	}
	action := ch.buildOutboundAction(msg)
	if action.RequestID != "gw-123" {
		t.Fatalf("expected request id propagated from metadata, got %q", action.RequestID)
	}
}
