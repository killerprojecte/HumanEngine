package credentials

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// All keys, nonces, plaintexts, and envelopes in these fixtures are synthetic.
// Original JS strings use UTF-16 arrays so encoding/json cannot erase surrogates.
type byteFixtures struct {
	Key, Nonce string
	Canonical  []struct {
		Label                                 string
		InputJSON                             string
		NamespaceUnits, NameUnits, ValueUnits []uint16
		AADHex, PlaintextHex, Envelope        string
		Decrypted                             string
	}
	Reader struct {
		Namespace, Name string
		ByteCases       []struct{ Label, Hex, Envelope, Value string }
		Cases           []struct{ Label, Envelope, Value, Error string }
	}
	UTF8Oracle struct {
		Alphabet                            []byte
		ShortBytes, Boundaries, RandomBytes struct {
			Count  int
			SHA256 string
		}
	}
}

func readByteFixtures(t *testing.T) byteFixtures {
	t.Helper()
	data, err := os.ReadFile("testdata/node-v1-byte-compat.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture byteFixtures
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func fixtureHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestNodeCanonicalCredentialBytes(t *testing.T) {
	f := readByteFixtures(t)
	c, err := NewCipher(fixtureHex(t, f.Key))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range f.Canonical {
		t.Run(item.Label, func(t *testing.T) {
			namespace := jsjson.FromUTF16(item.NamespaceUnits)
			name := jsjson.FromUTF16(item.NameUnits)
			value := jsjson.FromUTF16(item.ValueUnits)
			parsed, err := jsjson.Parse(item.InputJSON)
			if err != nil {
				t.Fatal(err)
			}
			input, ok := parsed.(*jsjson.Object)
			if !ok {
				t.Fatal("expected credential input object")
			}
			for key, want := range map[string]string{"namespace": namespace, "name": name, "value": value} {
				got, exists := input.Get(key)
				if !exists || got != want {
					t.Fatalf("source JSON input lost %s code units", key)
				}
			}
			aad, err := jsjson.Marshal([]any{namespace, name})
			if err != nil || !bytes.Equal(aad, fixtureHex(t, item.AADHex)) {
				t.Fatalf("JSON.stringify AAD bytes differ: %v", err)
			}
			plaintext, err := jsjson.UTF8Text(value)
			if err != nil || !bytes.Equal([]byte(plaintext), fixtureHex(t, item.PlaintextHex)) {
				t.Fatalf("Node cipher.update UTF-8 bytes differ: %v", err)
			}
			c.random = bytes.NewReader(fixtureHex(t, f.Nonce))
			envelope, err := c.Encrypt(namespace, name, value)
			if err != nil || envelope != item.Envelope {
				t.Fatalf("canonical SecretStore envelope bytes differ: %v", err)
			}
			decrypted, err := c.Decrypt(namespace, name, item.Envelope)
			if err != nil || decrypted != item.Decrypted {
				t.Fatalf("SecretStore decrypted value differs: %v", err)
			}
			for _, aad := range [][2]string{{namespace + "changed", name}, {namespace, name + "changed"}} {
				if _, err := c.Decrypt(aad[0], aad[1], item.Envelope); !errors.Is(err, ErrDecrypt) {
					t.Fatal("wrong AAD accepted")
				}
			}
			// AAD must not collapse lone-surrogate identities to UTF-8 replacement.
			nsText, _ := jsjson.UTF8Text(namespace)
			nameText, _ := jsjson.UTF8Text(name)
			if nsText != namespace || nameText != name {
				if _, err := c.Decrypt(nsText, nameText, item.Envelope); !errors.Is(err, ErrDecrypt) {
					t.Fatal("lossy AAD alias accepted")
				}
			}
		})
	}
}

func TestNodeAuthenticatedPlaintextUTF8(t *testing.T) {
	f := readByteFixtures(t)
	c, err := NewCipher(fixtureHex(t, f.Key))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range f.Reader.ByteCases {
		t.Run(item.Label, func(t *testing.T) {
			got, err := c.Decrypt(f.Reader.Namespace, f.Reader.Name, item.Envelope)
			if err != nil || got != item.Value {
				t.Fatalf("Node Buffer.toString differs for %s: got %x, want %x (%v)", item.Hex, got, item.Value, err)
			}
		})
	}
}

func TestCredentialReaderCompatibilityBoundary(t *testing.T) {
	f := readByteFixtures(t)
	c, err := NewCipher(fixtureHex(t, f.Key))
	if err != nil {
		t.Fatal(err)
	}
	// These are known source-reader successes that the candidate still rejects.
	// Keep the audit executable instead of claiming arbitrary v1 reader parity.
	divergent := map[string]bool{
		"extra_segment": true, "extra_empty_segment": true,
		"unpadded_base64": true, "base64url": true, "base64url_padded_alphabet": true, "base64_spaces": true,
		"base64_punctuation": true, "base64_after_padding": true,
		"nonce_1": true, "nonce_8": true, "nonce_16": true, "nonce_32": true, "nonce_128": true,
		"tag_4": true, "tag_8": true, "tag_12": true, "tag_13": true, "tag_14": true, "tag_15": true,
		"nonce_16_tag_12": true,
	}
	for _, item := range f.Reader.Cases {
		t.Run(item.Label, func(t *testing.T) {
			got, err := c.Decrypt(f.Reader.Namespace, f.Reader.Name, item.Envelope)
			if item.Error != "" || divergent[item.Label] {
				if item.Error != "" && item.Error != ErrDecrypt.Error() {
					t.Fatal("unexpected source reader error")
				}
				if divergent[item.Label] && item.Error != "" {
					t.Fatal("documented source success changed")
				}
				if !errors.Is(err, ErrDecrypt) || got != "" {
					t.Fatalf("unsupported/malformed envelope not rejected: %v", err)
				}
				return
			}
			if err != nil || got != item.Value {
				t.Fatalf("supported reader behavior differs: %v", err)
			}
		})
	}
}

func TestCredentialRejectsInvalidInternalStrings(t *testing.T) {
	f := readByteFixtures(t)
	c, err := NewCipher(fixtureHex(t, f.Key))
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"\xff", "\xc0\x80", "\xed\xa0\x80\xed\xb0\x80"} {
		for _, parts := range [][3]string{{invalid, "name", "value"}, {"namespace", invalid, "value"}, {"namespace", "name", invalid}} {
			if _, err := c.Encrypt(parts[0], parts[1], parts[2]); !errors.Is(err, jsjson.ErrStringEncoding) {
				t.Fatal("malformed/noncanonical WTF-8 input accepted")
			}
		}
		if _, err := c.Decrypt(invalid, f.Reader.Name, f.Reader.Cases[0].Envelope); !errors.Is(err, ErrDecrypt) {
			t.Fatal("invalid decryption AAD accepted")
		}
	}
}

func TestNodeBufferUTF8Oracle(t *testing.T) {
	f := readByteFixtures(t)
	verify := func(t *testing.T, count int, want string, produce func(func([]byte))) {
		t.Helper()
		hash := sha256.New()
		seen := 0
		produce(func(input []byte) {
			decoded, err := decodeUTF8(input)
			if err != nil {
				t.Fatal(err)
			}
			var lengths [8]byte
			binary.BigEndian.PutUint32(lengths[:4], uint32(len(input)))
			binary.BigEndian.PutUint32(lengths[4:], uint32(len(decoded)))
			hash.Write(lengths[:])
			hash.Write(input)
			hash.Write([]byte(decoded))
			seen++
		})
		if seen != count || hex.EncodeToString(hash.Sum(nil)) != want {
			t.Fatalf("Node UTF-8 replacement byte oracle differs (%d inputs, want %d)", seen, count)
		}
	}
	t.Run("all_zero_one_two_bytes", func(t *testing.T) {
		verify(t, f.UTF8Oracle.ShortBytes.Count, f.UTF8Oracle.ShortBytes.SHA256, func(add func([]byte)) {
			add(nil)
			for a := 0; a < 256; a++ {
				add([]byte{byte(a)})
				for b := 0; b < 256; b++ {
					add([]byte{byte(a), byte(b)})
				}
			}
		})
	})
	t.Run("three_four_byte_boundaries", func(t *testing.T) {
		verify(t, f.UTF8Oracle.Boundaries.Count, f.UTF8Oracle.Boundaries.SHA256, func(add func([]byte)) {
			for _, a := range f.UTF8Oracle.Alphabet {
				for _, b := range f.UTF8Oracle.Alphabet {
					for _, c := range f.UTF8Oracle.Alphabet {
						add([]byte{a, b, c})
						for _, d := range f.UTF8Oracle.Alphabet {
							add([]byte{a, b, c, d})
						}
					}
				}
			}
		})
	})
	t.Run("seeded_byte_sequences", func(t *testing.T) {
		verify(t, f.UTF8Oracle.RandomBytes.Count, f.UTF8Oracle.RandomBytes.SHA256, func(add func([]byte)) {
			state := uint32(0x6a09e667)
			random := func() uint32 {
				state ^= state << 13
				state ^= state >> 17
				state ^= state << 5
				return state
			}
			for i := 0; i < 4096; i++ {
				input := make([]byte, int(random()%129))
				for j := range input {
					input[j] = byte(random())
				}
				add(input)
			}
		})
	})
}
