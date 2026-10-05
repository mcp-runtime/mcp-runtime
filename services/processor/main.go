package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	_ "go.uber.org/automaxprocs" // align GOMAXPROCS with container CPU quota

	clickhousepkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/events"
	"mcp-runtime/pkg/serviceutil"
)

var (
	processorIntakePaused = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "processor_intake_paused",
		Help: "Whether Kafka intake is paused because the pending ClickHouse batch is full.",
	})
	processorIntakePauseTransitions = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "processor_intake_pause_transitions_total",
		Help: "Total number of times Kafka intake entered the paused state.",
	})
)

func init() {
	prometheus.MustRegister(processorIntakePaused, processorIntakePauseTransitions)
}

// main initializes and starts the MCP Runtime Processor service.
// It sets up Kafka consumer connection, ClickHouse database connection,
// configures batch processing parameters, initializes tracing,
// and starts consuming events from Kafka to insert into ClickHouse.
func main() {
	brokers := strings.Split(serviceutil.EnvOr("KAFKA_BROKERS", "kafka:9092"), ",")
	topic := serviceutil.EnvOr("KAFKA_TOPIC", "mcp.events")
	groupID := serviceutil.EnvOr("KAFKA_GROUP", "mcp-processor")
	metricsPort := serviceutil.EnvOr("METRICS_PORT", "9102")

	clickhouseAddr := serviceutil.EnvOr("CLICKHOUSE_ADDR", "clickhouse:9000")
	dbName := serviceutil.EnvOr("CLICKHOUSE_DB", "mcp")
	if err := clickhousepkg.ValidateDBName(dbName); err != nil {
		log.Fatalf("invalid CLICKHOUSE_DB: %v", err)
	}

	batchSize := serviceutil.EnvInt("BATCH_SIZE", 500)
	flushInterval := serviceutil.EnvDuration("FLUSH_INTERVAL", 2*time.Second)
	if batchSize <= 0 {
		log.Printf("invalid BATCH_SIZE=%d; using default 500", batchSize)
		batchSize = 500
	}
	if flushInterval <= 0 {
		log.Printf("invalid FLUSH_INTERVAL=%s; using default 2s", flushInterval)
		flushInterval = 2 * time.Second
	}

	clickhouseClient, err := clickhousepkg.NewClient(clickhousepkg.Config{
		Addr:        clickhouseAddr,
		Database:    dbName,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("failed to connect to clickhouse: %v", err)
	}
	defer clickhouseClient.Close()

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  groupID,
		MinBytes: 1,
		MaxBytes: 10e6,
	})
	defer reader.Close()

	metricsShutdown, metricsErrs := serviceutil.StartMetricsServer(metricsPort)
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = metricsShutdown(shutdownCtx)
	}()
	go func() {
		if err, ok := <-metricsErrs; ok {
			log.Printf("metrics server stopped: %v", err)
		}
	}()

	shutdown, err := serviceutil.InitTracer("mcp-processor")
	if err != nil {
		log.Printf("otel init failed: %v", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(ctx)
		}()
	}

	log.Printf("mcp-processor started")
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	tracer := otel.Tracer("mcp-processor")

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	batch := make([]events.Envelope, 0, batchSize)
	batchMessages := make([]kafka.Message, 0, batchSize)
	batchSpanContexts := make([]trace.SpanContext, 0, batchSize)
	pausedForFlush := false

	flush := func(parent context.Context) {
		if len(batch) == 0 {
			return
		}
		batchAttributes := clickhouseBatchTraceAttributes(batchMessages, len(batch))
		eventSpans := startClickHouseEventSpans(tracer, parent, batchSpanContexts, len(batch))
		flushCtx, spanOpts := clickhouseFlushTraceContext(parent, batchSpanContexts, batchAttributes)
		flushCtx, span := tracer.Start(flushCtx, "clickhouse.insert_batch", spanOpts...)
		if err := clickhouseClient.InsertEvents(flushCtx, batch); err != nil {
			log.Printf("insert failed: %v", err)
			span.RecordError(err)
			span.End()
			endClickHouseEventSpans(eventSpans, err)
			return
		}
		if err := reader.CommitMessages(flushCtx, batchMessages...); err != nil {
			log.Printf("commit failed: %v", err)
			span.RecordError(err)
		}
		span.End()
		endClickHouseEventSpans(eventSpans, nil)
		batch = batch[:0]
		batchMessages = batchMessages[:0]
		batchSpanContexts = batchSpanContexts[:0]
	}

	msgChan := make(chan kafka.Message, 100)
	errChan := make(chan error, 1)
	go func() {
		for {
			msg, err := reader.FetchMessage(ctx)
			if err != nil {
				select {
				case errChan <- err:
				default:
				}
				time.Sleep(500 * time.Millisecond)
				continue
			}
			select {
			case msgChan <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		messageInput := messageInputForBatch(len(batch), batchSize, msgChan)
		if messageInput == nil && !pausedForFlush {
			log.Printf("batch reached BATCH_SIZE=%d; pausing Kafka intake until ClickHouse insert succeeds", batchSize)
			pausedForFlush = true
			processorIntakePaused.Set(1)
			processorIntakePauseTransitions.Inc()
		} else if messageInput != nil {
			pausedForFlush = false
			processorIntakePaused.Set(0)
		}

		select {
		case <-ticker.C:
			flush(ctx)
		case err := <-errChan:
			log.Printf("read failed: %v", err)
		case <-ctx.Done():
			log.Printf("shutdown signal received, flushing final batch...")
			shutdownFlushCtx, shutdownFlushCancel := context.WithTimeout(context.Background(), 10*time.Second)
			flush(shutdownFlushCtx)
			shutdownFlushCancel()
			return
		case msg := <-messageInput:
			consumeCtx := serviceutil.ExtractKafkaHeaders(ctx, msg.Headers)
			consumeCtx, span := tracer.Start(consumeCtx, "kafka.consume",
				trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(
					attribute.String("kafka.topic", msg.Topic),
					attribute.Int("kafka.partition", msg.Partition),
					attribute.Int64("kafka.offset", msg.Offset),
				),
			)

			var payload events.Envelope
			if err := json.Unmarshal(msg.Value, &payload); err != nil {
				log.Printf("invalid message: %v", err)
				span.RecordError(err)
				span.End()
				if err := reader.CommitMessages(ctx, msg); err != nil {
					log.Printf("commit failed: %v", err)
				}
				continue
			}

			payload.EnsureTimestamp(time.Now().UTC())
			payload.SetTraceID(serviceutil.TraceIDFromContext(consumeCtx))

			batch = append(batch, payload)
			batchMessages = append(batchMessages, msg)
			batchSpanContexts = append(batchSpanContexts, trace.SpanContextFromContext(consumeCtx))
			span.End()
			if len(batch) >= batchSize {
				flush(ctx)
			}
		}
	}
}

func clickhouseFlushTraceContext(parent context.Context, spanContexts []trace.SpanContext, attrs []attribute.KeyValue) (context.Context, []trace.SpanStartOption) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
	}
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	validSpanContexts := make([]trace.SpanContext, 0, len(spanContexts))
	for _, spanContext := range spanContexts {
		if spanContext.IsValid() {
			validSpanContexts = append(validSpanContexts, spanContext)
		}
	}
	if len(validSpanContexts) == 0 {
		return parent, opts
	}
	parent = trace.ContextWithSpanContext(parent, validSpanContexts[0])
	if len(validSpanContexts) == 1 {
		return parent, opts
	}
	links := make([]trace.Link, 0, len(validSpanContexts)-1)
	for _, spanContext := range validSpanContexts[1:] {
		links = append(links, trace.Link{SpanContext: spanContext})
	}
	return parent, append(opts, trace.WithLinks(links...))
}

