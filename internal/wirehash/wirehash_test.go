package wirehash

import (
	"bytes"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"testing"
)

type invalidTextMarshaler struct{}

func (invalidTextMarshaler) MarshalText() ([]byte, error) {
	return []byte{0xff}, nil
}

type pointerTextMarshaler struct {
	calls *int
}

func (value *pointerTextMarshaler) MarshalText() ([]byte, error) {
	*value.calls++
	return []byte("custom"), nil
}

type hiddenCustomMarshaler struct {
	Value pointerTextMarshaler `json:"value"`
}

type customMarshalerThroughUnexportedEmbed struct {
	hiddenCustomMarshaler
}

type hiddenInvalidString struct {
	Value string `json:"value"`
}

type invalidStringThroughUnexportedEmbed struct {
	hiddenInvalidString
}

func TestADRVectors(t *testing.T) {
	tests := []struct {
		domain    string
		value     any
		canonical string
		hexDigest string
		base32    string
	}{
		{
			domain:    "qs.test.v1",
			value:     map[string]any{},
			canonical: `{}`,
			hexDigest: "6de84f9c7d4d7fa7c7679b7504509343c3a70cf44203e8c8c2694d5571bad46b",
			base32:    "nxue7hd5jv72pr3htn2qiuetipb2odhuiib6rsgcnfgvk4n22rvq",
		},
		{
			domain:    "twitter.target_set.v3",
			value:     []string{"usr_1", "usr_2"},
			canonical: `["usr_1","usr_2"]`,
			hexDigest: "9739bc1905b2d1cbc0faf007b913bf8db235c1aec4b4790a6485dc29239878d1",
		},
		{
			domain: "twitter.message_id.v2",
			value: struct {
				NodeID     string `json:"node_id"`
				SpoolEpoch string `json:"spool_epoch"`
				SpoolSeq   int64  `json:"spool_seq"`
			}{"twitter-node-a", "01J00000000000000000000000", 912},
			canonical: `{"node_id":"twitter-node-a","spool_epoch":"01J00000000000000000000000","spool_seq":912}`,
			hexDigest: "af06aa612a010e12d060646ffda173721d947a77605b757adc661bdfbb0ee285",
			base32:    "v4dkuyjkaehbfudamrx73iltoiozi6txmbnxk6w4myn57oyo4kcq",
		},
		{
			domain: "twitter.billing_dedup.v2",
			value: struct {
				NotifySourceRef string `json:"notify_source_ref"`
				UserID          string `json:"user_id"`
			}{"tns_zvkpeb6iwiiix7qzzjtfckaonyrcdhww3zbt6yxv5w5bq27zyzwa", "usr_1"},
			canonical: `{"notify_source_ref":"tns_zvkpeb6iwiiix7qzzjtfckaonyrcdhww3zbt6yxv5w5bq27zyzwa","user_id":"usr_1"}`,
			hexDigest: "f332748dc30699279ec152ddaaf95841644991d598d3d7217e70a9d23f64bd4b",
			base32:    "6mzhjdoda2msphwbklo2v6kyifseteovtdj5oil6ocu5ep3exvfq",
		},
		{
			domain: "twitter.billing_capture_basis.v1",
			value: struct {
				DecisionDay     string `json:"decision_day"`
				FirstCapturedAt string `json:"first_captured_at"`
			}{"2026-07-30", "2026-07-30T12:00:00.000000000Z"},
			canonical: `{"decision_day":"2026-07-30","first_captured_at":"2026-07-30T12:00:00.000000000Z"}`,
			hexDigest: "ababa3e6bec65c1fa1427abf7dd5dbb272ed72f9168e59d473d62e62e18d122e",
		},
		{
			domain: "notification.delivery_key.v2",
			value: struct {
				EventCode string `json:"event_code"`
				Source    string `json:"source"`
				SourceRef string `json:"source_ref"`
				UserID    string `json:"user_id"`
			}{"twitter.post.created", "twitter-monitor", "tns_zvkpeb6iwiiix7qzzjtfckaonyrcdhww3zbt6yxv5w5bq27zyzwa", "usr_1"},
			canonical: `{"event_code":"twitter.post.created","source":"twitter-monitor","source_ref":"tns_zvkpeb6iwiiix7qzzjtfckaonyrcdhww3zbt6yxv5w5bq27zyzwa","user_id":"usr_1"}`,
			hexDigest: "2fee984b7b111e9e0b1d0dd7ac994bc034a03e07281e3ed0aa5360d335517770",
			base32:    "f7xjqs33cepj4cy5bxl2zgklya2kapqhfapd5ufkknqngnkro5ya",
		},
	}
	for _, test := range tests {
		t.Run(test.domain, func(t *testing.T) {
			canonical, err := Canonicalize(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != test.canonical {
				t.Fatalf("canonical = %q, want %q", canonical, test.canonical)
			}
			digest, err := Digest(test.domain, test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(digest[:]); got != test.hexDigest {
				t.Fatalf("digest = %s, want %s", got, test.hexDigest)
			}
			if test.base32 != "" && Base32Lower(digest) != test.base32 {
				t.Fatalf("base32 = %s, want %s", Base32Lower(digest), test.base32)
			}
		})
	}
}

func TestCanonicalJSONStrictness(t *testing.T) {
	canonical, err := CanonicalizeJSON([]byte(`{"z":1e-6,"a":"<>&\u2028","\ud83d\ude00":1,"\ue000":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), `{"a":"<>& ","z":0.000001,"😀":1,"":2}`; got != want {
		t.Fatalf("canonical = %q, want %q", got, want)
	}
	for _, raw := range []string{
		`{"a":1,"a":2}`,
		`{"a":"\ud800"}`,
		`{"a":"\udc00"}`,
		`{"a":"\ufdd0"}`,
		`{"a":"\ufffe"}`,
		`{"a":"\ud83f\udffe"}`,
		`{"a":1} {"b":2}`,
	} {
		if _, err := CanonicalizeJSON([]byte(raw)); err == nil {
			t.Fatalf("CanonicalizeJSON(%q) succeeded", raw)
		}
	}
}

func TestCanonicalizeRejectsInvalidGoStringsBeforeJSONReplacement(t *testing.T) {
	for _, invalid := range []string{string([]byte{0xff}), string([]byte{0xfe})} {
		if _, err := Canonicalize(map[string]string{"x": invalid}); err == nil {
			t.Fatalf("Canonicalize accepted invalid UTF-8 bytes %x", []byte(invalid))
		}
	}
	if _, err := Canonicalize(struct {
		Value invalidTextMarshaler `json:"value"`
	}{}); err == nil {
		t.Fatal("Canonicalize accepted invalid UTF-8 from encoding.TextMarshaler")
	}
	if _, err := Canonicalize(map[invalidTextMarshaler]string{{}: "value"}); err == nil {
		t.Fatal("Canonicalize accepted an invalid UTF-8 TextMarshaler map key")
	}
	calls := 0
	value := struct {
		Field pointerTextMarshaler `json:"field"`
	}{Field: pointerTextMarshaler{calls: &calls}}
	if _, err := Canonicalize(&value); err == nil {
		t.Fatal("Canonicalize accepted a pointer-receiver encoding.TextMarshaler")
	}
	if calls != 0 {
		t.Fatalf("custom marshaler ran %d times before rejection", calls)
	}
	embeddedCalls := 0
	embedded := customMarshalerThroughUnexportedEmbed{
		hiddenCustomMarshaler{Value: pointerTextMarshaler{calls: &embeddedCalls}},
	}
	if _, err := Canonicalize(&embedded); err == nil {
		t.Fatal("Canonicalize accepted a custom marshaler through an unexported anonymous struct")
	}
	if embeddedCalls != 0 {
		t.Fatalf("embedded custom marshaler ran %d times before rejection", embeddedCalls)
	}
	invalidEmbedded := invalidStringThroughUnexportedEmbed{
		hiddenInvalidString{Value: string([]byte{0xff})},
	}
	if _, err := Canonicalize(invalidEmbedded); err == nil {
		t.Fatal("Canonicalize accepted invalid UTF-8 through an unexported anonymous struct")
	}
}

func TestRFC8785NumberSamples(t *testing.T) {
	tests := []struct {
		bits uint64
		want string
	}{
		{0x0000000000000000, "0"},
		{0x8000000000000000, "0"},
		{0x0000000000000001, "5e-324"},
		{0x8000000000000001, "-5e-324"},
		{0x7fefffffffffffff, "1.7976931348623157e+308"},
		{0xffefffffffffffff, "-1.7976931348623157e+308"},
		{0x4340000000000000, "9007199254740992"},
		{0xc340000000000000, "-9007199254740992"},
		{0x4430000000000000, "295147905179352830000"},
		{0x44b52d02c7e14af5, "9.999999999999997e+22"},
		{0x44b52d02c7e14af6, "1e+23"},
		{0x44b52d02c7e14af7, "1.0000000000000001e+23"},
		{0x444b1ae4d6e2ef4e, "999999999999999700000"},
		{0x444b1ae4d6e2ef4f, "999999999999999900000"},
		{0x444b1ae4d6e2ef50, "1e+21"},
		{0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"},
		{0x3eb0c6f7a0b5ed8d, "0.000001"},
		{0x41b3de4355555553, "333333333.3333332"},
		{0x41b3de4355555554, "333333333.33333325"},
		{0x41b3de4355555555, "333333333.3333333"},
		{0x41b3de4355555556, "333333333.3333334"},
		{0x41b3de4355555557, "333333333.33333343"},
		{0xbecbf647612f3696, "-0.0000033333333333333333"},
		{0x43143ff3c1cb0959, "1424953923781206.2"},
	}
	for _, test := range tests {
		t.Run(strconv.FormatUint(test.bits, 16), func(t *testing.T) {
			raw := []byte(strconv.FormatFloat(math.Float64frombits(test.bits), 'g', -1, 64))
			got, err := CanonicalizeJSON(raw)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("canonical number = %s, want %s", got, test.want)
			}
		})
	}
}

func TestMACFraming(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	mac, err := MAC(key, "qs.test.v1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("d2588d224e49cd2c542bbb15e957654848b1e9f2a61df2fd17b913985a7d45ab")
	if !bytes.Equal(mac[:], want) {
		t.Fatalf("MAC = %x, want %x", mac, want)
	}
}

func TestWireFormatting(t *testing.T) {
	digest, err := Digest("qs.test.v1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	wire := SHA256Wire(digest)
	parsed, err := ParseSHA256Wire(wire)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != digest {
		t.Fatal("parsed digest differs")
	}
	if _, err := ParseSHA256Wire("SHA256:" + wire[7:]); err == nil {
		t.Fatal("uppercase prefix accepted")
	}
	if _, err := ParseSHA256Wire("sha256:" + strings.ToUpper(wire[7:])); err == nil {
		t.Fatal("uppercase hex accepted")
	}
}
