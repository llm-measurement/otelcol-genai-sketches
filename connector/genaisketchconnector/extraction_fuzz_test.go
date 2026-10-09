// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// Exercise every OTLP value kind, including nested values, without letting
// the fuzz harness itself construct unbounded trees.
func fuzzAttribute(kind uint8, text string, integer int64, bits uint64) pcommon.Value {
	value := pcommon.NewValueEmpty()
	switch kind % 8 {
	case 0:
		value.SetStr(text)
	case 1:
		value.SetInt(integer)
	case 2:
		value.SetDouble(math.Float64frombits(bits))
	case 3:
		value.SetBool(integer != 0)
	case 4:
		value.SetEmptyBytes().FromRaw([]byte(text))
	case 5:
		value.SetEmptyMap().PutStr("nested", text)
	case 6:
		value.SetEmptySlice().AppendEmpty().SetStr(text)
	}
	return value
}

func FuzzSpanAttributes(f *testing.F) {
	for kind := uint8(0); kind < 8; kind++ {
		for _, text := range []string{"", "0", "42", "-1", "18446744073709551615", "\xff", "unavailable"} {
			f.Add(kind, text, int64(-1), math.Float64bits(math.NaN()))
		}
	}
	f.Add(uint8(2), "", int64(0), math.Float64bits(math.Inf(1)))
	f.Fuzz(func(t *testing.T, kind uint8, text string, integer int64, bits uint64) {
		if len(text) > 2*maxAttributeValueBytes {
			t.Skip()
		}
		state := newFleetFixtureState(t, defaultConfig())
		attributes := pcommon.NewMap()
		value := fuzzAttribute(kind, text, integer, bits)
		value.CopyTo(attributes.PutEmpty("gen_ai.usage.input_tokens"))
		value.CopyTo(attributes.PutEmpty("gen_ai.usage.output_tokens"))
		attributes.PutInt("gen_ai.usage.cache_read.input_tokens", 1)
		value.CopyTo(attributes.PutEmpty("gen_ai_sketch.usage.input.provenance"))
		before := attributes.AsRaw()
		got, err := tokenTotals(attributes, state.cfg)
		again, againErr := tokenTotals(attributes, state.cfg)
		if got != again || fmt.Sprint(err) != fmt.Sprint(againErr) {
			t.Fatal("non-deterministic token extraction")
		}
		if !reflect.DeepEqual(before, attributes.AsRaw()) {
			// NaN is not equal to itself, even when the map is untouched.
			if value.Type() != pcommon.ValueTypeDouble || !math.IsNaN(value.Double()) {
				t.Fatal("token extraction mutated attributes")
			}
		}
		if err != nil {
			return
		}
		_, valid, parseErr := valueAsUint(value)
		if !valid && parseErr == nil && (got.missingTokens != 1 || got.inputTokens != 0 || got.outputTokens != 0) {
			t.Fatal("invalid usage became reported usage")
		}
		attributes.PutStr("gen_ai_sketch.usage.input.provenance", "unavailable")
		unavailable, err := tokenTotals(attributes, state.cfg)
		if err == nil && (unavailable.inputTokens != 0 || unavailable.missingTokens != 1) {
			t.Fatal("unavailable usage did not stay missing")
		}
	})
}

func FuzzIdentityExtraction(f *testing.F) {
	for kind := uint8(0); kind < 8; kind++ {
		for _, text := range []string{"", "private-user-sentinel", " \tuser\r\n", "\xff", strings.Repeat("x", maxAttributeValueBytes+1)} {
			f.Add(kind, text, int64(42), math.Float64bits(1.5))
		}
	}
	f.Fuzz(func(t *testing.T, kind uint8, text string, integer int64, bits uint64) {
		if len(text) > 2*maxAttributeValueBytes {
			t.Skip()
		}
		state := newFleetFixtureState(t, fleetFixtureConfig(false, false))
		data := spanData{spanAttrs: pcommon.NewMap(), resourceAttrs: pcommon.NewMap()}
		value := fuzzAttribute(kind, text, integer, bits)
		value.CopyTo(data.spanAttrs.PutEmpty("user.id"))
		data.resourceAttrs.PutStr("enduser.id", "private-resource-sentinel")
		got, err := state.hashDataField(fieldUserKey, data)
		again, againErr := state.hashDataField(fieldUserKey, data)
		if got != again || fmt.Sprint(err) != fmt.Sprint(againErr) {
			t.Fatal("non-deterministic identity extraction")
		}
		// A present span alias wins even over a higher-priority resource alias.
		data.resourceAttrs.PutStr("enduser.id", "different-resource-sentinel")
		changed, changedErr := state.hashDataField(fieldUserKey, data)
		if got != changed || fmt.Sprint(err) != fmt.Sprint(changedErr) {
			t.Fatal("resource identity overrode a present span identity")
		}
		if value.AsString() == "" && (got.ok || err != nil) {
			t.Fatal("empty span identity fell back to a resource")
		}
		if len(value.AsString()) > maxAttributeValueBytes && (got.ok || err == nil) {
			t.Fatal("oversized identity accepted")
		}
		data.spanAttrs.Remove("user.id")
		fallback, err := state.hashDataField(fieldUserKey, data)
		want, wantErr := state.hashField(state.cfg.fields[fieldUserKey], "different-resource-sentinel")
		if err != nil || wantErr != nil || !fallback.ok || fallback.value != want {
			t.Fatal("resource fallback failed")
		}
		data.resourceAttrs.Clear()
		missing, err := state.hashDataField(fieldUserKey, data)
		if err != nil || missing.ok {
			t.Fatal("identity leaked across resources")
		}
	})
}
