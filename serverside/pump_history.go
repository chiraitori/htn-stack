package main

import (
	"encoding/json"
	"log"
)

const maxPumpHistoryEvents = 100

func (h *BackendLogicHook) recordPumpHistory(event PumpHistoryEvent) {
	h.mu.Lock()
	h.pumpHistory = append([]PumpHistoryEvent{event}, h.pumpHistory...)
	if len(h.pumpHistory) > maxPumpHistoryEvents {
		h.pumpHistory = h.pumpHistory[:maxPumpHistoryEvents]
	}
	h.mu.Unlock()

	payload, err := json.Marshal(event)
	if err != nil {
		log.Printf("[Backend] marshal pump history event failed: %v", err)
		return
	}

	if err := h.broker.Publish(topicPumpHistory, payload, false, 1); err != nil {
		log.Printf("[Backend] publish pump history event error: %v", err)
	}
	h.publishPumpHistoryState()
}

func (h *BackendLogicHook) publishPumpHistoryState() {
	h.mu.Lock()
	events := append([]PumpHistoryEvent(nil), h.pumpHistory...)
	h.mu.Unlock()

	payload, err := json.Marshal(PumpHistoryState{Events: events})
	if err != nil {
		log.Printf("[Backend] marshal pump history state failed: %v", err)
		return
	}

	if err := h.broker.Publish(topicPumpHistoryList, payload, true, 1); err != nil {
		log.Printf("[Backend] publish pump history state error: %v", err)
	}
}
