package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	SaltLength   = 16
	KeyLength    = 32
	NonceLength  = 12
	ArgonTime    = 3
	ArgonMemory  = 64 * 1024
	ArgonThreads = 4
)

var (
	ErrDecryptionFailed = errors.New("could not open file check your password")
	ErrInvalidPayload  = errors.New("file part is too small or damaged")
)

func GenerateSalt() ([]byte, error) {
	salt := make([]byte, SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("could not make random salt %w", err)
	}
	return salt, nil
}

func DeriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, ArgonTime, ArgonMemory, ArgonThreads, KeyLength)
}

func EncryptChunk(plaintext []byte, key []byte, additionalData []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("could not create cipher %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("could not create gcm %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("could not create nonce %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, plaintext, additionalData)
	return sealed, nil
}

func DecryptChunk(encryptedPayload []byte, key []byte, additionalData []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("could not create cipher %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("could not create gcm %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(encryptedPayload) < nonceSize+gcm.Overhead() {
		return nil, ErrInvalidPayload
	}

	nonce := encryptedPayload[:nonceSize]
	ciphertext := encryptedPayload[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return nil, ErrDecryptionFailed
	}

	return plaintext, nil
}

func ComputeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func ComputeStreamSHA256(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
