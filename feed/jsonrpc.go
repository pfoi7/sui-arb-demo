package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// DefaultRPCURL is the Sui mainnet fullnode JSON-RPC address. Public fullnodes
// have deprecated JSON-RPC, so this source is intended for self-hosted nodes
// that still expose it; use GraphQLSource for public endpoints.
const DefaultRPCURL = "https://fullnode.mainnet.sui.io:443"

// JSONRPCSource implements Source on top of Sui JSON-RPC. Its Stream polls the
// latest checkpoint and emits only the tip, deliberately leaving it to the
// Processor to backfill anything in between via Fetch.
type JSONRPCSource struct {
	URL          string
	Client       *http.Client
	PollInterval time.Duration
	id           atomic.Uint64
}

// NewJSONRPCSource creates a source for url (DefaultRPCURL if empty).
func NewJSONRPCSource(url string) *JSONRPCSource {
	if url == "" {
		url = DefaultRPCURL
	}
	return &JSONRPCSource{URL: url, Client: &http.Client{Timeout: 10 * time.Second}, PollInterval: time.Second}
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *JSONRPCSource) call(ctx context.Context, method string, params []any, out any) error {
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", ID: s.id.Add(1), Method: method, Params: params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rpc %s: http %d", method, resp.StatusCode)
	}
	var r rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if r.Error != nil {
		return fmt.Errorf("rpc %s: %d %s", method, r.Error.Code, r.Error.Message)
	}
	return json.Unmarshal(r.Result, out)
}

// LatestSeq returns the latest checkpoint sequence number known to the node.
func (s *JSONRPCSource) LatestSeq(ctx context.Context) (uint64, error) {
	var v string
	if err := s.call(ctx, "sui_getLatestCheckpointSequenceNumber", []any{}, &v); err != nil {
		return 0, err
	}
	return strconv.ParseUint(v, 10, 64)
}

// Fetch reads a single checkpoint by sequence number.
func (s *JSONRPCSource) Fetch(ctx context.Context, seq uint64) (Checkpoint, error) {
	var raw struct {
		SequenceNumber string   `json:"sequenceNumber"`
		Digest         string   `json:"digest"`
		TimestampMs    string   `json:"timestampMs"`
		Transactions   []string `json:"transactions"`
	}
	if err := s.call(ctx, "sui_getCheckpoint", []any{strconv.FormatUint(seq, 10)}, &raw); err != nil {
		return Checkpoint{}, err
	}
	n, err := strconv.ParseUint(raw.SequenceNumber, 10, 64)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("bad sequenceNumber %q: %w", raw.SequenceNumber, err)
	}
	ts, _ := strconv.ParseUint(raw.TimestampMs, 10, 64)
	return Checkpoint{Seq: n, Digest: raw.Digest, TimestampMs: ts, TxCount: len(raw.Transactions)}, nil
}

// Stream polls for new tips starting at fromSeq.
func (s *JSONRPCSource) Stream(ctx context.Context, fromSeq uint64) (Subscription, error) {
	return newPollSub(s, s.PollInterval, fromSeq), nil
}
