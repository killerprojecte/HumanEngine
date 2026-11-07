package credentials

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNodeEnvelopeBothDirections(t *testing.T) {
	data, err := os.ReadFile("testdata/node-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Key, Nonce, Namespace, Name, Value, Envelope string }
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(f.Key)
	nonce, _ := hex.DecodeString(f.Nonce)
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(f.Namespace, f.Name, f.Envelope)
	if err != nil || got != f.Value {
		t.Fatalf("Node decrypt: %q %v", got, err)
	}
	c.random = bytes.NewReader(nonce)
	envelope, err := c.Encrypt(f.Namespace, f.Name, f.Value)
	if err != nil || envelope != f.Envelope {
		t.Fatalf("Go byte parity: %v", err)
	}
	if _, err = c.Decrypt(f.Namespace, f.Name+"changed", f.Envelope); err != ErrDecrypt {
		t.Fatal("AAD mismatch accepted")
	}
}
func TestKeyCustody(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".secrets.key")
	if _, err := LoadKey(path, true); err == nil {
		t.Fatal("missing key regenerated")
	}
	first, err := LoadKey(path, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadKey(path, true)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("key changed", err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadKey(path, true); err == nil {
		t.Fatal("insecure key accepted")
	}
	link := path + ".link"
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadKey(link, true); err == nil {
		t.Fatal("symlink key accepted")
	}
}
