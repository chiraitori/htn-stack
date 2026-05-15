package main

import (
	"fmt"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

func (h *BackendLogicHook) ID() string {
	return "backend-logic-hook"
}

func (h *BackendLogicHook) Init(config any) error {
	deps, ok := config.(*hookDeps)
	if !ok || deps == nil || deps.Broker == nil || deps.HTTPClient == nil {
		return fmt.Errorf("invalid hook config")
	}

	h.broker = deps.Broker
	h.cfg = deps.Config
	h.httpClient = deps.HTTPClient
	h.history = deps.History
	return nil
}

func (h *BackendLogicHook) Provides(b byte) bool {
	return b == mqtt.OnPublished
}

func (h *BackendLogicHook) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	switch pk.TopicName {
	case topicSensorData:
		payloadCopy := append([]byte(nil), pk.Payload...)
		go h.processSensorMessage(cl.ID, payloadCopy)
	case topicPumpControl:
		h.processPumpControlMessage(cl.ID, pk.Payload)
	case topicGardenConfigSet:
		h.processGardenConfigSetMessage(cl.ID, pk.Payload)
	case topicGardenConfigGet:
		h.publishGardenConfigState("request")
	case topicGardenConfig:
		return
	case topicGardenRecommend:
		h.processGardenConfigRecommendMessage(cl.ID, pk.Payload)
	default:
		return
	}
}
