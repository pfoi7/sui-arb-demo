package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultGraphQLURL is the public Sui mainnet GraphQL endpoint.
const DefaultGraphQLURL = "https://graphql.mainnet.sui.io/graphql"

// GraphQLSource implements Source over the Sui GraphQL API.
type GraphQLSource struct {
	URL          string
	Client       *http.Client
	PollInterval time.Duration
}

// NewGraphQLSource creates a source for url (DefaultGraphQLURL if empty).
func NewGraphQLSource(url string) *GraphQLSource {
	if url == "" {
		url = DefaultGraphQLURL
	}
	return &GraphQLSource{URL: url, Client: &http.Client{Timeout: 10 * time.Second}, PollInterval: time.Second}
}

type gqlCheckpoint struct {
	SequenceNumber *uint64 `json:"sequenceNumber"`
	Digest         string  `json:"digest"`
	Timestamp      string  `json:"timestamp"`
}

func (s *GraphQLSource) query(ctx context.Context, q string, vars map[string]any) (*gqlCheckpoint, error) {
	body, _ := json.Marshal(map[string]any{"query": q, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graphql: http %d", resp.StatusCode)
	}
	var r struct {
		Data struct {
			Checkpoint *gqlCheckpoint `json:"checkpoint"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if len(r.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", r.Errors[0].Message)
	}
	if r.Data.Checkpoint == nil || r.Data.Checkpoint.SequenceNumber == nil {
		return nil, fmt.Errorf("graphql: checkpoint not found")
	}
	return r.Data.Checkpoint, nil
}

// LatestSeq returns the latest indexed checkpoint.
func (s *GraphQLSource) LatestSeq(ctx context.Context) (uint64, error) {
	c, err := s.query(ctx, `{ checkpoint { sequenceNumber } }`, nil)
	if err != nil {
		return 0, err
	}
	return *c.SequenceNumber, nil
}

// Fetch reads one checkpoint by sequence number.
func (s *GraphQLSource) Fetch(ctx context.Context, seq uint64) (Checkpoint, error) {
	c, err := s.query(ctx, `query($s: UInt53) { checkpoint(sequenceNumber: $s) { sequenceNumber digest timestamp } }`, map[string]any{"s": seq})
	if err != nil {
		return Checkpoint{}, err
	}
	out := Checkpoint{Seq: *c.SequenceNumber, Digest: c.Digest, TxCount: -1}
	if ts, err := time.Parse(time.RFC3339Nano, c.Timestamp); err == nil {
		out.TimestampMs = uint64(ts.UnixMilli())
	}
	return out, nil
}

// Stream polls for new tips starting at fromSeq.
func (s *GraphQLSource) Stream(ctx context.Context, fromSeq uint64) (Subscription, error) {
	return newPollSub(s, s.PollInterval, fromSeq), nil
}
