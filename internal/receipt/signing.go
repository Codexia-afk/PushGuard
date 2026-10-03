package receipt

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/pushguard/pushguard/internal/security"
	"os"
	"path/filepath"
	"strings"
)

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe evidence directory")
	}
	return security.SecureDirectory(path)
}

func SigningKey() (ed25519.PrivateKey, error) {
	base, err := Base()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "signing")
	if err = privateDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "ed25519.key")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(key)
		closeErr := f.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != ed25519.PrivateKeySize || !security.PrivateMode(info.Mode()) {
		return nil, fmt.Errorf("signing key must be a private regular Ed25519 key (0600)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ed25519.PrivateKey(data), nil
}

func PublicTrust() (map[string]ed25519.PublicKey, error) {
	key, err := SigningKey()
	if err != nil {
		return nil, err
	}
	pub := key.Public().(ed25519.PublicKey)
	return map[string]ed25519.PublicKey{Digest(pub): pub}, nil
}

func ParseTrust(data []byte) (map[string]ed25519.PublicKey, error) {
	if len(data) > 64<<10 {
		return nil, fmt.Errorf("trust store too large")
	}
	var encoded map[string]string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return nil, fmt.Errorf("trusted keys must be a JSON object of key ID to base64 public key")
	}
	keys := map[string]ed25519.PublicKey{}
	for id, text := range encoded {
		pub, err := base64.StdEncoding.DecodeString(text)
		if err != nil || len(pub) != ed25519.PublicKeySize || Digest(pub) != id {
			return nil, fmt.Errorf("invalid trusted public key %s", id)
		}
		keys[id] = ed25519.PublicKey(pub)
	}
	return keys, nil
}

func SaveVerification(root string, v *VerificationReceipt) error {
	if v == nil || v.ID != v.Hash() || len(v.ID) != 64 {
		return fmt.Errorf("invalid verification receipt")
	}
	dir, err := repositoryDir(root)
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "delivery")
	if err = privateDir(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 256<<10 {
		return fmt.Errorf("delivery receipt too large")
	}
	f, err := os.OpenFile(filepath.Join(dir, v.ID+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		existing, e := LoadVerification(root, v.ID)
		if e != nil {
			return e
		}
		old, _ := json.Marshal(existing)
		current, _ := json.Marshal(v)
		if !bytes.Equal(old, current) {
			return fmt.Errorf("immutable delivery receipt was altered or conflicts with the supplied evidence")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	e := f.Close()
	if err == nil {
		err = e
	}
	return err
}

func LoadVerification(root, id string) (*VerificationReceipt, error) {
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("invalid receipt identifier")
	}
	dir, err := repositoryDir(root)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "delivery", id+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return nil, fmt.Errorf("invalid receipt file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v VerificationReceipt
	if err = json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
