package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const maxPumpHistoryEvents = 100

func (h *BackendLogicHook) recordPumpHistory(event PumpHistoryEvent) {
	h.mu.Lock()
	h.pumpHistory = append([]PumpHistoryEvent{event}, h.pumpHistory...)
	if len(h.pumpHistory) > maxPumpHistoryEvents {
		h.pumpHistory = h.pumpHistory[:maxPumpHistoryEvents]
	}
	h.mu.Unlock()

	if err := h.savePumpHistoryEvent(event); err != nil {
		log.Printf("[MongoDB] save pump history failed: %v", err)
	}

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

func (h *BackendLogicHook) savePumpHistoryEvent(event PumpHistoryEvent) error {
	if h.history == nil {
		return nil
	}

	createdAt := time.Now().UTC()
	if parsedAt, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
		createdAt = parsedAt.UTC()
	}

	doc := bson.M{
		"type":       "pump_event",
		"created_at": createdAt,
		"pump_event": bson.M{
			"timestamp": event.Timestamp,
			"state":     event.State,
			"source":    event.Source,
			"client_id": event.ClientID,
			"reason":    event.Reason,
			"manual":    event.Manual,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := h.history.InsertOne(ctx, doc)
	return err
}

func (h *BackendLogicHook) loadPumpHistory() {
	if h.history == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cursor, err := h.history.Find(
		ctx,
		bson.M{"type": "pump_event"},
		options.Find().SetSort(bson.M{"created_at": -1}).SetLimit(maxPumpHistoryEvents),
	)
	if err != nil {
		log.Printf("[MongoDB] load pump history failed: %v", err)
		return
	}
	defer cursor.Close(ctx)

	events := make([]PumpHistoryEvent, 0, maxPumpHistoryEvents)
	for cursor.Next(ctx) {
		var doc struct {
			PumpEvent PumpHistoryEvent `bson:"pump_event"`
		}
		if err := cursor.Decode(&doc); err != nil {
			log.Printf("[MongoDB] decode pump history failed: %v", err)
			continue
		}
		if doc.PumpEvent.Timestamp == "" {
			continue
		}
		events = append(events, doc.PumpEvent)
	}
	if err := cursor.Err(); err != nil {
		log.Printf("[MongoDB] read pump history failed: %v", err)
		return
	}

	h.mu.Lock()
	h.pumpHistory = events
	h.mu.Unlock()
	log.Printf("[MongoDB] loaded %d pump history events", len(events))
}
