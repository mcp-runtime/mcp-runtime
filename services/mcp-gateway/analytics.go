package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"mcp-runtime/pkg/events"
	"mcp-runtime/pkg/serviceutil"
)

func (s *gatewayServer) startAnalyticsDispatcher() {
	if s.analyticsURL == "" {
		return
	}
	s.analyticsOnce.Do(func() {
		s.analyticsMu.Lock()
		if s.analyticsClosed {
			s.analyticsMu.Unlock()
			return
		}
		if s.analyticsQueue == nil {
			s.analyticsQueue = make(chan analyticsEvent, analyticsQueueSize)
		}
		queue := s.analyticsQueue
		s.analyticsWG.Add(analyticsWorkerCount)
		s.analyticsMu.Unlock()
		for i := 0; i < analyticsWorkerCount; i++ {
			go func() {
				defer s.analyticsWG.Done()
				for event := range queue {
					parentCtx := serviceutil.ContextWithTraceContext(context.Background(), event.TraceContext)
					ctx, cancel := context.WithTimeout(parentCtx, time.Duration(analyticsEmitTimeout)*time.Second)
					s.emit(ctx, event.Envelope)
					cancel()
				}
			}()
		}
	})
}

func (s *gatewayServer) stopAnalyticsDispatcher() {
	s.analyticsMu.Lock()
	if s.analyticsClosed {
		s.analyticsMu.Unlock()
		s.analyticsWG.Wait()
		return
	}
	s.analyticsClosed = true
	queue := s.analyticsQueue
	s.analyticsQueue = nil
	if queue != nil {
		close(queue)
	}
	s.analyticsMu.Unlock()
	s.analyticsWG.Wait()
}

func (s *gatewayServer) emitIfEnabled(ctx context.Context, event events.Envelope) {
	if s.analyticsURL == "" {
		return
	}
	s.analyticsMu.Lock()
	if s.analyticsClosed {
		s.analyticsMu.Unlock()
		dropped := s.analyticsDropped.Add(1)
		s.metrics.recordAnalyticsDrop()
		if shouldLogAnalyticsDrop(dropped) {
			log.Printf("gateway analytics dispatcher closed; dropped event total=%d source=%q event_type=%q", dropped, event.Source, event.EventType)
		}
		return
	}
	s.analyticsMu.Unlock()

	queue := s.analyticsEventQueue()
	if queue == nil {
		return
	}
	s.analyticsMu.Lock()
	item := analyticsEvent{
		Envelope:     event,
		TraceContext: serviceutil.CaptureTraceContext(ctx),
	}
	select {
	case queue <- item:
		s.analyticsMu.Unlock()
	default:
		s.analyticsMu.Unlock()
		dropped := s.analyticsDropped.Add(1)
		s.metrics.recordAnalyticsDrop()
		if shouldLogAnalyticsDrop(dropped) {
			log.Printf("gateway analytics queue full; dropped event total=%d source=%q event_type=%q", dropped, event.Source, event.EventType)
		}
	}
}

func (s *gatewayServer) analyticsEventQueue() chan analyticsEvent {
	s.analyticsMu.Lock()
	queue := s.analyticsQueue
	s.analyticsMu.Unlock()
	if queue != nil {
		return queue
	}

	if s.analyticsURL != "" {
		s.startAnalyticsDispatcher()
	}
	s.analyticsMu.Lock()
	defer s.analyticsMu.Unlock()
	if s.analyticsClosed {
		return nil
	}
	return s.analyticsQueue
}

func shouldLogAnalyticsDrop(dropped uint64) bool {
	if dropped == 1 {
		return true
	}
	return dropped != 0 && dropped&(dropped-1) == 0
}

// emit sends analytics events to the ingest service.
func (s *gatewayServer) emit(ctx context.Context, event events.Envelope) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.analyticsURL, bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("content-type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("x-api-key", s.apiKey)
	}
	if token, err := s.analyticsToken.Token(); err != nil {
		log.Printf("gateway analytics service account token unavailable: %v", err)
	} else if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.metrics.recordAnalyticsEmitFailure("transport")
		log.Printf("failed to emit gateway analytics event to %s: %v", s.analyticsURL, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := "rejected"
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			reason = "unauthorized"
		}
		s.metrics.recordAnalyticsEmitFailure(reason)
		log.Printf("gateway analytics emission failed with status %d to %s", resp.StatusCode, s.analyticsURL)
		_, _ = io.Copy(io.Discard, resp.Body)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}
