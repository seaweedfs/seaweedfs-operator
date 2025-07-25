package admin

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
)

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func GenerateKeyWithBytes(length int) (string, error) {
	b := make([]byte, length)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	for i := 0; i < length; i++ {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b), nil
}

func GenerateKeyWithBuilder(length int) (string, error) {
	var sb strings.Builder
	sb.Grow(length)

	// Generate random bytes
	b := make([]byte, length)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	// Map bytes to charset characters
	for i := 0; i < length; i++ {
		sb.WriteByte(charset[int(b[i])%len(charset)])
	}
	return sb.String(), nil
}

func GenerateAccountId() (string, error) {
	var b [8]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return "", err
	}

	// Convert bytes to uint64
	num := binary.BigEndian.Uint64(b[:])

	// Limit the number to 12 digits max
	accountId := num % 1_000_000_000_000

	// Format as zero-padded 12-digit string
	return fmt.Sprintf("%012d", accountId), nil
}
