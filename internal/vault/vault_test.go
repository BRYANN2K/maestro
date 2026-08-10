package vault

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSetGetDelete(t *testing.T) {
	v, err := Open(context.Background(), filepath.Join(t.TempDir(), "vault.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := v.Get("missing"); ok {
		t.Error("Get on missing key should be ok=false")
	}
	v.Set("openai", "sk-test")
	if got, ok := v.Get("openai"); !ok || got != "sk-test" {
		t.Errorf("Get = %q, %v", got, ok)
	}
	v.Delete("openai")
	if _, ok := v.Get("openai"); ok {
		t.Error("key should be deleted")
	}
	if v.Len() != 0 {
		t.Errorf("Len = %d, want 0", v.Len())
	}
}

func TestSaveReloadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maestro", "vault.json")
	ctx := context.Background()

	v, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v.Set("anthropic", "sk-ant")
	v.Set("github", "ghp-x")
	if err := v.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if val, ok := got.Get("anthropic"); !ok || val != "sk-ant" {
		t.Errorf("anthropic = %q, %v", val, ok)
	}
	if val, ok := got.Get("github"); !ok || val != "ghp-x" {
		t.Errorf("github = %q, %v", val, ok)
	}
}

func TestPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.json")
	ctx := context.Background()
	v, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v.Set("k", "v")
	if err := v.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("vault perm = %v, want 0600", fi.Mode().Perm())
	}
}

func TestCorruptVaultFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Error("Open of corrupt vault should fail")
	}
}

func TestKeysSorted(t *testing.T) {
	v, err := Open(context.Background(), filepath.Join(t.TempDir(), "vault.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v.Set("zeta", "1")
	v.Set("alpha", "2")
	keys := v.Keys()
	if len(keys) != 2 || keys[0] != "alpha" || keys[1] != "zeta" {
		t.Errorf("Keys = %v, want sorted", keys)
	}
}

func TestAESRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	ctx := context.Background()
	v, err := OpenAES(ctx, path, nil)
	if err != nil {
		t.Fatalf("OpenAES: %v", err)
	}
	v.Set("openai", "sk-aes-secret")
	if err := v.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The file must be encrypted (no plaintext).
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte("sk-aes-secret")) {
		t.Error("secret stored in plaintext")
	}
	if !bytes.HasPrefix(data, []byte(aesMagic)) {
		t.Errorf("missing AES magic: %q", data[:16])
	}
	got, err := OpenAES(ctx, path, nil)
	if err != nil {
		t.Fatalf("OpenAES 2: %v", err)
	}
	if val, ok := got.Get("openai"); !ok || val != "sk-aes-secret" {
		t.Errorf("decrypted = %q, %v", val, ok)
	}
	// Key file is 0600.
	fi, err := os.Stat(aesKeyPath(path))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key file = %v, %v", fi, err)
	}
}

func TestAESLegacyJSONFallsBackWithWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	ctx := context.Background()
	// A legacy plain-JSON vault (not AES) is read with a warning.
	if err := os.WriteFile(path, []byte(`{"legacy":"ok"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	warned := false
	v, err := OpenAES(ctx, path, func(w string) { warned = true })
	if err != nil {
		t.Fatalf("OpenAES legacy: %v", err)
	}
	if !warned {
		t.Error("legacy vault should warn")
	}
	if val, ok := v.Get("legacy"); !ok || val != "ok" {
		t.Errorf("legacy value = %q, %v", val, ok)
	}
}

func TestAESGarbageFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	ctx := context.Background()
	if err := os.WriteFile(path, []byte("not-json-not-aes"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := OpenAES(ctx, path, func(string) {})
	if err == nil {
		t.Error("garbage vault should error")
	}
}

func TestAESWrongKeyDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	ctx := context.Background()
	v, _ := OpenAES(ctx, path, nil)
	v.Set("k", "v")
	if err := v.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Replace the key file with a different one → decryption must fail.
	other := make([]byte, 32)
	for i := range other {
		other[i] = 7
	}
	if err := os.WriteFile(aesKeyPath(path), other, 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}
	warned := false
	got, err := OpenAES(ctx, path, func(string) { warned = true })
	if err == nil {
		// Falls back to JSON only when the blob is valid JSON; it is not.
		t.Fatalf("wrong key should fail or warn, got %+v", got)
	}
	if !warned {
		t.Log("wrong-key path errors out when JSON fallback also fails")
	}
}

func TestAESInvalidKeyIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	keyPath := aesKeyPath(path)
	invalid := []byte("do-not-destroy")
	if err := os.WriteFile(keyPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAES(context.Background(), path, nil); err == nil {
		t.Fatal("OpenAES accepted an invalid key")
	}
	got, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(got, invalid) {
		t.Fatalf("invalid key was overwritten: %q, %v", got, err)
	}
}

func TestConcurrentKeyCreationUsesOneKey(t *testing.T) {
	const (
		rounds  = 20
		workers = 24
	)
	for round := range rounds {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "vault.key")
		keys := make(chan []byte, workers)
		errs := make(chan error, workers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				key, err := loadOrCreateKey(keyPath)
				keys <- key
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(keys)
		close(errs)

		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: loadOrCreateKey: %v", round, err)
			}
		}
		onDisk, err := os.ReadFile(keyPath)
		if err != nil {
			t.Fatalf("round %d: read key: %v", round, err)
		}
		if len(onDisk) != 32 {
			t.Fatalf("round %d: key length = %d, want 32", round, len(onDisk))
		}
		for key := range keys {
			if !bytes.Equal(onDisk, key) {
				t.Fatalf("round %d: concurrent caller observed a different master key", round)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("round %d: read key directory: %v", round, err)
		}
		if len(entries) != 1 || entries[0].Name() != "vault.key" {
			t.Fatalf("round %d: temporary key files remain: %v", round, entries)
		}
	}
}

func TestAESMigrationFromLegacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	ctx := context.Background()
	// Write a legacy plain JSON vault, then open with OpenAES: it reads the
	// legacy data (with warning) and re-encrypts on the next save.
	legacy := `{"legacy":"value"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v, err := OpenAES(ctx, path, func(string) {})
	if err != nil {
		t.Fatalf("OpenAES legacy: %v", err)
	}
	if val, ok := v.Get("legacy"); !ok || val != "value" {
		t.Errorf("legacy value = %q, %v", val, ok)
	}
	if err := v.Save(ctx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.HasPrefix(data, []byte(aesMagic)) {
		t.Error("vault should be re-encrypted after save")
	}
}