func clickhouseBatchTraceAttributes(messages []kafka.Message, batchSize int) []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.Int("batch.size", batchSize)}
	if len(messages) == 0 {
		return attrs
	}

	firstOffset := messages[0].Offset
	lastOffset := messages[0].Offset
	topics := make(map[string]struct{})
	partitions := make(map[int]struct{})
	offsetRanges := make(map[string]kafkaOffsetRange)
	for _, msg := range messages {
		if msg.Offset < firstOffset {
			firstOffset = msg.Offset
		}
		if msg.Offset > lastOffset {
			lastOffset = msg.Offset
		}
		topics[msg.Topic] = struct{}{}
		partitions[msg.Partition] = struct{}{}
		rangeKey := fmt.Sprintf("%s/%d", msg.Topic, msg.Partition)
		current, ok := offsetRanges[rangeKey]
		if !ok {
			current = kafkaOffsetRange{topic: msg.Topic, partition: msg.Partition, first: msg.Offset, last: msg.Offset}
		}
		if msg.Offset < current.first {
			current.first = msg.Offset
		}
		if msg.Offset > current.last {
			current.last = msg.Offset
		}
		offsetRanges[rangeKey] = current
	}

	attrs = append(attrs,
		attribute.Int("kafka.batch.message_count", len(messages)),
		attribute.Int64("kafka.first_offset", firstOffset),
		attribute.Int64("kafka.last_offset", lastOffset),
		attribute.Int("kafka.partition_count", len(partitions)),
	)
	if len(topics) == 1 {
		attrs = append(attrs, attribute.String("kafka.topic", onlyTopic(topics)))
	} else {
		attrs = append(attrs, attribute.String("kafka.topics", joinTopics(topics)))
	}
	if len(partitions) == 1 {
		attrs = append(attrs, attribute.Int("kafka.partition", onlyPartition(partitions)))
	} else {
		attrs = append(attrs, attribute.String("kafka.partitions", joinPartitions(partitions)))
	}
	attrs = append(attrs, attribute.String("kafka.offset_ranges", joinOffsetRanges(offsetRanges)))
	return attrs
}

