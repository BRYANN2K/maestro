// Package vault stores API keys and other secrets in a JSON file with 0600
// permissions. B9 replaces the on-disk encoding with AES-256 while keeping
// this interface.
package vault

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// cryptoRandRead reads random bytes from crypto/rand.
func cryptoRandRead(b []byte) (int, error) { return rand.Read(b) }

// Vault is a key/value secret store persisted as JSON (or AES-256 with
// OpenAES).
type Vault struct {
	mu   sync.RWMutex
	path string
	data map[string]string
	aes  []byte // non-nil when the vault is encrypted
}

// Open loads the vault at path, creating an empty one (with 0600 permissions)
// if it does not exist yet.
func Open(ctx context.Context, path string) (*Vault, error) {
	v := &Vault{path: path, data: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return v, nil
		}
		return nil, fmt.Errorf("open vault %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &v.data); err != nil {
		return nil, fmt.Errorf("open vault %s: %w", path, err)
	}
	if v.data == nil {
		v.data = map[string]string{}
	}
	return v, nil
}

// Get returns the value for key.
func (v *Vault) Get(key string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	val, ok := v.data[key]
	return val, ok
}

// Set stores value for key. It is not persisted until Save.
func (v *Vault) Set(key, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.data[key] = value
}

// Delete removes key. It is not persisted until Save.
func (v *Vault) Delete(key string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.data, key)
}

// Keys returns the stored keys, sorted.
func (v *Vault) Keys() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	keys := make([]string, 0, len(v.data))
	for k := range v.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Len returns the number of stored keys.
func (v *Vault) Len() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.data)
}

// Save writes the vault atomically with 0600 permissions, creating the
// parent directory (0700) as needed. AES-encrypted vaults are sealed first.
func (v *Vault) Save(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v.path == "" {
		return errors.New("vault path is required")
	}
	v.mu.RLock()
	snapshot := make(map[string]string, len(v.data))
	for key, value := range v.data {
		snapshot[key] = value
	}
	aesKey := append([]byte(nil), v.aes...)
	v.mu.RUnlock()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("save vault: %w", err)
	}
	if aesKey != nil {
		data, err = encryptVault(aesKey, data)
		if err != nil {
			return fmt.Errorf("save vault: %w", err)
		}
	}
	dir := filepath.Dir(v.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("save vault: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("save vault: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("save vault: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("save vault: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save vault: %w", err)
	}
	if err := os.Rename(tmpName, v.path); err != nil {
		return fmt.Errorf("save vault: %w", err)
	}
	return nil
}

// ---- AES-256 encryption (B9) ---------------------------------------------

// aesMagic prefixes AES-encrypted vault files.
const aesMagic = "MAESTRO-AES-v1\n"

// aesKeyFile is the master key path next to the vault.
func aesKeyPath(vaultPath string) string {
	return filepath.Dir(vaultPath) + string(os.PathSeparator) + "vault.key"
}

func readKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != 32 {
		return nil, fmt.Errorf("vault key %s has invalid length %d; refusing to overwrite it", path, len(data))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure vault key: %w", err)
	}
	return data, nil
}

// loadOrCreateKey loads the 32-byte AES key, generating one with
// crypto/rand on first use.
func loadOrCreateKey(path string) ([]byte, error) {
	data, err := readKey(path)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read vault key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := cryptoRandRead(key); err != nil {
		return nil, fmt.Errorf("generate vault key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create vault key directory: %w", err)
	}

	// Write the candidate under a private temporary name first. Publishing a
	// fully written inode with Link keeps the final path absent until all 32
	// bytes are visible, while retaining O_EXCL-like no-replace semantics.
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".vault-key-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary vault key: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	closeWithError := func(cause error) ([]byte, error) {
		_ = f.Close()
		return nil, cause
	}
	if err := f.Chmod(0o600); err != nil {
		return closeWithError(fmt.Errorf("secure temporary vault key: %w", err))
	}
	if _, err := f.Write(key); err != nil {
		return closeWithError(fmt.Errorf("write temporary vault key: %w", err))
	}
	if err := f.Sync(); err != nil {
		return closeWithError(fmt.Errorf("sync temporary vault key: %w", err))
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close temporary vault key: %w", err)
	}

	err = os.Link(tmpPath, path)
	if errors.Is(err, os.ErrExist) {
		// Another process won the publication race. Read its complete key as
		// the single source of truth. Invalid existing data remains fail-closed
		// and is never overwritten.
		data, readErr := readKey(path)
		if readErr != nil {
			return nil, fmt.Errorf("read concurrently published vault key: %w", readErr)
		}
		return data, nil
	}
	if err != nil {
		return nil, fmt.Errorf("publish vault key: %w", err)
	}
	return key, nil
}

// encryptVault encrypts the JSON blob with AES-256-GCM.
func encryptVault(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := cryptoRandRead(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)
	out := make([]byte, 0, len(aesMagic)+len(sealed))
	out = append(out, aesMagic...)
	out = append(out, sealed...)
	return out, nil
}

// decryptVault decrypts an AES vault blob.
func decryptVault(key, data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte(aesMagic)) {
		return nil, errors.New("not an AES vault")
	}
	sealed := data[len(aesMagic):]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(sealed) < nonceSize {
		return nil, errors.New("vault too short")
	}
	nonce, body := sealed[:nonceSize], sealed[nonceSize:]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, errors.New("vault decryption failed (wrong key or corrupt file)")
	}
	return plain, nil
}

// OpenAES opens the vault in encrypted mode: reads the master key from the
// key file, decrypts on load, encrypts on save. A corrupt or legacy file
// falls back to plain JSON-0600 with a warning callback.
func OpenAES(ctx context.Context, path string, warn func(string)) (*Vault, error) {
	key, err := loadOrCreateKey(aesKeyPath(path))
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			v := &Vault{path: path, data: map[string]string{}, aes: key}
			return v, nil
		}
		return nil, fmt.Errorf("open vault %s: %w", path, err)
	}
	plain, err := decryptVault(key, data)
	if err != nil {
		if warn != nil {
			warn("vault: " + err.Error() + " — falling back to plain JSON 0600")
		}
		// Legacy / corrupt: try the JSON path; corrupt JSON fails too.
		var m map[string]string
		if jerr := json.Unmarshal(data, &m); jerr == nil {
			v := &Vault{path: path, data: m, aes: key}
			return v, nil
		}
		return nil, fmt.Errorf("open vault %s: %w", path, err)
	}
	var m map[string]string
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("open vault %s: %w", path, err)
	}
	return &Vault{path: path, data: m, aes: key}, nil
}
