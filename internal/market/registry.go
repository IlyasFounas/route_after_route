package market

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
)

// Factory builds a market from its JSON options. baseDir is the directory of
// the configuration file, used to resolve relative paths.
type Factory func(options json.RawMessage, baseDir string) (Market, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes an adapter available under typ. Adapters call it from init().
func Register(typ string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[typ]; dup {
		panic("market: duplicate adapter type " + typ)
	}
	registry[typ] = f
}

// Open instantiates a registered adapter and validates its descriptor.
func Open(typ string, options json.RawMessage, baseDir string) (Market, error) {
	registryMu.RLock()
	f, ok := registry[typ]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("market: unknown adapter type %q (available: %v)", typ, Types())
	}
	m, err := f(options, baseDir)
	if err != nil {
		return nil, fmt.Errorf("market %s: %w", typ, err)
	}
	if err := m.Descriptor().Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Types lists registered adapter types, sorted.
func Types() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// DecodeStrict decodes a single JSON object into v, rejecting unknown fields
// and trailing data. An empty input is treated as {}.
func DecodeStrict(raw []byte, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after JSON object")
	}
	return nil
}