type kafkaOffsetRange struct {
	topic     string
	partition int
	first     int64
	last      int64
}

func onlyTopic(topics map[string]struct{}) string {
	for topic := range topics {
		return topic
	}
	return ""
}

func joinTopics(topics map[string]struct{}) string {
	keys := make([]string, 0, len(topics))
	for topic := range topics {
		keys = append(keys, topic)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func onlyPartition(partitions map[int]struct{}) int {
	for partition := range partitions {
		return partition
	}
	return 0
}

func joinPartitions(partitions map[int]struct{}) string {
	keys := make([]int, 0, len(partitions))
	for partition := range partitions {
		keys = append(keys, partition)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, partition := range keys {
		parts = append(parts, strconv.Itoa(partition))
	}
	return strings.Join(parts, ",")
}

func joinOffsetRanges(offsetRanges map[string]kafkaOffsetRange) string {
	keys := make([]string, 0, len(offsetRanges))
	for key := range offsetRanges {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		offsetRange := offsetRanges[key]
		parts = append(parts, fmt.Sprintf("%s/%d:%d-%d", offsetRange.topic, offsetRange.partition, offsetRange.first, offsetRange.last))
	}
	return strings.Join(parts, ",")
}

func startClickHouseEventSpans(tracer trace.Tracer, parent context.Context, spanContexts []trace.SpanContext, batchSize int) []trace.Span {
	spans := make([]trace.Span, 0, len(spanContexts))
	for _, spanContext := range spanContexts {
		if !spanContext.IsValid() {
			continue
		}
		_, span := tracer.Start(
			trace.ContextWithSpanContext(parent, spanContext),
			"clickhouse.insert_event",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attribute.Int("batch.size", batchSize)),
		)
		spans = append(spans, span)
	}
	return spans
}

func endClickHouseEventSpans(spans []trace.Span, err error) {
	for _, span := range spans {
		if err != nil {
			span.RecordError(err)
		}
		span.End()
	}
}

func messageInputForBatch(batchLen, batchSize int, input <-chan kafka.Message) <-chan kafka.Message {
	if batchLen >= batchSize {
		return nil
	}
	return input
}
